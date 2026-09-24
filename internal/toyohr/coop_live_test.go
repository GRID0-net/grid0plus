package toyohr

import (
	"context"
	"reflect"
	"testing"
	"time"

	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

// TestLiveNormalCoopShiftsIsDeterministic is the whole point of
// liveNormalCoopShifts: the same real moment must produce the same
// shift, computed fresh, with nothing stored -- "random deterministic
// maps", the property this was built to give Coop that Vs already has.
func TestLiveNormalCoopShiftsIsDeterministic(t *testing.T) {
	now := time.Date(2026, 11, 3, 17, 0, 0, 0, time.UTC)
	a := liveNormalCoopShifts(now, 3)
	b := liveNormalCoopShifts(now, 3)
	if len(a) != 3 || len(b) != 3 {
		t.Fatalf("got %d/%d shifts, want 3/3", len(a), len(b))
	}
	for i := range a {
		if !reflect.DeepEqual(a[i], b[i]) {
			t.Errorf("shift %d differs across two calls for the same now: %+v vs %+v", i, a[i], b[i])
		}
	}

	// A different moment within the same 40-hour slot must produce the
	// exact same first shift -- the slot, not the instant, is what seeds it.
	later := now.Add(3 * time.Hour)
	c := liveNormalCoopShifts(later, 1)
	if !reflect.DeepEqual(c[0], a[0]) {
		t.Errorf("two moments in the same 40h slot produced different shifts: %+v vs %+v", c[0], a[0])
	}
}

// TestLiveNormalCoopShiftsAreValid guards against the live generator ever
// producing something SaveCoopShift's own Validate would reject -- it uses
// the same real id tables as GenerateCoopShifts, so it should be exactly as
// valid.
func TestLiveNormalCoopShiftsAreValid(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, shift := range liveNormalCoopShifts(now, 10) {
		if err := shift.Validate(); err != nil {
			t.Errorf("live shift failed Validate: %v (%+v)", err, shift)
		}
	}
}

// TestSelectCoopSchedulesLiveFillsGapsNotOverridesStoredSlots is the
// regression test for SelectCoopSchedules' merge policy: a hand-authored or
// previously-generated Normal shift for a given slot must still win over
// the live fallback for that same slot.
func TestSelectCoopSchedulesLiveFillsGapsNotOverridesStoredSlots(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc, _ := newService(t, now)
	sc := &scheduleService{s: svc}

	live := liveNormalCoopShifts(now, 1)[0]
	override := live
	override.StageID = otherNormalCoopStageID(t, live.StageID)
	if err := svc.SaveCoopShift(context.Background(), override); err != nil {
		t.Fatalf("SaveCoopShift: %v", err)
	}

	resp, err := sc.SelectCoopSchedules(context.Background(), &toyohrpb.SelectCoopSchedulesRequest{})
	if err != nil {
		t.Fatalf("SelectCoopSchedules: %v", err)
	}
	got := findCoopScheduleByShiftID(t, resp.GetSchedules(), override.ID())
	if got.GetNormal().GetStage() != override.StageID {
		t.Errorf("stored override for a live-covered slot was not returned as-is: got stage %d, want %d",
			got.GetNormal().GetStage(), override.StageID)
	}
}

func otherNormalCoopStageID(t *testing.T, avoid int32) int32 {
	t.Helper()
	for _, s := range normalCoopStages {
		if s.ID != avoid {
			return s.ID
		}
	}
	t.Fatal("normalCoopStages has fewer than two entries")
	return 0
}
