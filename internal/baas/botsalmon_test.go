package baas

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/toyohr"
	"github.com/n-popescu/grid0plus/internal/ugcstore"
	commonpb "github.com/n-popescu/grid0plus/proto/common"
)

const salmonTenant = "t-dce9377b-lp1"

func TestTheHazardIsTheAverageRankTimesPointTwoInFivePercentSteps(t *testing.T) {
	for _, tc := range []struct {
		totals []int64
		want   int64
	}{
		{[]int64{345, 480, 920}, 115}, // 581.7 -> 116.3%
		{[]int64{350, 420}, 75},       // 385 -> 77%
		{[]int64{0, 0}, 0},
		{[]int64{1799, 1799, 1799, 1799}, 333}, // capped at Eggsecutive VP 865
		{[]int64{1000, 1050}, 205},             // 1025 -> 205%
	} {
		if got := salmonHazard(tc.totals); got != tc.want {
			t.Errorf("salmonHazard(%v) = %d, want %d", tc.totals, got, tc.want)
		}
	}
}

func TestALANJobIsWorthWhatTheGameGives(t *testing.T) {
	for _, tc := range []struct {
		name    string
		result  db.SalmonResult
		halfWin bool
		want    int64
	}{
		{"clear", db.SalmonResult{Cleared: true}, false, 20},
		{"clear below the asked hazard", db.SalmonResult{Cleared: true}, true, 10},
		{"wave 1", db.SalmonResult{Wave: 1}, false, -20},
		{"wave 2", db.SalmonResult{Wave: 2}, true, -10},
		{"wave 3", db.SalmonResult{Wave: 3}, false, 0},
		{"wave 1, a teammate left", db.SalmonResult{Wave: 1, Disconnect: true}, false, -10},
		{"wave 2, a teammate left", db.SalmonResult{Wave: 2, Disconnect: true}, false, -5},
	} {
		r := tc.result
		if got := salmonDelta(db.SalmonJob{Result: &r, HalfWin: tc.halfWin}); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}
}

type salmonHarness struct {
	*botHarness
	shift string
}

func newSalmonHarness(t *testing.T) *salmonHarness {
	h := newHarness(t)
	bot := httptest.NewServer(h.srv.BotHandler(BotAPIOptions{Token: testBotToken, NPLNTenant: salmonTenant}))
	t.Cleanup(bot.Close)
	shift, ok, err := toyohr.CurrentCoopShift(t.Context(), h.store, h.clock())
	if err != nil || !ok {
		t.Fatalf("no current shift: %v", err)
	}
	return &salmonHarness{botHarness: &botHarness{harness: h, bot: bot}, shift: toyohr.CoopShiftID(shift.Start)}
}

// player registers a Discord user whose rank is total, in this rotation.
func (s *salmonHarness) player(discord, name string, total int64) string {
	s.t.Helper()
	s.registerDiscord(discord, name)
	u, err := s.store.GetUserByDiscordID(s.t.Context(), discord)
	if err != nil {
		s.t.Fatal(err)
	}
	fields, _ := proto.MarshalOptions{Deterministic: true}.Marshal(&commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"total_grade_point": {ValueType: &commonpb.Value_IntegerValue{IntegerValue: total}},
		"shift_id":          {ValueType: &commonpb.Value_StringValue{StringValue: s.shift}},
	}})
	now := s.clock()
	if err := s.store.PutUGCDocument(s.t.Context(), db.UGCDocument{
		Name:  "tenants/" + salmonTenant + "/documents/services/GameRecord/users/" + u.ID + "/CoopUserAttribute/Data",
		Owner: u.ID, Fields: fields, CreateTime: now, UpdateTime: now,
	}); err != nil {
		s.t.Fatal(err)
	}
	return u.ID
}

func (s *salmonHarness) total(uid string) int64 {
	s.t.Helper()
	total, _, err := ugcstore.CoopGradePoint(s.t.Context(), s.store, salmonTenant, uid)
	if err != nil {
		s.t.Fatal(err)
	}
	return total
}

