package matchmaking

import (
	"slices"
	"testing"
	"time"
)

const coopMode = "tenants/current/matchmakingConfigs/coop_regular_config"

// twoPlayerJob forms a Salmon Run job of two after its grace period, the
// way a crew of two friends gets one.
func twoPlayerJob(t *testing.T) (*Queue, *time.Time, Session) {
	t.Helper()
	q, clock := testGraceQueue(t, 4, 0, 20*time.Second, coopMode)
	enqueueAll(t, q, coopMode, "host", "friend")
	*clock = clock.Add(21 * time.Second)
	_, formed := q.Sweep()
	if len(formed) != 1 || len(formed[0].Players) != 2 {
		t.Fatalf("no two-player job formed: %v", formed)
	}
	return q, clock, formed[0]
}

// TestAPlayerSearchingJoinsACrewThatPressedAgain: "Again!" reopens the
// session the crew played in and Splatoon 3 then waits there for players.
// A crew of two must gather the next players searching, as it did when it
// first queued, instead of starting a second job of two with them.
func TestAPlayerSearchingJoinsACrewThatPressedAgain(t *testing.T) {
	q, _, job := twoPlayerJob(t)
	reopened, ok := q.OpenBackfill(job.ID)
	if !ok || reopened.MaxPlayers != 4 {
		t.Fatalf("OpenBackfill = %v, %v; want the job open up to four", reopened.MaxPlayers, ok)
	}

	ticket, formed, err := q.Enqueue("stranger", coopMode)
	if err != nil {
		t.Fatal(err)
	}
	if len(formed) != 0 {
		t.Fatalf("a new job formed instead of filling the crew: %v", formed)
	}
	got, _ := q.Get(ticket.ID)
	if got.State != StateSucceeded || got.SessionID != job.ID {
		t.Fatalf("ticket = %v in %q, want SUCCEEDED in the reopened job %q", got.State, got.SessionID, job.ID)
	}
	s, _ := q.Session(job.ID)
	if !slices.Equal(s.Players, []string{"host", "friend", "stranger"}) || s.Seat("stranger") != 3 {
		t.Errorf("players = %v, stranger's seat %d; want them third", s.Players, s.Seat("stranger"))
	}
	fills := q.TakeBackfills()
	if len(fills) != 1 || !slices.Equal(fills[0].Added, []string{"stranger"}) || fills[0].Session.ID != job.ID {
		t.Errorf("backfills = %+v, want stranger into the job", fills)
	}
	if again := q.TakeBackfills(); len(again) != 0 {
		t.Errorf("a backfill was reported twice: %+v", again)
	}
}

// TestASquadThatDoesNotFitStaysInTheQueue: a crew is never split to fill
// seats, the same rule a fresh job follows.
func TestASquadThatDoesNotFitStaysInTheQueue(t *testing.T) {
	q, _, job := twoPlayerJob(t)
	q.OpenBackfill(job.ID)

	squad, _, err := q.EnqueueSquad("lead", []string{"b", "c"}, coopMode, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := q.Get(squad.ID); got.SessionID == job.ID {
		t.Fatal("a squad of three was split or squeezed into two free seats")
	}
	if s, _ := q.Session(job.ID); len(s.Players) != 2 {
		t.Errorf("players = %v, want the crew alone", s.Players)
	}
}

// TestAClosedRematchTakesNobody: once the lobby locked, players searching
// form their own job.
func TestAClosedRematchTakesNobody(t *testing.T) {
	q, _, job := twoPlayerJob(t)
	q.OpenBackfill(job.ID)
	q.CloseBackfill(job.ID)

	enqueueAll(t, q, coopMode, "x", "y")
	if s, _ := q.Session(job.ID); len(s.Players) != 2 {
		t.Errorf("a locked job took players: %v", s.Players)
	}
	if fills := q.TakeBackfills(); len(fills) != 0 {
		t.Errorf("backfills after closing = %+v", fills)
	}
}

// TestOnlyAMatchedSalmonRunJobOpensForBackfill: a room is filled by its
// friends, and Versus has teams to balance.
func TestOnlyAMatchedSalmonRunJobOpensForBackfill(t *testing.T) {
	q, _ := testGraceQueue(t, 2, 0, 0, coopMode)
	room, _ := q.OpenRoom("host", "coop_private_config", 4)
	if _, ok := q.OpenBackfill(room.ID); ok {
		t.Error("a room opened for backfill")
	}
	formed := enqueueAll(t, q, "turf_war", "a", "b")
	if len(formed) != 1 {
		t.Fatalf("no versus match: %v", formed)
	}
	if _, ok := q.OpenBackfill(formed[0].ID); ok {
		t.Error("a versus match opened for backfill")
	}
}
