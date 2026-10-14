package gamesync

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
	"google.golang.org/protobuf/proto"
)

func TestDocumentReadersKeepAStableCommittedVersion(t *testing.T) {
	st := newStore(time.Now)
	name := "docs/state/player"
	st.apply([]*syncpb.WriteOperation{merge(doc(name, map[string]*commonpb.Value{"value": intValue(0)}))})
	old, _ := st.get(name)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; i <= 1000; i++ {
			st.apply([]*syncpb.WriteOperation{merge(doc(name, map[string]*commonpb.Value{"value": intValue(int64(i))}))})
		}
	}()
	for i := 0; i < 1000; i++ {
		if _, err := proto.Marshal(old); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	if old.Fields.Fields["value"].GetIntegerValue() != 0 {
		t.Fatal("a subsequent write mutated an already-read document")
	}
}

func TestEightConcurrentReportsKeepEveryPlayersResult(t *testing.T) {
	h := newHarness(t)
	const battle = "battle_room"
	var contexts []context.Context
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("player%d", i)
		caller, err := h.srv.newSession(id, "userSessions/"+id)
		if err != nil {
			t.Fatal(err)
		}
		caller.details = &SessionDetails{UserID: id, SessionID: "room"}
		contexts = append(contexts, h.authed(caller.accessToken))
	}
	if _, err := h.client.WriteDocuments(contexts[0], &syncpb.WriteDocumentsRequest{WriteOperations: []*syncpb.WriteOperation{merge(doc(refereeRequestPrefix+battle, map[string]*commonpb.Value{"users": intValue(8)}))}}); err != nil {
		t.Fatal(err)
	}
	errors := make(chan error, 8)
	for i, ctx := range contexts {
		go func(i int, ctx context.Context) {
			id := fmt.Sprintf("player%d", i)
			personal := &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: map[string]*commonpb.Value{id: {ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: map[string]*commonpb.Value{"npln_user_id": stringValue(id), "kill_num": intValue(int64(i))}}}}}}}}
			_, err := h.client.WriteDocuments(ctx, &syncpb.WriteDocumentsRequest{WriteOperations: []*syncpb.WriteOperation{merge(doc(refereeReportPrefix+battle+"/User/"+id, map[string]*commonpb.Value{"team_result": intValue(1), "personal_result": personal}))}})
			errors <- err
		}(i, ctx)
	}
	for range contexts {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	verdict, _ := h.srv.docs.in("room").snapshot(refereeResultPrefix + battle)
	results := verdict.GetFields().GetFields()["personal_result"].GetMapValue().GetFields()
	if len(results) != 8 {
		t.Fatalf("concurrent reports retained %d of 8 players", len(results))
	}
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("player%d", i)
		if got := results[id].GetMapValue().GetFields()["kill_num"].GetIntegerValue(); got != int64(i) {
			t.Fatalf("%s result changed: %d", id, got)
		}
	}
}

func TestASlowHistoryWriteDoesNotBlockAnotherRoomsWrites(t *testing.T) {
	h := newHarness(t)
	caller := &session{userID: "alice", userSession: "userSessions/alice", details: &SessionDetails{SessionID: "slow"}}
	name := refereeReportPrefix + "battle_slow/User/alice"
	ops := []*syncpb.WriteOperation{merge(doc(name, map[string]*commonpb.Value{"team_result": intValue(1), "personal_result": intValue(2)}))}
	h.srv.docsFor(caller).apply(ops)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	h.srv.onRefereeReport = func(context.Context, RefereeReport) error { close(entered); <-release; return nil }
	go func() {
		defer close(done)
		h.srv.observeRefereeReports(context.WithValue(context.Background(), callerKey{}, caller), ops)
	}()
	<-entered
	other := &session{userID: "bob", userSession: "userSessions/bob", details: &SessionDetails{SessionID: "fast"}}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		h.srv.afterWrites(context.WithValue(context.Background(), callerKey{}, other), other, []*syncpb.WriteOperation{merge(doc("docs/state/bob", nil))}, nil)
	}()
	select {
	case <-finished:
	case <-time.After(250 * time.Millisecond):
		close(release)
		<-done
		<-finished
		t.Fatal("an unrelated lobby write waited for the slow history callback")
	}
	close(release)
	<-done
}
