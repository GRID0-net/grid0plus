// Package matchmaking forms Splatoon 3 matches.
//
// A player creates a ticket, watches it over a stream, and receives a game
// session when enough other players are waiting for the same thing. The queue
// lives in memory: a ticket is worth nothing once the process restarts, because
// the console that owned it has already given up and made a new one. Completed
// matches are persisted, for the dashboard and for the record.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers
package matchmaking

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrNoSession means the named session does not exist, or no longer does.
	ErrNoSession = errors.New("matchmaking: no such session")
	// ErrSessionFull means the session has no room for another player.
	ErrSessionFull = errors.New("matchmaking: session is full")
)

// State is where a ticket is in its life. It mirrors the protocol's own states
// rather than inventing a parallel vocabulary, so the mapping to the wire is
// one-to-one and cannot drift.
type State int

const (
	// StateSearching means the ticket is queued and waiting.
	StateSearching State = iota
	// StatePlacing means a match was formed and the session is being made.
	StatePlacing
	// StateSucceeded means the ticket has a game session to join.
	StateSucceeded
	// StateTimedOut means nobody else turned up in time.
	StateTimedOut
	// StateCancelled means the player backed out.
	StateCancelled
)

// waiting reports whether a ticket is still in the queue, in either sense a
// grace period can leave it: actively Searching, or Placing because a
// countdown toward a match is already running.
func (s State) waiting() bool {
	return s == StateSearching || s == StatePlacing
}

func (s State) String() string {
	switch s {
	case StateSearching:
		return "searching"
	case StatePlacing:
		return "placing"
	case StateSucceeded:
		return "succeeded"
	case StateTimedOut:
		return "timed_out"
	case StateCancelled:
		return "cancelled"
	default:
		return "unspecified"
	}
}

// Ticket is one player's place in the queue.
type Ticket struct {
	ID string
	// UserID is the player, taken from their authenticated session and never
	// from anything the client said about itself.
	UserID string
	// Mode is what they are queuing for. Tickets only match within one mode.
	Mode string
	// FestTeam comes from the account's stored fest entry, never ticket metadata.
	FestTeam string
	// Members are the other players of a squad this ticket queues for, each
	// proved by a delegation naming the ticket's player. Each has a ticket of
	// its own in the queue (Party set to this ticket) so it counts as a
	// player, and the squad is only ever placed whole.
	Members []string
	// Party is, on a squad member's ticket, the ID of the ticket its squad
	// queued with. Members follow that ticket, never their own.
	Party string

	State     State
	CreatedAt time.Time

	// SessionID is set once the ticket succeeds.
	SessionID string
	// MatchedAt is when the ticket succeeded.
	MatchedAt time.Time
}

// Session is a formed match.
type Session struct {
	ID      string
	Mode    string
	Players []string
	// TeamSlots maps fest players onto the game's team1/team2 slots.
	TeamSlots map[string]string

	// MaxPlayers is the size the match was formed at. Carried on the session
	// rather than recomputed from the live mode size, because an operator
	// changing ModeSizes after formation must not retroactively shrink or
	// grow a match that already has its players -- Join checks room against
	// this, not against whatever the mode's size happens to be right now.
	MaxPlayers int

	// Host is the player whose console the others connect to.
	//
	// Splatoon 3's gameplay is peer-to-peer: once a match starts, the consoles
	// talk to each other and GRID0+ is not in the path. All this does is
	// tell them who to reach.
	Host string
	// HostPort is the port the host published via SyncGameSession, once it has
	// one. Zero until then: the host does not necessarily have a listening
	// socket open the instant a match forms, and joiners reading a zero port
	// know to wait rather than dial nothing.
	HostPort int32

	// Room is true for a private room a host opened directly
	// (GameSessionService.CreateGameSessionCreationTicket) rather than a match
	// formed from tickets. A room starts with only its host and fills as
	// friends join by name or code; a match is full the moment it exists.
	Room bool

	CreatedAt time.Time
	// ActiveAt is when a room was last opened, joined or read. Rooms expire
	// from this rather than CreatedAt: a host can sit in a room waiting for
	// friends well past SessionTTL, and pruning it mid-use would strand
	// everyone in it. Matches ignore it -- gameplay is peer-to-peer once one
	// starts, so nothing here would ever refresh it.
	ActiveAt time.Time

	// Seats is each player's 1-based place in the session, the rank Gamesync
	// hands Pia as their upcsid. Kept apart from the order of Players because
	// a player can leave (see Leave): renumbering the rest would give a
	// newcomer the rank a connected console already holds, and Pia elects its
	// host by the lowest one. A seat is never reused, and a player who comes
	// back gets their old one.
	Seats map[string]int
}

// seat gives a player joining the session the next seat, or back their own.
func (s *Session) seat(userID string) {
	if s.Seats[userID] != 0 {
		return
	}
	next := 0
	for _, seat := range s.Seats {
		next = max(next, seat)
	}
	s.Seats[userID] = next + 1
}

// Seat is a player's place in the session, or 0 for someone never in it.
func (s Session) Seat(userID string) int {
	return s.Seats[userID]
}

// Config tunes the queue.
type Config struct {
	// PlayersPerMatch is how many tickets make a match for a mode with no entry
	// in ModeSizes. Turf War is 4v4, so eight.
	PlayersPerMatch int

	// ModeSizes overrides PlayersPerMatch for named modes.
	//
	// The mode table this project has wanted since the queue was written. It is
	// a map rather than a fixed list because the mode strings come from the
	// game, not from here, and a mode nobody has seen a capture of should be
	// settable without a code change.
	ModeSizes map[string]int

	// KnownModes are modes the operator's view always lists, waiting or not,
	// so a size can be set before anyone queues.
	KnownModes []string

	// CoopModes names the modes formed like Salmon Run -- one team, not two --
	// rather than like Turf War/Anarchy. A set because mode strings come from
	// the game and this project has not captured Salmon Run's yet; empty by
	// default, so every mode is versus-shaped until an operator adds one here,
	// which the "unknown mode" log gives them the exact string for.
	CoopModes map[string]bool

	// VersusGracePeriod is how long a versus-shaped match below its mode's
	// size waits, with an even number of players, before forming anyway.
	// Every arrival or departure restarts it, and an odd count stops it until
	// one more player joins. A full match never waits.
	//
	// Zero turns early starts off: versus matches then form only when full.
	VersusGracePeriod time.Duration

	// CoopGracePeriod is how long a Salmon Run-shaped match waits after its
	// SECOND player joins before forming, restarting every time another
	// player joins (up to the mode's size, which forms immediately: there is
	// no point waiting out a clock for a room that cannot take anyone else).
	// A lone player is never on this clock at all -- see formCoopLocked.
	CoopGracePeriod time.Duration

	// TicketTTL is how long a ticket waits before giving up.
	//
	// A ticket that waits forever is worse than one that times out: the console
	// shows a spinner with no way back, and the player force-quits. Timing out
	// lets the game say "no opponents found" and offer to try again.
	TicketTTL time.Duration

	// SessionTTL is how long a formed session stays available to look up.
	//
	// Sessions have to outlive the ticket that produced it -- the console gets
	// a session on its ticket stream and only then calls GetGameSession and
	// JoinGameSession -- but not forever. Gameplay is peer-to-peer once a match
	// starts, so GRID0+ is out of the path and has no way to learn the match
	// ended; without a TTL the map only grows. Long enough to cover joining
	// plus a full match, short enough that a day's play does not accumulate.
	SessionTTL time.Duration
}

// DefaultConfig is a sensible starting point for a small private network.
//
// PlayersPerMatch is 8, the real 4v4 size, but for a versus mode it is a cap
// rather than a target: two, four or six players still get a match once the
// grace period passes with an even count (see formVersusLocked). That is what
// lets a handful of consoles play without waiting for eight, while a full
// lobby still starts at once. Salmon Run-shaped modes take their own size from
// ModeSizes; the real one is four.
//
// The grace periods are the operator's own choice for this deployment rather
// than anything measured -- there is no real server to capture a lobby-fill
// grace period from. Versus is 20 seconds, coop 20.
var DefaultConfig = Config{
	PlayersPerMatch:   8,
	TicketTTL:         3 * time.Minute,
	SessionTTL:        30 * time.Minute,
	VersusGracePeriod: 20 * time.Second,
	CoopGracePeriod:   20 * time.Second,
}

