package gamesync

import (
	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
	"testing"
)

func TestATransactionCannotMoveToAnotherRoom(t *testing.T) {
	h := newHarness(t)
	a, err := h.srv.newSession("alice", "userSessions/a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.srv.newSession("alice", "userSessions/b")
	if err != nil {
		t.Fatal(err)
	}
	a.details = &SessionDetails{SessionID: "room-a"}
	b.details = &SessionDetails{SessionID: "room-b"}
	begin, err := h.client.BeginTransaction(h.authed(a.accessToken), &syncpb.BeginTransactionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	writes := []*syncpb.WriteOperation{merge(doc("docs/test/a", map[string]*commonpb.Value{"number": intValue(1)}))}
	if _, err := h.client.CommitTransaction(h.authed(b.accessToken), &syncpb.CommitTransactionRequest{Transaction: begin.GetTransaction(), WriteOperations: writes}); err == nil {
		t.Fatal("transaction from another room accepted")
	}
	if _, err := h.client.CommitTransaction(h.authed(a.accessToken), &syncpb.CommitTransactionRequest{Transaction: begin.GetTransaction(), WriteOperations: writes}); err != nil {
		t.Fatalf("wrong-room attempt consumed transaction: %v", err)
	}
}
