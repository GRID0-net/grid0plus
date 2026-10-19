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
	twoPlayersConnected(s, caller)
	boolean := func(b bool) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_BooleanValue{BooleanValue: b}}
	}
	// What each console writes when it presses "Again!".
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
	if got := m.GetFields().GetFields()["bfmin"].GetIntegerValue(); got != 1 {
		t.Errorf("reopened __gs/m bfmin = %d, want captured 1", got)
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
	twoPlayersConnected(s, caller)
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
	guest := &session{userID: "bob", details: caller.details, userSession: "userSessions/us-bob"}
	s.noteAgain(guest, ops)
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

func TestVersusAgainRetriesCannotPostponeTheFirstDeadline(t *testing.T) {
	s := &Server{docs: newStoreSet(time.Now), watch: newWatchers(), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	caller := &session{userID: "alice", details: &SessionDetails{SessionID: "match", Mode: "regular_match_config", CreatedAt: time.Unix(1000, 0)}, userSession: "userSessions/us-alice"}
	ops := []*syncpb.WriteOperation{merge(doc("docs/__gs/m", map[string]*commonpb.Value{"cp": boolean(true), "ebf": boolean(true)}))}
	s.seedRoomDocuments(caller, ops)
	if _, _, err := s.docsFor(caller).apply(ops); err != nil {
		t.Fatal(err)
	}
	s.noteAgain(caller, ops)
	first := s.again.timers[scopeOf(caller)]
	if first == nil {
		t.Fatal("public Versus did not enter the rematch window")
	}
	defer first.timer.Stop()
	s.noteAgain(caller, ops)
	guest := &session{userID: "bob", details: caller.details, userSession: "userSessions/us-bob"}
	s.noteAgain(guest, ops)
	if s.again.timers[scopeOf(caller)] != first {
		t.Fatal("retry or second Versus player moved the deadline")
	}
	s.lockAfterAgain(caller, first)
	m, _ := s.docsFor(caller).snapshot("docs/__gs/m")
	if m.GetFields().GetFields()["cp"].GetBooleanValue() || m.GetFields().GetFields()["ebf"].GetBooleanValue() {
		t.Fatal("Versus rematch remained open")
	}
}

func TestANewMatchLockCancelsTheOldRematchTimer(t *testing.T) {
	s := &Server{docs: newStoreSet(time.Now), watch: newWatchers(), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	caller := &session{userID: "alice", details: &SessionDetails{SessionID: "match", Mode: "coop", CreatedAt: time.Unix(1000, 0)}, userSession: "userSessions/us-alice"}
	ops := []*syncpb.WriteOperation{merge(doc("docs/__gs/m", map[string]*commonpb.Value{"cp": boolean(true), "ebf": boolean(true)}))}
	s.seedRoomDocuments(caller, ops)
	s.noteAgain(caller, ops)
	old := s.again.timers[scopeOf(caller)]
	s.noteAgain(caller, []*syncpb.WriteOperation{merge(doc("docs/__gs/m", map[string]*commonpb.Value{"cp": boolean(false), "ebf": boolean(false)}))})
	if s.again.timers[scopeOf(caller)] != nil {
		t.Fatal("new match left its old rematch timer active")
	}
	s.lockAfterAgain(caller, old)
	m, _ := s.docsFor(caller).snapshot("docs/__gs/m")
	if !m.GetFields().GetFields()["cp"].GetBooleanValue() {
		t.Fatal("cancelled callback changed the new lobby")
	}
}

// twoPlayersConnected opens two players' streams in caller's session, as a
// Salmon Run lobby has when more than its host stays for "Again!".
func twoPlayersConnected(s *Server, caller *session) {
	s.watch.subscribe("alice", "us-alice", scopeOf(caller))
	s.watch.subscribe("bob", "us-bob", scopeOf(caller))
}

// TestSalmonRunAgainNeverLocksWithOnePlayerLeft: only the host's press is
// seen, so the lobby used to lock after the grace window even when everyone
// else had gone, and the host started a one-player job.
func TestSalmonRunAgainNeverLocksWithOnePlayerLeft(t *testing.T) {
	s := &Server{docs: newStoreSet(time.Now), watch: newWatchers(), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	caller := &session{userID: "alice", details: &SessionDetails{SessionID: "job", Mode: "coop", CreatedAt: time.Unix(1000, 0)}, userSession: "userSessions/us-alice"}
	s.watch.subscribe("alice", "us-alice", scopeOf(caller))
	ops := []*syncpb.WriteOperation{merge(doc("docs/__gs/m", map[string]*commonpb.Value{"cp": boolean(true), "ebf": boolean(true)}))}
	s.seedRoomDocuments(caller, ops)
	if _, _, err := s.docsFor(caller).apply(ops); err != nil {
		t.Fatal(err)
	}
	s.noteAgain(caller, ops)
	w := s.again.timers[scopeOf(caller)]
	w.timer.Stop()
	s.lockAfterAgain(caller, w)
	m, _ := s.docsFor(caller).snapshot("docs/__gs/m")
	if !m.GetFields().GetFields()["cp"].GetBooleanValue() {
		t.Fatal("a Salmon Run lobby with one player left was locked and would start")
	}
}

// TestAFullAgainLobbyLocksAtOnceAndReportsBothEnds: npln fills a reopened
// Salmon Run session between the window opening and its lock; once it is
// full there is nobody left to wait for.
func TestAFullAgainLobbyLocksAtOnceAndReportsBothEnds(t *testing.T) {
	old := againGrace
	againGrace = time.Hour
	t.Cleanup(func() { againGrace = old })

	opened, locked := make(chan string, 2), make(chan string, 2)
	s := &Server{docs: newStoreSet(time.Now), watch: newWatchers(), log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		againOpen: func(id string) { opened <- id }, againLocked: func(id string) { locked <- id }}
	caller := &session{userID: "alice", details: &SessionDetails{SessionID: "job", Mode: "coop_regular_config", CreatedAt: time.Unix(1000, 0)}, userSession: "userSessions/us-alice"}
	twoPlayersConnected(s, caller)
	ops := []*syncpb.WriteOperation{merge(doc("docs/__gs/m", map[string]*commonpb.Value{"cp": boolean(true), "ebf": boolean(true)}))}
	s.seedRoomDocuments(caller, ops)
	if _, _, err := s.docsFor(caller).apply(ops); err != nil {
		t.Fatal(err)
	}
	s.noteAgain(caller, ops)
	s.noteAgain(caller, ops)
	if got := <-opened; got != "job" {
		t.Fatalf("opened %q", got)
	}
	select {
	case again := <-opened:
		t.Fatalf("a repeated press opened the window again: %q", again)
	default:
	}

	s.AgainJoined("job", false)
	m, _ := s.docsFor(caller).snapshot("docs/__gs/m")
	if !m.GetFields().GetFields()["ebf"].GetBooleanValue() {
		t.Fatal("a player joining a lobby with seats left locked it")
	}
	s.AgainJoined("job", true)
	select {
	case got := <-locked:
		if got != "job" {
			t.Errorf("locked %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("a full Again! lobby was not locked")
	}
	// The seats close first and the lock is written right after.
	deadline := time.Now().Add(time.Second)
	for {
		m, _ = s.docsFor(caller).snapshot("docs/__gs/m")
		f := m.GetFields().GetFields()
		if !f["cp"].GetBooleanValue() && !f["ebf"].GetBooleanValue() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("__gs/m after filling = %v, want cp and ebf false", f)
		}
		time.Sleep(time.Millisecond)
	}
}