// Queue forms matches from waiting tickets.
//
// Safe for concurrent use: every console has its own goroutine on its own
// stream, and match formation mutates several tickets at once. The lock is held
// across formation rather than per-ticket, because a match half-formed under a
// finer lock is a match some players joined and others did not.
type Queue struct {
	cfg Config
	now func() time.Time

	mu sync.Mutex
	// modeSizes is Config.ModeSizes, mutable at runtime: an operator watching a
	// queue that will not fill needs to be able to shrink a mode without
	// restarting the server and dropping every ticket on it.
	modeSizes map[string]int
	// tricolorDefender is each Tricolor mode's defending fest team.
	tricolorDefender map[string]string
	tickets          map[string]*Ticket
	// waiting is the queue order per mode, holding ticket ids. A slice rather
	// than a heap: it is FIFO, it is short, and the order has to be readable
	// when something goes wrong.
	waiting map[string][]string

	// sessions are the matches formed so far, by id, so a console can look one
	// up after its ticket handed it the id. Pruned by Sweep once SessionTTL has
	// passed.
	sessions map[string]*Session

	// watchers are the streams waiting on a ticket's state to change.
	watchers map[string][]chan Ticket

	// grace is the pending countdown per mode, for the modes currently on one
	// (enough players for versus, or two or more for coop) -- see
	// formVersusLocked/formCoopLocked. Absent from the map means no countdown
	// is running for that mode.
	grace map[string]*graceState

	// backfill are the matched Salmon Run sessions an "Again!" reopened, by
	// id: see OpenBackfill. backfilled collects what filling them did, for
	// TakeBackfills.
	backfill   map[string]bool
	backfilled []Backfill
}

// graceState is one mode's pending countdown.
type graceState struct {
	// deadline is when the pending batch forms.
	deadline time.Time
	// lastCount is the waiting count the deadline was last set from. Coop
	// compares against this on every pass to notice a new joiner and restart
	// its own clock; versus does not use it (its countdown starts once and
	// runs to completion regardless of who else queues behind it).
	lastCount int
	// started is when this countdown was first armed. Re-arming on every
	// arrival or departure let one console that kept re-queuing hold a
	// whole mode in Placing -- exempt from the ticket TTL -- forever.
	started time.Time
}

// maxGraceRestarts caps how far re-arming can push a countdown: never past
// this many grace periods after it was first armed.
const maxGraceRestarts = 3

// regraceLocked (re)arms a mode's countdown for n waiting players, keeping
// when it first started so the deadline cannot drift past the cap.
func (q *Queue) regraceLocked(mode string, period time.Duration, n int) *graceState {
	now := q.now()
	started := now
	if g, ok := q.grace[mode]; ok && !g.started.IsZero() {
		started = g.started
	}
	deadline := now.Add(period)
	if limit := started.Add(maxGraceRestarts * period); deadline.After(limit) {
		deadline = limit
	}
	return &graceState{deadline: deadline, lastCount: n, started: started}
}

// New builds a queue.
func New(cfg Config, now func() time.Time) *Queue {
	if cfg.PlayersPerMatch < 2 {
		cfg.PlayersPerMatch = DefaultConfig.PlayersPerMatch
	}
	if cfg.TicketTTL <= 0 {
		cfg.TicketTTL = DefaultConfig.TicketTTL
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = DefaultConfig.SessionTTL
	}
	if now == nil {
		now = time.Now
	}
	sizes := make(map[string]int, len(cfg.ModeSizes))
	for mode, n := range cfg.ModeSizes {
		if n >= 1 {
			sizes[mode] = n
		}
	}

	return &Queue{
		cfg:       cfg,
		now:       now,
		modeSizes: sizes,
		tickets:   map[string]*Ticket{},
		waiting:   map[string][]string{},
		sessions:  map[string]*Session{},
		watchers:  map[string][]chan Ticket{},
		grace:     map[string]*graceState{},
		backfill:  map[string]bool{},
	}
}

// Enqueue creates a ticket for a player and tries to form a match.
//
// A player may hold only one ticket at a time. A second Enqueue replaces the
// first rather than queueing twice: a console that re-enters matchmaking after
// a network blip has forgotten its old ticket, and leaving that one in the
// queue would match a player who is no longer waiting — the other consoles then
// sit in a lobby that never fills.
func (q *Queue) Enqueue(userID, mode string) (Ticket, []Session, error) {
	return q.EnqueueWithFestTeam(userID, mode, "")
}

func (q *Queue) EnqueueWithFestTeam(userID, mode, team string) (Ticket, []Session, error) {
	return q.EnqueueSquad(userID, nil, mode, team)
}

// EnqueueSquad queues userID together with members, who must be matched in
// the same session. The returned ticket is userID's; every member follows it.
// Every member is given the leader's fest team; EnqueueSquadTeams gives each
// their own.
func (q *Queue) EnqueueSquad(userID string, members []string, mode, team string) (Ticket, []Session, error) {
	return q.EnqueueSquadTeams(userID, members, mode, team, nil)
}

