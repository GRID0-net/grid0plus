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
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Errors a caller has to tell apart, because the gRPC status they map to
// differs: a missing session is NotFound and a full one is FailedPrecondition,
// and a console reacts differently to each.
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

	// MaxPlayers is the size the match was formed at. Carried on the session
	// rather than looked up again later, because an operator who changes a
	// mode's size mid-session must not change what a match already in progress
	// reports about itself.
	MaxPlayers int

	// Host is the player whose console the others connect to.
	//
	// Splatoon 3's gameplay is peer-to-peer: once a match starts, the consoles
	// talk to each other and SwitchNet is not in the path. All this does is
	// tell them who to reach.
	Host string

	CreatedAt time.Time
}

// Config tunes the queue.
type Config struct {
	// PlayersPerMatch is how many tickets make a match, for any mode ModeSizes
	// does not name.
	PlayersPerMatch int

	// ModeSizes overrides PlayersPerMatch for one mode.
	//
	// The faithful sizes are not hardcoded here, and that is deliberate: the
	// mode names are whatever the game puts in MatchmakingTicket's
	// matchmaking_config, and nothing public documents those strings. A table
	// of plausible-looking names would be an invented Nintendo internal, which
	// is the one thing CLAUDE.md rule 6 rules out — so it is the operator's to
	// fill in from a log of what the console actually asked for, and the log
	// line every unknown mode produces is how they find out.
	//
	// For reference when it comes to that: Turf War and Anarchy are 4v4 (eight
	// players), Salmon Run is four, and Tricolour is 4v2v2 (also eight).
	ModeSizes map[string]int

	// TicketTTL is how long a ticket waits before giving up.
	//
	// A ticket that waits forever is worse than one that times out: the console
	// shows a spinner with no way back, and the player force-quits. Timing out
	// lets the game say "no opponents found" and offer to try again.
	TicketTTL time.Duration
}

// DefaultConfig is a sensible starting point for a small private network.
//
// PlayersPerMatch of 2 rather than 8 on purpose: this is a server for a handful
// of consoles the operator owns, where waiting for eight players means never
// playing. A number that lets two friends actually start a match is more useful
// than one that is faithful and idle; ModeSizes is how an operator who has
// enough consoles asks for the faithful sizes instead.
var DefaultConfig = Config{
	PlayersPerMatch: 2,
	TicketTTL:       3 * time.Minute,
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

	mu      sync.Mutex
	tickets map[string]*Ticket
	// waiting is the queue order per mode, holding ticket ids. A slice rather
	// than a heap: it is FIFO, it is short, and the order has to be readable
	// when something goes wrong.
	waiting map[string][]string
	// sessions are the matches formed so far.
	//
	// Kept because the console asks for its session again after the stream told
	// it about one — GetGameSession and JoinGameSession both address it by
	// name, and a server that formed a match and then could not describe it
	// leaves the lobby stuck on the loading screen with no error.
	sessions map[string]*Session

	// watchers are the streams waiting on a ticket's state to change.
	watchers map[string][]chan Ticket
}

// New builds a queue.
func New(cfg Config, now func() time.Time) *Queue {
	if cfg.PlayersPerMatch < 2 {
		cfg.PlayersPerMatch = DefaultConfig.PlayersPerMatch
	}
	if cfg.TicketTTL <= 0 {
		cfg.TicketTTL = DefaultConfig.TicketTTL
	}
	if now == nil {
		now = time.Now
	}
	return &Queue{
		cfg:      cfg,
		now:      now,
		tickets:  map[string]*Ticket{},
		waiting:  map[string][]string{},
		sessions: map[string]*Session{},
		watchers: map[string][]chan Ticket{},
	}
}

// PlayersFor returns how many players a mode's match takes.
func (q *Queue) PlayersFor(mode string) int {
	if n, ok := q.cfg.ModeSizes[mode]; ok && n >= 2 {
		return n
	}
	return q.cfg.PlayersPerMatch
}

