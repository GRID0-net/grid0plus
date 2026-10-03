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
	tickets   map[string]*Ticket
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
func (q *Queue) EnqueueSquad(userID string, members []string, mode, team string) (Ticket, []Session, error) {
	if userID == "" {
		return Ticket{}, nil, fmt.Errorf("matchmaking: no user")
	}
	if mode == "" {
		return Ticket{}, nil, fmt.Errorf("matchmaking: no mode")
	}

	q.mu.Lock()
	defer q.mu.Unlock()

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
			ID: uuid.NewString(), UserID: m, Mode: mode, FestTeam: team,
			Party: t.ID, State: StateSearching, CreatedAt: t.CreatedAt,
		}
		q.tickets[mt.ID] = mt
		q.waiting[mode] = append(q.waiting[mode], mt.ID)
	}

	sessions := q.formMatchesLocked()
	return *q.tickets[t.ID], sessions, nil
}

// Cancel withdraws a ticket.
//
// Cancelling a ticket that already succeeded does nothing: the match exists and
// the other players are already being told to connect. Pulling one player back
// out at that point leaves them waiting for somebody who will not arrive.
func (q *Queue) Cancel(ticketID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	t, ok := q.tickets[ticketID]
	if !ok || !t.State.waiting() {
		return false
	}
	mode := t.Mode
	q.setStateLocked(t, StateCancelled)
	q.removeFromWaitingLocked(t)
	// A squad leaves together: its members' tickets go with the one it queued.
	for _, other := range q.tickets {
		if other.Party == t.ID && other.State.waiting() {
			q.setStateLocked(other, StateCancelled)
			q.removeFromWaitingLocked(other)
		}
	}
	// The rest of the mode's grace period, if one was running, was counting
	// down to include this ticket. Re-evaluate rather than leave a stale
	// countdown (or the rest of the mode's tickets stuck in StatePlacing).
	if q.cfg.CoopModes[mode] {
		q.formCoopLocked(mode)
	} else {
		q.formVersusLocked(mode)
	}
	return true
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
	return s.clone(), nil
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
	return *s, true
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

	ids := q.liveWaitingLocked(mode)
	if isFestMode(mode) {
		ids = q.selectFestPlayersLocked(ids, q.playersForLocked(mode))
	}
	if len(ids) == 0 {
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
}

// Snapshot returns the queue and the live sessions.
func (q *Queue) Snapshot() Snapshot {
	q.mu.Lock()
	defer q.mu.Unlock()

	now := q.now()
	out := Snapshot{DefaultSize: q.cfg.PlayersPerMatch}

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
		out.Sessions = append(out.Sessions, *s)
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
		if q.cfg.CoopModes[mode] {
			formed = append(formed, q.formCoopLocked(mode)...)
		} else {
			formed = append(formed, q.formVersusLocked(mode)...)
		}
	}
	return formed
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
	for len(ids) >= size {
		chosen := q.wholeSquadsLocked(ids, size)
		if len(chosen) < size {
			break
		}
		delete(q.grace, mode)
		formed = append(formed, q.formSessionLocked(mode, chosen))
		ids = q.liveWaitingLocked(mode)
		if isFestMode(mode) {
			ids = q.selectFestPlayersLocked(ids, size)
		}
	}
	if len(ids) > size {
		// Squads that cannot fill a match exactly wait on the clock below
		// with a set that fits.
		ids = q.wholeSquadsLocked(ids, size)
	}

	n := len(ids)
	if q.cfg.VersusGracePeriod <= 0 || n < 2 || n%2 != 0 {
		q.cancelGraceLocked(mode)
		return formed
	}

	g, pending := q.grace[mode]
	if !pending || n != g.lastCount {
		q.grace[mode] = &graceState{deadline: q.now().Add(q.cfg.VersusGracePeriod), lastCount: n}
		q.setModeStateLocked(mode, StatePlacing)
		return formed
	}
	if q.now().Before(g.deadline) {
		return formed
	}
	delete(q.grace, mode)
	return append(formed, q.formSessionLocked(mode, ids))
}

func isFestMode(mode string) bool { return strings.Contains(strings.ToLower(mode), "fest") }

// selectFestPlayersLocked requires two different fest teams with equal
// numbers. The result is grouped by team so the session can assign stable
// team1/team2 slots. Extra tickets remain queued for the next match.
func (q *Queue) selectFestPlayersLocked(ids []string, size int) []string {
	byTeam := map[string][]string{}
	for _, id := range ids {
		if team := q.tickets[id].FestTeam; team != "" {
			byTeam[team] = append(byTeam[team], id)
		}
	}
	if len(byTeam) < 2 {
		return nil
	}
	teams := make([]string, 0, len(byTeam))
	for team := range byTeam {
		teams = append(teams, team)
	}
	sort.Slice(teams, func(i, j int) bool {
		if len(byTeam[teams[i]]) != len(byTeam[teams[j]]) {
			return len(byTeam[teams[i]]) > len(byTeam[teams[j]])
		}
		return teams[i] < teams[j]
	})
	perTeam := min(size/2, len(byTeam[teams[0]]), len(byTeam[teams[1]]))
	if perTeam == 0 {
		return nil
	}
	out := make([]string, 0, perTeam*2)
	out = append(out, byTeam[teams[0]][:perTeam]...)
	out = append(out, byTeam[teams[1]][:perTeam]...)
	return out
}

// formCoopLocked forms a Salmon Run-shaped match: one team, no even split.
// Nobody waits on a clock alone -- a countdown only exists once a second
// player is present, and it restarts every time another one joins, so a
// trickle of arrivals does not lose the ones already waiting to a clock that
// started before they got there. Reaching the mode's size forms immediately:
// a full room has nobody left to wait for.
func (q *Queue) formCoopLocked(mode string) []Session {
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

	if q.cfg.CoopGracePeriod <= 0 {
		return []Session{q.formSessionLocked(mode, ids)}
	}

	g, pending := q.grace[mode]
	if !pending || len(ids) != g.lastCount {
		// First player past solo, or a new arrival since the clock was last
		// set: (re)start it from here.
		q.grace[mode] = &graceState{deadline: q.now().Add(q.cfg.CoopGracePeriod), lastCount: len(ids)}
		q.setModeStateLocked(mode, StatePlacing)
		return nil
	}
	if q.now().Before(g.deadline) {
		return nil
	}
	delete(q.grace, mode)
	return []Session{q.formSessionLocked(mode, ids)}
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
	if isFestMode(mode) && len(chosen) > 0 {
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
	}

	for _, id := range chosen {
		t := q.tickets[id]
		t.SessionID = s.ID
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
	}
	// The first player hosts: for a match the longest-waiting one, for a room
	// the one who opened it. Arbitrary for a match but not random: it is
	// stable, it is explainable to somebody reading a match log, and it
	// spreads hosting over time rather than always picking whoever happens to
	// be first in a map.
	s.Host = s.Players[0]
	q.sessions[s.ID] = &s
	return s
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
	for _, t := range q.tickets {
		if t.UserID == userID && t.State.waiting() {
			q.setStateLocked(t, StateCancelled)
			q.removeFromWaitingLocked(t)
		}
	}
}

// setStateLocked changes a ticket's state and tells its watchers.
func (q *Queue) setStateLocked(t *Ticket, s State) {
	t.State = s
	for _, ch := range q.watchers[t.ID] {
		select {
		case ch <- *t:
		default:
			// The watcher is not keeping up. Dropping the update is better than
			// blocking match formation for every other player on this queue.
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