func TestALANSalmonJobMovesTheGameRankOnlyOnceAModAccepts(t *testing.T) {
	s := newSalmonHarness(t)
	alice := s.player(aliceDiscord, "alice", 345)
	s.player(bobDiscord, "bob", 480)

	if resp, body := s.call("POST", "/bot/v1/salmon/jobs", aliceDiscord,
		map[string]any{"players": []string{aliceDiscord}}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a one-player job: %d %v", resp.StatusCode, body)
	}
	if resp, body := s.call("POST", "/bot/v1/salmon/jobs", aliceDiscord,
		map[string]any{"players": []string{aliceDiscord, carolDiscord}}); resp.StatusCode != http.StatusConflict ||
		body["code"] != "players_not_registered" {
		t.Fatalf("a job with an unregistered player: %d %v", resp.StatusCode, body)
	}
	resp, job := s.call("POST", "/bot/v1/salmon/jobs", aliceDiscord,
		map[string]any{"players": []string{aliceDiscord, bobDiscord}})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("request: %d %v", resp.StatusCode, job)
	}
	// (345 + 480) / 2 = 412.5, x 0.2 = 82.5%, nearest 5%: 85%.
	if job["hazard"] != 85.0 || job["played"] != 85.0 || job["halfWin"] != false || job["stage"] == "" {
		t.Fatalf("job = %v", job)
	}
	id := job["id"].(string)

	if resp, _ := s.call("POST", "/bot/v1/salmon/jobs/"+id+"/report", carolDiscord,
		map[string]any{"cleared": true}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a report from outside the job: %d", resp.StatusCode)
	}
	if resp, body := s.call("POST", "/bot/v1/salmon/jobs/"+id+"/report", bobDiscord,
		map[string]any{"cleared": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("report: %d %v", resp.StatusCode, body)
	}
	if got := s.total(alice); got != 345 {
		t.Fatalf("a reported job moved the rank before review: %d", got)
	}
	for i := 0; i < 2; i++ {
		resp, body := s.call("POST", "/bot/v1/salmon/jobs/"+id+"/accept", modDiscord, nil)
		if want := []int{http.StatusOK, http.StatusConflict}[i]; resp.StatusCode != want {
			t.Fatalf("accept %d: %d %v", i, resp.StatusCode, body)
		}
	}
	if got := s.total(alice); got != 365 {
		t.Fatalf("alice = %d after one accepted clear, want 365", got)
	}
}

func TestAJobAbove200PercentOnAStockHostPaysHalfAClear(t *testing.T) {
	s := newSalmonHarness(t)
	alice := s.player(aliceDiscord, "alice", 1700)
	s.player(bobDiscord, "bob", 1300)
	_, job := s.call("POST", "/bot/v1/salmon/jobs", aliceDiscord,
		map[string]any{"players": []string{aliceDiscord, bobDiscord}})
	// 1700 counts as 1665: (1665 + 1300) / 2 x 0.2 = 296.5%, so 295%.
	if job["hazard"] != 295.0 || job["played"] != 200.0 || job["halfWin"] != true {
		t.Fatalf("job = %v", job)
	}
	id := job["id"].(string)
	s.call("POST", "/bot/v1/salmon/jobs/"+id+"/report", aliceDiscord, map[string]any{"cleared": true})
	s.call("POST", "/bot/v1/salmon/jobs/"+id+"/accept", modDiscord, nil)
	if got := s.total(alice); got != 1710 {
		t.Fatalf("alice = %d, want 1710 (+10)", got)
	}

	_, modded := s.call("POST", "/bot/v1/salmon/jobs", aliceDiscord,
		map[string]any{"players": []string{aliceDiscord, bobDiscord}, "modded": true})
	if modded["played"] != modded["hazard"] || modded["halfWin"] != false {
		t.Fatalf("modded job = %v", modded)
	}
}
