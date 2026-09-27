package toyohr

import (
	"context"
	"testing"
	"time"

	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
)

func TestPreviewVsScheduleMatchesSelectVsSchedules(t *testing.T) {
	at := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	rot := Rotation{RegularStages: []int32{1, 2}, StagesPerSlot: 2}
	sc := scheduleServiceFor(t, at, rot)

	wire, err := sc.SelectVsSchedules(context.Background(), &toyohrpb.SelectVsSchedulesRequest{})
	if err != nil {
		t.Fatalf("SelectVsSchedules: %v", err)
	}
	preview := sc.s.PreviewVsSchedule(at, 0)
	if got, want := len(preview), len(wire.GetSchedules()); got != want {
		t.Fatalf("preview has %d slots, wire has %d", got, want)
	}
	for i := range preview {
		if preview[i].GetName() != wire.GetSchedules()[i].GetName() {
			t.Errorf("slot %d: preview name %q != wire name %q", i, preview[i].GetName(), wire.GetSchedules()[i].GetName())
		}
	}
}

func TestPreviewVsScheduleEmptyWithNoRotation(t *testing.T) {
	at := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	sc := scheduleServiceFor(t, at, Rotation{})
	if got := sc.s.PreviewVsSchedule(at, 3); got != nil {
		t.Errorf("got %d slots with no rotation configured, want none", len(got))
	}
}

func TestPreviewCoopScheduleMatchesSelectCoopSchedules(t *testing.T) {
	at := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	svc, _ := newService(t, at)
	sc := &scheduleService{s: svc}

	wire, err := sc.SelectCoopSchedules(context.Background(), &toyohrpb.SelectCoopSchedulesRequest{})
	if err != nil {
		t.Fatalf("SelectCoopSchedules: %v", err)
	}
	preview, err := svc.PreviewCoopSchedule(context.Background(), at)
	if err != nil {
		t.Fatalf("PreviewCoopSchedule: %v", err)
	}
	if got, want := len(preview), len(wire.GetSchedules()); got != want {
		t.Fatalf("preview has %d shifts, wire has %d", got, want)
	}
}
