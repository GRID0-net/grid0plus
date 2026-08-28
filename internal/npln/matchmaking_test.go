package npln

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/n-popescu/switchnet/config"
	authpb "github.com/n-popescu/switchnet/proto/auth/v1"
	mmpb "github.com/n-popescu/switchnet/proto/matchmaking/v1"
)

// session logs a user in and returns a context carrying their NPLN token.
func (h *harness) session(userID, deviceAccountID string) context.Context {
	h.t.Helper()
	h.createUser(userID, deviceAccountID)

	resp, err := authpb.NewAuthClient(h.conn).IssueToken(h.ctx("", ""),
		&authpb.IssueTokenRequest{
			ExternalIdToken: &authpb.ExternalIdToken{
				Token: &authpb.ExternalIdToken_NsaIdToken{
					NsaIdToken: h.baasIDToken(userID, deviceAccountID),
				},
			},
		})
	if err != nil {
		h.t.Fatalf("issue token for %s: %v", userID, err)
	}
	return h.ctx(resp.GetToken().GetAccessToken(), userID)
}

func queueFor(h *harness, ctx context.Context, mode string) *mmpb.MatchmakingTicket {
	h.t.Helper()
	ticket, err := mmpb.NewMatchmakerClient(h.conn).CreateMatchmakingTicket(ctx,
		&mmpb.CreateMatchmakingTicketRequest{
			MatchmakingTicket: &mmpb.MatchmakingTicket{
				MatchmakingConfig: "tenants/" + testTenant + "/matchmakingConfigs/" + mode,
			},
		})
	if err != nil {
		h.t.Fatalf("create ticket: %v", err)
	}
	return ticket
}

func TestTwoPlayersQueueingAreToldAboutTheSameMatchOnTheirStreams(t *testing.T) {
	h := newHarness(t)
	alice := h.session("alice", "aaaaaaaaaaaaaaaa")
	bob := h.session("bob", "bbbbbbbbbbbbbbbb")

	client := mmpb.NewMatchmakerClient(h.conn)

	// Alice queues and starts tracking. Her stream is open BEFORE bob arrives,
	// which is the real sequence: the console lives on this stream and learns
	// about the match through it.
	aliceTicket := queueFor(h, alice, "turf_war")
	if aliceTicket.GetState() != mmpb.MatchmakingTicket_SEARCHING {
		t.Fatalf("alice's ticket is %s, want SEARCHING", aliceTicket.GetState())
	}

	aliceStream, err := client.TrackMatchmakingTicket(alice,
		&mmpb.TrackMatchmakingTicketRequest{Name: aliceTicket.GetName()})
	if err != nil {
		t.Fatalf("track: %v", err)
	}
	// The first send is the current state, so a stream that attached after its
	// ticket already matched does not wait for an event that has happened.
	if first, err := aliceStream.Recv(); err != nil {
		t.Fatalf("alice's first update: %v", err)
	} else if first.GetState() != mmpb.MatchmakingTicket_SEARCHING {
		t.Fatalf("alice's first update is %s, want SEARCHING", first.GetState())
	}

	bobTicket := queueFor(h, bob, "turf_war")
	if bobTicket.GetState() != mmpb.MatchmakingTicket_SUCCEEDED {
		t.Fatalf("bob's ticket is %s, want SUCCEEDED — his arrival forms the match",
			bobTicket.GetState())
	}

	aliceMatched, err := aliceStream.Recv()
	if err != nil {
		t.Fatalf("alice was never told her ticket matched: %v", err)
	}
	if aliceMatched.GetState() != mmpb.MatchmakingTicket_SUCCEEDED {
		t.Fatalf("alice's update is %s, want SUCCEEDED", aliceMatched.GetState())
	}

	// The same session, and the same host, on both sides. Two players told
	// about different sessions is a lobby that never fills.
	aliceSession := aliceMatched.GetGameSession()
	bobSession := bobTicket.GetGameSession()
	if aliceSession.GetName() == "" {
		t.Fatal("alice's matched ticket names no session")
	}
	if aliceSession.GetName() != bobSession.GetName() {
		t.Errorf("alice is in %q and bob in %q", aliceSession.GetName(), bobSession.GetName())
	}
	if aliceSession.GetHost() != bobSession.GetHost() {
		t.Errorf("alice's host is %q and bob's is %q",
			aliceSession.GetHost(), bobSession.GetHost())
	}
	if aliceSession.GetHost() != userResourceName("alice") {
		t.Errorf("host = %q, want alice, who waited longest", aliceSession.GetHost())
	}
	if got := aliceSession.GetCurrentParticipantCount(); got != 2 {
		t.Errorf("session has %d participants, want 2", got)
	}

	// Both players are named on the ticket, so the console does not need a
	// second call to find out who it is playing with.
	if got := len(aliceMatched.GetMatchedUserSessions()); got != 2 {
		t.Errorf("matched ticket names %d players, want 2", got)
	}

	// The stream ends once the ticket is terminal: it cannot change again.
	if _, err := aliceStream.Recv(); err == nil {
		t.Error("the stream stayed open after the ticket reached a terminal state")
	}
}

