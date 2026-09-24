package toyohr

import (
	"context"
	"sort"
	"time"

	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

// PreviewVsSchedule returns the same Vs rotation SelectVsSchedules would
// answer a console right now, for an operator-facing view (the dashboard's
// schedule viewer) rather than the wire RPC -- same vsSchedule function, same
// Rotation, so the two can never drift apart into showing different content.
func (s *Service) PreviewVsSchedule(now time.Time, slots int) []*toyohrpb.VsSchedule {
	rot := s.rotation
	if len(rot.RegularStages) == 0 && len(rot.BankaraStages) == 0 {
		return nil
	}
	if slots <= 0 {
		slots = scheduleHorizon
	}
	current := AlignToSlot(now)
	from := current.Add(-scheduleSlot)
	setID := scheduleSetID(current)
	out := make([]*toyohrpb.VsSchedule, 0, slots)
	for i := 0; i < slots; i++ {
		start := from.Add(time.Duration(i) * scheduleSlot)
		out = append(out, s.vsSchedule(start, i, rot, "default", setID))
	}
	return out
}

// liveCoopFillEnabled gates whether PreviewCoopSchedule fills gaps with
// liveNormalCoopShifts at all.
//
// Disabled (false) as of 2026-09-24: a real hardware test crashed twice in a
// row right as the hall would render Salmon Run content, and a real,
// decoded working capture (docs/CAPTURES/raw/nextendo-hall-20260923T205738Z,
// analysis/decoded-private.json's SelectCoopSchedules response) shows why --
// a real CoopSchedule_Normal always carries a populated `attributes` MapValue
// with a "CoopUsedRewardGears" key (an array of real gear id/kind pairs,
// ~24 entries observed), while coopScheduleProto (coop_schedule.go) has
// never set Attributes at all -- CoopSchedule_Normal.Attributes is field 7
// of this project's own compiled proto (proto/toyohr/v1/resources.pb.go),
// sent nil on every response. This gap predates today: SelectCoopSchedules
// only ever answered non-empty when an operator had manually stored a
// shift, so no hardware test before today's live-fill change (coop_live.go)
// had ever actually exercised a console rendering CoopSchedule content at
// all. Correlation, not a confirmed root cause -- disabling this stopped
// short of proving it -- but it is the most structurally sound lead found:
// a real, populated field the client evidently expects, that this project
// has always sent empty, now reachable for the first time.
//
// Leanny/splat3's GearInfoHead/Clothes/Shoes.json data dumps give real gear
// ids the same way vs_ids.go/coop_ids.go's tables were sourced, so building
// CoopUsedRewardGears correctly (rather than guessing at it) is possible --
// just not done under time pressure mid hardware-test-session. Re-enable
// once that exists, or once a capture/hardware test clears this as the
// cause.
// A var, not a const, so tests that exist specifically to cover the live-fill
// behavior (coop_live_test.go, schedule_preview_test.go) can still exercise
// it without that behavior being reachable from a real request.
var liveCoopFillEnabled = false

// PreviewCoopSchedule returns the same merge SelectCoopSchedules serves a
// console -- every stored shift not yet ended, plus (while
// liveCoopFillEnabled) a live Normal shift for any slot nothing was stored
// for -- for the dashboard's schedule viewer.
func (s *Service) PreviewCoopSchedule(ctx context.Context, now time.Time) ([]CoopShift, error) {
	rows, err := s.store.ListCoopShifts(ctx)
	if err != nil {
		return nil, err
	}

	storedNormalStarts := map[int64]bool{}
	out := make([]CoopShift, 0, len(rows)+coopLiveHorizonShifts)
	for _, row := range rows {
		if !row.EndAt.After(now) {
			continue
		}
		out = append(out, coopShiftFromRow(row))
		if row.Kind == CoopShiftNormal.String() {
			storedNormalStarts[row.StartAt.UTC().Unix()] = true
		}
	}
	if liveCoopFillEnabled {
		for _, shift := range liveNormalCoopShifts(now, coopLiveHorizonShifts) {
			if !shift.End.After(now) || storedNormalStarts[shift.Start.UTC().Unix()] {
				continue
			}
			out = append(out, shift)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}
