package matchmaking

import "testing"

// A watcher that has fallen behind still receives the latest state: the
// final Succeeded must never be the update that gets dropped.
func TestASlowWatcherStillSeesTheFinalState(t *testing.T) {
	q, _ := testQueue(t, 8)
	tk, _, err := q.Enqueue("slow", "turf_war")
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan Ticket, 2)
	q.mu.Lock()
	q.watchers[tk.ID] = append(q.watchers[tk.ID], ch)
	live := q.tickets[tk.ID]
	for i := 0; i < 5; i++ {
		q.setStateLocked(live, StatePlacing)
		q.setStateLocked(live, StateSearching)
	}
	q.setStateLocked(live, StateSucceeded)
	q.mu.Unlock()
	var last Ticket
	for len(ch) > 0 {
		last = <-ch
	}
	if last.State != StateSucceeded {
		t.Fatalf("last update the watcher sees is %s, want succeeded", last.State)
	}
}