// EnqueueSquadTeams is EnqueueSquad with each member's own fest team.
//
// A Splatfest squad is not always one team. "Continue with this team"
// (fest_match_regular_direct_pair_config) re-queues the whole lobby the
// players just shared, both sides of it, as one squad. Labelling them all with
// the leader's team made the pair look one-sided, and a one-sided fest queue
// never forms. A member missing from memberTeams falls back to team.
func (q *Queue) EnqueueSquadTeams(userID string, members []string, mode, team string, memberTeams map[string]string) (Ticket, []Session, error) {
	if userID == "" {
		return Ticket{}, nil, fmt.Errorf("matchmaking: no user")
	}
	if mode == "" {
		return Ticket{}, nil, fmt.Errorf("matchmaking: no mode")
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	if t := q.justMatchedLocked(userID, mode); t != nil {
		return *t, nil, nil
	}
	q.cancelUserTicketsLocked(userID)
	for _, m := range members {
		q.cancelUserTicketsLocked(m)
	}

	t := &Ticket{
		ID:        uuid.NewString(),
		UserID:    userID,
		Mode:      mode,
		FestTeam:  team,
		Members:   slices.Clone(members),
		State:     StateSearching,
		CreatedAt: q.now(),
	}
	q.tickets[t.ID] = t
	q.waiting[mode] = append(q.waiting[mode], t.ID)
	for _, m := range members {
		mt := &Ticket{
			ID: uuid.NewString(), UserID: m, Mode: mode, FestTeam: memberTeam(memberTeams, m, team),
			Party: t.ID, State: StateSearching, CreatedAt: t.CreatedAt,
		}
		q.tickets[mt.ID] = mt
		q.waiting[mode] = append(q.waiting[mode], mt.ID)
	}

	sessions := q.formMatchesLocked()
	return *q.tickets[t.ID], sessions, nil
}

// duplicateTicketWindow is how soon after a ticket is matched a second ticket
// from the same player, for the same mode, is taken as a repeat of the first.
// A console can send CreateMatchmakingTicket twice 6 ms apart: the first
// completes a four-player match on arrival, the second would be queued as a new
// player and given a match of its own, and the four-player job would start with
// its host alone.
const duplicateTicketWindow = 5 * time.Second

// justMatchedLocked returns userID's ticket for mode that was matched within
// duplicateTicketWindow, if there is one.
func (q *Queue) justMatchedLocked(userID, mode string) *Ticket {
	for _, t := range q.tickets {
		if t.UserID == userID && t.Mode == mode && t.State == StateSucceeded && t.Party == "" &&
			q.now().Sub(t.MatchedAt) < duplicateTicketWindow {
			return t
		}
	}
	return nil
}

func memberTeam(teams map[string]string, user, fallback string) string {
	if t := teams[user]; t != "" {
		return t
	}
	return fallback
}

// Cancel withdraws a ticket.
//
// Cancelling a ticket that already succeeded does nothing: the match exists and
// the other players are already being told to connect. Pulling one player back
// out at that point leaves them waiting for somebody who will not arrive.
func (q *Queue) Cancel(ticketID string) bool {
	ok, _ := q.CancelForming(ticketID)
	return ok
}

// CancelForming is Cancel that also returns any match the departure let
// form, so the caller can record it like any other.
func (q *Queue) CancelForming(ticketID string) (bool, []Session) {
	q.mu.Lock()
	defer q.mu.Unlock()

	t, ok := q.tickets[ticketID]
	if !ok || !t.State.waiting() {
		return false, nil
	}
	q.cancelTicketLocked(t)
	// The rest of the mode's grace period, if one was running, was counting
	// down to include this ticket. Re-evaluate with the mode's own rules --
	// the plain versus ones re-formed Tricolor, Open and Anarchy two-step
	// lobbies into shapes the game never starts -- rather than leave a stale
	// countdown (or the rest of the mode's tickets stuck in StatePlacing).
	return true, q.formModeLocked(t.Mode)
}

// cancelTicketLocked cancels one waiting ticket and, since a squad leaves
// together, its members' tickets with it.
func (q *Queue) cancelTicketLocked(t *Ticket) {
	q.setStateLocked(t, StateCancelled)
	q.removeFromWaitingLocked(t)
	for _, other := range q.tickets {
		if other.Party == t.ID && other.State.waiting() {
			q.setStateLocked(other, StateCancelled)
			q.removeFromWaitingLocked(other)
		}
	}
}

// Get returns a ticket by id.
func (q *Queue) Get(ticketID string) (Ticket, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	t, ok := q.tickets[ticketID]
	if !ok {
		return Ticket{}, false
	}
	return *t, true
}

// Session returns a formed match by id.
//
// Available until SessionTTL has passed and Sweep has run, which is what lets a
// console call GetGameSession and JoinGameSession after its ticket stream
// already handed it the id.
func (q *Queue) Session(sessionID string) (Session, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	s, ok := q.sessions[sessionID]
	if !ok {
		return Session{}, false
	}
	return s.clone(), true
}

// Join adds a player to an existing session, and is idempotent for a player who
// is already in it.
//
// Idempotent rather than an error, because the console calls JoinGameSession
// after its own ticket succeeded — it is joining the match it was just told it
// is in, and answering "you are already here" as a failure strands it on the
// loading screen.
//
// A session with no room left refuses. Silently over-filling would hand a
// ninth player to an eight-player match, and the game rejects that on the
// console with a generic error rather than saying the session was full.
func (q *Queue) Join(sessionID, userID string) (Session, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	s, ok := q.sessions[sessionID]
	if !ok {
		return Session{}, ErrNoSession
	}
	s.ActiveAt = q.now()
	for _, p := range s.Players {
		if p == userID {
			return s.clone(), nil
		}
	}
	if len(s.Players) >= s.MaxPlayers {
		return Session{}, ErrSessionFull
	}
	s.Players = append(s.Players, userID)
	s.seat(userID)
	return s.clone(), nil
}

// Leave removes a player from a session, reporting the session as it now is
// and whether the player was in it.
//
// Splatoon 3 has no "leave" call: a console that quits a room, or drops off
// the network, simply stops talking. Without this its id stays in Players
// forever, and every console that joins afterwards is handed it as a peer to
// connect to -- a connection that can only fail, which the joiner reports as
// 2318-1201. Internal/gamesync decides when a player has gone; this only
// records it.
//
// The session itself is left in place even when it empties: a room is closed
// by its owner (npln), a matched session by Sweep.
func (q *Queue) Leave(sessionID, userID string) (Session, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	s, ok := q.sessions[sessionID]
	if !ok {
		return Session{}, false
	}
	for i, p := range s.Players {
		if p == userID {
			s.Players = append(s.Players[:i], s.Players[i+1:]...)
			return s.clone(), true
		}
	}
	return s.clone(), false
}

// OpenRoom creates a private room with its host as the only player.
//
// Built by the same newSessionLocked as a matched session, so a room is a
// session in every respect GetGameSession, JoinGameSession and gamesync see:
// a second construction path is how the two would drift until one produced
// a session the game will not join.
func (q *Queue) OpenRoom(hostID, mode string, maxPlayers int) (Session, error) {
	if hostID == "" {
		return Session{}, errors.New("matchmaking: room with no host")
	}
	if maxPlayers < 1 {
		return Session{}, errors.New("matchmaking: room with no capacity")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	s := q.newSessionLocked(mode, maxPlayers, []string{hostID}, true)
	return s.clone(), nil
}

// TouchRoom keeps a room alive: see Session.ActiveAt.
func (q *Queue) TouchRoom(sessionID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if s, ok := q.sessions[sessionID]; ok && s.Room {
		s.ActiveAt = q.now()
	}
}

// CloseRoom removes a room, reporting whether there was one. Only rooms:
// a matched session is never closed from outside, since its players are
// already in a game this server has no part in.
func (q *Queue) CloseRoom(sessionID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	s, ok := q.sessions[sessionID]
	if !ok || !s.Room {
		return false
	}
	delete(q.sessions, sessionID)
	return true
}

// SessionsFor returns the sessions a player is in. For GameSessionService's
// per-user lookups.
func (q *Queue) SessionsFor(userID string) []Session {
	q.mu.Lock()
	defer q.mu.Unlock()

	var out []Session
	for _, s := range q.sessions {
		for _, p := range s.Players {
			if p == userID {
				out = append(out, s.clone())
				break
			}
		}
	}
	// Newest first, and by id when two were formed in the same instant — a
	// map range would reorder the answer between two identical calls.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// clone copies a session so a caller cannot mutate the queue's own slice.
func (s *Session) clone() Session {
	out := *s
	out.Players = append([]string(nil), s.Players...)
	out.Seats = maps.Clone(s.Seats)
	return out
}

// PlayersFor returns how many players a match in this mode takes.
func (q *Queue) PlayersFor(mode string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.playersForLocked(mode)
}

// SetHostPort records the port the host published for a session.
//
// The only piece of a synced GameSession this server has no other way to
// learn: participant counts, state and the host itself are already
// authoritative here, but a listening port is the host's own to report.
// Ownership -- that the caller actually IS the host -- is enforced by
// internal/npln, the same place JoinGameSession's membership check lives;
// this package has no notion of a caller at all, only data.
func (q *Queue) SetHostPort(sessionID string, port int32) (Session, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	s, ok := q.sessions[sessionID]
	if !ok {
		return Session{}, false
	}
	s.HostPort = port
	return s.clone(), true
}

// IsPlayer reports whether a user was matched into a session.
//
// The membership check every session-scoped call makes. Kept here rather than
// left to callers because the players list is the queue's to know, and a caller
// that reimplements the check is a caller that can get it subtly wrong.
func (q *Queue) IsPlayer(sessionID, userID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	s, ok := q.sessions[sessionID]
	if !ok {
		return false
	}
	for _, p := range s.Players {
		if p == userID {
			return true
		}
	}
	return false
}

// ForceMatch forms a match in a mode from whoever is waiting, however few that
// is, and returns it.
//
// The escape hatch for the situation this whole package is shaped around: a
// private server where the people who actually turned up are fewer than the
// mode wants. Without it a mode sized for eight never starts for three friends,
// and the queue is correct and useless.
//
// It takes the SAME path as a queue filling normally -- see formSessionLocked --
// so a forced match is a real match in every respect the console can see. What
// it does not do is guess whether the game will accept an undersized session:
// that is unknown, and finding out is the point of the button.
//
// A mode with nobody waiting is (Session{}, false, nil) rather than an error:
// asking to start an empty queue is a no-op, not a mistake.
func (q *Queue) ForceMatch(mode string) (Session, bool, error) {
	if mode == "" {
		return Session{}, false, fmt.Errorf("matchmaking: no mode")
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	if isFestRegularNormalMode(mode) {
		// One-team lobbies in pairs, the only shapes the game proceeds with
		// (see fest_regular.go); "start now" just skips the wait for them.
		var formed []Session
		for {
			a, b := pairedLobbies(q.festUnitsLocked(q.liveWaitingLocked(mode)), 4)
			if a == nil {
				break
			}
			formed = append(formed, q.formSessionLocked(mode, unitIDs(a)), q.formSessionLocked(mode, unitIDs(b)))
		}
		formed = append(formed, q.formSmallFestLobbiesLocked(mode)...)
		delete(q.grace, mode)
		if len(formed) == 0 {
			return Session{}, false, nil
		}
		return formed[0], true, nil
	}

	ids := q.liveWaitingLocked(mode)
	if isFestMode(mode) {
		ids = q.selectFestPlayersLocked(ids, q.playersForLocked(mode))
	}
	if len(ids) == 0 || (q.cfg.CoopModes[mode] && len(ids) < 2) {
		// Salmon Run is never started for one player, not even by force.
		return Session{}, false, nil
	}
	// Everyone waiting, not the first `size` of them: an operator forcing a
	// start means "play with who is here", and leaving somebody in the queue
	// they were about to be matched into is the opposite of that.
	sess := q.formSessionLocked(mode, append([]string(nil), ids...))
	// Whatever countdown was pending is moot: everybody it was waiting on just
	// got a match by another route.
	delete(q.grace, mode)
	return sess, true, nil
}

// SetModeSize changes how many tickets make a match in a mode, and forms any
// match the new size now allows.
//
// The durable fix that ForceMatch is the one-off for: an operator who sets turf
// war to two stops having to press a button every round. Passing zero removes
// the override and returns the mode to PlayersPerMatch.
//
// Matches are formed immediately rather than at the next Enqueue, because the
// tickets that a smaller size just made viable are already waiting and nobody
// is about to arrive to trigger it.
func (q *Queue) SetModeSize(mode string, players int) ([]Session, error) {
	if mode == "" {
		return nil, fmt.Errorf("matchmaking: no mode")
	}
	if players < 0 {
		return nil, fmt.Errorf("matchmaking: a match cannot have %d players", players)
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	if players == 0 {
		delete(q.modeSizes, mode)
	} else {
		q.modeSizes[mode] = players
	}
	return q.formMatchesLocked(), nil
}

// SetGracePeriods changes how long a short match waits for more players,
// without a restart. A countdown already running keeps its deadline; the next
// one uses the new length. Zero turns that kind of early start off, as in
// Config.
func (q *Queue) SetGracePeriods(versus, coop time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cfg.VersusGracePeriod = versus
	q.cfg.CoopGracePeriod = coop
}

// ModeQueue is one mode's queue, for the operator's view.
type ModeQueue struct {
	Mode string
	// Size is how many tickets currently make a match here.
	Size int
	// Overridden is true when Size comes from a mode override rather than from
	// PlayersPerMatch. The dashboard shows the difference, because "8" being a
	// default and "8" having been set are different things to look at.
	Overridden bool
	Waiting    []WaitingTicket
}

// WaitingTicket is one ticket still waiting -- searching, or placing because
// its mode's grace period is counting down.
type WaitingTicket struct {
	ID     string
	UserID string
	// WaitingFor is how long it has been queued.
	WaitingFor time.Duration
	// Placing is true once a grace-period countdown toward a match is
	// running for this ticket's mode.
	Placing bool
}

// Snapshot is everything the operator's view needs, taken under one lock.
//
// One call rather than several accessors: a view assembled from separate reads
// can show a ticket in a queue and the session it was just matched into at the
// same time, which reads as a bug in the server rather than in the page.
type Snapshot struct {
	Modes    []ModeQueue
	Sessions []Session
	// DefaultSize is PlayersPerMatch, for a mode with no override.
	DefaultSize int
	// VersusGrace and CoopGrace are the grace periods in force, which a
	// reload can change without a restart.
	VersusGrace, CoopGrace time.Duration
}

// Snapshot returns the queue and the live sessions.
func (q *Queue) Snapshot() Snapshot {
	q.mu.Lock()
	defer q.mu.Unlock()

	now := q.now()
	out := Snapshot{
		DefaultSize: q.cfg.PlayersPerMatch,
		VersusGrace: q.cfg.VersusGracePeriod, CoopGrace: q.cfg.CoopGracePeriod,
	}

	// Every mode with somebody waiting, plus every mode with an override, so a
	// size the operator set stays visible when its queue empties -- otherwise
	// the control they just used disappears.
	modes := map[string]bool{}
	for _, mode := range sortedKeys(q.waiting) {
		if len(q.liveWaitingLocked(mode)) > 0 {
			modes[mode] = true
		}
	}
	for mode := range q.modeSizes {
		modes[mode] = true
	}
	for _, mode := range q.cfg.KnownModes {
		modes[mode] = true
	}

	names := make([]string, 0, len(modes))
	for mode := range modes {
		names = append(names, mode)
	}
	sort.Strings(names)

	for _, mode := range names {
		_, overridden := q.modeSizes[mode]
		mq := ModeQueue{
			Mode:       mode,
			Size:       q.playersForLocked(mode),
			Overridden: overridden,
		}
		for _, id := range q.liveWaitingLocked(mode) {
			t := q.tickets[id]
			mq.Waiting = append(mq.Waiting, WaitingTicket{
				ID:         t.ID,
				UserID:     t.UserID,
				WaitingFor: now.Sub(t.CreatedAt),
				Placing:    t.State == StatePlacing,
			})
		}
		out.Modes = append(out.Modes, mq)
	}

	for _, s := range q.sessions {
		out.Sessions = append(out.Sessions, s.clone())
	}
	// Newest first: the match that just formed is the one being looked for.
	sort.Slice(out.Sessions, func(i, j int) bool {
		return out.Sessions[i].CreatedAt.After(out.Sessions[j].CreatedAt)
	})

	return out
}

// Watch returns a channel that receives the ticket every time its state
// changes, and a function to stop watching.
//
// Buffered, and a send that would block is dropped rather than waited on. A
// console that has stopped reading its stream must not be able to stall match
// formation for everybody else — the same reasoning as presencebus publishing
// on the request path.
func (q *Queue) Watch(ticketID string) (<-chan Ticket, func()) {
	ch := make(chan Ticket, 8)

	q.mu.Lock()
	defer q.mu.Unlock()

	t, ok := q.tickets[ticketID]
	if !ok {
		close(ch)
		return ch, func() {}
	}
	// Send the current state immediately: a watcher that attaches after the
	// ticket already succeeded would otherwise wait for a change that has
	// already happened.
	ch <- *t
	q.watchers[ticketID] = append(q.watchers[ticketID], ch)

	return ch, func() { q.unwatch(ticketID, ch) }
}

func (q *Queue) unwatch(ticketID string, ch chan Ticket) {
	q.mu.Lock()
	defer q.mu.Unlock()

	list := q.watchers[ticketID]
	for i, c := range list {
		if c == ch {
			q.watchers[ticketID] = append(list[:i], list[i+1:]...)
			close(ch)
			return
		}
	}
}

// Sweep times out tickets that have waited too long, and returns them. It also
// discards sessions and tickets that are old enough that nobody can still be
// asking about them, and forms any match whose grace period has run out --
// see formVersusLocked/formCoopLocked, both of which only start counting down
// on a ticket event and otherwise need this timer to ever finish counting.
//
// Called on a timer by the service. Not folded into Enqueue: a queue nobody is
// joining is exactly the one whose tickets need to expire (or whose grace
// period needs to end), and doing it only on arrival means the last player to
// give up, or to be kept waiting by a countdown, waits forever.
func (q *Queue) Sweep() (expired []Ticket, formed []Session) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.pruneLocked()

	deadline := q.now().Add(-q.cfg.TicketTTL)

	for _, mode := range sortedKeys(q.waiting) {
		for _, id := range append([]string(nil), q.waiting[mode]...) {
			t := q.tickets[id]
			// StatePlacing is excluded deliberately: a ticket mid-countdown
			// toward a match is not "nobody else turned up", and timing it out
			// here would strand the rest of its batch waiting on one that no
			// longer exists.
			if t == nil || t.State != StateSearching {
				continue
			}
			if t.CreatedAt.After(deadline) {
				// FIFO, so everything after this is younger still.
				break
			}
			q.setStateLocked(t, StateTimedOut)
			q.removeFromWaitingLocked(t)
			expired = append(expired, *t)
		}
	}
	formed = q.formMatchesLocked()
	return expired, formed
}

// Waiting returns how many tickets are queued per mode. For the dashboard.
func (q *Queue) Waiting() map[string]int {
	q.mu.Lock()
	defer q.mu.Unlock()

	out := make(map[string]int, len(q.waiting))
	for mode, ids := range q.waiting {
		n := 0
		for _, id := range ids {
			// Placing counts too: a mode mid-countdown is not an empty queue.
			if t := q.tickets[id]; t != nil && t.State.waiting() {
				n++
			}
		}
		if n > 0 {
			out[mode] = n
		}
	}
	return out
}

// ---- internals, all called with the lock held ----------------------------

// pruneLocked drops sessions and finished tickets older than SessionTTL.
//
// Gameplay is peer-to-peer, so nothing tells GRID0+ a match ended and the
// only honest signal is age. Tickets are pruned on the same clock rather than a
// shorter one: a ticket outlives its own TTL once it succeeds, because it is
// what the console reads its session id off, and dropping it while the session
// it points at is still live would make TrackMatchmakingTicket answer NotFound
// mid-match.
func (q *Queue) pruneLocked() {
	deadline := q.now().Add(-q.cfg.SessionTTL)

	for id, s := range q.sessions {
		since := s.CreatedAt
		if s.Room {
			since = s.ActiveAt
		}
		if since.Before(deadline) {
			delete(q.sessions, id)
		}
	}
	for id, t := range q.tickets {
		if t.State == StateSearching || !t.CreatedAt.Before(deadline) {
			continue
		}
		// A watcher still attached is a stream still open. Leave the ticket
		// alone: unwatch is what closes those channels, and deleting the ticket
		// out from under one strands the console on a stream that will never
		// say anything again.
		if len(q.watchers[id]) > 0 {
			continue
		}
		delete(q.tickets, id)
	}
}

// formMatchesLocked forms every match a mode is currently ready for -- either
// immediately (no grace period configured) or once its grace period has run
// out. Call on every state change (a ticket arrives, cancels, or times out)
// AND on a timer (Sweep): a grace period that started because enough players
// arrived can only finish counting down between ticket events, when nothing
// else has happened to call this again.
func (q *Queue) formMatchesLocked() []Session {
	var formed []Session

	for _, mode := range sortedKeys(q.waiting) {
		formed = append(formed, q.formModeLocked(mode)...)
	}
	return formed
}

// formModeLocked forms what one mode is ready for, by that mode's rules.
func (q *Queue) formModeLocked(mode string) []Session {
	switch {
	case q.cfg.CoopModes[mode]:
		return q.formCoopLocked(mode)
	case isTricolorTeamMode(mode):
		return q.formTricolorTeamLocked(mode)
	case isBankaraTeamMode(mode):
		return q.formBankaraTeamLocked(mode)
	case isBankaraChallengeMode(mode):
		return q.formBankaraChallengeLocked(mode)
	case isFestRegularNormalMode(mode):
		return q.formFestRegularNormalLocked(mode)
	case isFestMode(mode) && isDirectPairMode(mode):
		return q.formFestDirectPairLocked(mode)
	default:
		return q.formVersusLocked(mode)
	}
}

// formVersusLocked forms a Turf War/Anarchy-shaped match: two even teams.
//
// The mode's size is a cap, not a target. A full room forms immediately --
// there is nobody left to wait for. Below the cap, a match can still start,
// but only with an even number of players, so the two teams are equal: an
// even count of at least two starts a countdown, and if it runs out with the
// count unchanged the match forms with everyone waiting. Anyone arriving or
// leaving during the countdown resets it -- an odd count cancels it outright
// and waits for one more player, and a new even count starts it afresh -- so
// a player who queues a second late gets into the match instead of watching
// it leave without them.
//
// A zero grace period turns early starts off entirely: matches then form only
// when full.
func (q *Queue) formVersusLocked(mode string) []Session {
	size := q.playersForLocked(mode)
	var formed []Session

	ids := q.liveWaitingLocked(mode)
	if isFestMode(mode) {
		ids = q.selectFestPlayersLocked(ids, size)
	}
	// Two even teams with every squad whole; fest and Tricolor lobbies are
	// chosen by fest team above instead.
	pick := q.wholeSquadsLocked
	if !isFestMode(mode) && !isTricolorMode(mode) {
		pick = q.evenSquadsLocked
	}
	for len(ids) >= size {
		chosen := pick(ids, size)
		if len(chosen) < size {
			break
		}
		delete(q.grace, mode)
		formed = append(formed, q.formVersusSessionLocked(mode, chosen))
		ids = q.liveWaitingLocked(mode)
		if isFestMode(mode) {
			ids = q.selectFestPlayersLocked(ids, size)
		}
	}
	waiting := len(ids)
	if len(ids) > size || (!isFestMode(mode) && !isTricolorMode(mode)) {
		// Squads that cannot fill a match exactly wait on the clock below
		// with a set that fits -- for a plain versus mode, one that also
		// splits into two equal teams.
		ids = pick(ids, size)
	}

	if isDirectPairMode(mode) && len(ids) >= 2 && q.oneSquadLocked(ids) &&
		len(q.liveWaitingLocked(directPairOpenMode(mode))) == 0 {
		// "Continue with this team" re-queues the lobby the players just shared, both
		// fest teams in one squad. When nobody else is queued for Splatfest Open, nobody
		// is coming for it, so waiting out the grace period only delays the match. When
		// others are, it waits like any squad: starting it alone would split six players
		// into three 1v1s.
		q.cancelGraceLocked(mode)
		return append(formed, q.formSessionLocked(mode, ids))
	}

	n := len(ids)
	// Tricolor's three sides are never two even halves; its selection above
	// already decides which counts can play.
	// An odd number waiting waits for one more rather than leaving the
	// odd one out of a match that starts without them.
	odd := (n%2 != 0 || (waiting <= size && waiting%2 != 0)) && !isTricolorMode(mode)
	if q.cfg.VersusGracePeriod <= 0 || n < 2 || odd {
		q.cancelGraceLocked(mode)
		return formed
	}

	g, pending := q.grace[mode]
	if !pending || n != g.lastCount {
		q.grace[mode] = q.regraceLocked(mode, q.cfg.VersusGracePeriod, n)
		q.setModeStateLocked(mode, StatePlacing)
		return formed
	}
	if q.now().Before(g.deadline) {
		return formed
	}
	delete(q.grace, mode)
	return append(formed, q.formVersusSessionLocked(mode, ids))
}

// isTricolorTeamMode reports fest_match_tricolor_team_config, the first of
// Tricolor's two steps.
func isTricolorTeamMode(mode string) bool {
	return isTricolorMode(mode) && strings.Contains(strings.ToLower(mode), "team")
}

// tricolorSlot is a fest team's slot in a Tricolor match: the defender in
// team1, the two attackers in team2 and team3 in name order. It depends on
// the team alone, so a pair gathered in fest_match_tricolor_team_config keeps
// the slot it will play in.
func tricolorSlot(team, defender string) string {
	if team == defender {
		return "team1"
	}
	var attackers []string
	for _, t := range []string{"Alpha", "Bravo", "Charlie"} {
		if t != defender {
			attackers = append(attackers, t)
		}
	}
	if len(attackers) == 2 && team == attackers[1] {
		return "team3"
	}
	return "team2"
}

// formTricolorTeamLocked forms fest_match_tricolor_team_config lobbies.
//
// That queue is not the match. Its lobby is a pair of one fest team, which then
// re-queues together on fest_match_tricolor_oneshot_config to meet the other two
// teams. The game waits until the lobby holds exactly two players (match kind 8),
// so a mixed lobby never starts. A pair forms as soon as two players of one team
// wait; a player with no partner gets a lobby alone, which a patched game accepts,
// once every team is waiting and the grace period has passed.
func (q *Queue) formTricolorTeamLocked(mode string) []Session {
	var formed []Session
	// Friends of one team who queued together are the pair; strangers pair
	// in arrival order. A squad that cannot be one pair -- bigger than two,
	// or spanning teams -- is placed by member, as before.
	byTeam := map[string][]string{}
	var teams []string
	add := func(team string, ids ...string) {
		if _, seen := byTeam[team]; !seen {
			teams = append(teams, team)
		}
		byTeam[team] = append(byTeam[team], ids...)
	}
	for _, u := range q.festUnitsLocked(q.liveWaitingLocked(mode)) {
		switch {
		case u.team != "" && len(u.ids) == 2:
			formed = append(formed, q.formSessionLocked(mode, u.ids))
		case u.team != "" && len(u.ids) == 1:
			add(u.team, u.ids...)
		default:
			for _, id := range u.ids {
				if team := q.tickets[id].FestTeam; team != "" {
					add(team, id)
				}
			}
		}
	}
	var singles []string
	for _, team := range teams {
		ids := byTeam[team]
		for len(ids) >= 2 {
			formed = append(formed, q.formSessionLocked(mode, ids[:2]))
			ids = ids[2:]
		}
		singles = append(singles, ids...)
	}

	// A player alone only gets a lobby once all three fest teams are waiting
	// -- here, or already paired and waiting on the oneshot queue -- since a
	// Tricolor match needs every team. The grace period restarts whenever
	// anyone joins, so a late arrival is not left behind.
	present := map[string]bool{}
	for _, id := range append(q.liveWaitingLocked(mode), q.liveWaitingLocked(tricolorOneshotMode(mode))...) {
		if team := q.tickets[id].FestTeam; team != "" {
			present[team] = true
		}
	}
	if len(singles) == 0 || len(present) < 3 || q.cfg.VersusGracePeriod <= 0 {
		q.cancelGraceLocked(mode)
		return formed
	}
	n := len(q.liveWaitingLocked(mode)) + len(q.liveWaitingLocked(tricolorOneshotMode(mode)))
	g, pending := q.grace[mode]
	if !pending || n != g.lastCount {
		q.grace[mode] = q.regraceLocked(mode, q.cfg.VersusGracePeriod, n)
		return formed
	}
	if q.now().Before(g.deadline) {
		return formed
	}
	delete(q.grace, mode)
	for _, id := range singles {
		formed = append(formed, q.formSessionLocked(mode, []string{id}))
	}
	return formed
}

// tricolorOneshotMode is the oneshot queue a tricolor_team queue hands on to.
func tricolorOneshotMode(teamMode string) string {
	return strings.Replace(teamMode, "tricolor_team_config", "tricolor_oneshot_config", 1)
}

// directPairOpenMode is the ordinary Splatfest Open queue beside a
// continue-with-this-team queue.
func directPairOpenMode(mode string) string {
	return strings.Replace(mode, "direct_pair_config", "normal_config", 1)
}

func isDirectPairMode(mode string) bool {
	return strings.Contains(strings.ToLower(mode), "direct_pair")
}

// oneSquadLocked reports whether every ticket in ids belongs to one squad.
func (q *Queue) oneSquadLocked(ids []string) bool {
	leader := ""
	for _, id := range ids {
		t := q.tickets[id]
		party := t.Party
		if party == "" {
			party = t.ID
		}
		if leader == "" {
			leader = party
		} else if party != leader {
			return false
		}
	}
	return leader != ""
}

func isFestMode(mode string) bool { return strings.Contains(strings.ToLower(mode), "fest") }

// isTricolorMode reports a Splatfest Tricolor Turf War config
// (fest_match_tricolor_oneshot_config, fest_match_tricolor_team_config).
func isTricolorMode(mode string) bool { return strings.Contains(strings.ToLower(mode), "tricolor") }

// isServerTeamedMode reports the solo-queue ranked configs whose tickets carry
// no team: Anarchy Open, every Challenge and X Battle. Nextendo's server, which
// ran Anarchy Open on real consoles, splits those players into team1 and team2
// itself; left empty, every player is on one side and the host never starts
// the battle (Anarchy Open on grid0, 2026-10-21). Turf War is not in the list:
// its tickets also carry no team and it works without one.
//
// UNCONFIRMED for league_match and x_match: Nextendo only did this for
// bankara_match_open. They share the oneshot ticket shape, which is the
// reason they are included; a Challenge on hardware settles it.
func isServerTeamedMode(mode string) bool {
	m := strings.ToLower(mode)
	return strings.Contains(m, "bankara_match_open") ||
		strings.Contains(m, "league_match") ||
		strings.Contains(m, "x_match")
}

// splitTeams puts each squad, whole, on whichever side has fewer players so
// far, largest squads first, so the sides end up as even as squads allow.
func splitTeams(tickets map[string]*Ticket, chosen []string) map[string]string {
	squads := map[string][]string{}
	var order []string
	for _, id := range chosen {
		t := tickets[id]
		key := t.ID
		if t.Party != "" {
			key = t.Party
		}
		if _, ok := squads[key]; !ok {
			order = append(order, key)
		}
		squads[key] = append(squads[key], t.UserID)
	}
	slices.SortStableFunc(order, func(a, b string) int { return len(squads[b]) - len(squads[a]) })
	slots := map[string]string{}
	sizes := map[string]int{}
	for _, key := range order {
		side := "team1"
		if sizes["team2"] < sizes["team1"] {
			side = "team2"
		}
		for _, u := range squads[key] {
			slots[u] = side
		}
		sizes[side] += len(squads[key])
	}
	return slots
}

func (q *Queue) modeOf(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	if t := q.tickets[ids[0]]; t != nil {
		return t.Mode
	}
	return ""
}

// SetTricolorDefender records which fest team defends in a Tricolor mode: the
// team that led at the midterm (see npln, which knows the fest).
func (q *Queue) SetTricolorDefender(mode, team string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.tricolorDefender == nil {
		q.tricolorDefender = map[string]string{}
	}
	q.tricolorDefender[mode] = team
}

// selectTricolorPlayersLocked forms a Tricolor match: the defending team against
// two attacking teams with half as many players each -- 4 v 2 v 2 when full. An
// official Tricolor battle has four defenders, all of the midterm winner's team,
// against two pairs, and team1/team2/team3 slots. Below full size the same 2:1:1
// ratio is kept so a small group can play, and when even 2 v 1 v 1 is not possible
// but every team has a player, the match forms 1 v 1 v 1: a group too small for the
// official shape should still get to play Tricolor rather than wait forever. Result
// grouped defender first, then each attacker.
func (q *Queue) selectTricolorPlayersLocked(ids []string, size int) []string {
	defender := q.tricolorDefender[q.modeOf(ids)]
	// Whole units: a pair from fest_match_tricolor_team_config re-queues as
	// one squad and must play together.
	by, teams := festTeams(q.festUnitsLocked(ids))
	if defender == "" || len(by[defender]) == 0 {
		return nil
	}
	var attackers []string
	for _, team := range teams {
		if team != defender {
			attackers = append(attackers, team)
		}
	}
	if len(attackers) < 2 {
		return nil
	}
	sort.Strings(attackers)
	for k := size / 4; k >= 1; k-- {
		d, a, b := pickUnits(by[defender], 2*k), pickUnits(by[attackers[0]], k), pickUnits(by[attackers[1]], k)
		if d != nil && a != nil && b != nil {
			return append(append(unitIDs(d), unitIDs(a)...), unitIDs(b)...)
		}
	}
	d, a, b := pickUnits(by[defender], 1), pickUnits(by[attackers[0]], 1), pickUnits(by[attackers[1]], 1)
	if d == nil || a == nil || b == nil {
		return nil
	}
	return append(append(unitIDs(d), unitIDs(a)...), unitIDs(b)...)
}

// selectFestPlayersLocked requires two different fest teams with equal
// numbers. The result is grouped by team so the session can assign stable
// team1/team2 slots. Extra tickets remain queued for the next match.
func (q *Queue) selectFestPlayersLocked(ids []string, size int) []string {
	if isTricolorMode(q.modeOf(ids)) {
		return q.selectTricolorPlayersLocked(ids, size)
	}
	// Whole squads, so the session never holds half of one. Of the team pairs
	// that can play, the biggest match wins, and on a tie the pair holding
	// the longest-waiting player: picking the two biggest teams left a third
	// team waiting for as long as the other two kept queuing.
	by, teams := festTeams(q.festUnitsLocked(ids))
	// The size preference alone still starved a smaller team: Alpha and
	// Bravo kept making 4v4s while two Charlie players waited out the TTL.
	// Once the longest-waiting team has waited past festStarvation, a match
	// including it wins -- if one can start at all: without early starts
	// only a full match forms, and holding a full Alpha v Bravo back for a
	// Charlie match that cannot form would starve both instead.
	best := bestFestPair(by, teams, size, false)
	if len(teams) > 0 && q.now().Sub(q.tickets[by[teams[0]][0].ids[0]].CreatedAt) >= festStarvation {
		if first := bestFestPair(by, teams, size, true); len(first) == size || (len(first) > 0 && q.cfg.VersusGracePeriod > 0) {
			best = first
		}
	}
	return best
}

// bestFestPair is the biggest whole-squad match between two fest teams, the
// longest-waiting pair on a tie; firstOnly limits it to pairs including
// teams[0].
func bestFestPair(by map[string][]festUnit, teams []string, size int, firstOnly bool) []string {
	var best []string
	for i := 0; i < len(teams) && !(firstOnly && i > 0); i++ {
		for j := i + 1; j < len(teams); j++ {
			for k := size / 2; k > len(best)/2; k-- {
				a, b := pickUnits(by[teams[i]], k), pickUnits(by[teams[j]], k)
				if a != nil && b != nil {
					best = append(unitIDs(a), unitIDs(b)...)
					break
				}
			}
		}
	}
	return best
}

// festStarvation is how long a fest team's longest-waiting player waits
// before that team is guaranteed the next match it can be part of.
const festStarvation = time.Minute

// formCoopLocked forms a Salmon Run-shaped match: one team, no even split.
// Nobody waits on a clock alone -- a countdown only exists once a second
// player is present, and it restarts every time another one joins, so a
// trickle of arrivals does not lose the ones already waiting to a clock that
// started before they got there. Reaching the mode's size forms immediately:
// a full room has nobody left to wait for.
func (q *Queue) formCoopLocked(mode string) []Session {
	q.backfillCoopLocked(mode)
	size := q.playersForLocked(mode)

	ids := q.liveWaitingLocked(mode)
	switch {
	case len(ids) == 0:
		q.cancelGraceLocked(mode)
		return nil
	case len(ids) == 1:
		// Alone: no countdown runs at all, however long that lasts.
		q.cancelGraceLocked(mode)
		return nil
	case len(ids) >= size:
		if chosen := q.wholeSquadsLocked(ids, size); len(chosen) == size {
			delete(q.grace, mode)
			return []Session{q.formSessionLocked(mode, chosen)}
		}
		ids = q.wholeSquadsLocked(ids, size)
	}
	// Keeping whole squads in arrival order can leave one player: a solo
	// queued ahead of a squad too big to join them. Taking the biggest
	// squads first lets that squad play; Salmon Run never starts with one.
	if len(ids) < 2 {
		ids = q.wholeSquadsLocked(q.biggestSquadsFirstLocked(q.liveWaitingLocked(mode)), size)
	}
	if len(ids) < 2 {
		q.cancelGraceLocked(mode)
		return nil
	}

	if q.cfg.CoopGracePeriod <= 0 {
		return []Session{q.formSessionLocked(mode, ids)}
	}

	g, pending := q.grace[mode]
	if !pending || len(ids) != g.lastCount {
		// First player past solo, or a new arrival since the clock was last
		// set: (re)start it from here.
		q.grace[mode] = q.regraceLocked(mode, q.cfg.CoopGracePeriod, len(ids))
		q.setModeStateLocked(mode, StatePlacing)
		return nil
	}
	if q.now().Before(g.deadline) {
		return nil
	}
	delete(q.grace, mode)
	return []Session{q.formSessionLocked(mode, ids)}
}

// Backfill is players added to a session reopened by OpenBackfill.
type Backfill struct {
	Session Session
	Added   []string
}

// OpenBackfill lets the queue add waiting Salmon Run players to a matched
// session whose crew pressed "Again!", up to the mode's size, until
// CloseBackfill. It reports the session and whether it took the request.
//
// Pressing "Again!" sends no ticket. Every console reopens the session it
// just played in -- docs/__gs/m with cp and ebf true -- and Splatoon 3's
// lobby then waits for players to arrive in that session (see
// internal/gamesync's again_queue.go). For Salmon Run the game never goes
// back to matchmaking by itself: its ready check (0x71038aab34) sends only
// Anarchy and Splatfest lobbies short of their size to State::ActivateBackFill,
// and lets Salmon Run start with whoever is there. Filling the open seats is
// the server's job, so a crew of fewer than four that presses "Again!" gathers
// players the way it did when it first queued.
//
// Only matched Salmon Run sessions: a room is filled by its friends, and a
// Versus rematch has two teams this does not know how to balance.
func (q *Queue) OpenBackfill(sessionID string) (Session, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	s, ok := q.sessions[sessionID]
	if !ok || s.Room || !q.cfg.CoopModes[s.Mode] {
		return Session{}, false
	}
	// A match's capacity is the size it formed at (see formSessionLocked),
	// so a crew of two would otherwise stay full at two.
	s.MaxPlayers = max(s.MaxPlayers, q.playersForLocked(s.Mode))
	q.backfill[sessionID] = true
	q.backfillCoopLocked(s.Mode)
	return s.clone(), true
}

// CloseBackfill stops adding players to a session, once its lobby locked.
func (q *Queue) CloseBackfill(sessionID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.backfill, sessionID)
}

// TakeBackfills returns, and forgets, the players added to reopened sessions
// since the last call.
func (q *Queue) TakeBackfills() []Backfill {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.backfilled
	q.backfilled = nil
	return out
}

// UNCONFIRMED: no console has yet been seen accepting a ticket into a session
// that already played a job; the newcomer's IssueToken and @@Profile in the
// server log after "npln: players joined an Again! lobby" settle it. Change only
// this function if not.
//
// backfillCoopLocked moves waiting tickets of a mode into its reopened
// sessions, oldest session first, whole squads in queue order. Before a new
// match is formed from the same tickets: players who are searching while a
// crew holds open seats join that crew, which is what the crew is waiting
// for.
func (q *Queue) backfillCoopLocked(mode string) {
	var open []*Session
	for id := range q.backfill {
		s, ok := q.sessions[id]
		if !ok {
			delete(q.backfill, id)
			continue
		}
		if s.Mode == mode {
			open = append(open, s)
		}
	}
	sort.Slice(open, func(i, j int) bool {
		if !open[i].CreatedAt.Equal(open[j].CreatedAt) {
			return open[i].CreatedAt.Before(open[j].CreatedAt)
		}
		return open[i].ID < open[j].ID
	})
	for _, s := range open {
		free := s.MaxPlayers - len(s.Players)
		if free <= 0 {
			continue
		}
		chosen := slices.Clone(q.wholeSquadsLocked(q.liveWaitingLocked(mode), free))
		if len(chosen) == 0 {
			continue
		}
		added := make([]string, 0, len(chosen))
		for _, id := range chosen {
			t := q.tickets[id]
			s.Players = append(s.Players, t.UserID)
			s.seat(t.UserID)
			added = append(added, t.UserID)
			t.SessionID = s.ID
			t.MatchedAt = q.now()
			q.setStateLocked(t, StateSucceeded)
			q.removeFromWaitingLocked(t)
		}
		s.ActiveAt = q.now()
		q.backfilled = append(q.backfilled, Backfill{Session: s.clone(), Added: added})
	}
}

// cancelGraceLocked clears a mode's pending countdown, if any, and puts its
// waiting tickets back to Searching -- they were told a match was forming
// (StatePlacing) and that stopped being true.
func (q *Queue) cancelGraceLocked(mode string) {
	if _, pending := q.grace[mode]; !pending {
		return
	}
	delete(q.grace, mode)
	q.setModeStateLocked(mode, StateSearching)
}

// setModeStateLocked moves every still-searching-or-placing ticket in a mode
// to a state, for the grace-period transition in both directions.
func (q *Queue) setModeStateLocked(mode string, state State) {
	for _, id := range q.waiting[mode] {
		if t := q.tickets[id]; t != nil && t.State.waiting() {
			q.setStateLocked(t, state)
		}
	}
}

// formSessionLocked turns a chosen set of waiting tickets into a session.
//
// The only place a session is created. Force-starting takes the same path as
// filling a queue normally, so a forced match is a real match in every respect
// the console can see -- the alternative, a second construction path, is how
// the two drift until one of them produces a session the game will not join.
func (q *Queue) formSessionLocked(mode string, chosen []string) Session {
	// Callers pass a slice of the waiting list itself, and removing each
	// ticket below shifts that list in place. Without a copy the loop skips
	// every other ticket, leaving players in the match whose tickets still
	// say they are searching.
	chosen = slices.Clone(chosen)
	players := make([]string, 0, len(chosen))
	for _, id := range chosen {
		players = append(players, q.tickets[id].UserID)
	}
	// A match's capacity is the number it formed with, not the mode's cap: an
	// early four-player versus match is full at four, and a latecomer is
	// matched into the next one rather than squeezed into this.
	s := q.newSessionLocked(mode, len(players), players, false)
	if isTricolorMode(mode) && len(chosen) > 0 {
		// Grouped defender first, then the two attackers in name order (see
		// selectTricolorPlayersLocked). Which slot the defender takes is not measured;
		// team1 is assumed.
		defender := q.tricolorDefender[mode]
		s.TeamSlots = map[string]string{}
		for _, id := range chosen {
			s.TeamSlots[q.tickets[id].UserID] = tricolorSlot(q.tickets[id].FestTeam, defender)
		}
		q.sessions[s.ID].TeamSlots = s.TeamSlots
	} else if isFestMode(mode) && len(chosen) > 0 {
		firstTeam := q.tickets[chosen[0]].FestTeam
		s.TeamSlots = map[string]string{}
		for _, id := range chosen {
			slot := "team2"
			if q.tickets[id].FestTeam == firstTeam {
				slot = "team1"
			}
			s.TeamSlots[q.tickets[id].UserID] = slot
		}
		q.sessions[s.ID].TeamSlots = s.TeamSlots
	} else if isServerTeamedMode(mode) && len(chosen) > 0 {
		s.TeamSlots = splitTeams(q.tickets, chosen)
		q.sessions[s.ID].TeamSlots = s.TeamSlots
	}

	for _, id := range chosen {
		t := q.tickets[id]
		t.SessionID = s.ID
		t.MatchedAt = q.now()
		q.setStateLocked(t, StateSucceeded)
		q.removeFromWaitingLocked(t)
	}
	return s
}

// newSessionLocked builds and records a session. The one construction path
// for both a matched session and a room.
func (q *Queue) newSessionLocked(mode string, maxPlayers int, players []string, room bool) Session {
	now := q.now()
	s := Session{
		ID:         uuid.NewString(),
		Mode:       mode,
		MaxPlayers: maxPlayers,
		Players:    players,
		Room:       room,
		CreatedAt:  now,
		ActiveAt:   now,
		Seats:      make(map[string]int, len(players)),
	}
	for i, p := range players {
		s.Seats[p] = i + 1
	}
	// The first player hosts: for a match the longest-waiting one, for a room
	// the one who opened it. Arbitrary for a match but not random: it is
	// stable, it is explainable to somebody reading a match log, and it
	// spreads hosting over time rather than always picking whoever happens to
	// be first in a map.
	s.Host = s.Players[0]
	q.sessions[s.ID] = &s
	return s.clone()
}

// playersForLocked is how many tickets make a match in a mode.
func (q *Queue) playersForLocked(mode string) int {
	if n, ok := q.modeSizes[mode]; ok && n >= 1 {
		return n
	}
	return q.cfg.PlayersPerMatch
}

// liveWaitingLocked returns the still-waiting ticket ids for a mode (Searching
// or Placing -- see State.waiting), in queue order, discarding any that are
// neither.
func (q *Queue) liveWaitingLocked(mode string) []string {
	ids := q.waiting[mode]
	live := ids[:0]
	for _, id := range ids {
		if t := q.tickets[id]; t != nil && t.State.waiting() {
			live = append(live, id)
		}
	}
	q.waiting[mode] = live
	return live
}

func (q *Queue) removeFromWaitingLocked(t *Ticket) {
	ids := q.waiting[t.Mode]
	for i, id := range ids {
		if id == t.ID {
			q.waiting[t.Mode] = append(ids[:i], ids[i+1:]...)
			return
		}
	}
}

func (q *Queue) cancelUserTicketsLocked(userID string) {
	// The squad goes too: a member ticket left behind when its leader
	// re-queued stayed Searching as a squad of one, and could be matched
	// into a session that console never joined -- a ghost peer for everyone
	// else in it.
	for _, t := range q.tickets {
		if t.UserID == userID && t.State.waiting() {
			q.cancelTicketLocked(t)
		}
	}
}

// setStateLocked changes a ticket's state and tells its watchers.
func (q *Queue) setStateLocked(t *Ticket, s State) {
	t.State = s
	for _, ch := range q.watchers[t.ID] {
		select {
		case ch <- *t:
			continue
		default:
		}
		// The watcher is not keeping up, and blocking here would stall match
		// formation for every other player. Each update is a full snapshot,
		// so the oldest queued one is the one to lose: dropping the newest
		// lost a final Succeeded behind a run of Searching/Placing flips, and
		// that console never joined the match the others waited in.
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- *t:
		default:
		}
	}
}

func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Deterministic order. A map range would form matches in a different order
	// each run, which makes a failure that depends on ordering impossible to
	// reproduce.
	sort.Strings(out)
	return out
}

// wholeSquadsLocked picks, in queue order, up to size waiting tickets
// without splitting a squad: a squad that does not fit in what is left is
// skipped for a later match.
func (q *Queue) wholeSquadsLocked(ids []string, size int) []string {
	group := func(id string) string {
		if p := q.tickets[id].Party; p != "" {
			return p
		}
		return id
	}
	members := map[string][]string{}
	var order []string
	for _, id := range ids {
		g := group(id)
		if _, seen := members[g]; !seen {
			order = append(order, g)
		}
		members[g] = append(members[g], id)
	}
	out := make([]string, 0, size)
	for _, g := range order {
		if len(out)+len(members[g]) <= size {
			out = append(out, members[g]...)
		}
	}
	return out
}

// biggestSquadsFirstLocked orders waiting ids so larger squads come first,
// arrival order kept within a size.
func (q *Queue) biggestSquadsFirstLocked(ids []string) []string {
	size := map[string]int{}
	group := func(id string) string {
		if p := q.tickets[id].Party; p != "" {
			return p
		}
		return id
	}
	for _, id := range ids {
		size[group(id)]++
	}
	out := slices.Clone(ids)
	slices.SortStableFunc(out, func(a, b string) int { return size[group(b)] - size[group(a)] })
	return out
}

// evenSquadsLocked picks, from the waiting ids, whole squads that make the
// largest match of at most size players that splits into two equal teams
// with no squad divided -- so at least one player per team, and the same
// number on each side. A squad of three and a solo add up to an even four
// that no 2v2 holds; squads of 3, 3 and 2 add up to eight that no 4v4
// holds. wholeSquadsLocked fills greedily and would form either. Squads
// that have waited longest are preferred.
//
// It is a small dynamic program over (team A, team B) head counts, never
// more than (size/2+1)^2 states, so the queue length does not matter.
func (q *Queue) evenSquadsLocked(ids []string, size int) []string {
	chosen, _ := q.evenSidesLocked(ids, size)
	return chosen
}

// evenSidesLocked is evenSquadsLocked that also says which side each chosen
// ticket's squad plays on, "team1" or "team2".
func (q *Queue) evenSidesLocked(ids []string, size int) ([]string, map[string]string) {
	group := func(id string) string {
		if p := q.tickets[id].Party; p != "" {
			return p
		}
		return id
	}
	members := map[string][]string{}
	var order []string
	for _, id := range ids {
		g := group(id)
		if _, seen := members[g]; !seen {
			order = append(order, g)
		}
		members[g] = append(members[g], id)
	}
	half := size / 2
	type state struct{ a, b int }
	type pick struct {
		group, side string
	}
	// best[s] is the list of units reaching s, built from the oldest units
	// first: a state keeps the first way it was reached.
	best := map[state][]pick{{0, 0}: nil}
	for _, g := range order {
		n := len(members[g])
		next := make(map[state][]pick, len(best)*3)
		for s, units := range best {
			next[s] = units
		}
		for s, units := range best {
			for _, c := range []struct {
				t    state
				side string
			}{{state{s.a + n, s.b}, "team1"}, {state{s.a, s.b + n}, "team2"}} {
				if c.t.a > half || c.t.b > half {
					continue
				}
				if _, ok := next[c.t]; !ok {
					next[c.t] = append(slices.Clone(units), pick{g, c.side})
				}
			}
		}
		best = next
	}
	for k := half; k >= 1; k-- {
		units, ok := best[state{k, k}]
		if !ok {
			continue
		}
		side := map[string]string{}
		for _, u := range units {
			side[u.group] = u.side
		}
		out := make([]string, 0, 2*k)
		sides := map[string]string{}
		for _, id := range ids {
			if sd, ok := side[group(id)]; ok {
				out = append(out, id)
				sides[id] = sd
			}
		}
		return out, sides
	}
	return nil, nil
}

// isBankaraOpenMode reports Anarchy Battle (Open), where friends queue
// together.
func isBankaraOpenMode(mode string) bool {
	return strings.Contains(strings.ToLower(mode), "bankara_match_open")
}

// formVersusSessionLocked forms a plain versus match. In Anarchy Open it
// also puts each squad on one side, team1 or team2. Splatfest assigns sides the
// same way; Turf War's are left to the game, so only Open gets them.
func (q *Queue) formVersusSessionLocked(mode string, chosen []string) Session {
	var sides map[string]string
	if isBankaraOpenMode(mode) {
		_, sides = q.evenSidesLocked(chosen, len(chosen))
	}
	users := map[string]string{}
	for id, side := range sides {
		users[q.tickets[id].UserID] = side
	}
	s := q.formSessionLocked(mode, chosen)
	if len(users) == len(s.Players) {
		s.TeamSlots = users
		q.sessions[s.ID].TeamSlots = users
	}
	return s
}
