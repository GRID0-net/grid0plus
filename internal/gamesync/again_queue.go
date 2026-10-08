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
// queue. Public Versus therefore needs this window too: queue.Sweep cannot
// start a countdown for players who have not submitted new tickets.
// Salmon Run's "Again!" does not send a matchmaking ticket. Every console that
// presses it reopens the session it just played in with a masked update of
// docs/__gs/m -- cp, ebf and ip true, pw empty (loopback capture of two
// consoles, 2026-10-04) -- and then waits there. Nothing on GRID0+ answered
// that, so the lobby never started.
//
// GRID0+ treats the reopening as the group queuing again together: the first
// press opens a grace window, every further press in the same session
// restarts it, and when it runs out the session is locked again (cp and ebf
// false), the same shape the console itself writes when a fresh match locks.
//
// Keep bfmin as captured (1). The earlier bfmin=0 experiment did not fix
// rematches; lowering a protocol field is not a substitute for the client's
// separate eight-player minimum in its retained-lobby controller.
//
// TODO(capture-needed): the lock did not change anything on hardware. The
// host's State::ActivateBackFill runs a session-join operation and gives it
// ~32 s to complete before falling back to ordinary matchmaking (see
// docs/CAPTURES/README.md, Salmon Run "Again!"); what GRID0+ fails to deliver
// for that join is still open. Change: lockAfterAgain.
var againGrace = 20 * time.Second

type againQueue struct {
	mu     sync.Mutex
	timers map[string]*againWindow
}

type againWindow struct {
	timer *time.Timer
	users map[string]bool
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
	s.again.mu.Lock()
	defer s.again.mu.Unlock()
	if s.again.timers == nil {
		s.again.timers = map[string]*againWindow{}
	}
	if closed && !reopened {
		if t := s.again.timers[scope]; t != nil {
			t.timer.Stop()
			delete(s.again.timers, scope)
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
	window := &againWindow{users: map[string]bool{caller.userID: true}}
	if previous := s.again.timers[scope]; previous != nil {
		window.users = previous.users
	}
	s.again.timers[scope] = window
	window.timer = time.AfterFunc(againGrace, func() { s.lockAfterAgain(caller, window) })
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

	s.writeSessionMeta(caller, map[string]*commonpb.Value{
		"cp": boolean(false), "ebf": boolean(false), "bfmin": integer(1),
	}, "gamesync: Again! grace window over; session locked")
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
