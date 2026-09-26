package rendezvous

import (
	"context"
	"slices"
	"time"

	"github.com/n-popescu/switchnet/internal/nex"
)

// The matchmaking methods. Request and response shapes from Kinnay's
// Match-Making-Protocol, Match-Making-Protocol-Ext and
// Matchmake-Extension-Protocol pages.

func (m *Matchmaker) register() {
	s := m.s
	h := func(p uint16, pn string, id uint32, n string, f Method) { s.Handle(p, pn, id, n, f) }

	const mm, mmn = ProtocolMatchMaking, "MatchMaking"
	h(mm, mmn, 2, "UnregisterGathering", m.unregisterGathering)
	h(mm, mmn, 3, "UnregisterGatherings", m.unregisterGatherings)
	h(mm, mmn, 13, "GetParticipants", m.getParticipants(false))
	h(mm, mmn, 15, "GetDetailedParticipants", m.getDetailedParticipants(false))
	h(mm, mmn, 16, "GetParticipantsURLs", m.getParticipantsURLs)
	h(mm, mmn, 21, "FindBySingleID", m.findBySingleID)
	h(mm, mmn, 27, "UpdateSessionURL", m.updateSessionURL)
	h(mm, mmn, 28, "GetSessionURL", m.getSessionURL)
	h(mm, mmn, 36, "MigrateGatheringOwnershipV1", m.migrateOwnership(true, false))
	h(mm, mmn, 40, "UpdateSessionHostV1", m.updateSessionHost(false))
	h(mm, mmn, 41, "GetSessionURLs", m.getSessionURLs)
	h(mm, mmn, 42, "UpdateSessionHost", m.updateSessionHost(true))
	h(mm, mmn, 43, "UpdateGatheringOwnership", m.updateGatheringOwnership)
	h(mm, mmn, 44, "MigrateGatheringOwnership", m.migrateOwnership(false, true))

	const ext, extn = ProtocolMatchMakingExt, "MatchMakingExt"
	h(ext, extn, 1, "EndParticipation", m.endParticipation)
	h(ext, extn, 2, "GetParticipants", m.getParticipants(true))
	h(ext, extn, 3, "GetDetailedParticipants", m.getDetailedParticipants(true))
	h(ext, extn, 4, "GetParticipantsURLs", m.getParticipantsURLsList)

	const me, men = ProtocolMatchmakeExtension, "MatchmakeExtension"
	h(me, men, 1, "CloseParticipation", m.participation(false))
	h(me, men, 2, "OpenParticipation", m.participation(true))
	h(me, men, 3, "AutoMatchmake_Postpone", m.autoMatchmakeHolder(false, false))
	h(me, men, 4, "BrowseMatchmakeSession", m.browse(true, true))
	h(me, men, 6, "CreateMatchmakeSession", m.createMatchmakeSession)
	h(me, men, 7, "JoinMatchmakeSession", m.joinMatchmakeSession)
	h(me, men, 8, "ModifyCurrentGameAttribute", m.modifyCurrentGameAttribute)
	h(me, men, 9, "UpdateNotificationData", m.updateNotificationData)
	h(me, men, 10, "GetFriendNotificationData", m.getFriendNotificationData(false))
	h(me, men, 11, "UpdateApplicationBuffer", m.updateApplicationBuffer)
	h(me, men, 12, "UpdateMatchmakeSessionAttribute", m.updateMatchmakeSessionAttribute)
	h(me, men, 13, "GetlstFriendNotificationData", m.getFriendNotificationData(true))
	h(me, men, 14, "UpdateMatchmakeSession", m.updateMatchmakeSession)
	h(me, men, 15, "AutoMatchmakeWithSearchCriteria_Postpone", m.autoMatchmakeHolder(true, false))
	h(me, men, 16, "GetPlayingSession", m.getPlayingSession)
	h(me, men, 31, "GetSimplePlayingSession", m.getSimplePlayingSession(false))
	h(me, men, 33, "AutoMatchmakeWithGatheringId_Postpone", m.autoMatchmakeHolder(false, true))
	h(me, men, 34, "UpdateProgressScore", m.updateProgressScore)
	h(me, men, 38, "CreateMatchmakeSessionWithParam", m.createWithParam)
	h(me, men, 39, "JoinMatchmakeSessionWithParam", m.joinWithParam)
	h(me, men, 40, "AutoMatchmakeWithParam_Postpone", m.autoWithParam)
	h(me, men, 41, "FindMatchmakeSessionByGatheringIdDetail", m.findSingle)
	h(me, men, 44, "UpdateMatchmakeSessionPart", m.updateMatchmakeSessionPart)
	h(me, men, 48, "FindMatchmakeSessionByGatheringId", m.findByGatheringIDs)
	h(me, men, 49, "FindMatchmakeSessionBySingleGatheringId", m.findSingle)
	h(me, men, 50, "FindMatchmakeSessionByOwner", m.findByOwner)
	h(me, men, 51, "FindMatchmakeSessionByParticipant", m.findByParticipant)
	h(me, men, 52, "BrowseMatchmakeSessionNoHolderNoResultRange", m.browse(false, false))
}

