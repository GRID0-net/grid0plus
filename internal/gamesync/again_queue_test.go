package gamesync

import (
	"io"
	"log/slog"
	"testing"
	"time"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// TestAgainReopensTheSessionAndLocksItAfterTheGraceWindow: Salmon Run's
// "Again!" reopens the session it was played in (cp, ebf and ip true) and
// sends no ticket; the server locks it again once the group's grace runs out.
func TestAgainReopensTheSessionAndLocksItAfterTheGraceWindow(t *testing.T) {
	old := againGrace
	againGrace = 30 * time.Millisecond
	t.Cleanup(func() { againGrace = old })

	s := &Server{docs: newStoreSet(time.Now), watch: newWatchers(), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	caller := &session{userID: "alice", details: &SessionDetails{
		UserID: "alice", SessionID: "job", Mode: "tenants/current/matchmakingConfigs/coop_regular_config",
		Host: "example.invalid", Port: 443, ParticipantCount: 2, CreatedAt: time.Unix(1000, 0),
	}, userSession: "gameSessions/job/userSessions/us-alice"}
	boolean := func(b bool) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_BooleanValue{BooleanValue: b}}
	}
	// What each console writes when it presses "Again!" (loopback capture, 2026-10-04).
	ops := []*syncpb.WriteOperation{{OperationType: &syncpb.WriteOperation_UpdateDocument{
		UpdateDocument: &syncpb.UpdateDocumentRequest{
			Document: &syncpb.Document{Name: "docs/__gs/m", Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
				"cp": boolean(true), "ebf": boolean(true), "ip": boolean(true),
				"pw": {ValueType: &commonpb.Value_StringValue{StringValue: ""}},
			}}},
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"`ip`", "`ebf`", "`pw`", "`cp`"}},
		}}}}
	s.seedRoomDocuments(caller, ops)
	if _, _, err := s.docsFor(caller).apply(ops); err != nil {
		t.Fatal(err)
	}
	s.noteAgain(caller, ops)

	time.Sleep(10 * time.Millisecond)
	m, _ := s.docsFor(caller).snapshot("docs/__gs/m")
	if !m.GetFields().GetFields()["cp"].GetBooleanValue() {
		t.Fatal("the session was locked before the grace window ran out")
	}
	if got := m.GetFields().GetFields()["bfmin"].GetIntegerValue(); got != 0 {
		t.Errorf("reopened __gs/m bfmin = %d, want 0 (the backfill experiment)", got)
	}
	time.Sleep(100 * time.Millisecond)
	m, _ = s.docsFor(caller).snapshot("docs/__gs/m")
	f := m.GetFields().GetFields()
	if f["cp"].GetBooleanValue() || f["ebf"].GetBooleanValue() {
		t.Errorf("after the grace window __gs/m = %v, want cp and ebf false", f)
	}
	if f["bfmin"].GetIntegerValue() != 1 {
		t.Errorf("locking left bfmin = %v, want it restored to 1", f["bfmin"])
	}
	if f["bfmax"] == nil {
		t.Error("locking dropped the fields it did not name")
	}
}

func TestAnAlreadyStartedAgainTimerCannotLockTheReplacementWindow(t *testing.T) {
	s := &Server{docs: newStoreSet(time.Now), watch: newWatchers(), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	caller := &session{userID: "alice", details: &SessionDetails{SessionID: "job", Mode: "coop", CreatedAt: time.Unix(1000, 0)}, userSession: "userSessions/us-alice"}
	ops := []*syncpb.WriteOperation{merge(doc("docs/__gs/m", map[string]*commonpb.Value{"cp": boolean(true), "ebf": boolean(true)}))}
	s.seedRoomDocuments(caller, ops)
	if _, _, err := s.docsFor(caller).apply(ops); err != nil {
		t.Fatal(err)
	}
	s.noteAgain(caller, ops)
	old := s.again.timers[scopeOf(caller)]
	if old == nil {
		t.Fatal("merge reopening did not start a grace window")
	}
	old.timer.Stop()
	s.noteAgain(caller, ops)
	replacement := s.again.timers[scopeOf(caller)]
	defer replacement.timer.Stop()
	// Simulate the old callback that has already fired and was blocked on the
	// queue mutex while a second player's Again request replaced its window.
	s.lockAfterAgain(caller, old)
	room, _ := s.docsFor(caller).snapshot("docs/__gs/m")
	if !room.GetFields().GetFields()["cp"].GetBooleanValue() {
		t.Fatal("superseded timer closed the newly reopened lobby")
	}
	s.lockAfterAgain(caller, replacement)
	room, _ = s.docsFor(caller).snapshot("docs/__gs/m")
	if room.GetFields().GetFields()["cp"].GetBooleanValue() {
		t.Fatal("current timer failed to close the lobby")
	}
}
