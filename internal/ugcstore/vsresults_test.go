package ugcstore

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	commonpb "github.com/n-popescu/grid0plus/proto/common"
	ugcpb "github.com/n-popescu/grid0plus/proto/ugcstore/v1"
)

func TestAFinishedBattleAppearsInEachPlayersHistory(t *testing.T) {
	store := db.NewMemory()
	now := time.Date(2026, 10, 3, 14, 0, 0, 0, time.UTC)
	integer := func(n int64) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_IntegerValue{IntegerValue: n}}
	}
	player := func(team int64) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{
			Fields: map[string]*commonpb.Value{"team": integer(team)}}}}
	}
	report := &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"team_result": {ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{
			Fields: map[string]*commonpb.Value{"winner": integer(1)}}}},
		"personal_result": {ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{
			Fields: map[string]*commonpb.Value{"alice": player(1), "bob": player(2)}}}},
	}}
	ctx := context.Background()
	for _, battle := range []string{"20261003T135000_old", "20261003T140000_new"} {
		if err := RecordVsResults(ctx, store, "dce9377b", battle, []string{"alice", "bob"}, report, now); err != nil {
			t.Fatalf("RecordVsResults: %v", err)
		}
	}
	// Another document under the same player must not answer a battle query:
	// the console aborts with 2162-0001 when it gets one.
	_ = store.PutUGCDocument(ctx, db.UGCDocument{
		Name:  "tenants/dce9377b/documents/services/GameRecord/users/bob/VsUserAttribute/Data",
		Owner: "bob", CreateTime: now, UpdateTime: now,
	})

	svc := NewService(Options{
		Store: store, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Tenant: "dce9377b",
		CallerID: func(context.Context) (string, bool) { return "bob", true },
		Now:      func() time.Time { return now },
	})
	stream := &runQueryStream{ctx: ctx}
	err := svc.RunQuery(&ugcpb.RunQueryRequest{
		Parent: "tenants/current/documents/services/GameRecord/users/bob",
		QueryType: &ugcpb.RunQueryRequest_StructuredQuery{StructuredQuery: &ugcpb.StructuredQuery{
			From: []*ugcpb.StructuredQuery_CollectionSelector{{CollectionId: "VsResults"}},
		}},
	}, stream)
	if err != nil {
		t.Fatalf("RunQuery: %v", err)
	}
	if len(stream.sent) != 2 {
		t.Fatalf("sent %d documents, want bob's 2 battles and nothing else", len(stream.sent))
	}
	first := stream.sent[0].GetDocument()
	if got := first.GetFields().GetFields()["battle_id"].GetStringValue(); got != "20261003T140000_new" {
		t.Errorf("first battle = %q, want the newest", got)
	}
	if n := len(first.GetFields().GetFields()["personal_data_team2"].GetArrayValue().GetValues()); n != 1 {
		t.Errorf("personal_data_team2 has %d players, want bob", n)
	}
}