func gidOf(req *nex.Request) (uint32, *nex.Reader) {
	r := nex.NewReader(req.Body)
	return r.U32(), r
}

func boolBody(b bool) []byte {
	w := nex.NewWriter()
	w.Bool(b)
	return w.Bytes()
}

// ---- lifecycle ----------------------------------------------------------

func (m *Matchmaker) unregisterGathering(c *nex.Conn, req *nex.Request) *nex.Response {
	gid, r := gidOf(req)
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	return req.Success(boolBody(m.unregister(c.PID, gid)))
}

func (m *Matchmaker) unregisterGatherings(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	gids := nex.ReadList(r, (*nex.Reader).U32)
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	ok := true
	for _, gid := range gids {
		ok = m.unregister(c.PID, gid) && ok
	}
	return req.Success(boolBody(ok))
}

// endParticipation: u32 gid | String message -> Bool.
func (m *Matchmaker) endParticipation(c *nex.Conn, req *nex.Request) *nex.Response {
	gid, r := gidOf(req)
	_ = r.String()
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	return req.Success(boolBody(m.leave(c.PID, gid, NotifyParticipationEnded)))
}

// participation: u32 gid -> nothing.
//
// Close is on Splatoon 2's normal path, not an edge case: it closes a lobby
// as soon as it stops waiting for players and starts the match.
// NextendoNetwork measured an unanswered CloseParticipation as the last call
// before 2306-0103 (facts only).
func (m *Matchmaker) participation(open bool) Method {
	return func(c *nex.Conn, req *nex.Request) *nex.Response {
		gid, r := gidOf(req)
		if r.Err() != nil {
			return req.Error(nex.ResultInvalidArgument)
		}
		if code := m.setOpen(c.PID, gid, open); code != 0 {
			return req.Error(code)
		}
		return req.Success(nil)
	}
}

// ---- create, join, auto-matchmake --------------------------------------

func writeSession(s *MatchmakeSession) []byte {
	w := nex.NewWriter()
	WriteMatchmakeSession(w, s)
	return w.Bytes()
}

func holderBody(s *MatchmakeSession) []byte {
	w := nex.NewWriter()
	writeSessionHolder(w, s)
	return w.Bytes()
}

// autoMatchmakeHolder answers the three AnyDataHolder auto-matchmakes:
// (3) Data<Gathering> | String message
// (15) List<SearchCriteria> | Data<Gathering> | String message
// (33) List<u32> gids | Data<Gathering> | String message
// each -> Data<Gathering>.
func (m *Matchmaker) autoMatchmakeHolder(withCriteria, withGIDs bool) Method {
	return func(c *nex.Conn, req *nex.Request) *nex.Response {
		r := nex.NewReader(req.Body)
		var j joinRequest
		if withCriteria {
			j.criteria = nex.ReadList(r, readSearchCriteria)
		}
		if withGIDs {
			j.targetGIDs = nex.ReadList(r, (*nex.Reader).U32)
		}
		j.session = readSessionHolder(r)
		j.message = r.String()
		if r.Err() != nil || j.session == nil {
			return req.Error(nex.ResultInvalidArgument)
		}
		return req.Success(holderBody(m.autoMatchmake(c, j)))
	}
}

// autoWithParam: AutoMatchmakeParam -> MatchmakeSession. Splatoon 2's
// matchmaking call.
func (m *Matchmaker) autoWithParam(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	j := readAutoParam(r)
	if r.Err() != nil || j.session == nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	return req.Success(writeSession(m.autoMatchmake(c, j)))
}

// createMatchmakeSession: Data<Gathering> | String | u16 -> u32 gid | Buffer session key.
func (m *Matchmaker) createMatchmakeSession(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	j := joinRequest{session: readSessionHolder(r), message: r.String()}
	_ = r.U16()
	if r.Err() != nil || j.session == nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	s := m.createSession(c, j)
	w := nex.NewWriter()
	w.U32(s.ID)
	w.Buffer(s.SessionKey)
	return req.Success(w.Bytes())
}

