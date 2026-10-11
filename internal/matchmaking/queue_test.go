package matchmaking

import (
	"slices"
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

func TestFestOpenLobbiesAreOneTeamAndFormInPairs(t *testing.T) {
	q, _ := testQueue(t, 8)
	mode := "fest_match_regular_normal_config"
	for _, p := range []struct{ user, team string }{
		{"a1", "Alpha"}, {"a2", "Alpha"}, {"a3", "Alpha"}, {"a4", "Alpha"},
		{"b1", "Bravo"}, {"b2", "Bravo"}, {"b3", "Bravo"},
	} {
		if _, formed, err := q.EnqueueWithFestTeam(p.user, mode, p.team); err != nil || len(formed) != 0 {
			t.Fatalf("a lobby of 4 formed with no opposing lobby of 4: %v, %v", formed, err)
		}
	}
	_, formed, err := q.EnqueueWithFestTeam("b4", mode, "Bravo")
	if err != nil || len(formed) != 2 {
		t.Fatalf("formed %v, %v; want an Alpha lobby and a Bravo lobby", formed, err)
	}
	for _, s := range formed {
		if len(s.Players) != 4 {
			t.Fatalf("lobby %v is not 4 players", s.Players)
		}
		team := s.Players[0][:1]
		for _, p := range s.Players {
			if p[:1] != team {
				t.Fatalf("lobby %v mixes fest teams", s.Players)
			}
		}
	}
}

// TestAFestSquadOfThreeFillsWithItsOwnTeamAndIsNeverSplit is a host with two
// friends: the lobby is completed by one stranger of the same team, never
// split, and never completed from the other team.
func TestAFestSquadOfThreeFillsWithItsOwnTeamAndIsNeverSplit(t *testing.T) {
	q, clock := testQueue(t, 8)
	q.cfg.VersusGracePeriod = 20 * time.Second
	mode := "fest_match_regular_normal_config"
	if _, _, err := q.EnqueueSquad("a1", []string{"a2", "a3"}, mode, "Alpha"); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"b1", "b2", "b3", "b4"} {
		q.EnqueueWithFestTeam(u, mode, "Bravo")
	}
	*clock = clock.Add(time.Minute)
	if _, f := q.Sweep(); len(f) != 0 {
		t.Fatalf("formed %v: the squad of 3 cannot make a lobby of 2 or 4 yet", f)
	}
	_, formed, _ := q.EnqueueWithFestTeam("a4", mode, "Alpha")
	if len(formed) != 2 {
		t.Fatalf("formed %v, want the completed Alpha lobby and the Bravo lobby", formed)
	}
	for _, sess := range formed {
		if slices.Contains(sess.Players, "a1") {
			for _, p := range []string{"a1", "a2", "a3", "a4"} {
				if !slices.Contains(sess.Players, p) {
					t.Fatalf("squad lobby %v is missing %s", sess.Players, p)
				}
			}
			return
		}
	}
	t.Fatalf("formed %v without the squad", formed)
}

// TestTwoPerTeamPlaysAsTwoPairedLobbies is the 2 v 2 that failed on
// 2026-10-10 as one mixed lobby: after the wait it is a pair of one-team
// lobbies of 2, which then meet in direct_pair.
func TestTwoPerTeamPlaysAsTwoPairedLobbies(t *testing.T) {
	q, clock := testQueue(t, 8)
	q.cfg.VersusGracePeriod = 20 * time.Second
	open := "fest_match_regular_normal_config"
	pair := "fest_match_regular_direct_pair_config"
	for _, p := range []struct{ user, team string }{
		{"a1", "Alpha"}, {"b1", "Bravo"}, {"a2", "Alpha"}, {"b2", "Bravo"},
	} {
		if _, f, _ := q.EnqueueWithFestTeam(p.user, open, p.team); len(f) != 0 {
			t.Fatalf("premature: %v", f)
		}
	}
	*clock = clock.Add(21 * time.Second)
	_, formed := q.Sweep()
	if len(formed) != 2 || len(formed[0].Players) != 2 || len(formed[1].Players) != 2 {
		t.Fatalf("formed %v, want two lobbies of 2", formed)
	}

	// Each lobby's host re-queues it as a squad; the two squads meet.
	if _, f, _ := q.EnqueueSquad("a1", []string{"a2"}, pair, "Alpha"); len(f) != 0 {
		t.Fatalf("a one-team squad played alone: %v", f)
	}
	_, f, _ := q.EnqueueSquad("b1", []string{"b2"}, pair, "Bravo")
	if len(f) != 1 || len(f[0].Players) != 4 {
		t.Fatalf("formed %v, want one 2 v 2", f)
	}
	s := f[0].TeamSlots
	if s["a1"] != s["a2"] || s["b1"] != s["b2"] || s["a1"] == s["b1"] {
		t.Fatalf("slots = %v, want each squad on its own side", s)
	}
}

