package matchmaking

import (
	"testing"
	"time"
)

// TestForceMatchStartsWithFewerPlayersThanTheModeWants is the point of the
// whole control: a mode sized for eight, on a private server where three people
// turned up, otherwise never starts.
func TestForceMatchStartsWithFewerPlayersThanTheModeWants(t *testing.T) {
	q, _ := testQueue(t, 8)

	for _, user := range []string{"alice", "bob", "carol"} {
		if _, sessions, err := q.Enqueue(user, "turf_war"); err != nil {
			t.Fatalf("Enqueue(%s): %v", user, err)
		} else if len(sessions) != 0 {
			t.Fatalf("a match formed with 3 of 8 players before being forced")
		}
	}

	session, ok, err := q.ForceMatch("turf_war")
	if err != nil {
		t.Fatalf("ForceMatch: %v", err)
	}
	if !ok {
		t.Fatal("ForceMatch formed nothing with three players waiting")
	}
	if len(session.Players) != 3 {
		t.Errorf("players = %d, want all 3 who were waiting", len(session.Players))
	}
	if session.Host != "alice" {
		t.Errorf("host = %q, want the longest-waiting player", session.Host)
	}

	// A forced match is a real match: the tickets succeeded, they carry the
	// session id, and the queue is empty.
	if waiting := q.Waiting()["turf_war"]; waiting != 0 {
		t.Errorf("%d tickets are still queued after being matched", waiting)
	}
	if got, ok := q.Session(session.ID); !ok || len(got.Players) != 3 {
		t.Error("the forced session cannot be looked up")
	}
	if !q.IsPlayer(session.ID, "carol") {
		t.Error("a forced-in player is not a member of the session")
	}
}

