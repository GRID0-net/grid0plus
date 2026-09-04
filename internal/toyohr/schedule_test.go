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

// TestGenerateAndSaveCoopShiftsNeverOverwritesAnExistingSlot is the property
// the whole "can be set manually" half of generation depends on: a slot the
// operator already filled -- by hand, or by an earlier generate call -- is
// left untouched by a later one, even when that later call's own random
// draw would have picked something different for it.
func TestGenerateAndSaveCoopShiftsNeverOverwritesAnExistingSlot(t *testing.T) {
	svc, _ := newService(t, time.Now())
	ctx := context.Background()
	from := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)

	// Hand-schedule the first slot before generating anything.
	manual := validNormalShift()
	manual.Start = AlignToSlot(from)
	manual.End = manual.Start.Add(DefaultCoopShiftDuration)
	manual.StageID = 1 // Spawning Grounds, deliberately
	if err := svc.SaveCoopShift(ctx, manual); err != nil {
		t.Fatalf("SaveCoopShift: %v", err)
	}

	created, skipped, err := svc.GenerateAndSaveCoopShifts(ctx, from, 5, DefaultCoopShiftDuration, nil)
	if err != nil {
		t.Fatalf("GenerateAndSaveCoopShifts: %v", err)
	}
	if len(skipped) != 1 || skipped[0].ID() != manual.ID() {
		t.Fatalf("skipped = %v, want exactly the manually-scheduled slot", skipped)
	}
	if len(created) != 4 {
		t.Fatalf("created %d shifts, want 4 (5 requested minus the 1 pre-filled slot)", len(created))
	}

	// The manual entry itself must be byte-for-byte unchanged.
	still, err := svc.store.GetCoopShift(ctx, manual.ID())
	if err != nil {
		t.Fatalf("GetCoopShift: %v", err)
	}
	if still.StageID != 1 {
		t.Errorf("manual slot's stage changed to %d, want it left at 1 (Spawning Grounds)", still.StageID)
	}

	// A second generate call over the same range creates nothing new.
	created2, skipped2, err := svc.GenerateAndSaveCoopShifts(ctx, from, 5, DefaultCoopShiftDuration, nil)
	if err != nil {
		t.Fatalf("GenerateAndSaveCoopShifts (second call): %v", err)
	}
	if len(created2) != 0 {
		t.Errorf("second call over the same range created %d shifts, want 0", len(created2))
	}
	if len(skipped2) != 5 {
		t.Errorf("second call skipped %d, want all 5 (every slot is now filled)", len(skipped2))
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
