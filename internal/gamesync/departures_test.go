package gamesync

import (
	"testing"
	"time"
)

func shortMemberGoneGrace(t *testing.T, d time.Duration) {
	t.Helper()
	old := memberGoneGrace
	memberGoneGrace = d
	t.Cleanup(func() { memberGoneGrace = old })
}

// TestAPlayerWhoClosesTheirLastStreamIsReportedGone: the room kept a member
// who had quit, and the next friend to join failed with 2318-1201 trying to
// reach them. The closed watch stream is the only sign a console gives.
func TestAPlayerWhoClosesTheirLastStreamIsReportedGone(t *testing.T) {
	shortMemberGoneGrace(t, 20*time.Millisecond)
	h := newHarness(t)
	gone := make(chan [2]string, 1)
	h.srv.memberGone = func(sessionID, userID string) { gone <- [2]string{sessionID, userID} }

	caller, err := h.srv.newSession("lizzy", "userSessions/us-lizzy")
	if err != nil {
		t.Fatal(err)
	}
	caller.details = &SessionDetails{SessionID: "room", Room: true}
	h.srv.beginSessionStream(caller)
	h.srv.endSessionStream(caller)

	select {
	case got := <-gone:
		if got != [2]string{"room", "lizzy"} {
			t.Errorf("reported %v, want [room lizzy]", got)
		}
	case <-time.After(time.Second):
		t.Fatal("a player with no stream left was never reported gone")
	}
}

// TestAPlayerWhoReconnectsWithinTheGraceStays: a stream the console re-opens
// straight away must not cost the player their seat.
func TestAPlayerWhoReconnectsWithinTheGraceStays(t *testing.T) {
	shortMemberGoneGrace(t, 50*time.Millisecond)
	h := newHarness(t)
	gone := make(chan string, 1)
	h.srv.memberGone = func(_, userID string) { gone <- userID }

	caller, err := h.srv.newSession("jade", "userSessions/us-jade")
	if err != nil {
		t.Fatal(err)
	}
	caller.details = &SessionDetails{SessionID: "room", Room: true}
	h.srv.beginSessionStream(caller)
	h.srv.endSessionStream(caller)
	h.srv.beginSessionStream(caller)

	select {
	case user := <-gone:
		t.Fatalf("%s was reported gone while their stream was open again", user)
	case <-time.After(4 * memberGoneGrace):
	}

	// And once they really leave, the grace starts over from that close.
	h.srv.endSessionStream(caller)
	select {
	case <-gone:
	case <-time.After(time.Second):
		t.Fatal("a player who left after reconnecting was never reported gone")
	}
}

// TestASecondStreamKeepsThePlayerIn: streams are counted per user session,
// and closing one of two is not leaving.
func TestASecondStreamKeepsThePlayerIn(t *testing.T) {
	shortMemberGoneGrace(t, 20*time.Millisecond)
	h := newHarness(t)
	gone := make(chan string, 1)
	h.srv.memberGone = func(_, userID string) { gone <- userID }

	caller, err := h.srv.newSession("host", "userSessions/us-host")
	if err != nil {
		t.Fatal(err)
	}
	caller.details = &SessionDetails{SessionID: "room", Room: true}
	h.srv.beginSessionStream(caller)
	h.srv.beginSessionStream(caller)
	h.srv.endSessionStream(caller)

	select {
	case user := <-gone:
		t.Fatalf("%s was reported gone with a stream still open", user)
	case <-time.After(5 * memberGoneGrace):
	}
}

// TestARankComesFromTheSeatNotTheRosterOrder: after a player left, the
// roster index of everyone behind them shifted down, and a newcomer took the
// upcsid of a console still connected. Pia elects its host by lowest upcsid.
func TestARankComesFromTheSeatNotTheRosterOrder(t *testing.T) {
	newcomer := &session{userID: "c", details: &SessionDetails{
		Players: []string{"host", "b", "c"},
		Seats:   map[string]int{"host": 1, "a": 2, "b": 3, "c": 4},
	}}
	if got := participantRank(newcomer); got != 4 {
		t.Errorf("rank = %d, want the seat 4 rather than roster place 3, which b holds", got)
	}
}
