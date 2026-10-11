package matchmaking

import (
	"testing"
	"time"
)

// Grace periods changed on a live queue apply to the next countdown, so an
// operator's reload takes effect without a restart.
func TestChangedGracePeriodsApplyToTheNextCountdown(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	q := New(Config{PlayersPerMatch: 8, VersusGracePeriod: time.Minute}, func() time.Time { return now })
	q.SetGracePeriods(20*time.Second, 10*time.Second)
	for _, u := range []string{"a", "b"} {
		if _, _, err := q.Enqueue(u, "regular"); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(21 * time.Second)
	if _, formed := q.Sweep(); len(formed) != 1 {
		t.Fatalf("after 21 s with a 20 s grace: %d matches, want 1", len(formed))
	}
}
