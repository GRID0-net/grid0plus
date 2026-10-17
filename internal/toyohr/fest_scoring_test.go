package toyohr

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/n-popescu/grid0plus/internal/db"
	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
)

func TestCompletedFestRanksRealMatchModesAndFreezesTheMidpoint(t *testing.T) {
	svc, store, fest := liveFest(t, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	keys := []string{strings.Repeat("11", 16), strings.Repeat("22", 16), "",
		strings.Repeat("33", 16), strings.Repeat("44", 16), strings.Repeat("55", 16)}
	if err := svc.SaveFest(context.Background(), fest, true, keys); err != nil {
		t.Fatal(err)
	}
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
	if len(points) != 3 || points[0].GetFestTeam() != "Alpha" || points[0].GetPoints() != 250 ||
		points[1].GetFestTeam() != "Bravo" || points[1].GetPoints() != 215 || points[2].GetPoints() != 120 {
		// Votes 70/35/0; Open Bravo 120, Alpha and Charlie tied second 60
		// each; Pro Alpha 120, the others 60; no Tricolor game and no conch
		// points, so nothing from either.
		t.Fatalf("five-category points = %v, want Alpha 250 Bravo 215 Charlie 120; tied teams share the best rank of their tie", points)
	}
	if len(final.GetOverall().GetWeights().GetFields()) != 15 {
		t.Fatal("final result lost captured points weights")
	}
	key, err := svc.GetFestDecryptionKey(context.Background(), &toyohrpb.GetFestDecryptionKeyRequest{Name: fest.ID})
	if err != nil || key.GetWinningTeam() != points[0].GetFestTeam() {
		t.Fatalf("result-pack winner %q does not match awarded points %v (error %v)", key.GetWinningTeam(), points, err)
	}
	wire, err := proto.Marshal(key)
	if err != nil {
		t.Fatal(err)
	}
	seenResult := false
	for len(wire) > 0 {
		field, kind, n := protowire.ConsumeTag(wire)
		if n < 0 {
			t.Fatal("invalid result key wire tag")
		}
		wire = wire[n:]
		if field >= 5 && field <= 7 {
			t.Fatal("ordinary result response leaked optional team keys")
		}
		if field == 4 {
			value, n := protowire.ConsumeString(wire)
			if kind != protowire.BytesType || n < 0 || value != keys[3] {
				t.Fatalf("winner's result-pack key is missing or wrong: %q", value)
			}
			seenResult = true
		}
		n = protowire.ConsumeFieldValue(field, kind, wire)
		if n < 0 {
			t.Fatal("invalid result key wire value")
		}
		wire = wire[n:]
	}
	if !seenResult {
		t.Fatal("winning archive key omitted from wire field 4")
	}
	unknown := final.GetHonsaiFinal().ProtoReflect().GetUnknown()
	field, kind, n := protowire.ConsumeTag(unknown)
	if n < 0 || field != 5 || kind != protowire.BytesType {
		t.Fatalf("tricolor ranking missing from final wire field 5: %x", unknown)
	}
}

func TestAShareNeverReachesAHundredPercent(t *testing.T) {
	// One team holding every point is a whole 1.0, which the halftime report
	// showed as 0% for everyone.
	ranked := rankedShares(map[string]float64{"Alpha": 1, "Bravo": 0, "Charlie": 0})
	if ranked[0].GetFestTeam() != "Alpha" || ranked[0].GetRatio() >= 1 {
		t.Fatalf("ranked = %v, want Alpha first below 1.0", ranked)
	}
}