func TestACancelledTicketIsNeverMatched(t *testing.T) {
	h := newHarness(t)
	alice := h.session("alice", "aaaaaaaaaaaaaaaa")
	bob := h.session("bob", "bbbbbbbbbbbbbbbb")

	client := mmpb.NewMatchmakerClient(h.conn)

	aliceTicket := queueFor(h, alice, "turf_war")
	if _, err := client.CancelMatchmakingTicket(alice,
		&mmpb.CancelMatchmakingTicketRequest{Name: aliceTicket.GetName()}); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	// Bob arriving must not match against a ticket nobody is waiting on.
	bobTicket := queueFor(h, bob, "turf_war")
	if bobTicket.GetState() != mmpb.MatchmakingTicket_SEARCHING {
		t.Fatalf("bob matched a cancelled ticket: his state is %s", bobTicket.GetState())
	}
	if bobTicket.GetGameSession() != nil {
		t.Error("bob was handed a session formed against a cancelled ticket")
	}
}

func TestOnePlayerCannotTrackOrCancelAnothersTicket(t *testing.T) {
	h := newHarness(t)
	alice := h.session("alice", "aaaaaaaaaaaaaaaa")
	bob := h.session("bob", "bbbbbbbbbbbbbbbb")

	client := mmpb.NewMatchmakerClient(h.conn)
	aliceTicket := queueFor(h, alice, "turf_war")

	// Bob tracking alice's ticket would tell him who she is matched with and
	// when. Refused rather than served read-only.
	stream, err := client.TrackMatchmakingTicket(bob,
		&mmpb.TrackMatchmakingTicketRequest{Name: aliceTicket.GetName()})
	if err == nil {
		_, err = stream.Recv()
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("bob tracking alice's ticket gave %v, want PermissionDenied", err)
	}

	if _, err := client.CancelMatchmakingTicket(bob,
		&mmpb.CancelMatchmakingTicketRequest{Name: aliceTicket.GetName()}); status.Code(err) != codes.PermissionDenied {
		t.Errorf("bob cancelling alice's ticket gave %v, want PermissionDenied", err)
	}
}

func TestADelegatedTicketIsRefusedRatherThanQuietlyQueueingOnePlayer(t *testing.T) {
	h := newHarness(t)
	alice := h.session("alice", "aaaaaaaaaaaaaaaa")

	// A console that asked to queue two players and got one back sits in a
	// lobby waiting for somebody who was never queued, so this refuses rather
	// than ignoring the delegation.
	_, err := mmpb.NewMatchmakerClient(h.conn).CreateMatchmakingTicket(alice,
		&mmpb.CreateMatchmakingTicketRequest{
			MatchmakingTicket: &mmpb.MatchmakingTicket{
				MatchmakingConfig: "tenants/" + testTenant + "/matchmakingConfigs/turf_war",
			},
			UserDelegationTokens: []string{"not-a-real-delegation"},
		})
	if status.Code(err) != codes.Unimplemented {
		t.Errorf("a delegated ticket gave %v, want Unimplemented", err)
	}
}

func TestATicketWithNoMatchmakingConfigIsRefused(t *testing.T) {
	h := newHarness(t)
	alice := h.session("alice", "aaaaaaaaaaaaaaaa")

	// Without a config there is no mode, and configless tickets matching each
	// other would put two players queuing for different things in one match.
	_, err := mmpb.NewMatchmakerClient(h.conn).CreateMatchmakingTicket(alice,
		&mmpb.CreateMatchmakingTicketRequest{
			MatchmakingTicket: &mmpb.MatchmakingTicket{},
		})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("a configless ticket gave %v, want InvalidArgument", err)
	}
}

