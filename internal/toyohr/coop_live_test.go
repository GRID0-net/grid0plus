package toyohr

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
)

func TestCoopScheduleDoesNotChangeOnEveryPoll(t *testing.T) {
	now := time.Date(2026, 9, 25, 9, 46, 22, 0, time.UTC)
	svc, _ := newService(t, now)
	svc.now = func() time.Time { return now }
	sc := &scheduleService{s: svc}
	read := func() *toyohrpb.SelectCoopSchedulesResponse {
		t.Helper()
		resp, err := sc.SelectCoopSchedules(context.Background(), &toyohrpb.SelectCoopSchedulesRequest{})
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	first := read()
	now = now.Add(20 * time.Second)
	second := read()
	if !proto.Equal(first, second) {
		t.Fatal("unchanged Coop rotation changed between polls")
	}
	for _, schedule := range first.GetSchedules() {
		want := schedule.GetStartTime().AsTime().Add(-7 * 24 * time.Hour)
		if got := schedule.GetTimestamp().AsTime(); !got.Equal(want) {
			t.Errorf("schedule timestamp = %s, want captured publication offset %s", got, want)
		}
	}
}

// TestLiveNormalCoopShiftsIsDeterministic is the whole point of
// liveNormalCoopShifts: the same real moment must produce the same
// shift, computed fresh, with nothing stored -- "random deterministic
// maps", the property this was built to give Coop that Vs already has.
func TestLiveNormalCoopShiftsIsDeterministic(t *testing.T) {
	now := time.Date(2026, 11, 3, 17, 0, 0, 0, time.UTC)
	a := liveNormalCoopShifts(now, coopLiveHorizonShifts)
	b := liveNormalCoopShifts(now, coopLiveHorizonShifts)
	if len(a) != coopLiveHorizonShifts || len(b) != coopLiveHorizonShifts {
		t.Fatalf("got %d/%d shifts, want %d/%d", len(a), len(b), coopLiveHorizonShifts, coopLiveHorizonShifts)
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
	got := findCoopScheduleByNameID(t, resp.GetSchedules(), override.ID())
	if got.GetNormal().GetStage() != override.StageID {
		t.Errorf("stored override for a live-covered slot was not returned as-is: got stage %d, want %d",
			got.GetNormal().GetStage(), override.StageID)
	}
}

func TestABigRunReplacesEveryOverlappingLiveNormalShift(t *testing.T) {
	now := time.Date(2026, 9, 28, 7, 16, 0, 0, time.UTC)
	svc, _ := newService(t, now)
	shift := validBigRunShift()
	shift.Start = time.Date(2026, 9, 28, 6, 0, 0, 0, time.UTC)
	shift.End = shift.Start.Add(48 * time.Hour)
	if err := svc.SaveCoopShift(context.Background(), shift); err != nil {
		t.Fatal(err)
	}
	preview, err := svc.PreviewCoopSchedule(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	current := 0
	for _, got := range preview {
		if got.Start.Before(shift.End) && shift.Start.Before(got.End) {
			if got.ID() != shift.ID() {
				t.Errorf("Big Run %s overlaps another Coop shift %s", shift.ID(), got.ID())
			}
			if got.Start.Before(now) && got.End.After(now) {
				current++
			}
		}
	}
	if current != 1 {
		t.Errorf("current overlapping shifts = %d, want Big Run only", current)
	}
}

func TestNormalRotationResumesAtBigRunEndWithoutAGap(t *testing.T) {
	for _, hours := range []int{22, 48} {
		t.Run(fmt.Sprintf("%dh", hours), func(t *testing.T) {
			now := time.Date(2026, 9, 28, 19, 0, 0, 0, time.UTC)
			svc, _ := newService(t, now)
			shift := validBigRunShift()
			shift.Start = time.Date(2026, 9, 28, 18, 0, 0, 0, time.UTC)
			shift.End = shift.Start.Add(time.Duration(hours) * time.Hour)
			if err := svc.SaveCoopShift(context.Background(), shift); err != nil {
				t.Fatal(err)
			}
			preview, err := svc.PreviewCoopSchedule(context.Background(), now)
			if err != nil {
				t.Fatal(err)
			}
			if len(preview) < coopLiveHorizonShifts {
				t.Fatalf("only %d shifts, want at least %d", len(preview), coopLiveHorizonShifts)
			}
			for i := 1; i < len(preview); i++ {
				if preview[i].Start.Before(preview[i-1].End) {
					t.Fatalf("overlapping shifts: %s and %s", preview[i-1].ID(), preview[i].ID())
				}
			}
			for i, got := range preview {
				if got.ID() == shift.ID() {
					if i+1 >= len(preview) || !preview[i+1].Start.Equal(shift.End) {
						t.Fatalf("rotation does not resume at Big Run end %s: %v", shift.End, preview)
					}
					return
				}
			}
			t.Fatal("Big Run missing from preview")
		})
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