// createWithParam: CreateMatchmakeSessionParam -> MatchmakeSession.
func (m *Matchmaker) createWithParam(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	j := readCreateParam(r)
	if r.Err() != nil || j.session == nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	return req.Success(writeSession(m.createSession(c, j)))
}

// joinMatchmakeSession: u32 gid | String -> Buffer session key.
func (m *Matchmaker) joinMatchmakeSession(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	j := joinRequest{gid: r.U32(), message: r.String()}
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	s, code := m.join(c, j)
	if code != 0 {
		return req.Error(code)
	}
	w := nex.NewWriter()
	w.Buffer(s.SessionKey)
	return req.Success(w.Bytes())
}

// joinWithParam: JoinMatchmakeSessionParam -> MatchmakeSession. How a
// friend joins a friend's lobby.
func (m *Matchmaker) joinWithParam(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	j := readJoinParam(r)
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	s, code := m.join(c, j)
	if code != 0 {
		return req.Error(code)
	}
	return req.Success(writeSession(s))
}

// ---- modifying a gathering ---------------------------------------------

// modifyCurrentGameAttribute: u32 gid | u32 index | u32 value.
//
// Splatoon 2's host restamps an attribute as the lobby locks for a match;
// joiners read the attributes back, so it is applied, not just
// acknowledged. Unanswered, NextendoNetwork measured 2306-0103 at exactly
// that moment (facts only).
func (m *Matchmaker) modifyCurrentGameAttribute(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	gid, index, value := r.U32(), r.U32(), r.U32()
	if r.Err() != nil || index > 64 {
		return req.Error(nex.ResultInvalidArgument)
	}
	code := m.modify(c.PID, gid, false, func(g *gathering) {
		for len(g.session.Attribs) <= int(index) {
			g.session.Attribs = append(g.session.Attribs, 0)
		}
		g.session.Attribs[index] = value
	})
	if code != 0 {
		return req.Error(code)
	}
	return req.Success(nil)
}

// updateApplicationBuffer: u32 gid | Buffer. The host publishes its match
// configuration here and joiners read it back off the session.
func (m *Matchmaker) updateApplicationBuffer(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	gid, buf := r.U32(), r.Buffer()
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	if code := m.modify(c.PID, gid, false, func(g *gathering) { g.session.ApplicationBuffer = buf }); code != 0 {
		return req.Error(code)
	}
	return req.Success(nil)
}

// updateMatchmakeSessionAttribute: u32 gid | List<u32>.
func (m *Matchmaker) updateMatchmakeSessionAttribute(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	gid := r.U32()
	attribs := nex.ReadList(r, (*nex.Reader).U32)
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	if code := m.modify(c.PID, gid, false, func(g *gathering) { g.session.Attribs = attribs }); code != 0 {
		return req.Error(code)
	}
	return req.Success(nil)
}

// updateProgressScore: u32 gid | u8.
func (m *Matchmaker) updateProgressScore(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	gid, score := r.U32(), r.U8()
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	if code := m.modify(c.PID, gid, true, func(g *gathering) { g.session.ProgressScore = score }); code != 0 {
		return req.Error(code)
	}
	return req.Success(nil)
}

// updateMatchmakeSession: Data<Gathering>. The owner replaces the session's
// own fields; the server-owned ones (id, owner, host, key) are kept.
func (m *Matchmaker) updateMatchmakeSession(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	in := readSessionHolder(r)
	if r.Err() != nil || in == nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	code := m.modify(c.PID, in.ID, false, func(g *gathering) {
		s := g.session
		keep := *s
		*s = *in
		s.ID, s.Owner, s.Host, s.SessionKey = keep.ID, keep.Owner, keep.Host, keep.SessionKey
		s.StartedTime, s.SystemPasswordEnabled = keep.StartedTime, keep.SystemPasswordEnabled
		s.UserPasswordEnabled = s.UserPassword != ""
	})
	if code != 0 {
		return req.Error(code)
	}
	return req.Success(nil)
}

