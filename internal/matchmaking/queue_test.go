package matchmaking

import (
	"sync"
	"testing"
	"time"
)

func testQueue(t *testing.T, playersPerMatch int) (*Queue, *time.Time) {
	t.Helper()
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	clock := &now
	return New(Config{PlayersPerMatch: playersPerMatch, TicketTTL: 3 * time.Minute},
		func() time.Time { return *clock }), clock
}

func TestAMatchFormsWhenEnoughPlayersAreWaiting(t *testing.T) {
	q, _ := testQueue(t, 2)

	first, sessions, err := q.Enqueue("alice", "turf_war")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("a match formed with one player waiting")
	}
	if first.State != StateSearching {
		t.Errorf("state = %s, want searching", first.State)
	}

	_, sessions, err = q.Enqueue("bob", "turf_war")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("formed %d sessions, want 1", len(sessions))
	}

	s := sessions[0]
	if len(s.Players) != 2 {
		t.Errorf("session has %d players, want 2", len(s.Players))
	}
	// The longest-waiting player hosts.
	if s.Host != "alice" {
		t.Errorf("host = %q, want alice, who waited longest", s.Host)
	}

	// Both tickets ended up succeeded, and pointing at the same session.
	got, ok := q.Get(first.ID)
	if !ok {
		t.Fatal("the first ticket vanished")
	}
	if got.State != StateSucceeded {
		t.Errorf("state = %s, want succeeded", got.State)
	}
	if got.SessionID != s.ID {
		t.Errorf("ticket points at session %q, want %q", got.SessionID, s.ID)
	}
}

// TestPlayersInDifferentModesDoNotMatchEachOther.
//
// Two people queuing for different things are not two people who can play
// together. Matching them produces a lobby that never starts.
func TestPlayersInDifferentModesDoNotMatchEachOther(t *testing.T) {
	q, _ := testQueue(t, 2)

	if _, sessions, _ := q.Enqueue("alice", "turf_war"); len(sessions) != 0 {
		t.Fatal("a match formed immediately")
	}
	_, sessions, _ := q.Enqueue("bob", "salmon_run")
	if len(sessions) != 0 {
		t.Fatal("players queuing for different modes were matched")
	}

	if got := q.Waiting(); got["turf_war"] != 1 || got["salmon_run"] != 1 {
		t.Errorf("waiting = %v, want one in each mode", got)
	}
}

// TestASecondTicketReplacesTheFirstRatherThanQueueingTwice.
//
// A console that re-enters matchmaking after a network blip has forgotten its
// old ticket. Leaving that one queued matches a player who is no longer
// waiting, and the other consoles then sit in a lobby that never fills.
func TestASecondTicketReplacesTheFirstRatherThanQueueingTwice(t *testing.T) {
	q, _ := testQueue(t, 2)

	first, _, _ := q.Enqueue("alice", "turf_war")
	second, _, _ := q.Enqueue("alice", "turf_war")

	if first.ID == second.ID {
		t.Fatal("the same ticket came back twice")
	}
	if got, _ := q.Get(first.ID); got.State != StateCancelled {
		t.Errorf("the first ticket is %s, want cancelled", got.State)
	}
	if got := q.Waiting()["turf_war"]; got != 1 {
		t.Errorf("%d tickets waiting, want 1: one player should occupy one place", got)
	}

	// And the stale ticket must not be matchable.
	_, sessions, _ := q.Enqueue("bob", "turf_war")
	if len(sessions) != 1 {
		t.Fatalf("formed %d sessions, want 1", len(sessions))
	}
	for _, p := range sessions[0].Players {
		if p == "alice" {
			goto found
		}
	}
	t.Error("alice is not in the match she was queued for")
found:
	if len(sessions[0].Players) != 2 {
		t.Errorf("the match has %d players, want 2 -- a stale ticket was matched",
			len(sessions[0].Players))
	}
}

func TestCancellingATicketTakesItOutOfTheQueue(t *testing.T) {
	q, _ := testQueue(t, 2)

	ticket, _, _ := q.Enqueue("alice", "turf_war")
	if !q.Cancel(ticket.ID) {
		t.Fatal("cancel returned false for a searching ticket")
	}
	if got, _ := q.Get(ticket.ID); got.State != StateCancelled {
		t.Errorf("state = %s, want cancelled", got.State)
	}

	// A cancelled ticket must not be matched.
	_, sessions, _ := q.Enqueue("bob", "turf_war")
	if len(sessions) != 0 {
		t.Error("a cancelled ticket was matched")
	}
}

// TestCancellingAnAlreadyMatchedTicketDoesNothing.
//
// The match exists and the other players are already being told to connect.
// Pulling one back out leaves them waiting for somebody who will not arrive.
func TestCancellingAnAlreadyMatchedTicketDoesNothing(t *testing.T) {
	q, _ := testQueue(t, 2)

	ticket, _, _ := q.Enqueue("alice", "turf_war")
	q.Enqueue("bob", "turf_war")

	if q.Cancel(ticket.ID) {
		t.Error("a ticket that already had a match was cancelled")
	}
	if got, _ := q.Get(ticket.ID); got.State != StateSucceeded {
		t.Errorf("state = %s, want succeeded", got.State)
	}
}

// TestATicketTimesOutRatherThanWaitingForever.
//
// A ticket that never resolves shows the player a spinner with no way back, and
// they force-quit. Timing out lets the game say "no opponents found".
func TestATicketTimesOutRatherThanWaitingForever(t *testing.T) {
	q, clock := testQueue(t, 2)

	ticket, _, _ := q.Enqueue("alice", "turf_war")

	if expired := q.Sweep(); len(expired) != 0 {
		t.Fatalf("a fresh ticket expired: %v", expired)
	}

	*clock = clock.Add(4 * time.Minute)

	expired := q.Sweep()
	if len(expired) != 1 || expired[0].ID != ticket.ID {
		t.Fatalf("expired = %v, want the one waiting ticket", expired)
	}
	if got, _ := q.Get(ticket.ID); got.State != StateTimedOut {
		t.Errorf("state = %s, want timed_out", got.State)
	}
	if got := q.Waiting()["turf_war"]; got != 0 {
		t.Errorf("%d tickets still waiting after timing out", got)
	}
}

