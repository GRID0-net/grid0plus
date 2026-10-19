package gamesync

import (
	"strings"
	"sync"
	"time"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// "Again!" reuses the old Gamesync session rather than entering the ticket
// queue. Public Versus therefore needs this window too: queue.Sweep cannot start
// a countdown for players who have not submitted new tickets.
//
// Salmon Run's "Again!" does not send a matchmaking ticket. Every console that
// presses it reopens the session it just played in with a masked update of
// docs/__gs/m -- cp, ebf and ip true, pw empty -- and then waits there. If
// nothing answers that, the lobby never starts.
//
// GRID0+ treats the reopening as the group queuing again together: the first
// press opens a grace window, every further press in the same session restarts
// it, and when it runs out the session is locked again (cp and ebf false), the
// same shape the console itself writes when a fresh match locks.
//
// Keep bfmin as sent (1). Lowering it to 0 does not fix rematches; it is not a
// substitute for the client's separate eight-player minimum in its
// retained-lobby controller.
//
// With bfmin 0 on the reopen, the next job starts in the same session 3-4 s
// after this lock, every time. The host's State::ActivateBackFill rejoin
// completes only once the session is locked, so the grace window is the whole of
// the group's wait. It cannot end early on everyone having pressed: only the
// host's reopen write is seen.
var againGrace = 20 * time.Second

type againQueue struct {
	mu     sync.Mutex
	timers map[string]*againWindow
}

type againWindow struct {
	timer *time.Timer
	users map[string]bool
	// caller is the press that opened the window, whose session the lock
	// is written as.
	caller *session
}

// noteAgain restarts a session's grace window when a write reopens it.
func (s *Server) noteAgain(caller *session, ops []*syncpb.WriteOperation) {
	scope := scopeOf(caller)
	if scope == "" || caller.details.Room || !publicRematchMode(caller.details.Mode) {
		return
	}
	reopened := false
	closed := false
	for _, op := range ops {
		doc := op.GetUpdateDocument().GetDocument()
		if doc == nil {
			doc = op.GetMergeDocument().GetDocument()
		}
		if doc.GetName() != "docs/__gs/m" {
			continue
		}
		f := doc.GetFields().GetFields()
		if cp, ebf := f["cp"], f["ebf"]; cp != nil && ebf != nil && !cp.GetBooleanValue() && !ebf.GetBooleanValue() {
			closed = true
		}
		if f["ebf"].GetBooleanValue() && f["cp"].GetBooleanValue() {
			reopened = true
		}
	}
	if !reopened && !closed {
		return
	}
	// The hooks run after the lock is released: npln answers an opening by
	// filling seats, which comes back here through AgainJoined.
	var opened, locked bool
	defer func() {
		if opened && s.againOpen != nil {
			s.againOpen(scope)
		}
		if locked && s.againLocked != nil {
			s.againLocked(scope)
		}
	}()
	s.again.mu.Lock()
	defer s.again.mu.Unlock()
	if s.again.timers == nil {
		s.again.timers = map[string]*againWindow{}
	}
	if closed && !reopened {
		if t := s.again.timers[scope]; t != nil {
			t.timer.Stop()
			delete(s.again.timers, scope)
			locked = true
		}
		return
	}
	if t := s.again.timers[scope]; t != nil {
		// Retries by one console must not postpone the match indefinitely.
		if t.users[caller.userID] {
			return
		}
		t.users[caller.userID] = true
		// Versus keeps the first player's deadline. Coop grants a fresh
		// window to each additional player, as its initial queue does.
		if !strings.Contains(caller.details.Mode, "coop") {
			return
		}
		t.timer.Stop()
	}
	s.log.Info("gamesync: Again! pressed; the grace window restarts", "session", scope, "user", caller.userID)
	window := &againWindow{users: map[string]bool{caller.userID: true}, caller: caller}
	if previous := s.again.timers[scope]; previous != nil {
		window.users = previous.users
		window.caller = previous.caller
	} else {
		opened = strings.Contains(caller.details.Mode, "coop")
	}
	s.again.timers[scope] = window
	window.timer = time.AfterFunc(againGrace, func() { s.lockAfterAgain(window.caller, window) })
}

// AgainJoined is told that players were added to a Salmon Run session whose
// "Again!" window is open (internal/matchmaking's OpenBackfill).
//
// Each arrival restarts the window, as each new player does in the queue's
// own Salmon Run grace period, so a crew that is filling keeps waiting for
// the next. A full session locks at once: there is nobody left to wait for.
func (s *Server) AgainJoined(sessionID string, full bool) {
	s.again.mu.Lock()
	defer s.again.mu.Unlock()
	window := s.again.timers[sessionID]
	if window == nil {
		return
	}
	window.timer.Stop()
	wait := againGrace
	if full {
		wait = 0
	}
	s.log.Info("gamesync: players joined the Again! lobby", "session", sessionID, "full", full)
	window.timer = time.AfterFunc(wait, func() { s.lockAfterAgain(window.caller, window) })
}

func publicRematchMode(mode string) bool {
	if strings.Contains(mode, "private") || strings.Contains(mode, "join_room") {
		return false
	}
	for _, marker := range []string{"coop", "regular", "turf", "bankara", "league", "x_match", "fest"} {
		if strings.Contains(mode, marker) {
			return true
		}
	}
	return false
}

// lockAfterAgain closes a reopened session once its grace window has passed.
func (s *Server) lockAfterAgain(caller *session, window *againWindow) {
	scope := scopeOf(caller)
	s.again.mu.Lock()
	defer s.again.mu.Unlock()
	// Stop cannot retract a callback already waiting for this mutex. Only the
	// current generation may lock the lobby or remove its pending window.
	if s.again.timers[scope] != window {
		return
	}
	delete(s.again.timers, scope)

	// Only the host's press is seen, so the window cannot tell who stayed. The open
	// streams can: a Salmon Run lobby everyone else left would otherwise lock with
	// the host alone and start a one-player job. Streams do stay open across Again!
	// (they hold through the press and the lock), so a real group counts as two
	// here.
	if strings.Contains(caller.details.Mode, "coop") {
		if n := s.watch.playersIn(scope); n < 2 {
			s.log.Info("gamesync: Again! lobby left unlocked; one player cannot start Salmon Run alone",
				"session", scope, "players", n)
			s.closeAgain(scope)
			return
		}
	}

	// Seats close before the lock is written: a ticket matched in between
	// would land in a job that is already starting.
	s.closeAgain(scope)
	s.writeSessionMeta(caller, map[string]*commonpb.Value{
		"cp": boolean(false), "ebf": boolean(false), "bfmin": integer(1),
	}, "gamesync: Again! grace window over; session locked")
}

// closeAgain tells npln a window has ended. Safe under the again lock: npln
// only closes the session's seats in its queue and never calls back.
func (s *Server) closeAgain(scope string) {
	if s.againLocked != nil {
		s.againLocked(scope)
	}
}

// writeSessionMeta applies a server-side masked update to docs/__gs/m and
// publishes it to the session, as if a member had written it.
func (s *Server) writeSessionMeta(caller *session, fields map[string]*commonpb.Value, done string) {
	scope := scopeOf(caller)
	paths := make([]string, 0, len(fields))
	for k := range fields {
		paths = append(paths, "`"+k+"`")
	}
	ops := []*syncpb.WriteOperation{{OperationType: &syncpb.WriteOperation_UpdateDocument{UpdateDocument: &syncpb.UpdateDocumentRequest{
		Document:   &syncpb.Document{Name: "docs/__gs/m", Fields: &commonpb.MapValue{Fields: fields}},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: paths},
	}}}}
	s.seedRoomDocuments(caller, ops)
	_, commit, err := s.docsFor(caller).apply(ops)
	if err != nil {
		s.log.Warn("gamesync: could not update the session meta", "session", scope, "error", err)
		return
	}
	s.log.Info(done, "session", scope)
	s.watch.publish(scope, s.normalizeSessionChanges(caller, commit.changes))
}

func boolean(b bool) *commonpb.Value {
	return &commonpb.Value{ValueType: &commonpb.Value_BooleanValue{BooleanValue: b}}
}

func integer(v int64) *commonpb.Value {
	return &commonpb.Value{ValueType: &commonpb.Value_IntegerValue{IntegerValue: v}}
}