func TestQueueingUnauthenticatedIsRefused(t *testing.T) {
	h := newHarness(t)

	_, err := mmpb.NewMatchmakerClient(h.conn).CreateMatchmakingTicket(h.ctx("", ""),
		&mmpb.CreateMatchmakingTicketRequest{
			MatchmakingTicket: &mmpb.MatchmakingTicket{
				MatchmakingConfig: "matchmakingConfigs/turf_war",
			},
		})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("queueing with no token gave %v, want Unauthenticated", err)
	}
}

func TestAPlayerOutsideASessionCannotSeeThatItExists(t *testing.T) {
	h := newHarness(t)
	alice := h.session("alice", "aaaaaaaaaaaaaaaa")
	bob := h.session("bob", "bbbbbbbbbbbbbbbb")
	carol := h.session("carol", "cccccccccccccccc")

	queueFor(h, alice, "turf_war")
	bobTicket := queueFor(h, bob, "turf_war")
	name := bobTicket.GetGameSession().GetName()

	sessions := mmpb.NewGameSessionServiceClient(h.conn)

	if _, err := sessions.GetGameSession(alice,
		&mmpb.GetGameSessionRequest{Name: name}); err != nil {
		t.Fatalf("alice cannot read the session she is in: %v", err)
	}

	// NotFound rather than PermissionDenied: carol should not learn that a
	// session she is not in exists.
	_, err := sessions.GetGameSession(carol, &mmpb.GetGameSessionRequest{Name: name})
	if status.Code(err) != codes.NotFound {
		t.Errorf("carol reading a session she is not in gave %v, want NotFound", err)
	}
}

func TestJoiningTheMatchYouWereJustMatchedIntoSucceeds(t *testing.T) {
	h := newHarness(t)
	alice := h.session("alice", "aaaaaaaaaaaaaaaa")
	bob := h.session("bob", "bbbbbbbbbbbbbbbb")
	carol := h.session("carol", "cccccccccccccccc")

	queueFor(h, alice, "turf_war")
	bobTicket := queueFor(h, bob, "turf_war")
	name := bobTicket.GetGameSession().GetName()

	sessions := mmpb.NewGameSessionServiceClient(h.conn)

	// Idempotent: the console joins the match it was just told it is in.
	resp, err := sessions.JoinGameSession(alice, &mmpb.JoinGameSessionRequest{Name: name})
	if err != nil {
		t.Fatalf("alice cannot join her own match: %v", err)
	}
	if got := len(resp.GetMatchedUserSessions()); got != 2 {
		t.Errorf("join named %d players, want 2", got)
	}
	if resp.GetGameSession().GetName() != name {
		t.Errorf("join returned session %q, want %q", resp.GetGameSession().GetName(), name)
	}

	// A two-player match has no room for a third. Over-filling silently hands
	// the game a participant it rejects on the console with a generic error.
	_, err = sessions.JoinGameSession(carol, &mmpb.JoinGameSessionRequest{Name: name})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("carol joining a full match gave %v, want FailedPrecondition", err)
	}

	_, err = sessions.JoinGameSession(alice,
		&mmpb.JoinGameSessionRequest{Name: "tenants/" + testTenant + "/gameSessions/nope"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("joining a session nobody issued gave %v, want NotFound", err)
	}
}

