package rendezvous

import (
	"context"
	"crypto/rand"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/n-popescu/switchnet/internal/nex"
)

// The matchmaking engine behind MatchMaking (21), MatchMakingExt (50) and
// MatchmakeExtension (109). Gatherings live in memory: a gathering is a
// lobby of consoles that are connected right now, and it means nothing once
// they are not. Players leave every gathering the moment their connection
// ends.
//
// Pretendo's AGPL matchmaking (nex-protocols-common-go) is the model for the
// semantics -- search criteria, ownership migration, who is told what when
// someone leaves. Where Splatoon 2's proven server behaves differently, that
// behaviour wins, and the comment says so.

// Protocol ids.
const (
	ProtocolMatchMaking        = 0x15
	ProtocolMatchMakingExt     = 0x32
	ProtocolMatchmakeExtension = 0x6D
)

// Matchmaker holds every live gathering.
type Matchmaker struct {
	s  *Server
	mu sync.Mutex

	gatherings map[uint32]*gathering
	nextGID    uint32

	// notifData is what each player published with UpdateNotificationData,
	// by type. Friends read it back; it lives as long as the connection.
	notifData map[uint64]map[uint32]NotificationEvent

	onStart []func(GatheringInfo)
}

type gathering struct {
	session      *MatchmakeSession
	participants []uint64
	messages     map[uint64]string
	sessionURL   string
	created      time.Time
	started      bool
}

// GatheringInfo is a read-only snapshot of a gathering, for the dashboard
// and for hooks.
type GatheringInfo struct {
	ID           uint32
	Owner        uint64
	Host         uint64
	GameMode     uint32
	Attribs      []uint32
	Open         bool
	Private      bool
	Participants []uint64
	Max          uint16
	Created      time.Time
	Started      bool
}

func (g *gathering) info() GatheringInfo {
	return GatheringInfo{
		ID: g.session.ID, Owner: g.session.Owner, Host: g.session.Host,
		GameMode: g.session.GameMode, Attribs: slices.Clone(g.session.Attribs),
		Open: g.session.OpenParticipation, Private: g.session.UserPasswordEnabled || g.session.SystemPasswordEnabled,
		Participants: slices.Clone(g.participants), Max: g.session.MaxParticipants,
		Created: g.created, Started: g.started,
	}
}

func (g *gathering) has(pid uint64) bool { return slices.Contains(g.participants, pid) }

// copyFor is the session as a given reader should see it: the session key
// only for a participant, and never the password.
func (g *gathering) copyFor(pid uint64) *MatchmakeSession {
	c := *g.session
	c.Attribs = slices.Clone(c.Attribs)
	c.Params = slices.Clone(c.Params)
	c.ApplicationBuffer = slices.Clone(c.ApplicationBuffer)
	c.ParticipationCount = uint32(len(g.participants))
	c.UserPassword = ""
	if !g.has(pid) {
		c.SessionKey = nil
	} else {
		c.SessionKey = slices.Clone(c.SessionKey)
	}
	return &c
}

func (s *Server) registerMatchmaking() {
	m := &Matchmaker{s: s, gatherings: map[uint32]*gathering{}, nextGID: 1,
		notifData: map[uint64]map[uint32]NotificationEvent{}}
	s.mm = m
	forget := func(pid uint64) {
		m.leaveAll(pid, NotifyParticipantLeft)
		m.mu.Lock()
		delete(m.notifData, pid)
		m.mu.Unlock()
	}
	// A secure connection that has just been made is in no lobby: anything
	// its player is still listed in belongs to a previous connection, which a
	// console that reconnects -- a dropped connection, a restarted game --
	// knows nothing about. Left there, the lobby holds a player who is not in
	// it and never starts. It runs before the new connection is served a
	// single call, so nothing it has joined can be undone.
	s.OnConnect(func(c *nex.Conn) { forget(c.PID) })
	s.OnDisconnect(func(c *nex.Conn) {
		// A newer connection for the same player has already replaced this
		// one, and its connect has already done this; doing it again now
		// would take the newer connection out of the lobby it has joined
		// since.
		if s.Player(c.PID) != nil {
			return
		}
		forget(c.PID)
	})
	m.register()
}