// TestOnePerTeamStillPlaysOneVersusOne keeps the smallest game: one player
// of each team share a lobby of 2 after the wait.
func TestOnePerTeamStillPlaysOneVersusOne(t *testing.T) {
	q, clock := testQueue(t, 8)
	q.cfg.VersusGracePeriod = 20 * time.Second
	mode := "fest_match_regular_normal_config"
	q.EnqueueWithFestTeam("a1", mode, "Alpha")
	q.EnqueueWithFestTeam("b1", mode, "Bravo")
	*clock = clock.Add(21 * time.Second)
	_, formed := q.Sweep()
	if len(formed) != 1 || len(formed[0].Players) != 2 {
		t.Fatalf("formed %v, want a 1 v 1 lobby", formed)
	}
	if s := formed[0].TeamSlots; s["a1"] == s["b1"] {
		t.Fatalf("slots = %v", s)
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

	if expired, _ := q.Sweep(); len(expired) != 0 {
		t.Fatalf("a fresh ticket expired: %v", expired)
	}

	*clock = clock.Add(4 * time.Minute)

	expired, _ := q.Sweep()
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

// testGraceQueue is testQueue with both grace periods set, for the tests
// below.
func testGraceQueue(t *testing.T, playersPerMatch int, versus, coop time.Duration, coopModes ...string) (*Queue, *time.Time) {
	t.Helper()
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	clock := &now
	cfg := Config{
		PlayersPerMatch: playersPerMatch, TicketTTL: 3 * time.Minute,
		VersusGracePeriod: versus, CoopGracePeriod: coop,
	}
	if len(coopModes) > 0 {
		cfg.CoopModes = map[string]bool{}
		for _, m := range coopModes {
			cfg.CoopModes[m] = true
		}
	}
	return New(cfg, func() time.Time { return *clock }), clock
}

// enqueueAll queues each player in a mode and returns every match that formed
// along the way.
func enqueueAll(t *testing.T, q *Queue, mode string, players ...string) []Session {
	t.Helper()
	var formed []Session
	for _, who := range players {
		_, sessions, err := q.Enqueue(who, mode)
		if err != nil {
			t.Fatalf("enqueue %s: %v", who, err)
		}
		formed = append(formed, sessions...)
	}
	return formed
}

// enqueueTicket queues one player and returns their ticket.
func enqueueTicket(t *testing.T, q *Queue, who, mode string) Ticket {
	t.Helper()
	ticket, _, err := q.Enqueue(who, mode)
	if err != nil {
		t.Fatalf("enqueue %s: %v", who, err)
	}
	return ticket
}

func TestAFullVersusLobbyStartsWithoutWaiting(t *testing.T) {
	q, _ := testGraceQueue(t, 8, 20*time.Second, 0)

	formed := enqueueAll(t, q, "turf_war", "p1", "p2", "p3", "p4", "p5", "p6", "p7")
	if len(formed) != 0 {
		t.Fatalf("a match formed before the lobby was full: %v", formed)
	}
	formed = enqueueAll(t, q, "turf_war", "p8")
	if len(formed) != 1 || len(formed[0].Players) != 8 {
		t.Fatalf("formed = %v, want one 8-player match the moment the eighth arrived", formed)
	}
}

// TestAnEvenVersusLobbyStartsAfterTheGracePeriod: below the cap, an even count
// starts a countdown (tracked as StatePlacing), and Sweep forms the match with
// everyone waiting once it runs out -- nothing else calls into the queue while
// the players just wait.
func TestAnEvenVersusLobbyStartsAfterTheGracePeriod(t *testing.T) {
	q, clock := testGraceQueue(t, 8, 20*time.Second, 0)

	if formed := enqueueAll(t, q, "turf_war", "alice", "bob", "carol", "dave"); len(formed) != 0 {
		t.Fatalf("four players formed a match without waiting: %v", formed)
	}
	for _, id := range q.waiting["turf_war"] {
		if got := q.tickets[id].State; got != StatePlacing {
			t.Errorf("state = %s, want placing during the countdown", got)
		}
	}

	*clock = clock.Add(19 * time.Second)
	if _, formed := q.Sweep(); len(formed) != 0 {
		t.Fatalf("formed before the grace period elapsed: %v", formed)
	}
	*clock = clock.Add(2 * time.Second)
	_, formed := q.Sweep()
	if len(formed) != 1 || len(formed[0].Players) != 4 {
		t.Fatalf("formed = %v, want one 4-player match once the grace period elapsed", formed)
	}
	if formed[0].MaxPlayers != 4 {
		t.Errorf("an early match reports room for %d, want the 4 it formed with", formed[0].MaxPlayers)
	}
}

// TestAnOddArrivalStopsTheCountdownUntilTheTeamsAreEven: a third player
// joining two who are counting down stops the clock, since three cannot split
// into two equal teams, and nothing starts however long they wait. A fourth
// starts a fresh countdown, and the match that forms has all four.
func TestAnOddArrivalStopsTheCountdownUntilTheTeamsAreEven(t *testing.T) {
	q, clock := testGraceQueue(t, 8, 20*time.Second, 0)

	enqueueAll(t, q, "turf_war", "alice", "bob")
	*clock = clock.Add(15 * time.Second)
	carol := enqueueTicket(t, q, "carol", "turf_war")
	if got, _ := q.Get(carol.ID); got.State != StateSearching {
		t.Errorf("carol's state = %s, want searching with an odd count", got.State)
	}

	*clock = clock.Add(time.Minute)
	if _, formed := q.Sweep(); len(formed) != 0 {
		t.Fatalf("a match formed with three players: %v", formed)
	}

	enqueueAll(t, q, "turf_war", "dave")
	*clock = clock.Add(19 * time.Second)
	if _, formed := q.Sweep(); len(formed) != 0 {
		t.Fatalf("the fresh countdown fired early: %v", formed)
	}
	*clock = clock.Add(2 * time.Second)
	_, formed := q.Sweep()
	if len(formed) != 1 || len(formed[0].Players) != 4 {
		t.Fatalf("formed = %v, want one match with all four players", formed)
	}
}

// TestLeavingToAnOddCountStopsTheCountdown: the same rule for a departure.
func TestLeavingToAnOddCountStopsTheCountdown(t *testing.T) {
	q, clock := testGraceQueue(t, 8, 20*time.Second, 0)

	enqueueAll(t, q, "turf_war", "alice", "bob", "carol")
	dave := enqueueTicket(t, q, "dave", "turf_war")
	if !q.Cancel(dave.ID) {
		t.Fatal("cancelling a ticket mid-countdown was refused")
	}
	*clock = clock.Add(time.Minute)
	if _, formed := q.Sweep(); len(formed) != 0 {
		t.Fatalf("a match formed with three players left: %v", formed)
	}
}

// TestPlayersPastAFullLobbyGetTheirOwnMatch: the first eight start at once,
// and the rest are counted afresh under the same even-count rule.
func TestPlayersPastAFullLobbyGetTheirOwnMatch(t *testing.T) {
	q, clock := testGraceQueue(t, 8, 20*time.Second, 0)

	formed := enqueueAll(t, q, "turf_war", "p1", "p2", "p3", "p4", "p5", "p6", "p7", "p8", "p9", "p10")
	if len(formed) != 1 || len(formed[0].Players) != 8 {
		t.Fatalf("formed = %v, want one full match from the first eight", formed)
	}
	*clock = clock.Add(21 * time.Second)
	_, formed = q.Sweep()
	if len(formed) != 1 || len(formed[0].Players) != 2 {
		t.Fatalf("formed = %v, want the other two in their own match", formed)
	}
}

// TestNoVersusGracePeriodMeansOnlyFullMatches: zero turns early starts off.
func TestNoVersusGracePeriodMeansOnlyFullMatches(t *testing.T) {
	q, clock := testGraceQueue(t, 4, 0, 0)

	enqueueAll(t, q, "turf_war", "alice", "bob")
	*clock = clock.Add(time.Minute)
	if _, formed := q.Sweep(); len(formed) != 0 {
		t.Fatalf("an early match formed with early starts off: %v", formed)
	}
	if formed := enqueueAll(t, q, "turf_war", "carol", "dave"); len(formed) != 1 {
		t.Fatalf("formed = %v, want one match once the lobby was full", formed)
	}
}

// TestCoopWaitsAloneIndefinitelyThenStartsACountdownPerJoin covers Salmon
// Run's different rule: a lone player is on no clock at all, a second player
// starts one, and each further arrival restarts it -- so a trickle of joiners
// does not lose the ones already waiting to a clock that started before they
// got there.
func TestCoopWaitsAloneIndefinitelyThenStartsACountdownPerJoin(t *testing.T) {
	q, clock := testGraceQueue(t, 4, 0, 30*time.Second, "coop")

	q.Enqueue("alice", "coop")
	*clock = clock.Add(time.Minute) // well under the 3-minute ticket TTL
	if _, formed := q.Sweep(); len(formed) != 0 {
		t.Fatalf("alice matched herself: %v", formed)
	}
	if got, _ := q.Get(mustGet(t, q, "alice")); got.State != StateSearching {
		t.Errorf("alone: state = %s, want searching (no countdown at all)", got.State)
	}

	q.Enqueue("bob", "coop")
	if got, _ := q.Get(mustGet(t, q, "alice")); got.State != StatePlacing {
		t.Errorf("after a 2nd player: state = %s, want placing", got.State)
	}

	// 20s in, a third joins: the clock restarts from here, not from bob's join.
	*clock = clock.Add(20 * time.Second)
	q.Enqueue("carol", "coop")
	*clock = clock.Add(20 * time.Second) // 20s since carol, would be 40s since bob
	if _, formed := q.Sweep(); len(formed) != 0 {
		t.Fatalf("formed on bob's original clock, ignoring carol's restart: %v", formed)
	}
	*clock = clock.Add(11 * time.Second) // 31s since carol
	_, formed := q.Sweep()
	if len(formed) != 1 || len(formed[0].Players) != 3 {
		t.Fatalf("formed = %v, want one 3-player match (below the mode's size of 4, which nobody reached)", formed)
	}
}

// TestCoopFormsImmediatelyOnceFull: a full room has nobody left to wait for,
// so reaching the mode's size skips whatever remains of the countdown.
func TestCoopFormsImmediatelyOnceFull(t *testing.T) {
	q, _ := testGraceQueue(t, 2, 0, 30*time.Second, "coop")

	q.Enqueue("alice", "coop")
	_, sessions, _ := q.Enqueue("bob", "coop")
	if len(sessions) != 1 {
		t.Fatalf("sessions = %v, want one formed the instant the (size-2) mode filled", sessions)
	}
}

func mustGet(t *testing.T, q *Queue, userID string) string {
	t.Helper()
	for _, tk := range q.tickets {
		if tk.UserID == userID {
			return tk.ID
		}
	}
	t.Fatalf("no ticket for %s", userID)
	return ""
}

// TestARoomInUseOutlivesTheSessionTTL: a room expires from its last activity,
// not its creation, because a host can wait in one for friends well past the
// TTL. A matched session still expires from creation.
func TestARoomInUseOutlivesTheSessionTTL(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cfg := DefaultConfig
	cfg.SessionTTL = 30 * time.Minute
	q := New(cfg, func() time.Time { return now })

	room, err := q.OpenRoom("host", "private_match_config", 10)
	if err != nil {
		t.Fatalf("OpenRoom: %v", err)
	}
	if !room.Room || len(room.Players) != 1 || room.MaxPlayers != 10 {
		t.Fatalf("room = %+v, want a room of one out of ten", room)
	}

	now = now.Add(25 * time.Minute)
	if _, err := q.Join(room.ID, "friend"); err != nil {
		t.Fatalf("Join: %v", err)
	}
	now = now.Add(20 * time.Minute) // 45 minutes after opening, 20 after the join
	q.Sweep()
	if _, ok := q.Session(room.ID); !ok {
		t.Fatal("a room joined 20 minutes ago was pruned on its creation time")
	}

	now = now.Add(31 * time.Minute)
	q.Sweep()
	if _, ok := q.Session(room.ID); ok {
		t.Error("a room idle past the TTL was kept")
	}
}

// TestOnlyARoomCanBeClosed: closing is the host abandoning a room; a matched
// session's players are already playing and are never pulled out of it.
func TestOnlyARoomCanBeClosed(t *testing.T) {
	q := New(Config{PlayersPerMatch: 2}, nil)
	if _, _, err := q.Enqueue("a", "turf"); err != nil {
		t.Fatal(err)
	}
	_, sessions, err := q.Enqueue("b", "turf")
	if err != nil || len(sessions) != 1 {
		t.Fatalf("no match formed: %v %v", sessions, err)
	}
	if q.CloseRoom(sessions[0].ID) {
		t.Error("a matched session was closed as if it were a room")
	}
	room, _ := q.OpenRoom("host", "coop_private_config", 4)
	if !q.CloseRoom(room.ID) {
		t.Error("CloseRoom did not close a room")
	}
	if _, ok := q.Session(room.ID); ok {
		t.Error("a closed room is still there")
	}
}

// TestEveryPlayerInABigMatchHasTheirTicketMarkedMatched: forming a match of
// more than two once walked the waiting list while removing from it, and
// skipped every other ticket -- the skipped players were in the match but their
// tickets still said searching, so their consoles never learned of it.
func TestEveryPlayerInABigMatchHasTheirTicketMarkedMatched(t *testing.T) {
	q, _ := testQueue(t, 4)

	var tickets []Ticket
	var formed []Session
	for _, who := range []string{"alice", "bob", "carol", "dave"} {
		ticket, sessions, err := q.Enqueue(who, "turf_war")
		if err != nil {
			t.Fatalf("enqueue %s: %v", who, err)
		}
		tickets = append(tickets, ticket)
		formed = append(formed, sessions...)
	}
	if len(formed) != 1 {
		t.Fatalf("formed %d matches, want one", len(formed))
	}
	for _, ticket := range tickets {
		got, _ := q.Get(ticket.ID)
		if got.State != StateSucceeded || got.SessionID != formed[0].ID {
			t.Errorf("%s: state %s, session %q; want succeeded in %s",
				ticket.UserID, got.State, got.SessionID, formed[0].ID)
		}
	}
	if left := q.liveWaitingLocked("turf_war"); len(left) != 0 {
		t.Errorf("%d tickets still waiting after everyone was matched", len(left))
	}
}

// TestASquadIsNeverSplitAcrossMatches: three solo players and a squad of two
// queue for a 4-player Salmon Run; the first match takes the solos plus
// nobody from the squad rather than one of its two members.
func TestASquadIsNeverSplitAcrossMatches(t *testing.T) {
	q, clock := testGraceQueue(t, 4, 0, 20*time.Second, "coop")
	q.Enqueue("solo1", "coop")
	q.Enqueue("solo2", "coop")
	q.Enqueue("solo3", "coop")
	squad, formed, err := q.EnqueueSquad("leader", []string{"member"}, "coop", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(formed) != 0 {
		t.Fatalf("formed %v: the only full match would split the squad", formed)
	}
	*clock = clock.Add(21 * time.Second)
	_, formed = q.Sweep()
	if len(formed) != 1 {
		t.Fatalf("formed %d matches after the wait, want 1", len(formed))
	}
	players := formed[0].Players
	if slices.Contains(players, "leader") != slices.Contains(players, "member") {
		t.Fatalf("squad split: %v", players)
	}

	// A squad leaves together.
	other, _, _ := q.EnqueueSquad("leader2", []string{"member2"}, "coop", "")
	q.Cancel(other.ID)
	for _, id := range q.liveWaitingLocked("coop") {
		if u := q.tickets[id].UserID; u == "member2" {
			t.Errorf("member2 is still queued after the squad's ticket was cancelled")
		}
	}
	_ = squad
}

// TestATricolorMatchIsFourDefendersAgainstTwoPairs: the midterm leader
// defends with four, the two other teams attack with two each, each team in
// its own slot (an official Tricolor battle's shape, per NextendoNetwork's
// capture notes).
func TestATricolorMatchIsFourDefendersAgainstTwoPairs(t *testing.T) {
	q, _ := testQueue(t, 8)
	mode := "fest_match_tricolor_oneshot_config"
	q.SetTricolorDefender(mode, "Bravo")
	var formed []Session
	for _, p := range []struct{ user, team string }{
		{"b1", "Bravo"}, {"b2", "Bravo"}, {"b3", "Bravo"}, {"b4", "Bravo"}, {"b5", "Bravo"},
		{"a1", "Alpha"}, {"a2", "Alpha"}, {"c1", "Charlie"}, {"c2", "Charlie"},
	} {
		_, f, err := q.EnqueueWithFestTeam(p.user, mode, p.team)
		if err != nil {
			t.Fatal(err)
		}
		formed = append(formed, f...)
	}
	if len(formed) != 1 || len(formed[0].Players) != 8 {
		t.Fatalf("formed %v, want one 8-player Tricolor match", formed)
	}
	slots := formed[0].TeamSlots
	count := map[string]int{}
	for _, slot := range slots {
		count[slot]++
	}
	if count["team1"] != 4 || count["team2"] != 2 || count["team3"] != 2 || slots["b1"] != "team1" ||
		slots["a1"] != "team2" || slots["c1"] != "team3" || slots["b5"] != "" {
		t.Fatalf("Tricolor slots = %v, want Bravo 4 in team1, Alpha 2 in team2, Charlie 2 in team3", slots)
	}
}

// TestASmallTricolorGroupKeepsTheTwoToOneRatio: below full size a Tricolor
// match still forms, 2 v 1 v 1, after the grace period.
func TestASmallTricolorGroupKeepsTheTwoToOneRatio(t *testing.T) {
	q, clock := testQueue(t, 8)
	q.cfg.VersusGracePeriod = 20 * time.Second
	mode := "fest_match_tricolor_oneshot_config"
	q.SetTricolorDefender(mode, "Alpha")
	for _, p := range []struct{ user, team string }{
		{"a1", "Alpha"}, {"a2", "Alpha"}, {"b1", "Bravo"}, {"c1", "Charlie"},
	} {
		if _, f, err := q.EnqueueWithFestTeam(p.user, mode, p.team); err != nil || len(f) != 0 {
			t.Fatalf("premature: %v %v", f, err)
		}
	}
	*clock = clock.Add(21 * time.Second)
	_, formed := q.Sweep()
	if len(formed) != 1 || len(formed[0].Players) != 4 {
		t.Fatalf("formed %v, want one 2v1v1 Tricolor match", formed)
	}
	if s := formed[0].TeamSlots; s["a1"] != "team1" || s["a2"] != "team1" || s["b1"] != "team2" || s["c1"] != "team3" {
		t.Fatalf("slots = %v", s)
	}
}

func TestAFestSquadSpanningTwoTeamsFormsOnItsOwn(t *testing.T) {
	q := New(Config{VersusGracePeriod: time.Second}, time.Now)
	mode := "tenants/current/matchmakingConfigs/fest_match_regular_direct_pair_config"

	// "Continue with this team" re-queues both sides of the lobby as one
	// squad. Labelled with the leader's team alone, it waited forever for an
	// opposing team that was already in it.
	_, formed, err := q.EnqueueSquadTeams("rock", []string{"paper"}, mode, "Alpha",
		map[string]string{"paper": "Bravo"})
	if err != nil {
		t.Fatal(err)
	}
	// The squad is the whole lobby, so it forms at once rather than after
	// the grace period.
	if len(formed) != 1 || len(formed[0].Players) != 2 {
		t.Fatalf("formed %v, want the pair matched immediately", formed)
	}
}

// A continue-with-this-team pair starts alone only when nobody else wants
// Splatfest Open; with others queued it waits, or six players become three
// separate 1v1s.
func TestAFestSquadWaitsWhileOthersAreQueuedForOpen(t *testing.T) {
	q := New(Config{VersusGracePeriod: time.Minute}, time.Now)
	pair := "tenants/current/matchmakingConfigs/fest_match_regular_direct_pair_config"
	open := "tenants/current/matchmakingConfigs/fest_match_regular_normal_config"
	if _, _, err := q.EnqueueWithFestTeam("scissors", open, "Charlie"); err != nil {
		t.Fatal(err)
	}
	_, formed, err := q.EnqueueSquadTeams("rock", []string{"paper"}, pair, "Alpha",
		map[string]string{"paper": "Bravo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(formed) != 0 {
		t.Fatalf("formed %v while another player waited for Open", formed)
	}
}

// TestOnePlayerPerTeamStillPlaysTricolor: with one player on each fest team,
// Tricolor forms 1 v 1 v 1 after the grace period instead of waiting forever
// for a second defender.
func TestOnePlayerPerTeamStillPlaysTricolor(t *testing.T) {
	q, clock := testQueue(t, 8)
	q.cfg.VersusGracePeriod = 20 * time.Second
	mode := "fest_match_tricolor_oneshot_config"
	q.SetTricolorDefender(mode, "Alpha")
	for _, p := range []struct{ user, team string }{
		{"a1", "Alpha"}, {"b1", "Bravo"}, {"c1", "Charlie"},
	} {
		if _, f, err := q.EnqueueWithFestTeam(p.user, mode, p.team); err != nil || len(f) != 0 {
			t.Fatalf("premature: %v %v", f, err)
		}
	}
	*clock = clock.Add(21 * time.Second)
	_, formed := q.Sweep()
	if len(formed) != 1 || len(formed[0].Players) != 3 {
		t.Fatalf("formed %v, want one 1v1v1 Tricolor match", formed)
	}
	if s := formed[0].TeamSlots; s["a1"] != "team1" || s["b1"] != "team2" || s["c1"] != "team3" {
		t.Fatalf("slots = %v", s)
	}
}

// TestATricolorTeamLobbyHoldsOneFestTeam: the first Tricolor step gathers a
// pair of one team at once. Players without a partner get a lobby alone only
// once all three teams are waiting, after a grace period that any new
// arrival restarts.
func TestATricolorTeamLobbyHoldsOneFestTeam(t *testing.T) {
	q, clock := testQueue(t, 8)
	q.cfg.VersusGracePeriod = 20 * time.Second
	mode := "fest_match_tricolor_team_config"
	q.SetTricolorDefender(mode, "Alpha")
	enqueue := func(user, team string) []Session {
		_, f, err := q.EnqueueWithFestTeam(user, mode, team)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}

	if f := append(enqueue("a1", "Alpha"), enqueue("a2", "Alpha")...); len(f) != 1 || len(f[0].Players) != 2 || f[0].TeamSlots["a1"] != "team1" {
		t.Fatalf("formed %v, want the Alpha pair at once", f)
	}

	// Two teams waiting alone is not a Tricolor match, however long it waits.
	enqueue("b1", "Bravo")
	enqueue("c1", "Charlie")
	*clock = clock.Add(time.Minute)
	if _, f := q.Sweep(); len(f) != 0 {
		t.Fatalf("formed %v with only Bravo and Charlie waiting", f)
	}
}

func TestTricolorSinglesWaitForAllThreeTeamsAndRestartOnArrival(t *testing.T) {
	q, clock := testQueue(t, 8)
	q.cfg.VersusGracePeriod = 20 * time.Second
	mode := "fest_match_tricolor_team_config"
	q.SetTricolorDefender(mode, "Alpha")
	for _, p := range []struct{ user, team string }{{"a1", "Alpha"}, {"b1", "Bravo"}} {
		q.EnqueueWithFestTeam(p.user, mode, p.team)
	}
	*clock = clock.Add(30 * time.Second)
	if _, f := q.Sweep(); len(f) != 0 {
		t.Fatalf("formed %v before every team was waiting", f)
	}
	q.EnqueueWithFestTeam("c1", mode, "Charlie")
	*clock = clock.Add(15 * time.Second)
	q.Sweep()
	// A fourth player joining restarts the grace period: b2 pairs with b1 at
	// once, and the pair moves on to the oneshot queue.
	if _, f, _ := q.EnqueueWithFestTeam("b2", mode, "Bravo"); len(f) != 1 || len(f[0].Players) != 2 {
		t.Fatalf("formed %v, want the Bravo pair", f)
	}
	q.EnqueueSquadTeams("b1", []string{"b2"}, tricolorOneshotMode(mode), "Bravo", nil)
	*clock = clock.Add(15 * time.Second)
	if _, f := q.Sweep(); len(f) != 0 {
		t.Fatalf("formed %v before the restarted grace ended", f)
	}
	*clock = clock.Add(10 * time.Second)
	_, f := q.Sweep()
	if len(f) != 2 {
		t.Fatalf("formed %v, want Alpha and Charlie alone after the grace", f)
	}
}