// TestAWatcherIsToldWhenItsTicketIsMatched is the stream the console lives on:
// it creates a ticket and then waits for exactly this.
func TestAWatcherIsToldWhenItsTicketIsMatched(t *testing.T) {
	q, _ := testQueue(t, 2)

	ticket, _, _ := q.Enqueue("alice", "turf_war")
	ch, stop := q.Watch(ticket.ID)
	defer stop()

	// The current state arrives immediately, so a watcher attaching late does
	// not wait for a change that already happened.
	select {
	case got := <-ch:
		if got.State != StateSearching {
			t.Errorf("first update is %s, want searching", got.State)
		}
	default:
		t.Fatal("no immediate update on attaching a watcher")
	}

	q.Enqueue("bob", "turf_war")

	select {
	case got := <-ch:
		if got.State != StateSucceeded {
			t.Errorf("update is %s, want succeeded", got.State)
		}
		if got.SessionID == "" {
			t.Error("the succeeded update carries no session to join")
		}
	default:
		t.Fatal("the watcher was not told its ticket matched")
	}
}

// TestAWatcherThatStopsReadingDoesNotStallEverybodyElse.
//
// A console that wandered off must not be able to block match formation. The
// send is dropped instead — the same trade presencebus makes on the request
// path, for the same reason.
func TestAWatcherThatStopsReadingDoesNotStallEverybodyElse(t *testing.T) {
	q, _ := testQueue(t, 2)

	stalled, _, _ := q.Enqueue("alice", "turf_war")
	_, stop := q.Watch(stalled.ID)
	defer stop()
	// Deliberately never read from that channel.

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Enough state changes to overflow the watcher's buffer several times.
		for i := 0; i < 50; i++ {
			q.Enqueue("alice", "turf_war")
		}
		q.Enqueue("bob", "turf_war")
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("match formation blocked on a watcher that stopped reading")
	}
}

// TestConcurrentPlayersEachEndUpInExactlyOneMatch.
//
// Every console has its own goroutine, and formation mutates several tickets at
// once. A player in two matches, or in none while the count says otherwise, is
// the failure this guards.
func TestConcurrentPlayersEachEndUpInExactlyOneMatch(t *testing.T) {
	q, _ := testQueue(t, 2)

	const players = 40
	var wg sync.WaitGroup
	var mu sync.Mutex
	var allSessions []Session

	for i := 0; i < players; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, sessions, err := q.Enqueue(userName(i), "turf_war")
			if err != nil {
				t.Errorf("enqueue: %v", err)
				return
			}
			mu.Lock()
			allSessions = append(allSessions, sessions...)
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	if len(allSessions) != players/2 {
		t.Errorf("formed %d sessions from %d players, want %d",
			len(allSessions), players, players/2)
	}

	seen := map[string]int{}
	for _, s := range allSessions {
		if len(s.Players) != 2 {
			t.Errorf("session %s has %d players, want 2", s.ID, len(s.Players))
		}
		for _, p := range s.Players {
			seen[p]++
		}
	}
	for player, count := range seen {
		if count != 1 {
			t.Errorf("%s is in %d matches, want exactly 1", player, count)
		}
	}
	if len(seen) != players {
		t.Errorf("%d distinct players were matched, want %d", len(seen), players)
	}
	if left := q.Waiting()["turf_war"]; left != 0 {
		t.Errorf("%d players left waiting, want 0", left)
	}
}

func TestAnEmptyUserOrModeIsRefused(t *testing.T) {
	q, _ := testQueue(t, 2)

	if _, _, err := q.Enqueue("", "turf_war"); err == nil {
		t.Error("a ticket with no user was accepted")
	}
	if _, _, err := q.Enqueue("alice", ""); err == nil {
		t.Error("a ticket with no mode was accepted")
	}
}

// TestSetHostPortUpdatesTheSessionAndOnlyThatSession is SyncGameSession's
// underlying data operation: it must land on the right session and not leak
// into another one formed around the same time.
func TestSetHostPortUpdatesTheSessionAndOnlyThatSession(t *testing.T) {
	q, _ := testQueue(t, 2)
	_, _, _ = q.Enqueue("alice", "turf_war")
	_, sessions, _ := q.Enqueue("bob", "turf_war")
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	id := sessions[0].ID

	before, ok := q.Session(id)
	if !ok || before.HostPort != 0 {
		t.Fatalf("a fresh session has HostPort %d, want 0", before.HostPort)
	}

	updated, ok := q.SetHostPort(id, 12345)
	if !ok {
		t.Fatal("SetHostPort reported the session missing")
	}
	if updated.HostPort != 12345 {
		t.Errorf("HostPort = %d, want 12345", updated.HostPort)
	}

	again, ok := q.Session(id)
	if !ok || again.HostPort != 12345 {
		t.Errorf("Session() = %+v, want HostPort 12345", again)
	}
}

func TestSetHostPortOnAnUnknownSessionReportsNotFound(t *testing.T) {
	q, _ := testQueue(t, 2)
	if _, ok := q.SetHostPort("no-such-session", 1); ok {
		t.Error("SetHostPort succeeded on a session that does not exist")
	}
}

func userName(i int) string {
	return string(rune('a'+i%26)) + string(rune('0'+i/26))
}
