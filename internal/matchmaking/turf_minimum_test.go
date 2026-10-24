package matchmaking

import (
	"testing"
	"time"
)

// Two friends queuing Turf War together arrive as two solo tickets; they
// must wait for two more players rather than start a 1v1 against each other.
func TestTurfWarNeedsFourPlayers(t *testing.T) {
	now := time.Unix(0, 0)
	q := New(DefaultConfig, func() time.Time { return now })
	step := func(d time.Duration) []Session { now = now.Add(d); _, f := q.Sweep(); return f }
	const mode = "tenants/current/matchmakingConfigs/regular_match_config"

	q.Enqueue("a", mode)
	q.Enqueue("b", mode)
	if f := step(time.Minute); len(f) != 0 {
		t.Fatalf("two Turf War players formed %v, want them to wait", f)
	}
	q.Enqueue("c", mode)
	q.Enqueue("d", mode)
	if f := step(DefaultConfig.VersusGracePeriod - time.Second); len(f) != 0 {
		t.Fatalf("formed before the grace period: %v", f)
	}
	if f := step(2 * time.Second); len(f) != 1 || len(f[0].Players) != 4 {
		t.Fatalf("formed %v, want one four-player match", f)
	}
}
