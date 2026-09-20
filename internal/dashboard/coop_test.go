package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/n-popescu/switchnet/internal/toyohr"
)

// TestCoopShiftsCanBeGeneratedAndNeverOverwriteAManualOne exercises the
// dashboard's own front end for toyohr.GenerateAndSaveCoopShifts -- the
// server-side property (never overwriting an existing slot) is already
// covered in internal/toyohr/schedule_test.go; this checks the HTTP layer
// wires request/response correctly and does not lose that guarantee.
func TestCoopShiftsCanBeGeneratedAndNeverOverwriteAManualOne(t *testing.T) {
	now := time.Now().UTC()
	svc, store := newTestServer(t, "", now)
	h := svc.Handler()
	ctx := context.Background()

	from := toyohr.AlignToSlot(now.Add(2 * time.Hour))
	manual := toyohr.CoopShift{
		Kind: toyohr.CoopShiftNormal, StageID: 1, KumaWeaponID: 20900,
		Weapons: [4]int64{20000, 20010, 20020, 20030},
		Start:   from, End: from.Add(toyohr.DefaultCoopShiftDuration),
	}
	if err := store.UpsertCoopShift(ctx, toyohr.RowFromCoopShift(manual)); err != nil {
		t.Fatalf("seed manual shift: %v", err)
	}

	body := `{"fromAt":"` + from.Format(time.RFC3339) + `","count":3}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/coop-shifts/generate", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("generate: %d %s", rec.Code, rec.Body)
	}
	var resp struct{ Created, Skipped int }
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Created != 2 || resp.Skipped != 1 {
		t.Fatalf("created=%d skipped=%d, want created=2 skipped=1 (the pre-filled slot)", resp.Created, resp.Skipped)
	}

	still, err := store.GetCoopShift(ctx, manual.ID())
	if err != nil {
		t.Fatalf("GetCoopShift: %v", err)
	}
	if still.StageID != 1 {
		t.Errorf("manual slot's stage changed to %d, want it left at 1", still.StageID)
	}

	rows, err := svc.coopShiftRows(ctx)
	if err != nil {
		t.Fatalf("coopShiftRows: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d shifts stored total, want 3", len(rows))
	}
}

func TestGenerateCoopShiftsRejectsAZeroCount(t *testing.T) {
	srv, _ := newTestServer(t, "", time.Now().UTC())
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/coop-shifts/generate",
		strings.NewReader(`{"fromAt":"2026-09-05T00:00:00Z","count":0}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("count=0: %d %s, want 400", rec.Code, rec.Body)
	}
}

