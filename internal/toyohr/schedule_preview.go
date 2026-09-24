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
	from := AlignToSlot(now)
	out := make([]*toyohrpb.VsSchedule, 0, slots)
	for i := 0; i < slots; i++ {
		start := from.Add(time.Duration(i) * scheduleSlot)
		out = append(out, s.vsSchedule(start, i, rot))
	}
	return out
}

// PreviewCoopSchedule returns the same merge SelectCoopSchedules serves a
// console -- every stored shift not yet ended, plus a live Normal shift for
// any slot nothing was stored for -- for the dashboard's schedule viewer.
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
	for _, shift := range liveNormalCoopShifts(now, coopLiveHorizonShifts) {
		if !shift.End.After(now) || storedNormalStarts[shift.Start.UTC().Unix()] {
			continue
		}
		out = append(out, shift)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}
