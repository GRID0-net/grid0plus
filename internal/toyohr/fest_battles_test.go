package toyohr

import (
	"context"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/gamesync"
	commonpb "github.com/n-popescu/grid0plus/proto/common"
)

func TestRefereeReportRecordsOneBattleForTheAuthenticatedFestSession(t *testing.T) {
	svc, store, fest := liveFest(t, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	as(t, "p1")
	entryFor(t, svc, fest.ID, "Alpha")
	as(t, "p2")
	entryFor(t, svc, fest.ID, "Bravo")
	str := func(s string) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_StringValue{StringValue: s}}
	}
	integer := func(n int64) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_IntegerValue{IntegerValue: n}}
	}
	mapV := func(f map[string]*commonpb.Value) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: f}}}
	}
	report := gamesync.RefereeReport{
		BattleID: "20260905T010000_room-1", SessionID: "room-1", Mode: "fest_match_regular_normal_config",
		UserID: "p1", Players: []string{"p1", "p2"},
		Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
			"team_result": mapV(map[string]*commonpb.Value{"winner": integer(2)}),
			"personal_result": mapV(map[string]*commonpb.Value{
				"a":      mapV(map[string]*commonpb.Value{"npln_user_id": str("p1"), "team": integer(1), "paint_point": integer(900)}),
				"b":      mapV(map[string]*commonpb.Value{"npln_user_id": str("p2"), "team": integer(2), "paint_point": integer(1400)}),
				"forged": mapV(map[string]*commonpb.Value{"npln_user_id": str("outsider"), "team": integer(2), "paint_point": integer(99999)}),
			}),
		}},
	}
	when := fest.Timetable().Start.Add(time.Hour)
	for range 2 {
		if err := RecordFestReport(context.Background(), store, report, when); err != nil {
			t.Fatal(err)
		}
	}
	battles, err := store.ListFestBattles(context.Background(), fest.ID)
	if err != nil || len(battles) != 1 || battles[0].WinningTeam != "Bravo" ||
		battles[0].Ink["Bravo"] != 1400 || battles[0].Ink["Alpha"] != 900 {
		t.Fatalf("stored battle = %v, %v", battles, err)
	}
	report.BattleID = "unrelated-room"
	if err := RecordFestReport(context.Background(), store, report, when); err != nil {
		t.Fatal(err)
	}
	again, _ := store.ListFestBattles(context.Background(), fest.ID)
	if len(again) != 1 {
		t.Fatal("report for an unrelated session was counted")
	}
}
