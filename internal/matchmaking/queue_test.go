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

func userName(i int) string {
	return string(rune('a'+i%26)) + string(rune('0'+i/26))
}

func TestAModeSizeOverridesTheDefaultForThatModeOnly(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	q := New(Config{
		PlayersPerMatch: 2,
		ModeSizes:       map[string]int{"salmon_run": 4},
		TicketTTL:       3 * time.Minute,
	}, func() time.Time { return now })

	if got := q.PlayersFor("salmon_run"); got != 4 {
		t.Errorf("salmon_run takes %d players, want the override's 4", got)
	}
	if got := q.PlayersFor("turf_war"); got != 2 {
		t.Errorf("turf_war takes %d players, want the default 2", got)
	}

	// Three in the overridden mode is one short, so nothing forms.
	for _, who := range []string{"alice", "bob", "carol"} {
		if _, sessions, err := q.Enqueue(who, "salmon_run"); err != nil {
			t.Fatalf("enqueue: %v", err)
		} else if len(sessions) != 0 {
			t.Fatalf("a four-player match formed with %s, the third to arrive", who)
		}
	}
	_, sessions, err := q.Enqueue("dave", "salmon_run")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if len(sessions) != 1 || len(sessions[0].Players) != 4 {
		t.Fatalf("formed %d sessions, want one of four players", len(sessions))
	}
	if sessions[0].MaxPlayers != 4 {
		t.Errorf("session reports room for %d, want the 4 it was formed at", sessions[0].MaxPlayers)
	}
}

func TestAFormedSessionCanBeLookedUpAgain(t *testing.T) {
	q, _ := testQueue(t, 2)

	_, _, _ = q.Enqueue("alice", "turf_war")
	_, sessions, _ := q.Enqueue("bob", "turf_war")
	if len(sessions) != 1 {
		t.Fatalf("formed %d sessions, want 1", len(sessions))
	}

	// The console asks for the session again after its stream named one. A
	// server that formed a match and cannot then describe it leaves the lobby
	// on the loading screen with nothing to say why.
	got, ok := q.Session(sessions[0].ID)
	if !ok {
		t.Fatal("the session cannot be looked up by the name the ticket gave")
	}
	if got.Host != sessions[0].Host || len(got.Players) != 2 {
		t.Errorf("session read back as %+v, want %+v", got, sessions[0])
	}

	if _, ok := q.Session("not-a-session"); ok {
		t.Error("a session id nobody issued resolved to a session")
	}
}

func TestRejoiningAMatchYouAreAlreadyInSucceeds(t *testing.T) {
	q, _ := testQueue(t, 2)

	_, _, _ = q.Enqueue("alice", "turf_war")
	_, sessions, _ := q.Enqueue("bob", "turf_war")
	id := sessions[0].ID

	// Idempotent on purpose: the console joins the match it was just told it
	// is in, and refusing that strands it.
	got, err := q.Join(id, "alice")
	if err != nil {
		t.Fatalf("alice cannot rejoin her own match: %v", err)
	}
	if len(got.Players) != 2 {
		t.Errorf("rejoining added a duplicate player: %v", got.Players)
	}

	if _, err := q.Join(id, "carol"); err != ErrSessionFull {
		t.Errorf("joining a full match gave %v, want ErrSessionFull", err)
	}
	if _, err := q.Join("not-a-session", "carol"); err != ErrNoSession {
		t.Errorf("joining a session nobody issued gave %v, want ErrNoSession", err)
	}
}

func TestAPlayerCanFindTheSessionTheyAreIn(t *testing.T) {
	q, _ := testQueue(t, 2)

	_, _, _ = q.Enqueue("alice", "turf_war")
	_, sessions, _ := q.Enqueue("bob", "turf_war")

	mine := q.SessionsFor("alice")
	if len(mine) != 1 || mine[0].ID != sessions[0].ID {
		t.Fatalf("alice is in %d sessions, want the one she was matched into", len(mine))
	}
	if got := q.SessionsFor("carol"); len(got) != 0 {
		t.Errorf("carol, who never queued, is in %d sessions", len(got))
	}
}

func TestMutatingAReturnedSessionDoesNotChangeTheQueuesOwn(t *testing.T) {
	q, _ := testQueue(t, 2)

	_, _, _ = q.Enqueue("alice", "turf_war")
	_, sessions, _ := q.Enqueue("bob", "turf_war")

	got, _ := q.Session(sessions[0].ID)
	got.Players[0] = "attacker"

	again, _ := q.Session(sessions[0].ID)
	if again.Players[0] == "attacker" {
		t.Error("a caller rewrote the queue's own player list through the value it was handed")
	}
}
