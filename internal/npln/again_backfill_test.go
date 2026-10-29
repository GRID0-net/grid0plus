package npln

import (
	"testing"

	"github.com/n-popescu/grid0plus/config"
	mmpb "github.com/n-popescu/grid0plus/proto/matchmaking/v1"
)

// TestAPlayerSearchingIsMatchedIntoACrewsAgainLobby: a crew of two that
// presses "Again!" waits in its old session, which sends no ticket. A player
// who searches meanwhile must be handed that session -- not a job of their
// own -- and Gamesync told, so the lobby keeps waiting for the rest.
func TestAPlayerSearchingIsMatchedIntoACrewsAgainLobby(t *testing.T) {
	h := newHarnessWith(t, func(c *config.NPLN) {
		c.CoopModes = []string{"salmon-run"}
		c.CoopGracePeriod = 1
		c.ModePlayers = map[string]int{"salmon-run": 4}
	})
	for _, u := range []string{"crew000000000001", "crew000000000002", "stranger00000001"} {
		h.createUser(u, "device-"+u)
	}
	one := h.ctx(h.sessionFor(t, "crew000000000001", "device-crew000000000001"), "")
	two := h.ctx(h.sessionFor(t, "crew000000000002", "device-crew000000000002"), "")
	stranger := h.ctx(h.sessionFor(t, "stranger00000001", "device-stranger00000001"), "")
	client := mmpb.NewMatchmakerClient(h.conn)

	if _, err := client.CreateMatchmakingTicket(one, ticketFor("salmon-run")); err != nil {
		t.Fatal(err)
	}
	second, err := client.CreateMatchmakingTicket(two, ticketFor("salmon-run"))
	if err != nil {
		t.Fatal(err)
	}
	// The harness does not run the production queue sweeper.
	if _, ok, err := h.srv.queue.ForceMatch("salmon-run"); err != nil || !ok {
		t.Fatalf("form the initial crew: %v, %v", ok, err)
	}
	stream, err := client.TrackMatchmakingTicket(two, &mmpb.TrackMatchmakingTicketRequest{Name: second.GetName()})
	if err != nil {
		t.Fatal(err)
	}
	job := awaitSucceeded(t, stream).GetGameSession().GetName()
	if job == "" {
		t.Fatal("the crew's job has no session")
	}

	type joined struct {
		session string
		full    bool
	}
	told := make(chan joined, 4)
	h.srv.SetAgainJoined(func(id string, full bool) { told <- joined{id, full} })
	h.srv.GamesyncAgainOpen(lastSegment(job))

	ticket, err := client.CreateMatchmakingTicket(stranger, ticketFor("salmon-run"))
	if err != nil {
		t.Fatal(err)
	}
	stream, err = client.TrackMatchmakingTicket(stranger, &mmpb.TrackMatchmakingTicketRequest{Name: ticket.GetName()})
	if err != nil {
		t.Fatal(err)
	}
	if got := awaitSucceeded(t, stream).GetGameSession().GetName(); got != job {
		t.Fatalf("the stranger was matched into %q, want the crew's job %q", got, job)
	}
	if sess, _ := h.srv.queue.Session(lastSegment(job)); len(sess.Players) != 3 || sess.MaxPlayers != 4 {
		t.Errorf("the job holds %d of %d, want 3 of 4", len(sess.Players), sess.MaxPlayers)
	}
	select {
	case j := <-told:
		if j.session != lastSegment(job) || j.full {
			t.Errorf("Gamesync was told %+v, want the job, not full", j)
		}
	default:
		t.Error("Gamesync was not told a player joined the Again! lobby")
	}
}
