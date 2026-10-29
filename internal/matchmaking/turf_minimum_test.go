package matchmaking

import (
	"testing"
	"time"
)

// Ordinary Turf War searches can start with two players after the grace period.
func TestTurfWarSoloTicketsStartAfterGrace(t *testing.T) {
	now := time.Unix(0, 0)
	q := New(DefaultConfig, func() time.Time { return now })
	step := func(d time.Duration) []Session { now = now.Add(d); _, f := q.Sweep(); return f }
	const mode = "tenants/current/matchmakingConfigs/regular_match_config"

	q.Enqueue("a", mode)
	q.Enqueue("b", mode)
	if f := step(DefaultConfig.VersusGracePeriod - time.Second); len(f) != 0 {
		t.Fatalf("formed before the grace period: %v", f)
	}
	if f := step(2 * time.Second); len(f) != 1 || len(f[0].Players) != 2 {
		t.Fatalf("formed %v, want one two-player match", f)
	}
}

func TestTurfWarDoesNotSplitAFriendSquad(t *testing.T) {
	q, clock := testGraceQueue(t, 8, 20*time.Second, 0)
	const mode = "tenants/current/matchmakingConfigs/regular_match_config"
	q.EnqueueSquad("host", []string{"friend"}, mode, "")
	*clock = clock.Add(time.Minute)
	if _, f := q.Sweep(); len(f) != 0 {
		t.Fatalf("split friend squad: %v", f)
	}
	q.Enqueue("opponent1", mode)
	q.Enqueue("opponent2", mode)
	*clock = clock.Add(21 * time.Second)
	if _, f := q.Sweep(); len(f) != 1 || len(f[0].Players) != 4 {
		t.Fatalf("want squad against two opponents: %v", f)
	}
}
