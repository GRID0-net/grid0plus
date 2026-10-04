package gamesync

import (
	"strings"
	"sync"
	"time"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

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
// TODO(capture-needed): the lock did not change anything on hardware. The
// host's State::ActivateBackFill waits ~32 s for players to be backfilled
// into the reopened session (see docs/CAPTURES/README.md, Salmon Run
// "Again!"), then falls back to ordinary matchmaking. What GRID0+ should send
// to end that wait with no newcomers is still open. Change: lockAfterAgain.
var againGrace = 20 * time.Second

type againQueue struct {
	mu     sync.Mutex
	timers map[string]*time.Timer
}

// noteAgain restarts a session's grace window when a write reopens it.
func (s *Server) noteAgain(caller *session, ops []*syncpb.WriteOperation) {
	scope := scopeOf(caller)
	if scope == "" || !strings.Contains(caller.details.Mode, "coop") {
		return
	}
	reopened := false
	for _, op := range ops {
		doc := op.GetUpdateDocument().GetDocument()
		if doc.GetName() != "docs/__gs/m" {
			continue
		}
		f := doc.GetFields().GetFields()
		if f["ebf"].GetBooleanValue() && f["cp"].GetBooleanValue() {
			reopened = true
		}
	}
	if !reopened {
		return
	}
	s.again.mu.Lock()
	defer s.again.mu.Unlock()
	if s.again.timers == nil {
		s.again.timers = map[string]*time.Timer{}
	}
	if t := s.again.timers[scope]; t != nil {
		t.Stop()
	}
	s.log.Info("gamesync: Again! pressed; the grace window restarts", "session", scope, "user", caller.userID)
	s.again.timers[scope] = time.AfterFunc(againGrace, func() { s.lockAfterAgain(caller) })
}

// lockAfterAgain closes a reopened session once its grace window has passed.
func (s *Server) lockAfterAgain(caller *session) {
	scope := scopeOf(caller)
	s.again.mu.Lock()
	delete(s.again.timers, scope)
	s.again.mu.Unlock()

	boolean := func(b bool) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_BooleanValue{BooleanValue: b}}
	}
	ops := []*syncpb.WriteOperation{{OperationType: &syncpb.WriteOperation_UpdateDocument{UpdateDocument: &syncpb.UpdateDocumentRequest{
		Document: &syncpb.Document{Name: "docs/__gs/m", Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
			"cp": boolean(false), "ebf": boolean(false),
		}}},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"`cp`", "`ebf`"}},
	}}}}
	s.seedRoomDocuments(caller, ops)
	_, commit, err := s.docsFor(caller).apply(ops)
	if err != nil {
		s.log.Warn("gamesync: could not lock the session after Again!", "session", scope, "error", err)
		return
	}
	s.log.Info("gamesync: Again! grace window over; session locked", "session", scope)
	s.watch.publish(scope, s.normalizeSessionChanges(caller, commit.changes))
}
