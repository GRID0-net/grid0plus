package toyohr

import (
	"context"
	"sort"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

func scheduleServiceFor(t *testing.T, at time.Time, rot Rotation) *scheduleService {
	t.Helper()
	svc, _ := newService(t, at)
	svc.rotation = rot
	return &scheduleService{s: svc}
}

func TestEveryGeneratedVsSlotLandsOnATwoHourBoundary(t *testing.T) {
	// The same rule a Splatfest's phases obey: Splatoon 3 files scheduled
	// things into 7200-second slots, so a window with boundaries elsewhere
	// describes a rotation the game cannot place.
	at := time.Date(2026, 9, 5, 13, 37, 0, 0, time.UTC)
	sc := scheduleServiceFor(t, at, Rotation{
		RegularStages: []int32{1, 2, 3, 4},
		BankaraRules:  []int32{1, 2},
		BankaraStages: []int32{5, 6, 7, 8},
	})

	resp, err := sc.SelectVsSchedules(context.Background(), &toyohrpb.SelectVsSchedulesRequest{})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if len(resp.GetSchedules()) != scheduleHorizon {
		t.Fatalf("got %d slots, want %d", len(resp.GetSchedules()), scheduleHorizon)
	}

	var previousEnd time.Time
	for i, s := range resp.GetSchedules() {
		start := s.GetStartTime().AsTime()
		end := s.GetEndTime().AsTime()

		if !IsSlotAligned(start) || !IsSlotAligned(end) {
			t.Errorf("slot %d runs %s..%s, which is not on a two-hour boundary", i, start, end)
		}
		if end.Sub(start) != scheduleSlot {
			t.Errorf("slot %d is %s long, want %s", i, end.Sub(start), scheduleSlot)
		}
		// No gaps: a gap is a window in which the lobby has no rotation at all.
		if i > 0 && !start.Equal(previousEnd) {
			t.Errorf("slot %d starts at %s but the previous ended at %s", i, start, previousEnd)
		}
		previousEnd = end

		if s.GetRegularSettings() == nil {
			t.Errorf("slot %d offers no Turf War, so its lobby cannot start a match", i)
		}
		// Anarchy runs two modes at once. A slot offering one rule is a lobby
		// with half its buttons inert.
		if got := len(s.GetBankaraSettings()); got != 2 {
			t.Errorf("slot %d offers %d Anarchy rules, want 2", i, got)
		}
	}
}

func TestTheRotationWrapsRatherThanRunningOut(t *testing.T) {
	at := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	// Two stages, two per slot: every slot has to reuse them.
	sc := scheduleServiceFor(t, at, Rotation{RegularStages: []int32{11, 22}})

	resp, err := sc.SelectVsSchedules(context.Background(), &toyohrpb.SelectVsSchedulesRequest{})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	for i, s := range resp.GetSchedules() {
		stages := s.GetRegularSettings().GetStages()
		// A slot with no stages is a lobby that cannot start a match, which is
		// why pick wraps instead of returning short.
		if len(stages) != 2 {
			t.Fatalf("slot %d offers %d stages, want 2", i, len(stages))
		}
		got := append([]int32(nil), stages...)
		sort.Slice(got, func(a, b int) bool { return got[a] < got[b] })
		if got[0] != 11 || got[1] != 22 {
			t.Errorf("slot %d offers %v, want the two configured stages", i, stages)
		}
	}
}

func TestTheSameRequestAtTheSameTimeGivesTheSameEtag(t *testing.T) {
	at := time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC)
	rot := Rotation{RegularStages: []int32{1, 2, 3, 4}}

	first, err := scheduleServiceFor(t, at, rot).SelectVsSchedules(
		context.Background(), &toyohrpb.SelectVsSchedulesRequest{})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	second, err := scheduleServiceFor(t, at, rot).SelectVsSchedules(
		context.Background(), &toyohrpb.SelectVsSchedulesRequest{})
	if err != nil {
		t.Fatalf("select: %v", err)
	}

	// The etag is what a client's conditional request compares. One that moves
	// for no reason costs a needless refetch.
	if first.GetEtag() == "" {
		t.Fatal("no etag, so a client cannot cache the rotation")
	}
	if first.GetEtag() != second.GetEtag() {
		t.Errorf("the same rotation produced two etags: %s and %s",
			first.GetEtag(), second.GetEtag())
	}

	// And a different rotation must produce a different one, or the lobby keeps
	// showing yesterday's stages.
	changed, err := scheduleServiceFor(t, at, Rotation{RegularStages: []int32{9, 8, 7, 6}}).
		SelectVsSchedules(context.Background(), &toyohrpb.SelectVsSchedulesRequest{})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if changed.GetEtag() == first.GetEtag() {
		t.Error("changing the stages did not change the etag")
	}
}