// updateMatchmakeSessionPart: UpdateMatchmakeSessionParam, applying only
// the fields its modification flags name.
//
// Splatoon 2 sends it at the end of a Salmon Run shift, after the results
// screen, and NextendoNetwork measured 2306-0103 there when it went
// unanswered (facts only). Any participant may send it: whoever is left at
// the end of a shift is not necessarily the owner any more.
func (m *Matchmaker) updateMatchmakeSessionPart(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	u := readUpdateParam(r)
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	code := m.modify(c.PID, u.gid, true, func(g *gathering) {
		s := g.session
		f := u.flags
		if f&modAttributes != 0 {
			s.Attribs = u.attribs
		}
		if f&modOpenParticipation != 0 {
			s.OpenParticipation = u.open
		}
		if f&modApplicationBuffer != 0 {
			s.ApplicationBuffer = u.appBuffer
		}
		if f&modProgressScore != 0 {
			s.ProgressScore = u.progress
		}
		if f&modMatchmakeParam != 0 {
			for _, p := range u.params {
				s.Params = setParam(s.Params, p.Key, p.Value)
			}
		}
		if f&modStartedTime != 0 {
			s.StartedTime = u.started
		}
		if f&modUserPassword != 0 {
			s.UserPassword = u.userPassword
			s.UserPasswordEnabled = u.userPassword != ""
		}
		if f&modGameMode != 0 {
			s.GameMode = u.gameMode
		}
		if f&modDescription != 0 {
			s.Description = u.description
		}
		if f&modMinParticipants != 0 {
			s.MinParticipants = u.minP
		}
		if f&modMaxParticipants != 0 {
			s.MaxParticipants = u.maxP
		}
		if f&modMatchmakeSystemType != 0 {
			s.MatchmakeSystemType = u.systemType
		}
		if f&modCodeword != 0 {
			s.Codeword = u.codeword
		}
	})
	switch code {
	case 0:
	case nex.ResultSessionVoid:
		// The gathering is already gone -- everyone else left first. The
		// update has nothing to change and the console only needs the
		// answer to finish its transition.
	default:
		return req.Error(code)
	}
	return req.Success(nil)
}

// updateSessionHost: u32 gid | (V2) bool migrate owner.
func (m *Matchmaker) updateSessionHost(withOwner bool) Method {
	return func(c *nex.Conn, req *nex.Request) *nex.Response {
		gid, r := gidOf(req)
		migrate := false
		if withOwner {
			migrate = r.Bool()
		}
		if r.Err() != nil {
			return req.Error(nex.ResultInvalidArgument)
		}
		var rest []uint64
		var ownerChanged bool
		m.mu.Lock()
		g := m.gatherings[gid]
		if g == nil || !g.has(c.PID) {
			m.mu.Unlock()
			return req.Error(nex.ResultPermissionDenied)
		}
		g.session.Host = c.PID
		if migrate && g.session.Owner != c.PID {
			g.session.Owner = c.PID
			ownerChanged = true
		}
		rest = slices.Clone(g.participants)
		m.mu.Unlock()
		m.s.Notify(NotificationEvent{Source: c.PID, Type: NotifyHostChanged, Param1: uint64(gid)}, rest...)
		if ownerChanged {
			m.s.Notify(NotificationEvent{Source: c.PID, Type: NotifyOwnershipChanged, Param1: uint64(gid), Param2: c.PID}, rest...)
		}
		return req.Success(nil)
	}
}

// migrateOwnership: u32 gid | List<PID> candidates | (44) bool participantsOnly.
// (36) answers Bool; (44) answers nothing.
//
// Splatfest team battles combine two teams by migrating a gathering's
// ownership rather than by a plain join (NextendoNetwork's reading of
// Kinnay's MatchMaking page, facts only), so this is on Splatoon 2's path
// during a fest.
func (m *Matchmaker) migrateOwnership(v1, hasParticipantsOnly bool) Method {
	return func(c *nex.Conn, req *nex.Request) *nex.Response {
		gid, r := gidOf(req)
		candidates := nex.ReadList(r, (*nex.Reader).PID)
		participantsOnly := false
		if hasParticipantsOnly {
			participantsOnly = r.Bool()
		}
		if r.Err() != nil {
			return req.Error(nex.ResultInvalidArgument)
		}
		var newOwner uint64
		var rest []uint64
		m.mu.Lock()
		g := m.gatherings[gid]
		if g != nil && (g.session.Owner == c.PID || g.session.Host == c.PID) {
			for _, p := range candidates {
				if p == g.session.Owner || m.s.Player(p) == nil {
					continue
				}
				if participantsOnly && !g.has(p) {
					continue
				}
				newOwner = p
				break
			}
			if newOwner != 0 {
				g.session.Owner = newOwner
				rest = slices.Clone(g.participants)
			}
		}
		m.mu.Unlock()
		if newOwner != 0 {
			m.s.Notify(NotificationEvent{Source: c.PID, Type: NotifyOwnershipChanged, Param1: uint64(gid), Param2: newOwner}, rest...)
		}
		if v1 {
			return req.Success(boolBody(newOwner != 0))
		}
		if newOwner == 0 {
			return req.Error(nex.ResultPermissionDenied)
		}
		return req.Success(nil)
	}
}