// Matchmaker returns the server's matchmaking engine.
func (s *Server) Matchmaker() *Matchmaker { return s.mm }

// OnMatchStart registers a hook run once per gathering, when it stops
// taking players (CloseParticipation): the moment a lobby becomes a match.
func (m *Matchmaker) OnMatchStart(f func(GatheringInfo)) {
	m.mu.Lock()
	m.onStart = append(m.onStart, f)
	m.mu.Unlock()
}

// Gatherings snapshots every live gathering.
func (m *Matchmaker) Gatherings() []GatheringInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]GatheringInfo, 0, len(m.gatherings))
	for _, g := range m.gatherings {
		out = append(out, g.info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// GatheringOf is the gathering a player is in, if any (the most recent one).
func (m *Matchmaker) GatheringOf(pid uint64) (GatheringInfo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best *gathering
	for _, g := range m.gatherings {
		if g.has(pid) && (best == nil || g.session.ID > best.session.ID) {
			best = g
		}
	}
	if best == nil {
		return GatheringInfo{}, false
	}
	return best.info(), true
}

func newSessionKey() []byte {
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	return k
}

// create registers a gathering from the caller's template (mutex held).
//
// The server owns the id, the owner and host, the session key, the start
// time and the password flags; everything else is the console's. Two
// parameters are added to MatchmakeParam: @SR=true and @GIR=3. Pretendo's
// matchmaking sets both on every session it creates, and NextendoNetwork
// records a session without them being rejected by the console (facts
// only). What they mean is not documented.
func (m *Matchmaker) create(pid uint64, tmpl *MatchmakeSession, now time.Time) *gathering {
	s := *tmpl
	s.ID = m.nextGID
	m.nextGID++
	if m.nextGID == 0 {
		m.nextGID = 1
	}
	s.Owner, s.Host = pid, pid
	if s.MaxParticipants == 0 {
		s.MaxParticipants = 8
	}
	s.SessionKey = newSessionKey()
	s.StartedTime = nex.NewDateTime(now)
	s.UserPasswordEnabled = s.UserPassword != ""
	s.SystemPasswordEnabled = false
	s.Params = setParam(s.Params, "@SR", nex.Variant{Type: nex.VariantBool, Bool: true})
	s.Params = setParam(s.Params, "@GIR", nex.Variant{Type: nex.VariantInt64, Int: 3})
	g := &gathering{session: &s, messages: map[uint64]string{}, created: now}
	m.gatherings[s.ID] = g
	return g
}

func setParam(params []Param, key string, v nex.Variant) []Param {
	for i := range params {
		if params[i].Key == key {
			params[i].Value = v
			return params
		}
	}
	return append(params, Param{Key: key, Value: v})
}

// add puts players into a gathering (mutex held). Returns the players
// actually added.
func (g *gathering) add(pids []uint64, message string) []uint64 {
	var added []uint64
	for _, p := range pids {
		if p == 0 || g.has(p) {
			continue
		}
		g.participants = append(g.participants, p)
		g.messages[p] = message
		added = append(added, p)
	}
	g.session.ParticipationCount = uint32(len(g.participants))
	return added
}

// announce tells a gathering about players who just joined it (called
// without the mutex, with a snapshot).
//
// Every participant -- the joiner included -- is told about each newcomer,
// and each newcomer is told about everyone already there. That is the shape
// NextendoNetwork measured Splatoon 2's proven server sending (facts only),
// and both halves are load-bearing: a host alone in a new lobby waits for
// its OWN Participate before Pia moves on, and a joiner that is never told
// who was already there never builds the mesh ("A communication error has
// occurred"). Pretendo sends the same events when a gathering's flags ask
// for verbose participation; Splatoon 2 needs them either way.
//
// Players added by someone else (a squad joining together) are also told
// which gathering they were put in (122000, "switch gathering").
func (m *Matchmaker) announce(caller uint64, gid uint32, before, added []uint64, message string) {
	all := append(slices.Clone(before), added...)
	count := uint64(len(all))
	for _, p := range added {
		if p != caller {
			m.s.Notify(NotificationEvent{Source: caller, Type: NotifyAddedToGathering,
				Param1: uint64(gid), Param2: p}, p)
		}
	}
	for _, n := range added {
		m.s.Notify(NotificationEvent{Source: caller, Type: NotifyParticipate, Param1: uint64(gid),
			Param2: n, StrParam: message, Param3: count}, all...)
		for _, old := range before {
			m.s.Notify(NotificationEvent{Source: caller, Type: NotifyParticipate, Param1: uint64(gid),
				Param2: old, StrParam: message, Param3: count}, n)
		}
	}
}

// natClassOf is a connected player's NAT class.
func (m *Matchmaker) natClassOf(pid uint64) NATClass {
	if c := m.s.Player(pid); c != nil {
		return NATOf(c).Class()
	}
	return NATUnknown
}

// matches reports whether a candidate gathering satisfies auto-matchmake for
// a party of size party with the given template and criteria (mutex held).
func (m *Matchmaker) matches(g *gathering, caller uint64, party int, tmpl *MatchmakeSession,
	criteria []SearchCriteria, targets []uint32, friends map[uint64]bool) bool {
	s := g.session
	if len(targets) > 0 && !slices.Contains(targets, s.ID) {
		return false
	}
	if g.has(caller) || !s.OpenParticipation || s.UserPasswordEnabled || s.SystemPasswordEnabled {
		return false
	}
	if int(s.MaxParticipants) < len(g.participants)+party {
		return false
	}
	if m.s.Player(s.Host) == nil {
		// A lobby whose host is gone is a lobby nobody can connect to.
		return false
	}
	if s.ParticipationPolicy == policyFriendsOnly && !friends[s.Owner] {
		return false
	}
	joiner := m.natClassOf(caller)
	for _, p := range g.participants {
		if !NATCompatible(joiner, m.natClassOf(p)) {
			return false
		}
	}
	if len(criteria) == 0 {
		return s.GameMode == tmpl.GameMode && s.MatchmakeSystemType == tmpl.MatchmakeSystemType &&
			len(s.Attribs) == len(tmpl.Attribs)
	}
	for _, c := range criteria {
		if criterionMatches(c, g, party) {
			return true
		}
	}
	return false
}

// criterionMatches applies one MatchmakeSessionSearchCriteria, with
// Pretendo's reading of it: each numeric filter is "" (any), "n", or "a,b"
// inclusive; attribute 1 is skipped because it carries the selection method,
// not a value to match; the attribute count and the codeword must agree.
func criterionMatches(c SearchCriteria, g *gathering, party int) bool {
	s := g.session
	if s.ReferGID != c.ReferGID || s.Codeword != c.Codeword {
		return false
	}
	if len(c.Attribs) != len(s.Attribs) {
		return false
	}
	for i, a := range c.Attribs {
		if i == 1 {
			continue
		}
		if !inRange(a, uint64(s.Attribs[i])) {
			return false
		}
	}
	if !inRange(c.GameMode, uint64(s.GameMode)) || !inRange(c.MinParticipants, uint64(s.MinParticipants)) ||
		!inRange(c.MaxParticipants, uint64(s.MaxParticipants)) || !inRange(c.MatchmakeSystemType, uint64(s.MatchmakeSystemType)) {
		return false
	}
	if c.VacantOnly {
		need := int(c.VacantParticipants)
		if need == 0 {
			need = party
		}
		if len(g.participants)+need > int(s.MaxParticipants) {
			return false
		}
	}
	if c.ExcludeLocked && !s.OpenParticipation {
		return false
	}
	return true
}

// inRange reports whether v satisfies a criteria string. A malformed string
// matches nothing: a filter the server cannot read is not a wildcard.
func inRange(spec string, v uint64) bool {
	if spec == "" {
		return true
	}
	lo, hi, isRange := strings.Cut(spec, ",")
	a, err := strconv.ParseUint(strings.TrimSpace(lo), 10, 64)
	if err != nil {
		return false
	}
	if !isRange {
		return v == a
	}
	b, err := strconv.ParseUint(strings.TrimSpace(hi), 10, 64)
	if err != nil {
		return false
	}
	return v >= a && v <= b
}

// friendsOf returns a player's friends, as PIDs.
func (s *Server) friendsOf(pid uint64) map[uint64]bool {
	out := map[uint64]bool{}
	if s.store == nil {
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fs, err := s.store.ListFriends(ctx, UserIDForPID(pid), 0)
	if err != nil {
		s.log.Error("nex: list friends", "pid", pid, "error", err)
		return out
	}
	for _, f := range fs {
		if p, err := PIDForUserID(f.FriendID); err == nil {
			out[p] = true
		}
	}
	return out
}

// autoMatchmake joins the best open gathering that matches, or creates one.
// The best is the fullest: on a server with few players, filling one lobby
// beats spreading everyone across several that never start.
func (m *Matchmaker) autoMatchmake(c *nex.Conn, j joinRequest) *MatchmakeSession {
	friends := m.s.friendsOf(c.PID)
	party := append([]uint64{c.PID}, j.additional...)
	now := m.s.now()

	m.mu.Lock()
	var best *gathering
	for _, g := range m.gatherings {
		if !m.matches(g, c.PID, len(party), j.session, j.criteria, j.targetGIDs, friends) {
			continue
		}
		if best == nil || len(g.participants) > len(best.participants) ||
			(len(g.participants) == len(best.participants) && g.session.ID < best.session.ID) {
			best = g
		}
	}
	created := best == nil
	if created {
		best = m.create(c.PID, j.session, now)
	}
	before := slices.Clone(best.participants)
	added := best.add(party, j.message)
	gid := best.session.ID
	out := best.copyFor(c.PID)
	m.mu.Unlock()

	m.s.log.Info("nex: matchmade", "pid", c.PID, "gid", gid, "created", created,
		"players", len(before)+len(added), "mode", out.GameMode)
	m.announce(c.PID, gid, before, added, j.message)
	return out
}

// createSession always creates.
func (m *Matchmaker) createSession(c *nex.Conn, j joinRequest) *MatchmakeSession {
	m.mu.Lock()
	g := m.create(c.PID, j.session, m.s.now())
	added := g.add(append([]uint64{c.PID}, j.additional...), j.message)
	gid := g.session.ID
	out := g.copyFor(c.PID)
	m.mu.Unlock()
	m.s.log.Info("nex: gathering created", "pid", c.PID, "gid", gid, "mode", out.GameMode, "private", out.UserPasswordEnabled)
	m.announce(c.PID, gid, nil, added, j.message)
	return out
}

// join puts the caller into a named gathering, checking what an explicit
// join must: it exists, there is room, the password matches, and a
// friends-only gathering belongs to a friend.
func (m *Matchmaker) join(c *nex.Conn, j joinRequest) (*MatchmakeSession, uint32) {
	friends := m.s.friendsOf(c.PID)
	m.mu.Lock()
	g := m.gatherings[j.gid]
	if g == nil {
		m.mu.Unlock()
		return nil, nex.ResultSessionVoid
	}
	s := g.session
	if j.behavior == 1 {
		// "I am already joined": the caller asserts membership and asks only
		// for the session.
		if !g.has(c.PID) {
			m.mu.Unlock()
			return nil, nex.ResultNotParticipated
		}
		out := g.copyFor(c.PID)
		m.mu.Unlock()
		return out, 0
	}
	party := append([]uint64{c.PID}, j.additional...)
	switch {
	case g.has(c.PID):
		out := g.copyFor(c.PID)
		m.mu.Unlock()
		return out, 0
	case len(g.participants)+len(party) > int(s.MaxParticipants):
		m.mu.Unlock()
		return nil, nex.ResultSessionFull
	case s.UserPasswordEnabled && !constantTimeEqual(s.UserPassword, j.userPassword):
		m.mu.Unlock()
		return nil, nex.ResultPermissionDenied
	case s.ParticipationPolicy == policyFriendsOnly && !friends[s.Owner]:
		m.mu.Unlock()
		return nil, nex.ResultPermissionDenied
	}
	before := slices.Clone(g.participants)
	added := g.add(party, j.message)
	out := g.copyFor(c.PID)
	m.mu.Unlock()
	m.s.log.Info("nex: joined gathering", "pid", c.PID, "gid", j.gid, "players", len(before)+len(added))
	m.announce(c.PID, j.gid, before, added, j.message)
	return out, 0
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var d byte
	for i := 0; i < len(a); i++ {
		d |= a[i] ^ b[i]
	}
	return d == 0
}

// leave removes a player from one gathering and tells the rest, migrating
// ownership and host as needed (mutex NOT held on entry).
func (m *Matchmaker) leave(pid uint64, gid uint32, why uint32) bool {
	m.mu.Lock()
	g := m.gatherings[gid]
	if g == nil || !g.has(pid) {
		m.mu.Unlock()
		return false
	}
	g.participants = slices.DeleteFunc(g.participants, func(p uint64) bool { return p == pid })
	delete(g.messages, pid)
	g.session.ParticipationCount = uint32(len(g.participants))
	rest := slices.Clone(g.participants)
	s := g.session

	if len(rest) == 0 {
		delete(m.gatherings, gid)
		m.mu.Unlock()
		return true
	}

	var events []NotificationEvent
	if s.Owner == pid {
		if s.Flags&FlagMigrateOwner == 0 {
			// The owner leaving ends a gathering that did not ask to outlive
			// them. Everyone else is told it is gone.
			delete(m.gatherings, gid)
			m.mu.Unlock()
			m.s.Notify(NotificationEvent{Source: pid, Type: NotifyGatheringUnregisterd, Param1: uint64(gid)}, rest...)
			return true
		}
		s.Owner = rest[0]
		events = append(events, NotificationEvent{Source: pid, Type: NotifyOwnershipChanged,
			Param1: uint64(gid), Param2: s.Owner})
	}
	if s.Host == pid {
		s.Host = s.Owner
		events = append(events, NotificationEvent{Source: pid, Type: NotifyHostChanged, Param1: uint64(gid)})
	}
	events = append(events, NotificationEvent{Source: pid, Type: why, Param1: uint64(gid), Param2: pid})
	m.mu.Unlock()

	for _, e := range events {
		m.s.Notify(e, rest...)
	}
	return true
}

// leaveAll removes a player from every gathering they are in.
func (m *Matchmaker) leaveAll(pid uint64, why uint32) {
	m.mu.Lock()
	var gids []uint32
	for gid, g := range m.gatherings {
		if g.has(pid) {
			gids = append(gids, gid)
		}
	}
	m.mu.Unlock()
	for _, gid := range gids {
		m.leave(pid, gid, why)
	}
}

// unregister deletes a gathering. Only its owner may.
func (m *Matchmaker) unregister(pid uint64, gid uint32) bool {
	m.mu.Lock()
	g := m.gatherings[gid]
	if g == nil || g.session.Owner != pid {
		m.mu.Unlock()
		return false
	}
	delete(m.gatherings, gid)
	rest := slices.DeleteFunc(slices.Clone(g.participants), func(p uint64) bool { return p == pid })
	m.mu.Unlock()
	m.s.Notify(NotificationEvent{Source: pid, Type: NotifyGatheringUnregisterd, Param1: uint64(gid)}, rest...)
	return true
}

// modify runs f on a gathering the caller may change: its owner, its host,
// or -- when anyParticipant -- anyone in it.
func (m *Matchmaker) modify(pid uint64, gid uint32, anyParticipant bool, f func(g *gathering)) uint32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	g := m.gatherings[gid]
	if g == nil {
		return nex.ResultSessionVoid
	}
	if g.session.Owner != pid && g.session.Host != pid && !(anyParticipant && g.has(pid)) {
		return nex.ResultPermissionDenied
	}
	f(g)
	return 0
}

// setOpen opens or closes a gathering to new players. Closing is how a
// lobby turns into a match, so that is when OnMatchStart fires.
func (m *Matchmaker) setOpen(pid uint64, gid uint32, open bool) uint32 {
	var start *GatheringInfo
	code := m.modify(pid, gid, false, func(g *gathering) {
		g.session.OpenParticipation = open
		if !open && !g.started {
			g.started = true
			i := g.info()
			start = &i
		}
	})
	if start != nil {
		m.mu.Lock()
		hooks := slices.Clone(m.onStart)
		m.mu.Unlock()
		for _, h := range hooks {
			h(*start)
		}
	}
	return code
}
