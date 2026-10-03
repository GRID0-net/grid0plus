package ugcstore

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	"google.golang.org/protobuf/types/known/timestamppb"

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
		if err := RecordVsResults(ctx, store, "dce9377b", battle, []string{"alice", "bob"}, report, nil, now); err != nil {
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

// TestTheHistoryFieldsSitAtTheTopOfTheBattleDocument: the history screen's
// reader takes started_at, winner, score_team%d and the rest from the top
// level; only inside team_result it showed 1/1/1970, 0p and a win for all.
func TestTheHistoryFieldsSitAtTheTopOfTheBattleDocument(t *testing.T) {
	store := db.NewMemory()
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 21, 50, 0, 0, time.UTC)
	integer := func(n int64) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_IntegerValue{IntegerValue: n}}
	}
	started := &commonpb.Value{ValueType: &commonpb.Value_TimestampValue{TimestampValue: timestamppb.New(now)}}
	report := &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"team_result": {ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
			"started_at": started, "winner": integer(2), "score_team1": integer(311), "score_team2": integer(846),
		}}}},
	}}
	verdict := &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"stage": integer(14), "game_rule": integer(0), "game_mode": integer(1),
	}}
	if err := RecordVsResults(ctx, store, "dce9377b", "20261003T215000_m", []string{"alice"}, report, verdict, now); err != nil {
		t.Fatal(err)
	}
	d, err := store.GetUGCDocument(ctx, "tenants/dce9377b/documents/services/GameRecord/users/alice/VsResults/20261003T215000_m")
	if err != nil {
		t.Fatal(err)
	}
	f, _ := decodeFields(d.Fields)
	top := f.GetFields()
	if !top["started_at"].GetTimestampValue().AsTime().Equal(now) {
		t.Errorf("started_at = %v, want the battle's start at the top level", top["started_at"])
	}
	for k, want := range map[string]int64{"winner": 2, "score_team1": 311, "score_team2": 846, "stage": 14, "game_mode": 1} {
		if got := top[k].GetIntegerValue(); got != want || top[k] == nil {
			t.Errorf("%s = %v, want %d at the top level", k, top[k], want)
		}
	}
}
