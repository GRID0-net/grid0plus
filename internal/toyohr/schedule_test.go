package toyohr

import (
	"context"
	"testing"
	"time"

	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

// TestScheduleRPCsAnswerEmptyRatherThanUnimplemented is the "minimum that
// does not crash the client" property: SwitchNet has no map/mode rotation
// data (that is Nintendo's own game design content, not something this
// server can fabricate -- see schedule.go), but the console calling these
// early in boot must get a structurally valid "nothing scheduled" answer,
// not a stream of Unimplemented refusals.
func TestScheduleRPCsAnswerEmptyRatherThanUnimplemented(t *testing.T) {
	svc, _ := newService(t, time.Now())
	ctx := context.Background()

	if _, err := svc.SelectVsParams(ctx, &toyohrpb.SelectVsParamsRequest{}); err != nil {
		t.Errorf("SelectVsParams: %v", err)
	}
	vs, err := svc.SelectVsSchedules(ctx, &toyohrpb.SelectVsSchedulesRequest{})
	if err != nil {
		t.Errorf("SelectVsSchedules: %v", err)
	} else if len(vs.GetSchedules()) != 0 {
		t.Errorf("SelectVsSchedules: got %d schedules, want 0", len(vs.GetSchedules()))
	}
	coop, err := svc.SelectCoopSchedules(ctx, &toyohrpb.SelectCoopSchedulesRequest{})
	if err != nil {
		t.Errorf("SelectCoopSchedules: %v", err)
	} else if len(coop.GetSchedules()) != 0 {
		t.Errorf("SelectCoopSchedules: got %d schedules, want 0", len(coop.GetSchedules()))
	}
	season, err := svc.SelectSeasonSchedules(ctx, &toyohrpb.SelectSeasonSchedulesRequest{})
	if err != nil {
		t.Errorf("SelectSeasonSchedules: %v", err)
	} else if len(season.GetSchedules()) != 0 {
		t.Errorf("SelectSeasonSchedules: got %d schedules, want 0", len(season.GetSchedules()))
	}
	league, err := svc.SelectLeagueSchedules(ctx, &toyohrpb.SelectLeagueSchedulesRequest{})
	if err != nil {
		t.Errorf("SelectLeagueSchedules: %v", err)
	} else if len(league.GetSchedules()) != 0 {
		t.Errorf("SelectLeagueSchedules: got %d schedules, want 0", len(league.GetSchedules()))
	}
}

// TestSelectCoopSchedulesServesASavedBigRunShiftAsTheBigRunSubMessage checks
// the one property CLAUDE.md rule 6 makes matter here: a shift saved as
// big_run must come back with BigRun populated and Normal nil, carrying the
// exact stage/weapon/boss ids it was saved with -- not a shape this project
// invented, but nn.npln.toyohr.v1.CoopSchedule's own decompiled fields.
func TestSelectCoopSchedulesServesASavedBigRunShiftAsTheBigRunSubMessage(t *testing.T) {
	now := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	svc, _ := newService(t, now)
	ctx := context.Background()

	shift := CoopShift{
		Kind:         CoopShiftBigRun,
		Start:        now.Add(2 * time.Hour),
		End:          now.Add(50 * time.Hour),
		StageID:      100, // Wahoo World
		Boss:         "SakeRope",
		Weapons:      [4]int64{21000, 21010, 21020, 21030},
		KumaWeaponID: 21900,
		Rewards:      []int64{100, 200, 300},
	}
	if err := svc.SaveCoopShift(ctx, shift); err != nil {
		t.Fatalf("SaveCoopShift: %v", err)
	}

	resp, err := svc.SelectCoopSchedules(ctx, &toyohrpb.SelectCoopSchedulesRequest{})
	if err != nil {
		t.Fatalf("SelectCoopSchedules: %v", err)
	}
	if len(resp.GetSchedules()) != 1 {
		t.Fatalf("got %d schedules, want 1", len(resp.GetSchedules()))
	}

	got := resp.GetSchedules()[0]
	if got.GetNormal() != nil {
		t.Error("a big_run shift was served with Normal populated")
	}
	br := got.GetBigRun()
	if br == nil {
		t.Fatal("a big_run shift was served with BigRun nil")
	}
	if br.GetStage() != 100 || br.GetBoss() != "SakeRope" || br.GetKumaWeapon() != 21900 {
		t.Errorf("BigRun = %+v, does not match the saved shift", br)
	}
	if len(br.GetMainWeapons()) != 4 || br.GetMainWeapons()[0] != 21000 {
		t.Errorf("BigRun.MainWeapons = %v, want [21000 21010 21020 21030] in order", br.GetMainWeapons())
	}
	if len(br.GetRewards()) != 3 || br.GetRewards()[0] != 100 {
		t.Errorf("BigRun.Rewards = %v, want [100 200 300] in order", br.GetRewards())
	}
}

// TestSelectCoopSchedulesDropsShiftsThatHaveAlreadyEnded mirrors
// currentFest's own "not just anything stored" filtering: a console asking
// what is scheduled should not be told about a shift whose window has
// already closed.
func TestSelectCoopSchedulesDropsShiftsThatHaveAlreadyEnded(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	svc, _ := newService(t, now)
	ctx := context.Background()

	past := CoopShift{
		Kind: CoopShiftNormal,
		// Both well before now, so End is in the past.
		Start: now.Add(-48 * time.Hour), End: now.Add(-40 * time.Hour),
		StageID: 1, Weapons: [4]int64{20000, 20010, 20020, 20030}, KumaWeaponID: 20900,
	}
	if err := svc.SaveCoopShift(ctx, past); err != nil {
		t.Fatalf("SaveCoopShift: %v", err)
	}

	resp, err := svc.SelectCoopSchedules(ctx, &toyohrpb.SelectCoopSchedulesRequest{})
	if err != nil {
		t.Fatalf("SelectCoopSchedules: %v", err)
	}
	if len(resp.GetSchedules()) != 0 {
		t.Errorf("got %d schedules, want 0 (the only shift stored has already ended)", len(resp.GetSchedules()))
	}
}

// TestSaveCoopShiftRejectsAnInvalidShift checks that SaveCoopShift actually
// calls Validate rather than storing whatever it is given -- coop_schedule_test.go
// already covers Validate's own rules in detail, so this only needs one.
func TestSaveCoopShiftRejectsAnInvalidShift(t *testing.T) {
	svc, _ := newService(t, time.Now())
	bad := validNormalShift()
	bad.StageID = 999999
	if err := svc.SaveCoopShift(context.Background(), bad); err == nil {
		t.Error("SaveCoopShift accepted a shift with an unknown stage id")
	}
}