// TestForceMatchTakesEveryoneWaitingNotJustEnoughForOne guards the thing an
// operator would find most surprising: pressing "start" and being left with
// somebody still in the queue.
func TestForceMatchTakesEveryoneWaitingNotJustEnoughForOne(t *testing.T) {
	q, _ := testQueue(t, 8)

	for _, user := range []string{"a", "b", "c", "d", "e"} {
		if _, _, err := q.Enqueue(user, "turf_war"); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	session, ok, _ := q.ForceMatch("turf_war")
	if !ok {
		t.Fatal("nothing formed")
	}
	if len(session.Players) != 5 {
		t.Errorf("players = %d, want all 5", len(session.Players))
	}
	if waiting := q.Waiting()["turf_war"]; waiting != 0 {
		t.Errorf("%d players were left behind", waiting)
	}
}

// TestForcingAnEmptyQueueIsANoOpNotAnError: asking to start a queue nobody is
// in is a mistake to report to the operator, not a match with no players.
func TestForcingAnEmptyQueueIsANoOpNotAnError(t *testing.T) {
	q, _ := testQueue(t, 2)

	session, ok, err := q.ForceMatch("turf_war")
	if err != nil {
		t.Fatalf("ForceMatch on an empty queue errored: %v", err)
	}
	if ok {
		t.Fatalf("a match formed out of nobody: %+v", session)
	}

	if _, _, err := q.ForceMatch(""); err == nil {
		t.Error("an empty mode was accepted")
	}
}

// TestForceMatchIgnoresCancelledTickets is the cancelled-ticket rule again, on
// the path that bypasses the size check -- the one place it could be missed.
func TestForceMatchIgnoresCancelledTickets(t *testing.T) {
	q, _ := testQueue(t, 8)

	quitter, _, _ := q.Enqueue("quitter", "turf_war")
	if _, _, err := q.Enqueue("stayer", "turf_war"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if !q.Cancel(quitter.ID) {
		t.Fatal("Cancel returned false")
	}

	session, ok, _ := q.ForceMatch("turf_war")
	if !ok {
		t.Fatal("nothing formed")
	}
	if len(session.Players) != 1 || session.Players[0] != "stayer" {
		t.Errorf("players = %v, want just the player who stayed", session.Players)
	}
}

// TestAWatcherIsToldAboutAForcedMatch: the console lives on that stream, so a
// forced match it is never told about is a match it never joins.
func TestAWatcherIsToldAboutAForcedMatch(t *testing.T) {
	q, _ := testQueue(t, 8)

	ticket, _, _ := q.Enqueue("alice", "turf_war")
	updates, stop := q.Watch(ticket.ID)
	defer stop()

	// Watch sends the current state immediately.
	if first := <-updates; first.State != StateSearching {
		t.Fatalf("first update = %v, want searching", first.State)
	}

	if _, ok, _ := q.ForceMatch("turf_war"); !ok {
		t.Fatal("nothing formed")
	}

	select {
	case got := <-updates:
		if got.State != StateSucceeded {
			t.Errorf("state = %v, want succeeded", got.State)
		}
		if got.SessionID == "" {
			t.Error("the ticket carries no session id")
		}
	case <-time.After(time.Second):
		t.Fatal("the watcher was never told its ticket was forced into a match")
	}
}

// TestSettingAModeSizeFormsTheMatchesItNowAllows is the durable fix: shrinking
// a mode has to act on the tickets already waiting, because nobody is about to
// arrive to trigger formation.
func TestSettingAModeSizeFormsTheMatchesItNowAllows(t *testing.T) {
	q, _ := testQueue(t, 8)

	for _, user := range []string{"a", "b", "c", "d"} {
		if _, sessions, _ := q.Enqueue(user, "turf_war"); len(sessions) != 0 {
			t.Fatal("a match formed before the size changed")
		}
	}

	sessions, err := q.SetModeSize("turf_war", 2)
	if err != nil {
		t.Fatalf("SetModeSize: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("sessions = %d, want 2 (four players at two each)", len(sessions))
	}
	if waiting := q.Waiting()["turf_war"]; waiting != 0 {
		t.Errorf("%d players are still waiting", waiting)
	}

	// And it keeps applying to new arrivals.
	if _, _, err := q.Enqueue("e", "turf_war"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	_, sessions2, _ := q.Enqueue("f", "turf_war")
	if len(sessions2) != 1 {
		t.Errorf("the new size did not apply to later tickets: %d sessions", len(sessions2))
	}
}

// TestAModeSizeOnlyAffectsItsOwnMode stops one control quietly changing every
// other mode.
func TestAModeSizeOnlyAffectsItsOwnMode(t *testing.T) {
	q, _ := testQueue(t, 4)

	if _, err := q.SetModeSize("turf_war", 2); err != nil {
		t.Fatalf("SetModeSize: %v", err)
	}

	q.Enqueue("a", "salmon_run")
	if _, sessions, _ := q.Enqueue("b", "salmon_run"); len(sessions) != 0 {
		t.Error("salmon_run matched at two players; the override was not mode-scoped")
	}
	q.Enqueue("c", "turf_war")
	if _, sessions, _ := q.Enqueue("d", "turf_war"); len(sessions) != 1 {
		t.Error("turf_war did not match at its own override of two")
	}
}

// TestClearingAModeSizeReturnsItToTheDefault covers the zero case, which is the
// only way back to "whatever PlayersPerMatch says".
func TestClearingAModeSizeReturnsItToTheDefault(t *testing.T) {
	q, _ := testQueue(t, 4)

	q.SetModeSize("turf_war", 2)
	if _, err := q.SetModeSize("turf_war", 0); err != nil {
		t.Fatalf("SetModeSize(0): %v", err)
	}

	for _, user := range []string{"a", "b"} {
		if _, sessions, _ := q.Enqueue(user, "turf_war"); len(sessions) != 0 {
			t.Fatal("matched at two after the override was cleared")
		}
	}
	q.Enqueue("c", "turf_war")
	if _, sessions, _ := q.Enqueue("d", "turf_war"); len(sessions) != 1 {
		t.Error("did not match at the default of four")
	}

	if _, err := q.SetModeSize("turf_war", -1); err == nil {
		t.Error("a negative size was accepted")
	}
	if _, err := q.SetModeSize("", 2); err == nil {
		t.Error("an empty mode was accepted")
	}
}

// TestASnapshotShowsTheQueueAndTheSessionsTogether covers the operator's view.
func TestASnapshotShowsTheQueueAndTheSessionsTogether(t *testing.T) {
	q, clock := testQueue(t, 8)

	q.Enqueue("alice", "turf_war")
	*clock = clock.Add(90 * time.Second)
	q.Enqueue("bob", "turf_war")
	q.Enqueue("carol", "salmon_run")
	q.SetModeSize("anarchy", 4)

	snap := q.Snapshot()

	if snap.DefaultSize != 8 {
		t.Errorf("DefaultSize = %d, want 8", snap.DefaultSize)
	}

	byMode := map[string]ModeQueue{}
	for _, m := range snap.Modes {
		byMode[m.Mode] = m
	}

	// A mode with an override but nobody waiting still appears, so the control
	// the operator just used does not vanish.
	if anarchy, ok := byMode["anarchy"]; !ok {
		t.Error("a mode with an override and an empty queue is missing")
	} else if !anarchy.Overridden || anarchy.Size != 4 {
		t.Errorf("anarchy = %+v, want an override of 4", anarchy)
	}

	turf := byMode["turf_war"]
	if len(turf.Waiting) != 2 {
		t.Fatalf("turf_war waiting = %d, want 2", len(turf.Waiting))
	}
	if turf.Overridden {
		t.Error("turf_war is marked overridden but was never set")
	}
	if turf.Size != 8 {
		t.Errorf("turf_war size = %d, want the default 8", turf.Size)
	}
	// Queue order, and the wait is measured rather than guessed.
	if turf.Waiting[0].UserID != "alice" || turf.Waiting[1].UserID != "bob" {
		t.Errorf("waiting order = %v", turf.Waiting)
	}
	if turf.Waiting[0].WaitingFor != 90*time.Second {
		t.Errorf("alice has waited %v, want 90s", turf.Waiting[0].WaitingFor)
	}
	if turf.Waiting[1].WaitingFor != 0 {
		t.Errorf("bob has waited %v, want 0", turf.Waiting[1].WaitingFor)
	}

	// Force a match and it moves from the queue to the sessions.
	session, _, _ := q.ForceMatch("turf_war")
	snap = q.Snapshot()

	for _, m := range snap.Modes {
		if m.Mode == "turf_war" && len(m.Waiting) != 0 {
			t.Errorf("turf_war still shows %d waiting after being matched", len(m.Waiting))
		}
	}
	found := false
	for _, s := range snap.Sessions {
		if s.ID == session.ID {
			found = true
		}
	}
	if !found {
		t.Error("the forced session is not in the snapshot")
	}
}

// TestASnapshotDoesNotShowSomebodyAsQueuedAndMatchedAtOnce is why Snapshot is
// one call under one lock rather than several accessors.
func TestASnapshotDoesNotShowSomebodyAsQueuedAndMatchedAtOnce(t *testing.T) {
	q, _ := testQueue(t, 2)

	q.Enqueue("alice", "turf_war")
	q.Enqueue("bob", "turf_war")

	snap := q.Snapshot()

	queued := map[string]bool{}
	for _, m := range snap.Modes {
		for _, w := range m.Waiting {
			queued[w.UserID] = true
		}
	}
	for _, s := range snap.Sessions {
		for _, p := range s.Players {
			if queued[p] {
				t.Errorf("%s is shown both queued and in a match", p)
			}
		}
	}
}