func TestACoopShiftCanBeScheduledAndDeleted(t *testing.T) {
	now := time.Now().UTC()
	srv, store := newTestServer(t, "", now)
	h := srv.Handler()

	start := toyohr.AlignToSlot(now.Add(2 * time.Hour))
	end := toyohr.AlignToSlot(now.Add(10 * time.Hour))
	body := `{
		"kind": "big_run",
		"stageId": 100,
		"boss": "SakeRope",
		"weapons": [21000, 21010, 21020, 21030],
		"kumaWeaponId": 21900,
		"rewards": [100, 200, 300],
		"startAt": "` + start.Format(time.RFC3339) + `",
		"endAt": "` + end.Format(time.RFC3339) + `"
	}`

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/coop-shifts", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var created struct{ ID string }
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if created.ID == "" {
		t.Fatal("no shift id returned")
	}

	if _, err := store.GetCoopShift(context.Background(), created.ID); err != nil {
		t.Fatalf("shift was not stored: %v", err)
	}

	rows, err := srv.coopShiftRows(context.Background())
	if err != nil {
		t.Fatalf("coopShiftRows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	row := rows[0]
	if row.StageName != "Wahoo World" {
		t.Errorf("stageName = %q, want the name coop_ids.go gives stage 100", row.StageName)
	}
	if row.KumaWeaponName != "Grizzco Roller" {
		t.Errorf("kumaWeaponName = %q, want Grizzco Roller", row.KumaWeaponName)
	}
	if len(row.WeaponNames) != 4 || row.WeaponNames[0] != "Carbon Roller" {
		t.Errorf("weaponNames = %v, want [Carbon Roller ...] in order", row.WeaponNames)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/coop-shifts/"+created.ID, nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if _, err := store.GetCoopShift(context.Background(), created.ID); err == nil {
		t.Error("the shift survived deletion")
	}
}

// TestATeamContestCoopShiftCanBeScheduled mirrors
// TestACoopShiftCanBeScheduledAndDeleted for team_contest specifically,
// since it is the one Kind with fields (boss, reward gear id) the wire
// message has no room for -- omitting both here is the happy path a real
// operator's request looks like once dashboard.js clears them before
// sending, per updateCoopKindFields/the coop-form submit handler.
func TestATeamContestCoopShiftCanBeScheduled(t *testing.T) {
	now := time.Now().UTC()
	srv, store := newTestServer(t, "", now)
	h := srv.Handler()

	start := toyohr.AlignToSlot(now.Add(2 * time.Hour))
	end := toyohr.AlignToSlot(now.Add(10 * time.Hour))
	body := `{
		"kind": "team_contest",
		"stageId": 1,
		"weapons": [20000, 20010, 20020, 20030],
		"kumaWeaponId": 20900,
		"rewards": [50, 100],
		"startAt": "` + start.Format(time.RFC3339) + `",
		"endAt": "` + end.Format(time.RFC3339) + `"
	}`

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/coop-shifts", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var created struct{ ID string }
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}

	shift, err := store.GetCoopShift(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("shift was not stored: %v", err)
	}
	if got, want := shift.Kind, "team_contest"; got != want {
		t.Errorf("stored kind = %q, want %q", got, want)
	}
}

// TestATeamContestCoopShiftWithABossIsRefused checks the HTTP layer
// actually goes through CoopShift.Validate for this kind's own rule
// (CoopSchedule_TeamContest has no boss field), the same reasoning
// TestCreatingACoopShiftWithAFakeIdIsRefused applies to a bad stage id.
func TestATeamContestCoopShiftWithABossIsRefused(t *testing.T) {
	now := time.Now().UTC()
	srv, _ := newTestServer(t, "", now)
	h := srv.Handler()

	start := toyohr.AlignToSlot(now.Add(2 * time.Hour))
	end := toyohr.AlignToSlot(now.Add(10 * time.Hour))
	body := `{
		"kind": "team_contest",
		"stageId": 1,
		"boss": "SakeJaw",
		"weapons": [20000, 20010, 20020, 20030],
		"kumaWeaponId": 20900,
		"startAt": "` + start.Format(time.RFC3339) + `",
		"endAt": "` + end.Format(time.RFC3339) + `"
	}`

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/coop-shifts", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create with a boss on team_contest: %d %s, want 400", rec.Code, rec.Body)
	}
}

// TestCreatingACoopShiftWithAFakeIdIsRefused checks that the dashboard
// actually goes through CoopShift.Validate rather than storing whatever it
// is handed -- the one property CLAUDE.md rule 6 makes matter for this
// endpoint specifically.
func TestCreatingACoopShiftWithAFakeIdIsRefused(t *testing.T) {
	now := time.Now().UTC()
	srv, _ := newTestServer(t, "", now)
	h := srv.Handler()

	start := toyohr.AlignToSlot(now.Add(2 * time.Hour))
	end := toyohr.AlignToSlot(now.Add(10 * time.Hour))
	body := `{
		"kind": "normal",
		"stageId": 999999,
		"weapons": [20000, 20010, 20020, 20030],
		"kumaWeaponId": 20900,
		"startAt": "` + start.Format(time.RFC3339) + `",
		"endAt": "` + end.Format(time.RFC3339) + `"
	}`

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/coop-shifts", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create with a fake stage id: %d %s, want 400", rec.Code, rec.Body)
	}
}