func TestTheConfiguredIceServersAreHandedToTheConsole(t *testing.T) {
	h := newHarness(t)
	h.srv.iceServers = []config.IceServer{
		{Kind: "stun", Host: "192.168.1.50", Port: 3478, Protocol: "udp"},
		{Kind: "turn", Host: "192.168.1.50", Port: 3479, Protocol: "tcp",
			Username: "switchnet", Password: "secret"},
	}
	alice := h.session("alice", "aaaaaaaaaaaaaaaa")

	set, err := mmpb.NewGameSessionServiceClient(h.conn).AllocateIceServerSet(alice,
		&mmpb.AllocateIceServerSetRequest{})
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}

	stun := set.GetStunServer()
	if stun.GetHost() != "192.168.1.50" || stun.GetPort() != 3478 {
		t.Errorf("stun = %s:%d, want 192.168.1.50:3478", stun.GetHost(), stun.GetPort())
	}
	if stun.GetProtocol() != mmpb.StunServer_UDP {
		t.Errorf("stun protocol = %s, want UDP", stun.GetProtocol())
	}
	if len(set.GetTurnServers()) != 1 {
		t.Fatalf("got %d turn servers, want 1", len(set.GetTurnServers()))
	}
	turn := set.GetTurnServers()[0]
	if turn.GetProtocol() != mmpb.TurnServer_TCP {
		t.Errorf("turn protocol = %s, want TCP", turn.GetProtocol())
	}
	if turn.GetUsername() != "switchnet" || turn.GetPassword() != "secret" {
		t.Error("the turn credentials did not reach the console")
	}
	if set.GetTtl().AsDuration() != iceServerSetTTL {
		t.Errorf("ttl = %s, want %s", set.GetTtl().AsDuration(), iceServerSetTTL)
	}
}

func TestNoIceServersIsAnEmptySetRatherThanAnError(t *testing.T) {
	h := newHarness(t)
	alice := h.session("alice", "aaaaaaaaaaaaaaaa")

	// Peers on one LAN reach each other without ICE, so a deployment with none
	// configured is legitimate. Failing here would break matchmaking for it.
	set, err := mmpb.NewGameSessionServiceClient(h.conn).AllocateIceServerSet(alice,
		&mmpb.AllocateIceServerSetRequest{})
	if err != nil {
		t.Fatalf("allocate with no ice servers configured: %v", err)
	}
	if set.GetStunServer() != nil || len(set.GetTurnServers()) != 0 {
		t.Error("a server appeared in the set that was never configured")
	}
}

func TestAModeWithAConfiguredSizeWaitsForThatManyPlayers(t *testing.T) {
	h := newHarnessWith(t, func(c *config.NPLN) {
		c.ModePlayers = map[string]int{"salmon_run": 4}
	})

	alice := h.session("alice", "aaaaaaaaaaaaaaaa")
	bob := h.session("bob", "bbbbbbbbbbbbbbbb")
	carol := h.session("carol", "cccccccccccccccc")
	dave := h.session("dave", "dddddddddddddddd")

	for _, ctx := range []context.Context{alice, bob, carol} {
		if got := queueFor(h, ctx, "salmon_run"); got.GetState() != mmpb.MatchmakingTicket_SEARCHING {
			t.Fatalf("a four-player match formed early: %s", got.GetState())
		}
	}
	last := queueFor(h, dave, "salmon_run")
	if last.GetState() != mmpb.MatchmakingTicket_SUCCEEDED {
		t.Fatalf("the fourth player did not form the match: %s", last.GetState())
	}
	if got := last.GetGameSession().GetMaxParticipantCount(); got != 4 {
		t.Errorf("session reports room for %d, want the configured 4", got)
	}
}

func TestAnExpiredTicketIsSweptAndItsWatcherToldSoRatherThanWaitingForever(t *testing.T) {
	h := newHarness(t)
	alice := h.session("alice", "aaaaaaaaaaaaaaaa")

	ticket := queueFor(h, alice, "turf_war")

	stream, err := mmpb.NewMatchmakerClient(h.conn).TrackMatchmakingTicket(alice,
		&mmpb.TrackMatchmakingTicketRequest{Name: ticket.GetName()})
	if err != nil {
		t.Fatalf("track: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("first update: %v", err)
	}

	// The sweeper runs on a 15-second timer, which a test must not wait for.
	// Sweeping directly exercises the same code the timer calls: what is being
	// asserted is that the timeout reaches the watcher, not the timer.
	h.now = h.now.Add(10 * time.Minute)
	if swept := h.srv.queue.Sweep(); len(swept) != 1 {
		t.Fatalf("swept %d tickets, want 1", len(swept))
	}

	update, err := stream.Recv()
	if err != nil {
		t.Fatalf("the watcher was never told its ticket expired: %v", err)
	}
	if update.GetState() != mmpb.MatchmakingTicket_TIMED_OUT {
		t.Errorf("state = %s, want TIMED_OUT", update.GetState())
	}
}
