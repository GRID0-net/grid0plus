package gamesync

import (
	"testing"
	"time"
)

// Every match kept its documents until npln restarted. An idle, unwatched
// game session is dropped; one somebody still watches is kept.
func TestAnIdleGameSessionsDocumentsAreDropped(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	ss := newStoreSet(func() time.Time { return now })
	ss.in("old-match")
	ss.in("watched-room")
	now = now.Add(time.Hour)
	ss.in("live-match")
	n := ss.drop(now.Add(-idleStoreAge), func(scope string) bool { return scope == "watched-room" })
	if n != 1 {
		t.Fatalf("dropped %d stores, want only the idle unwatched one", n)
	}
	if _, ok := ss.stores["old-match"]; ok {
		t.Fatal("the idle match's documents are still held")
	}
	for _, keep := range []string{"watched-room", "live-match"} {
		if _, ok := ss.stores[keep]; !ok {
			t.Fatalf("%s was dropped", keep)
		}
	}
}

// A session past its expiry by more than one TTL, and a transaction never
// committed, are swept; a session still inside its refresh window is not.
func TestSweepDropsDeadSessionsAndAbandonedTransactions(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	s := &Server{now: func() time.Time { return now }, sessionTTL: time.Hour,
		docs: newStoreSet(func() time.Time { return now }), watch: newWatchers(),
		sessions: map[string]*session{
			"dead":        {expiresAt: now.Add(-2 * time.Hour)},
			"refreshable": {expiresAt: now.Add(-30 * time.Minute)},
		},
		txns: map[string]*transaction{"abandoned": {startedAt: now.Add(-time.Hour)}, "open": {startedAt: now}},
	}
	s.sweep()
	if _, ok := s.sessions["dead"]; ok {
		t.Error("a session two TTLs past expiry was kept")
	}
	if _, ok := s.sessions["refreshable"]; !ok {
		t.Error("a session still inside its refresh window was dropped")
	}
	if _, ok := s.txns["abandoned"]; ok {
		t.Error("an hour-old uncommitted transaction was kept")
	}
	if _, ok := s.txns["open"]; !ok {
		t.Error("a just-opened transaction was dropped")
	}
}
