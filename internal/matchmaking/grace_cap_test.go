package matchmaking

import (
	"testing"
	"time"
)

// A queue that keeps changing -- a console cancelling and re-queuing every
// 15 seconds -- used to restart a 20-second countdown forever, holding
// everyone in Placing and out of the ticket TTL. The countdown is now capped
// at three grace periods from when it first started.
func TestChurnCannotHoldAMatchOpenForever(t *testing.T) {
	q, clock := testGraceQueue(t, 8, 0, 20*time.Second, "coop")
	enqueueAll(t, q, "coop", "a", "b")
	var formed []Session
	for i := 0; i < 10 && len(formed) == 0; i++ {
		*clock = clock.Add(15 * time.Second)
		tk, f, err := q.Enqueue("flaky", "coop")
		if err != nil {
			t.Fatal(err)
		}
		formed = append(formed, f...)
		_, f = q.Sweep()
		formed = append(formed, f...)
		if len(formed) == 0 {
			_, f := q.CancelForming(tk.ID)
			formed = append(formed, f...)
		}
	}
	if len(formed) == 0 {
		t.Fatal("churn kept the countdown restarting and no match ever formed")
	}
}
