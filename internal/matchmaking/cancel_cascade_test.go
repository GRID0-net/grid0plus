package matchmaking

import "testing"

// A leader who re-queues solo takes the old squad with them. The member's
// ticket used to stay Searching as a squad of one and could be matched into
// a session that console never joined.
func TestALeaderRequeuingCancelsTheOldSquadsMemberTickets(t *testing.T) {
	q, _ := testQueue(t, 8)
	old, _, err := q.EnqueueSquad("leader", []string{"friend"}, "turf_war", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := q.Enqueue("leader", "turf_war"); err != nil {
		t.Fatal(err)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, tk := range q.tickets {
		if tk.Party == old.ID && tk.State.waiting() {
			t.Fatalf("member ticket %s of the abandoned squad is still %s", tk.UserID, tk.State)
		}
	}
	for _, id := range q.waiting["turf_war"] {
		if q.tickets[id].UserID == "friend" {
			t.Fatal("the friend is still queued from the abandoned squad")
		}
	}
}
