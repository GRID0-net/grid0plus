package npln

import (
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/matchmaking"
)

// Every ticket's user definitions stayed in memory until npln restarted.
// Once the queue has pruned a ticket, its definitions go too.
func TestAPrunedTicketsDefinitionsAreForgotten(t *testing.T) {
	q := matchmaking.New(matchmaking.Config{PlayersPerMatch: 8, TicketTTL: time.Minute}, time.Now)
	live, _, err := q.Enqueue("alice", "turf_war")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{queue: q}
	s.ticketDefs.Store(live.ID+"/alice", "def")
	s.ticketDefs.Store("gone-ticket/bob", "def")
	s.forgetPrunedTicketDefs()
	if _, ok := s.ticketDefs.Load("gone-ticket/bob"); ok {
		t.Error("a pruned ticket's definition was kept")
	}
	if _, ok := s.ticketDefs.Load(live.ID + "/alice"); !ok {
		t.Error("a live ticket's definition was dropped")
	}
}