// updateGatheringOwnership: u32 gid | bool participantsOnly -> Bool. The
// caller takes ownership.
func (m *Matchmaker) updateGatheringOwnership(c *nex.Conn, req *nex.Request) *nex.Response {
	gid, r := gidOf(req)
	participantsOnly := r.Bool()
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	var rest []uint64
	ok := false
	m.mu.Lock()
	if g := m.gatherings[gid]; g != nil && (g.has(c.PID) || !participantsOnly) {
		ok = true
		if g.session.Owner != c.PID {
			g.session.Owner = c.PID
			rest = slices.Clone(g.participants)
		}
	}
	m.mu.Unlock()
	if rest != nil {
		m.s.Notify(NotificationEvent{Source: c.PID, Type: NotifyOwnershipChanged, Param1: uint64(gid), Param2: c.PID}, rest...)
	}
	return req.Success(boolBody(ok))
}

func (m *Matchmaker) updateSessionURL(c *nex.Conn, req *nex.Request) *nex.Response {
	gid, r := gidOf(req)
	url := r.String()
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	code := m.modify(c.PID, gid, false, func(g *gathering) { g.sessionURL = url })
	return req.Success(boolBody(code == 0))
}

func (m *Matchmaker) getSessionURL(c *nex.Conn, req *nex.Request) *nex.Response {
	gid, _ := gidOf(req)
	m.mu.Lock()
	g := m.gatherings[gid]
	var url string
	if g != nil {
		url = g.sessionURL
	}
	m.mu.Unlock()
	w := nex.NewWriter()
	w.Bool(g != nil)
	w.String(url)
	return req.Success(w.Bytes())
}

// ---- who is in it, and where --------------------------------------------

func (m *Matchmaker) participantsOf(gid uint32) ([]uint64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g := m.gatherings[gid]
	if g == nil {
		return nil, false
	}
	return slices.Clone(g.participants), true
}

func (m *Matchmaker) getParticipants(ext bool) Method {
	return func(c *nex.Conn, req *nex.Request) *nex.Response {
		gid, r := gidOf(req)
		if ext {
			_ = r.Bool()
		}
		pids, _ := m.participantsOf(gid)
		w := nex.NewWriter()
		nex.WriteList(w, pids, (*nex.Writer).PID)
		return req.Success(w.Bytes())
	}
}

// getDetailedParticipants -> List<ParticipantDetails{PID, String name, String message, u16 count}>.
func (m *Matchmaker) getDetailedParticipants(ext bool) Method {
	return func(c *nex.Conn, req *nex.Request) *nex.Response {
		gid, r := gidOf(req)
		if ext {
			_ = r.Bool()
		}
		m.mu.Lock()
		var pids []uint64
		msgs := map[uint64]string{}
		if g := m.gatherings[gid]; g != nil {
			pids = slices.Clone(g.participants)
			for k, v := range g.messages {
				msgs[k] = v
			}
		}
		m.mu.Unlock()
		w := nex.NewWriter()
		nex.WriteList(w, pids, func(w *nex.Writer, p uint64) {
			w.Struct(0, func(w *nex.Writer) {
				w.PID(p)
				w.String(m.s.Nickname(p))
				w.String(msgs[p])
				w.U16(1)
			})
		})
		return req.Success(w.Bytes())
	}
}

// Nickname is a player's SwitchNet nickname, "" when unknown.
func (s *Server) Nickname(pid uint64) string {
	if s.store == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	u, err := s.store.GetUser(ctx, UserIDForPID(pid))
	if err != nil {
		return ""
	}
	return u.Nickname
}

func (m *Matchmaker) stationsOfPlayers(viewer *nex.Conn, pids []uint64) []*nex.StationURL {
	var out []*nex.StationURL
	for _, p := range pids {
		if c := m.s.Player(p); c != nil {
			for _, st := range Stations(c) {
				out = append(out, m.s.stationFor(c, viewer, st))
			}
		}
	}
	return out
}

func (m *Matchmaker) getParticipantsURLs(c *nex.Conn, req *nex.Request) *nex.Response {
	gid, _ := gidOf(req)
	pids, _ := m.participantsOf(gid)
	w := nex.NewWriter()
	nex.WriteList(w, m.stationsOfPlayers(c, pids), (*nex.Writer).StationURL)
	return req.Success(w.Bytes())
}

