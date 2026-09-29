package toyohr

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/n-popescu/grid0plus/internal/db"
	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
)

func TestCompletedFestRanksRealMatchModesAndFreezesTheMidpoint(t *testing.T) {
	svc, store, fest := liveFest(t, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	for _, pick := range []struct{ user, team string }{
		{"p1", "Alpha"}, {"p2", "Alpha"}, {"p3", "Bravo"},
	} {
		as(t, pick.user)
		entryFor(t, svc, fest.ID, pick.team)
	}
	as(t, "reader")
	mid := fest.Timetable().Mid
	for _, b := range []db.FestBattle{
		{FestID: fest.ID, BattleID: "before", Mode: "fest_match_regular_normal_config",
			WinningTeam: "Bravo", Ink: map[string]int64{"Bravo": 2000, "Alpha": 1000}, FinishedAt: mid.Add(-time.Minute)},
		{FestID: fest.ID, BattleID: "after", Mode: "fest_match_challenge_oneshot_config",
			WinningTeam: "Alpha", Ink: map[string]int64{"Alpha": 3000}, FinishedAt: mid.Add(time.Minute)},
	} {
		if err := store.RecordFestBattle(context.Background(), b); err != nil {
			t.Fatal(err)
		}
	}
	get := func() *toyohrpb.FestResult {
		t.Helper()
		result, err := svc.GetFestResult(context.Background(), &toyohrpb.GetFestResultRequest{
			Name: "tenants/" + testTenant + "/festResults/" + fest.ID,
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	svc.now = func() time.Time { return mid.Add(2 * time.Minute) }
	live := get()
	if live.GetHonsaiMidterm().GetTeamRatios()[0].GetFestTeam() != "Bravo" || live.GetHonsaiFinal() != nil {
		t.Fatalf("midpoint must use only pre-midpoint ink and omit the final: %v", live)
	}
	svc.now = func() time.Time { return fest.Timetable().Close.Add(time.Minute) }
	final := get()
	if final.GetHonsaiMidterm().GetTeamRatios()[0].GetFestTeam() != "Bravo" {
		t.Fatalf("late battle changed frozen midpoint: %v", final.GetHonsaiMidterm())
	}
	if final.GetHonsaiFinal().GetTeamRatios2()[0].GetFestTeam() != "Bravo" ||
		final.GetHonsaiFinal().GetTeamRatios3()[0].GetFestTeam() != "Alpha" {
		t.Fatalf("regular and challenge wins were not scored separately: %v", final.GetHonsaiFinal())
	}
	points := final.GetOverall().GetPoints()
	if len(points) != 3 || points[0].GetFestTeam() != "Alpha" || points[0].GetPoints() != 460 ||
		points[1].GetFestTeam() != "Bravo" || points[1].GetPoints() != 290 {
		t.Fatalf("five-category points = %v, want Alpha 460 Bravo 290 Charlie 0; tied zero-win teams rank third", points)
	}
	if len(final.GetOverall().GetWeights().GetFields()) != 15 {
		t.Fatal("final result lost captured points weights")
	}
	key, err := svc.GetFestDecryptionKey(context.Background(), &toyohrpb.GetFestDecryptionKeyRequest{Name: fest.ID})
	if err != nil || key.GetWinningTeam() != points[0].GetFestTeam() {
		t.Fatalf("result-pack winner %q does not match awarded points %v (error %v)", key.GetWinningTeam(), points, err)
	}
	unknown := final.GetHonsaiFinal().ProtoReflect().GetUnknown()
	field, kind, n := protowire.ConsumeTag(unknown)
	if n < 0 || field != 5 || kind != protowire.BytesType {
		t.Fatalf("tricolor ranking missing from final wire field 5: %x", unknown)
	}
}
