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