// getParticipantsURLsList: List<u32> -> List<GatheringURLs{u32 gid, List<StationURL>}>.
func (m *Matchmaker) getParticipantsURLsList(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	gids := nex.ReadList(r, (*nex.Reader).U32)
	w := nex.NewWriter()
	nex.WriteList(w, gids, func(w *nex.Writer, gid uint32) {
		pids, _ := m.participantsOf(gid)
		w.Struct(0, func(w *nex.Writer) {
			w.U32(gid)
			nex.WriteList(w, m.stationsOfPlayers(c, pids), (*nex.Writer).StationURL)
		})
	})
	return req.Success(w.Bytes())
}

// replaceURLWait bounds how long a joiner's GetSessionURLs waits for the host
// to re-report its station after its NAT check. The joiner routinely asks
// about a second before the host sends it (NextendoNetwork's measurement,
// facts only), and the stations from before it lack what a probe needs.
const replaceURLWait = 2500 * time.Millisecond

// getSessionURLs: u32 gid -> List<StationURL>: where the host is.
//
// If the host has not re-reported its station yet, the answer waits for it
// -- on its own goroutine, so that the joiner's connection keeps being
// acknowledged meanwhile -- and is sent with whatever the host has when the
// wait runs out, because no answer at all is strictly worse.
func (m *Matchmaker) getSessionURLs(c *nex.Conn, req *nex.Request) *nex.Response {
	gid, _ := gidOf(req)
	m.mu.Lock()
	g := m.gatherings[gid]
	var hostPID uint64
	if g != nil {
		hostPID = g.session.Host
	}
	m.mu.Unlock()
	if g == nil {
		return req.Error(nex.ResultSessionVoid)
	}
	host := m.s.Player(hostPID)
	answer := func() *nex.Response {
		w := nex.NewWriter()
		var urls []*nex.StationURL
		if host != nil {
			urls = m.stationsOfPlayers(c, []uint64{hostPID})
		}
		nex.WriteList(w, urls, (*nex.Writer).StationURL)
		return req.Success(w.Bytes())
	}
	if host == nil || host == c || stationsOf(host).replaced() {
		return answer()
	}
	go func() {
		deadline := time.Now().Add(replaceURLWait)
		for time.Now().Before(deadline) && !stationsOf(host).replaced() {
			select {
			case <-c.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
		_ = c.Reply(answer())
	}()
	return nil
}

// ---- finding and browsing ----------------------------------------------

func (m *Matchmaker) findBySingleID(c *nex.Conn, req *nex.Request) *nex.Response {
	gid, _ := gidOf(req)
	m.mu.Lock()
	g := m.gatherings[gid]
	var s *MatchmakeSession
	if g != nil {
		s = g.copyFor(c.PID)
	}
	m.mu.Unlock()
	w := nex.NewWriter()
	w.Bool(s != nil)
	if s == nil {
		s = &MatchmakeSession{}
	}
	writeSessionHolder(w, s)
	return req.Success(w.Bytes())
}

// findSingle: u32 gid -> MatchmakeSession, or SessionVoid.
func (m *Matchmaker) findSingle(c *nex.Conn, req *nex.Request) *nex.Response {
	gid, _ := gidOf(req)
	m.mu.Lock()
	g := m.gatherings[gid]
	var s *MatchmakeSession
	if g != nil {
		s = g.copyFor(c.PID)
	}
	m.mu.Unlock()
	if s == nil {
		return req.Error(nex.ResultSessionVoid)
	}
	return req.Success(writeSession(s))
}

func (m *Matchmaker) findByGatheringIDs(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	gids := nex.ReadList(r, (*nex.Reader).U32)
	var out []*MatchmakeSession
	m.mu.Lock()
	for _, gid := range gids {
		if g := m.gatherings[gid]; g != nil {
			out = append(out, g.copyFor(c.PID))
		}
	}
	m.mu.Unlock()
	w := nex.NewWriter()
	nex.WriteList(w, out, WriteMatchmakeSession)
	return req.Success(w.Bytes())
}

// findByOwner: PID | ResultRange -> List<MatchmakeSession>.
func (m *Matchmaker) findByOwner(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	owner := r.PID()
	var offset, size uint32
	r.Struct(func(r *nex.Reader, _ uint8) { offset, size = r.U32(), r.U32() })
	var out []*MatchmakeSession
	m.mu.Lock()
	for _, g := range m.sortedLocked() {
		if g.session.Owner == owner {
			out = append(out, g.copyFor(c.PID))
		}
	}
	m.mu.Unlock()
	w := nex.NewWriter()
	nex.WriteList(w, page(out, offset, size), WriteMatchmakeSession)
	return req.Success(w.Bytes())
}

// findByParticipant: FindMatchmakeSessionByParticipantParam ->
// List<FindMatchmakeSessionByParticipantResult{PID, MatchmakeSession}>.
// Only the caller's friends (and the caller) are looked up: which lobby a
// stranger is in is not the caller's business.
func (m *Matchmaker) findByParticipant(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	var pids []uint64
	r.Struct(func(r *nex.Reader, _ uint8) {
		pids = nex.ReadList(r, (*nex.Reader).PID)
		_ = r.U32()
		readBlockList(r)
	})
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	friends := m.s.friendsOf(c.PID)
	type result struct {
		pid uint64
		s   *MatchmakeSession
	}
	var out []result
	m.mu.Lock()
	for _, p := range pids {
		if p != c.PID && !friends[p] {
			continue
		}
		for _, g := range m.sortedLocked() {
			if g.has(p) {
				out = append(out, result{p, g.copyFor(c.PID)})
			}
		}
	}
	m.mu.Unlock()
	w := nex.NewWriter()
	nex.WriteList(w, out, func(w *nex.Writer, x result) {
		w.Struct(0, func(w *nex.Writer) {
			w.PID(x.pid)
			WriteMatchmakeSession(w, x.s)
		})
	})
	return req.Success(w.Bytes())
}

// browse: (4) SearchCriteria | ResultRange -> List<Data<Gathering>>;
// (52) SearchCriteria -> List<MatchmakeSession>. Open, password-free lobbies
// matching the criteria.
func (m *Matchmaker) browse(withRange, holders bool) Method {
	return func(c *nex.Conn, req *nex.Request) *nex.Response {
		r := nex.NewReader(req.Body)
		crit := readSearchCriteria(r)
		offset, size := crit.ResultOffset, crit.ResultSz
		if withRange {
			r.Struct(func(r *nex.Reader, _ uint8) { offset, size = r.U32(), r.U32() })
		}
		if r.Err() != nil {
			return req.Error(nex.ResultInvalidArgument)
		}
		var out []*MatchmakeSession
		m.mu.Lock()
		for _, g := range m.sortedLocked() {
			s := g.session
			if !s.OpenParticipation || s.UserPasswordEnabled || s.SystemPasswordEnabled {
				continue
			}
			if criterionMatches(crit, g, 1) {
				out = append(out, g.copyFor(c.PID))
			}
		}
		m.mu.Unlock()
		out = page(out, offset, size)
		w := nex.NewWriter()
		if holders {
			nex.WriteList(w, out, writeSessionHolder)
		} else {
			nex.WriteList(w, out, WriteMatchmakeSession)
		}
		return req.Success(w.Bytes())
	}
}

func (m *Matchmaker) sortedLocked() []*gathering {
	out := make([]*gathering, 0, len(m.gatherings))
	for _, g := range m.gatherings {
		out = append(out, g)
	}
	slices.SortFunc(out, func(a, b *gathering) int { return int(a.session.ID) - int(b.session.ID) })
	return out
}

// page applies a ResultRange. An offset of 0xFFFFFFFF means "from the
// start" (Pretendo), and a size of 0 means no limit.
func page[T any](in []T, offset, size uint32) []T {
	if offset == 0xFFFFFFFF {
		offset = 0
	}
	if int(offset) >= len(in) {
		return nil
	}
	in = in[offset:]
	if size > 0 && int(size) < len(in) {
		in = in[:size]
	}
	return in
}

// ---- friends ------------------------------------------------------------

// getPlayingSession: List<PID> -> List<PlayingSession{PID, Data<Gathering>}>.
func (m *Matchmaker) getPlayingSession(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	pids := nex.ReadList(r, (*nex.Reader).PID)
	type ps struct {
		pid uint64
		s   *MatchmakeSession
	}
	var out []ps
	for _, p := range m.friendPIDs(c.PID, pids, false) {
		m.mu.Lock()
		for _, g := range m.sortedLocked() {
			if g.has(p) {
				out = append(out, ps{p, g.copyFor(c.PID)})
				break
			}
		}
		m.mu.Unlock()
	}
	w := nex.NewWriter()
	nex.WriteList(w, out, func(w *nex.Writer, x ps) {
		w.Struct(0, func(w *nex.Writer) {
			w.PID(x.pid)
			writeSessionHolder(w, x.s)
		})
	})
	return req.Success(w.Bytes())
}

// friendPIDs filters asked to the caller's friends (and, when includeSelf,
// the caller).
func (m *Matchmaker) friendPIDs(caller uint64, asked []uint64, includeSelf bool) []uint64 {
	friends := m.s.friendsOf(caller)
	var out []uint64
	for _, p := range asked {
		if (p == caller && includeSelf) || (p != caller && friends[p]) {
			out = append(out, p)
		}
	}
	return out
}

// SimplePlayingSession is {PID, u32 gid, u32 game mode, u32 attribute 0}.
type SimplePlayingSession struct {
	PID, GID, GameMode, Attribute0 uint64
}

// SimplePlayingSessions returns, for each of the caller's friends in asked
// that is in a gathering, which one. How a friend-join finds its lobby.
func (m *Matchmaker) SimplePlayingSessions(caller uint64, asked []uint64, includeSelf bool) []SimplePlayingSession {
	var out []SimplePlayingSession
	for _, p := range m.friendPIDs(caller, asked, includeSelf) {
		m.mu.Lock()
		var best *gathering
		for _, g := range m.gatherings {
			if g.has(p) && (best == nil || g.session.ID > best.session.ID) {
				best = g
			}
		}
		if best != nil {
			var a0 uint32
			if len(best.session.Attribs) > 0 {
				a0 = best.session.Attribs[0]
			}
			out = append(out, SimplePlayingSession{PID: p, GID: uint64(best.session.ID),
				GameMode: uint64(best.session.GameMode), Attribute0: uint64(a0)})
		}
		m.mu.Unlock()
	}
	return out
}

// WriteSimplePlayingSessions writes List<SimplePlayingSession>.
func WriteSimplePlayingSessions(w *nex.Writer, list []SimplePlayingSession) {
	nex.WriteList(w, list, func(w *nex.Writer, s SimplePlayingSession) {
		w.Struct(0, func(w *nex.Writer) {
			w.PID(s.PID)
			w.U32(uint32(s.GID))
			w.U32(uint32(s.GameMode))
			w.U32(uint32(s.Attribute0))
		})
	})
}

// getSimplePlayingSession: List<PID> | bool includeLoginUser -> List<SimplePlayingSession>.
func (m *Matchmaker) getSimplePlayingSession(_ bool) Method {
	return func(c *nex.Conn, req *nex.Request) *nex.Response {
		r := nex.NewReader(req.Body)
		pids := nex.ReadList(r, (*nex.Reader).PID)
		includeSelf := r.Bool()
		if r.Err() != nil {
			return req.Error(nex.ResultInvalidArgument)
		}
		w := nex.NewWriter()
		WriteSimplePlayingSessions(w, m.SimplePlayingSessions(c.PID, pids, includeSelf))
		return req.Success(w.Bytes())
	}
}

// updateNotificationData: u32 type | u64 p1 | u64 p2 | String. What a
// player publishes to their friends ("I opened a room"), kept while they are
// connected.
func (m *Matchmaker) updateNotificationData(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	e := NotificationEvent{Source: c.PID, Type: r.U32(), Param1: r.U64(), Param2: r.U64(), StrParam: r.String()}
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	m.mu.Lock()
	if m.notifData[c.PID] == nil {
		m.notifData[c.PID] = map[uint32]NotificationEvent{}
	}
	m.notifData[c.PID][e.Type] = e
	m.mu.Unlock()
	return req.Success(nil)
}

// getFriendNotificationData: (10) s32 type, or (13) List<u32> types ->
// List<NotificationEvent>: what the caller's connected friends published.
func (m *Matchmaker) getFriendNotificationData(list bool) Method {
	return func(c *nex.Conn, req *nex.Request) *nex.Response {
		r := nex.NewReader(req.Body)
		var types []uint32
		if list {
			types = nex.ReadList(r, (*nex.Reader).U32)
		} else {
			types = []uint32{uint32(r.S32())}
		}
		if r.Err() != nil {
			return req.Error(nex.ResultInvalidArgument)
		}
		friends := m.s.friendsOf(c.PID)
		var out []NotificationEvent
		m.mu.Lock()
		for pid, data := range m.notifData {
			if !friends[pid] {
				continue
			}
			for _, t := range types {
				if e, ok := data[t]; ok {
					out = append(out, e)
				}
			}
		}
		m.mu.Unlock()
		slices.SortFunc(out, func(a, b NotificationEvent) int {
			if a.Source != b.Source {
				if a.Source < b.Source {
					return -1
				}
				return 1
			}
			return int(a.Type) - int(b.Type)
		})
		w := nex.NewWriter()
		nex.WriteList(w, out, WriteNotificationEvent)
		return req.Success(w.Bytes())
	}
}
