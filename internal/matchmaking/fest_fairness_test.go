package matchmaking

import (
	"slices"
	"testing"
	"time"
)

// A pair from the Tricolor team step re-queues as one squad. Taking players
// by team split it: one attacker played, the partner was left in the queue.
func TestATricolorPairIsNeverSplitAcrossMatches(t *testing.T) {
	q, clock := testQueue(t, 8)
	q.cfg.VersusGracePeriod = 20 * time.Second
	mode := "fest_match_tricolor_oneshot_config"
	q.SetTricolorDefender(mode, "Alpha")
	q.EnqueueWithFestTeam("a1", mode, "Alpha")
	q.EnqueueWithFestTeam("a2", mode, "Alpha")
	q.EnqueueSquadTeams("b1", []string{"b2"}, mode, "Bravo", nil)
	q.EnqueueWithFestTeam("b3", mode, "Bravo")
	q.EnqueueWithFestTeam("c1", mode, "Charlie")
	*clock = clock.Add(21 * time.Second)
	_, formed := q.Sweep()
	if len(formed) != 1 {
		t.Fatalf("formed %v, want one 2v1v1 match", formed)
	}
	p := formed[0].Players
	if slices.Contains(p, "b1") != slices.Contains(p, "b2") {
		t.Fatalf("match %v holds half of the b1+b2 pair", p)
	}
	if !slices.Contains(p, "b3") {
		t.Fatalf("match %v skipped the solo attacker that fits", p)
	}
}

// Two friends of one team queuing Tricolor together are each other's pair,
// not paired off with strangers who happened to queue first.
func TestFriendsOfOneTeamFormTheirOwnTricolorPair(t *testing.T) {
	q, _ := testQueue(t, 8)
	mode := "fest_match_tricolor_team_config"
	q.SetTricolorDefender(mode, "Alpha")
	q.EnqueueWithFestTeam("stranger", mode, "Bravo")
	_, formed, err := q.EnqueueSquadTeams("b1", []string{"b2"}, mode, "Bravo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(formed) != 1 || !slices.Equal(sorted(formed[0].Players), []string{"b1", "b2"}) {
		t.Fatalf("formed %v, want the friends b1+b2 as one pair", formed)
	}
}

// Splatfest Pro with a squad in the queue: the match takes the squad whole
// or leaves it, never half.
func TestAFestProMatchNeverHoldsHalfASquad(t *testing.T) {
	q, _ := testQueue(t, 4)
	mode := "fest_match_challenge_oneshot_config"
	q.EnqueueSquadTeams("a1", []string{"a2"}, mode, "Alpha", nil)
	q.EnqueueWithFestTeam("b1", mode, "Bravo")
	_, formed, _ := q.EnqueueWithFestTeam("b2", mode, "Bravo")
	if len(formed) != 1 || len(formed[0].Players) != 4 {
		t.Fatalf("formed %v, want the a1+a2 squad against b1 and b2", formed)
	}
}

// With all three teams queued and no full match, the grace period starts
// the team that has waited longest rather than the biggest pair of teams:
// before, Alpha and Bravo kept playing while Charlie, first in the queue,
// waited.
func TestTheLongestWaitingFestTeamIsMatchedFirst(t *testing.T) {
	q, clock := testQueue(t, 4)
	q.cfg.VersusGracePeriod = 20 * time.Second
	mode := "fest_match_challenge_oneshot_config"
	q.EnqueueWithFestTeam("c1", mode, "Charlie")
	q.EnqueueWithFestTeam("a1", mode, "Alpha")
	q.EnqueueWithFestTeam("b1", mode, "Bravo")
	if _, f, _ := q.EnqueueWithFestTeam("a2", mode, "Alpha"); len(f) != 0 {
		t.Fatalf("formed %v before the grace period", f)
	}
	*clock = clock.Add(21 * time.Second)
	_, formed := q.Sweep()
	if len(formed) != 1 || !slices.Contains(formed[0].Players, "c1") {
		t.Fatalf("formed %v, want a 1v1 with Charlie, who waited longest", formed)
	}
}

func sorted(s []string) []string { out := slices.Clone(s); slices.Sort(out); return out }

func queueStarvedCharlie(t *testing.T, grace time.Duration) (*Queue, *time.Time, []Session) {
	t.Helper()
	q, clock := testQueue(t, 8)
	q.cfg.VersusGracePeriod = grace
	mode := "fest_match_challenge_oneshot_config"
	q.EnqueueWithFestTeam("c1", mode, "Charlie")
	q.EnqueueWithFestTeam("c2", mode, "Charlie")
	*clock = clock.Add(2 * time.Minute)
	var formed []Session
	for _, p := range []struct{ u, team string }{
		{"a1", "Alpha"}, {"a2", "Alpha"}, {"a3", "Alpha"}, {"b1", "Bravo"}, {"b2", "Bravo"},
		{"b3", "Bravo"}, {"a4", "Alpha"}, {"b4", "Bravo"},
	} {
		_, f, _ := q.EnqueueWithFestTeam(p.u, mode, p.team)
		formed = append(formed, f...)
	}
	return q, clock, formed
}

// Splatfest Pro: once Charlie's players have waited a minute, the next match
// includes Charlie even though Alpha and Bravo could make a bigger one.
func TestAStarvedFestTeamGetsTheNextMatchEvenIfSmaller(t *testing.T) {
	q, clock, formed := queueStarvedCharlie(t, 20*time.Second)
	if len(formed) != 0 {
		t.Fatalf("formed %v ahead of Charlie, who has waited two minutes", formed[0].Players)
	}
	*clock = clock.Add(21 * time.Second)
	_, formed = q.Sweep()
	if len(formed) != 1 || !slices.Contains(formed[0].Players, "c1") || len(formed[0].Players) != 4 {
		t.Fatalf("formed %v, want Charlie's 2v2", formed)
	}
}

// Without early starts a Charlie match cannot form at all, so the full
// Alpha v Bravo is not held back for it.
func TestAStarvedTeamDoesNotBlockAFullMatchItCannotJoin(t *testing.T) {
	_, _, formed := queueStarvedCharlie(t, 0)
	if len(formed) != 1 || len(formed[0].Players) != 8 {
		t.Fatalf("formed %v, want the full Alpha v Bravo", formed)
	}
}
