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
// Real Nintendo servers always answer a Salmon Run schedule alongside Turf
// War/Anarchy/League -- an operator running GRID0+ with none is not the shape
// this project is reproducing -- so this is on by default. The kuma_weapon
// scalar field, which a real schedule never populates, versus
// attributes.KumaWeapons (an array), which it always does, is handled by
// coopKumaWeaponsAttribute (coop_schedule.go).
//
// CoopUsedRewardGears (also present in a real CoopSchedule and VsParams
// attributes, real gear id/kind pairs) is still not sent: it needs a real gear
// id table this project has not built (Leanny/splat3's
// GearInfoHead/Clothes/Shoes.json can source one the same way
// vs_ids.go/coop_ids.go's tables were).
//
// A var, not a const, so a future finding can disable it again without hunting
// down every call site.
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
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	if liveCoopFillEnabled {
		// Special events interrupt the 40-hour clock. Resume it at the event's
		// end instead of discarding a partly-overlapped generated shift: that
		// used to leave a full day without a Coop schedule after a Big Run.
		cursor := liveNormalCoopShifts(now, 1)[0].Start
		generated := make([]CoopShift, 0, coopLiveHorizonShifts)
		visible := 0
		fillUntil := func(end time.Time) {
			for cursor.Before(end) && visible < coopLiveHorizonShifts {
				shift := coopShiftAtSlot(cursor, DefaultCoopShiftDuration)
				if shift.End.After(end) {
					shift.End = end
				}
				if shift.End.After(now) {
					generated = append(generated, shift)
					visible++
				}
				cursor = shift.End
			}
		}
		for _, stored := range out {
			fillUntil(stored.Start)
			if stored.End.After(now) {
				visible++
			}
			if stored.End.After(cursor) {
				cursor = stored.End
			}
		}
		for visible < coopLiveHorizonShifts {
			shift := coopShiftAtSlot(cursor, DefaultCoopShiftDuration)
			generated = append(generated, shift)
			cursor = shift.End
			visible++
		}
		out = append(out, generated...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}
