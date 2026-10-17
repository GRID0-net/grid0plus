package toyohr

import (
	"context"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/ugcstore"
)

// coopShiftIDLayout is how the game names a shift in CoopUserAttribute's
// shift_id and in InitializeAttributes coop_shift: its start, in UTC
// (grid0's logs: "20261017080000" for the shift opening 2026-10-17 08:00).
const coopShiftIDLayout = "20060102150405"

// CoopShiftID is the game's name for the shift starting at start.
func CoopShiftID(start time.Time) string { return start.UTC().Format(coopShiftIDLayout) }

// CurrentCoopShift is the shift open at now, as SelectCoopSchedules serves
// it: a stored one, else the live-generated Normal shift. ok is false when
// nothing is open.
func CurrentCoopShift(ctx context.Context, store db.Store, now time.Time) (CoopShift, bool, error) {
	rows, err := store.ListCoopShifts(ctx)
	if err != nil {
		return CoopShift{}, false, err
	}
	for _, r := range rows {
		if !r.StartAt.After(now) && r.EndAt.After(now) {
			return coopShiftFromRow(r), true, nil
		}
	}
	if !liveCoopFillEnabled {
		return CoopShift{}, false, nil
	}
	return liveNormalCoopShifts(now, 1)[0], true, nil
}

// coopShiftByID finds the shift the game named, stored or live-generated.
func coopShiftByID(ctx context.Context, store db.Store, id string) (CoopShift, bool, error) {
	start, err := time.Parse(coopShiftIDLayout, id)
	if err != nil {
		return CoopShift{}, false, nil
	}
	rows, err := store.ListCoopShifts(ctx)
	if err != nil {
		return CoopShift{}, false, err
	}
	for _, r := range rows {
		if r.StartAt.Equal(start) {
			return coopShiftFromRow(r), true, nil
		}
	}
	if liveCoopFillEnabled && start.Unix()%int64(DefaultCoopShiftDuration/time.Second) == 0 {
		return coopShiftAtSlot(start, DefaultCoopShiftDuration), true, nil
	}
	return CoopShift{}, false, nil
}

// CoopStageBest is uid's best Eggsecutive VP points on stage, over every
// shift GRID0+ knows they played there: what the stage's badge is.
func CoopStageBest(ctx context.Context, store db.Store, tenant, uid string, stage int32) (int64, error) {
	bests, err := ugcstore.CoopBests(ctx, store, tenant, uid)
	if err != nil {
		return 0, err
	}
	var best int64
	for id, points := range bests {
		if points <= best {
			continue
		}
		shift, ok, err := coopShiftByID(ctx, store, id)
		if err != nil {
			return 0, err
		}
		if ok && shift.StageID == stage {
			best = points
		}
	}
	return best, nil
}

// RollCoopRotation resets uid's points if shift is a new rotation for them,
// with the badge of shift's stage. A Big Run moves no grade and resets
// nothing.
func RollCoopRotation(ctx context.Context, store db.Store, tenant, uid string, shift CoopShift, now time.Time) (bool, error) {
	if shift.Kind != CoopShiftNormal {
		return false, nil
	}
	best, err := CoopStageBest(ctx, store, tenant, uid, shift.StageID)
	if err != nil {
		return false, err
	}
	return ugcstore.RollCoopRotation(ctx, store, tenant, uid, CoopShiftID(shift.Start), best, now)
}
