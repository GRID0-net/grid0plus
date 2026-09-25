package ugcstore

import (
	"testing"
	"time"
)

// TestPreviousCoopShiftIDIsTheSlotBeforeTheCurrentOne: on 2026-09-25 at
// 16:39 UTC the console's current shift was 20260925160000.
func TestPreviousCoopShiftIDIsTheSlotBeforeTheCurrentOne(t *testing.T) {
	now := time.Date(2026, 9, 25, 16, 39, 0, 0, time.UTC)
	if got := previousCoopShiftID(now); got != "20260924000000" {
		t.Errorf("previousCoopShiftID = %s, want 20260924000000", got)
	}
}