// Enqueue creates a ticket for a player and tries to form a match.
//
// A player may hold only one ticket at a time. A second Enqueue replaces the
// first rather than queueing twice: a console that re-enters matchmaking after
// a network blip has forgotten its old ticket, and leaving that one in the
// queue would match a player who is no longer waiting — the other consoles then
// sit in a lobby that never fills.
func (q *Queue) Enqueue(userID, mode string) (Ticket, []Session, error) {
	if userID == "" {
		return Ticket{}, nil, fmt.Errorf("matchmaking: no user")
	}
	if mode == "" {
		return Ticket{}, nil, fmt.Errorf("matchmaking: no mode")
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	q.cancelUserTicketsLocked(userID)

	t := &Ticket{
		ID:        uuid.NewString(),
		UserID:    userID,
		Mode:      mode,
		State:     StateSearching,
		CreatedAt: q.now(),
	}
	q.tickets[t.ID] = t
	q.waiting[mode] = append(q.waiting[mode], t.ID)

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
	if !ok || t.State != StateSearching {
		return false
	}
	q.setStateLocked(t, StateCancelled)
	q.removeFromWaitingLocked(t)
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

// Sweep times out tickets that have waited too long, and returns them.
//
// Called on a timer by the service. Not folded into Enqueue: a queue nobody is
// joining is exactly the one whose tickets need to expire, and doing it only on
// arrival means the last player to give up waits forever.
func (q *Queue) Sweep() []Ticket {
	q.mu.Lock()
	defer q.mu.Unlock()

	deadline := q.now().Add(-q.cfg.TicketTTL)
	var expired []Ticket

	for _, mode := range sortedKeys(q.waiting) {
		for _, id := range append([]string(nil), q.waiting[mode]...) {
			t := q.tickets[id]
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
	return expired
}

// Waiting returns how many tickets are queued per mode. For the dashboard.
func (q *Queue) Waiting() map[string]int {
	q.mu.Lock()
	defer q.mu.Unlock()

	out := make(map[string]int, len(q.waiting))
	for mode, ids := range q.waiting {
		n := 0
		for _, id := range ids {
			if t := q.tickets[id]; t != nil && t.State == StateSearching {
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

// formMatchesLocked pairs off waiting tickets into sessions.
func (q *Queue) formMatchesLocked() []Session {
	var formed []Session

	for _, mode := range sortedKeys(q.waiting) {
		size := q.PlayersFor(mode)
		for {
			ids := q.liveWaitingLocked(mode)
			if len(ids) < size {
				break
			}
			chosen := ids[:size]

			s := &Session{
				ID:         uuid.NewString(),
				Mode:       mode,
				MaxPlayers: size,
				CreatedAt:  q.now(),
			}
			for _, id := range chosen {
				t := q.tickets[id]
				s.Players = append(s.Players, t.UserID)
			}
			// The longest-waiting player hosts. Arbitrary but not random: it is
			// stable, it is explainable to somebody reading a match log, and it
			// spreads hosting over time rather than always picking whoever
			// happens to be first in a map.
			s.Host = s.Players[0]
			q.sessions[s.ID] = s

			for _, id := range chosen {
				t := q.tickets[id]
				t.SessionID = s.ID
				q.setStateLocked(t, StateSucceeded)
				q.removeFromWaitingLocked(t)
			}
			formed = append(formed, *s)
		}
	}
	return formed
}

// liveWaitingLocked returns the still-searching ticket ids for a mode, in queue
// order, discarding any that are no longer searching.
func (q *Queue) liveWaitingLocked(mode string) []string {
	ids := q.waiting[mode]
	live := ids[:0]
	for _, id := range ids {
		if t := q.tickets[id]; t != nil && t.State == StateSearching {
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
		if t.UserID == userID && t.State == StateSearching {
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