func TestAClientAskingForALimitedWindowGetsOne(t *testing.T) {
	at := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	sc := scheduleServiceFor(t, at, Rotation{RegularStages: []int32{1, 2}})

	resp, err := sc.SelectVsSchedules(context.Background(), &toyohrpb.SelectVsSchedulesRequest{
		SelectDuration: durationpb.New(6 * time.Hour),
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if got := len(resp.GetSchedules()); got != 3 {
		t.Errorf("six hours gave %d slots, want 3", got)
	}

	// But not more than the horizon: a schedule stretching further than the
	// operator has thought about is a promise about stages that may change.
	resp, err = sc.SelectVsSchedules(context.Background(), &toyohrpb.SelectVsSchedulesRequest{
		SelectDuration: durationpb.New(30 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if got := len(resp.GetSchedules()); got != scheduleHorizon {
		t.Errorf("a month gave %d slots, want the horizon's %d", got, scheduleHorizon)
	}
}

func TestNoConfiguredRotationIsAnEmptyScheduleRatherThanAnError(t *testing.T) {
	at := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	sc := scheduleServiceFor(t, at, Rotation{})

	// An empty schedule is what the game sees between seasons on a real server,
	// so it is a state it has a reading for. An error is not.
	resp, err := sc.SelectVsSchedules(context.Background(), &toyohrpb.SelectVsSchedulesRequest{})
	if err != nil {
		t.Fatalf("select with no rotation configured: %v", err)
	}
	if len(resp.GetSchedules()) != 0 {
		t.Errorf("got %d slots from an empty rotation", len(resp.GetSchedules()))
	}

	coop, err := sc.SelectCoopSchedules(context.Background(), &toyohrpb.SelectCoopSchedulesRequest{})
	if err != nil {
		t.Fatalf("select coop: %v", err)
	}
	if len(coop.GetSchedules()) != 0 {
		t.Errorf("got %d co-op shifts, but none can be described without invented ids",
			len(coop.GetSchedules()))
	}
}

func TestTheSeasonIsAQuarterAndIsAligned(t *testing.T) {
	// Mid-quarter, so the answer cannot come from the clock by accident.
	at := time.Date(2026, 8, 28, 21, 22, 0, 0, time.UTC)
	sc := scheduleServiceFor(t, at, Rotation{})

	resp, err := sc.SelectSeasonSchedules(context.Background(),
		&toyohrpb.SelectSeasonSchedulesRequest{SeasonScheduleCount: 2})
	if err != nil {
		t.Fatalf("select season: %v", err)
	}
	if len(resp.GetSchedules()) != 2 {
		t.Fatalf("got %d seasons, want 2", len(resp.GetSchedules()))
	}

	first := resp.GetSchedules()[0]
	start := first.GetStartTime().AsTime()
	if start.Month() != time.July || start.Day() != 1 {
		t.Errorf("season starts %s, want 1 July — the quarter containing 28 August", start)
	}
	if !IsSlotAligned(start) {
		t.Errorf("season start %s is not slot-aligned", start)
	}
	// Back to back, so a record made on a boundary has somewhere to go.
	if !resp.GetSchedules()[1].GetStartTime().AsTime().Equal(first.GetEndTime().AsTime()) {
		t.Error("there is a gap between one season and the next")
	}
}

func TestAnUnauthenticatedCallCannotReadTheSchedule(t *testing.T) {
	at := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	sc := scheduleServiceFor(t, at, Rotation{RegularStages: []int32{1, 2}})
	as(t, "")

	if _, err := sc.SelectVsSchedules(context.Background(),
		&toyohrpb.SelectVsSchedulesRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("reading the schedule with no caller gave %v, want Unauthenticated", err)
	}
}

func TestARotationWithATypoIsRefusedRatherThanReadAsStageZero(t *testing.T) {
	// Stage 0 is a perfectly plausible valid id, so a zero produced by a failed
	// parse would be indistinguishable from an intended one.
	if _, err := RotationFromStrings([]string{"1", "two"}, nil, nil, 0); err == nil {
		t.Error("a non-numeric stage id was accepted")
	}
	rot, err := RotationFromStrings([]string{"1", " 2 ", ""}, []string{"3"}, []string{"4"}, 1)
	if err != nil {
		t.Fatalf("a valid rotation was refused: %v", err)
	}
	if len(rot.RegularStages) != 2 || rot.RegularStages[1] != 2 {
		t.Errorf("regular stages = %v, want [1 2] with the blank dropped", rot.RegularStages)
	}
	if rot.StagesPerSlot != 1 {
		t.Errorf("stages per slot = %d, want 1", rot.StagesPerSlot)
	}
}