func TestCoopShiftStatusReflectsWhereItIsInTime(t *testing.T) {
	now := time.Now().UTC()
	srv, store := newTestServer(t, "", now)
	ctx := context.Background()

	upcoming := toyohr.CoopShift{
		Kind: toyohr.CoopShiftNormal, StageID: 1, KumaWeaponID: 20900,
		Weapons: [4]int64{20000, 20010, 20020, 20030},
		Start:   toyohr.AlignToSlot(now.Add(24 * time.Hour)), End: toyohr.AlignToSlot(now.Add(32 * time.Hour)),
	}
	live := toyohr.CoopShift{
		Kind: toyohr.CoopShiftNormal, StageID: 2, KumaWeaponID: 20900,
		Weapons: [4]int64{20000, 20010, 20020, 20030},
		Start:   toyohr.AlignToSlot(now.Add(-2 * time.Hour)), End: toyohr.AlignToSlot(now.Add(6 * time.Hour)),
	}
	ended := toyohr.CoopShift{
		Kind: toyohr.CoopShiftNormal, StageID: 4, KumaWeaponID: 20900,
		Weapons: [4]int64{20000, 20010, 20020, 20030},
		Start:   toyohr.AlignToSlot(now.Add(-48 * time.Hour)), End: toyohr.AlignToSlot(now.Add(-40 * time.Hour)),
	}
	for _, c := range []toyohr.CoopShift{upcoming, live, ended} {
		if err := store.UpsertCoopShift(ctx, toyohr.RowFromCoopShift(c)); err != nil {
			t.Fatalf("seed shift: %v", err)
		}
	}

	rows, err := srv.coopShiftRows(ctx)
	if err != nil {
		t.Fatalf("coopShiftRows: %v", err)
	}
	got := map[int32]string{}
	for _, r := range rows {
		got[r.StageID] = r.Status
	}
	want := map[int32]string{1: "upcoming", 2: "live", 4: "ended"}
	for stage, status := range want {
		if got[stage] != status {
			t.Errorf("stage %d status = %q, want %q", stage, got[stage], status)
		}
	}
}

func TestCoopReferenceServesTheRealTables(t *testing.T) {
	srv, _ := newTestServer(t, "", time.Now().UTC())
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/coop-reference", nil))
	if rec.Code != 200 {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}

	var resp struct {
		Stages      []coopReferenceEntry `json:"stages"`
		Weapons     []coopReferenceEntry `json:"weapons"`
		KumaWeapons []coopReferenceEntry `json:"kumaWeapons"`
		Bosses      []string             `json:"bosses"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Stages) != len(toyohr.CoopStages) {
		t.Errorf("got %d stages, want %d", len(resp.Stages), len(toyohr.CoopStages))
	}
	if len(resp.Weapons) != len(toyohr.CoopWeapons) {
		t.Errorf("got %d weapons, want %d", len(resp.Weapons), len(toyohr.CoopWeapons))
	}
	if len(resp.KumaWeapons) != len(toyohr.CoopKumaWeapons) {
		t.Errorf("got %d kuma weapons, want %d", len(resp.KumaWeapons), len(toyohr.CoopKumaWeapons))
	}
	if len(resp.Bosses) != len(toyohr.CoopBosses) {
		t.Errorf("got %d bosses, want %d", len(resp.Bosses), len(toyohr.CoopBosses))
	}
}

// TestCoopReferenceStagesAlwaysCarryABigRunKey is a regression test: an
// earlier version tagged BigRun with `json:"bigRun,omitempty"`, so a normal
// stage's response had no "bigRun" key at all rather than "bigRun":false.
// dashboard.js's stage filter compares this field against a JS boolean
// (`s.bigRun === bigRun`), and a missing key reads as `undefined`, which is
// never `=== false` -- every normal stage silently vanished from the
// schedule form's dropdown. Only caught by actually running the page in a
// browser; this pins the wire contract that bug depended on.
func TestCoopReferenceStagesAlwaysCarryABigRunKey(t *testing.T) {
	srv, _ := newTestServer(t, "", time.Now().UTC())
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/coop-reference", nil))

	var raw struct {
		Stages []map[string]any `json:"stages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, s := range raw.Stages {
		if _, ok := s["bigRun"]; !ok {
			t.Fatalf("stage %v has no \"bigRun\" key at all", s)
		}
	}
}
