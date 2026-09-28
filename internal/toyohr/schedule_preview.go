package toyohr

import (
	"context"
	"sort"
	"time"

	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
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
	setID := rotationSetID("vs", current)
	out := make([]*toyohrpb.VsSchedule, 0, slots)
	for i := 0; i < slots; i++ {
		start := from.Add(time.Duration(i) * scheduleSlot)
		out = append(out, s.vsSchedule(start, rot, "default", setID))
	}
	return out
}

// liveCoopFillEnabled gates whether PreviewCoopSchedule fills gaps with
// liveNormalCoopShifts at all.
//
// Was disabled 2026-09-24 after two hardware crashes right as the hall
// would render Salmon Run content. The strongest confirmed gap found in
// that window -- CoopSchedule_Normal/BigRun/TeamContest's kuma_weapon
// scalar field, which both working captures never populate, versus
// attributes.KumaWeapons (an array), which both always do -- is now fixed
// (coopKumaWeaponsAttribute, coop_schedule.go). Real Nintendo servers
// always answer a Salmon Run schedule alongside Turf War/Anarchy/League --
// an operator running GRID0+ with none is not the real-server shape
// this project is reproducing -- so this is back on rather than left off
// indefinitely.
//
// CoopUsedRewardGears (also present in both captures' CoopSchedule and
// VsParams attributes, real gear id/kind pairs) is still not sent: it needs
// a real gear id table this project has not built (Leanny/splat3's
// GearInfoHead/Clothes/Shoes.json can source one the same way
// vs_ids.go/coop_ids.go's tables were). That gap stays open and is worth
// watching on the next hardware test, but the confirmed, fixed
// kuma_weapon/attributes mismatch was the stronger, more clear-cut lead.
//
// A var, not a const, so a future finding can disable it again the same
// way without hunting down every call site.
var liveCoopFillEnabled = true

// PreviewCoopSchedule returns the same merge SelectCoopSchedules serves a
// console -- every stored shift not yet ended, plus (while
// liveCoopFillEnabled) a live Normal shift for any slot nothing was stored
// for -- for the dashboard's schedule viewer.
func (s *Service) PreviewCoopSchedule(ctx context.Context, now time.Time) ([]CoopShift, error) {
	rows, err := s.store.ListCoopShifts(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]CoopShift, 0, len(rows)+coopLiveHorizonShifts)
	for _, row := range rows {
		if !row.EndAt.After(now) {
			continue
		}
		out = append(out, coopShiftFromRow(row))
	}
	if liveCoopFillEnabled {
		for _, shift := range liveNormalCoopShifts(now, coopLiveHorizonShifts) {
			if !shift.End.After(now) {
				continue
			}
			// Big Run and Team Contest replace the ordinary rotation for their
			// entire window. Filtering only an identical Normal start used to
			// send two simultaneously-active Coop schedules when a Big Run was
			// scheduled across a generated 40-hour Normal slot.
			overlapsStored := false
			for _, stored := range out {
				if shift.Start.Before(stored.End) && stored.Start.Before(shift.End) {
					overlapsStored = true
					break
				}
			}
			if overlapsStored {
				continue
			}
			out = append(out, shift)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}
