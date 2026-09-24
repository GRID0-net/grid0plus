package npln

import (
	"context"
	"io"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/n-popescu/switchnet/config"
	mmpb "github.com/n-popescu/switchnet/proto/matchmaking/v1"
)

// TestTwoPlayersQueueAndBothReceiveTheSameSession is TODO.md §1.1's own
// acceptance criterion: two clients queue, both receive a session on their
// streams naming the same session id and the same host.
//
// Driven through a real gRPC client over bufconn rather than by calling the
// service methods directly, because the stream, the metadata and the
// interceptor that authenticates it are the parts most likely to be wrong.
func TestTwoPlayersQueueAndBothReceiveTheSameSession(t *testing.T) {
	h := newHarness(t)
	h.createUser("player-one", "device-one")
	h.createUser("player-two", "device-two")

	one := h.ctx(h.sessionFor(t, "player-one", "device-one"), "")
	two := h.ctx(h.sessionFor(t, "player-two", "device-two"), "")

	client := mmpb.NewMatchmakerClient(h.conn)

	// The first player queues and finds nobody: the default is two per match.
	first, err := client.CreateMatchmakingTicket(one, ticketFor("turf-war"))
	if err != nil {
		t.Fatalf("CreateMatchmakingTicket (one): %v", err)
	}
	if first.GetState() != mmpb.MatchmakingTicket_SEARCHING {
		t.Fatalf("a lone ticket should still be searching, got %v", first.GetState())
	}

	// Attach the stream BEFORE the second player arrives, which is the order a
	// console does it in and the order that actually exercises a live update
	// rather than Watch's immediate first send.
	streamOne, err := client.TrackMatchmakingTicket(one,
		&mmpb.TrackMatchmakingTicketRequest{Name: first.GetName()})
	if err != nil {
		t.Fatalf("TrackMatchmakingTicket (one): %v", err)
	}

	second, err := client.CreateMatchmakingTicket(two, ticketFor("turf-war"))
	if err != nil {
		t.Fatalf("CreateMatchmakingTicket (two): %v", err)
	}
	if second.GetState() != mmpb.MatchmakingTicket_SUCCEEDED {
		t.Fatalf("the ticket that completes a match should succeed, got %v", second.GetState())
	}

	streamTwo, err := client.TrackMatchmakingTicket(two,
		&mmpb.TrackMatchmakingTicketRequest{Name: second.GetName()})
	if err != nil {
		t.Fatalf("TrackMatchmakingTicket (two): %v", err)
	}

	gotOne := awaitSucceeded(t, streamOne)
	gotTwo := awaitSucceeded(t, streamTwo)

	if gotOne.GetGameSession().GetName() == "" {
		t.Fatal("a succeeded ticket carries no game session")
	}
	if gotOne.GetGameSession().GetName() != gotTwo.GetGameSession().GetName() {
		t.Errorf("the two players were placed in different sessions: %q and %q",
			gotOne.GetGameSession().GetName(), gotTwo.GetGameSession().GetName())
	}
	if gotOne.GetGameSession().GetHost() != gotTwo.GetGameSession().GetHost() {
		t.Errorf("the two players were told to connect to different hosts: %q and %q",
			gotOne.GetGameSession().GetHost(), gotTwo.GetGameSession().GetHost())
	}
	// Host/port is where each console opens its gamesync session: the
	// configured session server, never a player. A player resource name here
	// is an address the console cannot dial, and 2321-4992.
	if gs := gotOne.GetGameSession(); gs.GetHost() != testSessionHost || gs.GetPort() != 443 {
		t.Errorf("session endpoint = %s:%d, want %s:443", gs.GetHost(), gs.GetPort(), testSessionHost)
	}
	if n := gotOne.GetGameSession().GetCurrentParticipantCount(); n != 2 {
		t.Errorf("participant count = %d, want 2", n)
	}

	// Both players can then read the session back, which is what the console
	// does between matching and connecting.
	sessions := mmpb.NewGameSessionServiceClient(h.conn)
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{{"one", one}, {"two", two}} {
		got, err := sessions.GetGameSession(tc.ctx,
			&mmpb.GetGameSessionRequest{Name: gotOne.GetGameSession().GetName()})
		if err != nil {
			t.Fatalf("GetGameSession (%s): %v", tc.name, err)
		}
		if got.GetName() != gotOne.GetGameSession().GetName() {
			t.Errorf("GetGameSession (%s) returned a different session", tc.name)
		}
	}
}

// TestACancelledTicketIsNeverMatched is the other half of §1.1's criterion.
//
// The dangerous version of getting this wrong is silent: the cancelled player
// is matched anyway, never connects, and the other console sits in a lobby that
// never fills -- which reads as a matchmaking bug rather than a cancellation
// one.
func TestACancelledTicketIsNeverMatched(t *testing.T) {
	h := newHarness(t)
	h.createUser("quitter", "device-quitter")
	h.createUser("stayer", "device-stayer")

	quitter := h.ctx(h.sessionFor(t, "quitter", "device-quitter"), "")
	stayer := h.ctx(h.sessionFor(t, "stayer", "device-stayer"), "")

	client := mmpb.NewMatchmakerClient(h.conn)

	first, err := client.CreateMatchmakingTicket(quitter, ticketFor("turf-war"))
	if err != nil {
		t.Fatalf("CreateMatchmakingTicket: %v", err)
	}
	if _, err := client.CancelMatchmakingTicket(quitter,
		&mmpb.CancelMatchmakingTicketRequest{Name: first.GetName()}); err != nil {
		t.Fatalf("CancelMatchmakingTicket: %v", err)
	}

	// A second player arrives. There is nobody left to match with.
	second, err := client.CreateMatchmakingTicket(stayer, ticketFor("turf-war"))
	if err != nil {
		t.Fatalf("CreateMatchmakingTicket (stayer): %v", err)
	}
	if second.GetState() != mmpb.MatchmakingTicket_SEARCHING {
		t.Fatalf("matched against a cancelled ticket: state = %v", second.GetState())
	}
	if second.GetGameSession().GetName() != "" {
		t.Errorf("a session was formed with a cancelled player: %q",
			second.GetGameSession().GetName())
	}

	// And the cancelled ticket stays cancelled rather than being revived.
	got, err := client.TrackMatchmakingTicket(quitter,
		&mmpb.TrackMatchmakingTicketRequest{Name: first.GetName()})
	if err != nil {
		t.Fatalf("TrackMatchmakingTicket: %v", err)
	}
	msg, err := got.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if msg.GetState() != mmpb.MatchmakingTicket_CANCELLED {
		t.Errorf("state = %v, want CANCELLED", msg.GetState())
	}
}

// TestATicketIsCreatedForTheTokenNotForTheUserTheRequestNames is the guard
// against this package's founding bug in its matchmaking form.
//
// The request carries UserDefinitions naming who is queuing. Honouring that
// would let any authenticated console queue as anybody else -- the same shape
// as the device-account fallback internal/baas is built around, which made
// every console act as one person.
func TestATicketIsCreatedForTheTokenNotForTheUserTheRequestNames(t *testing.T) {
	h := newHarness(t)
	h.createUser("liar", "device-liar")
	h.createUser("victim", "device-victim")

	liar := h.ctx(h.sessionFor(t, "liar", "device-liar"), "")

	req := ticketFor("turf-war")
	req.MatchmakingTicket.UserDefinitions = []*mmpb.UserDefinition{
		{User: h.srv.userResourceName("victim")},
	}

	got, err := mmpb.NewMatchmakerClient(h.conn).CreateMatchmakingTicket(liar, req)
	if err != nil {
		t.Fatalf("CreateMatchmakingTicket: %v", err)
	}

	users := got.GetUserDefinitions()
	if len(users) != 1 {
		t.Fatalf("expected exactly one user definition, got %d", len(users))
	}
	if want := h.srv.userResourceName("liar"); users[0].GetUser() != want {
		t.Errorf("ticket belongs to %q, want the authenticated caller %q",
			users[0].GetUser(), want)
	}
}

// TestOnePlayerCannotTrackOrCancelAnothersTicket keeps a ticket private to the
// console that made it.
//
// NotFound rather than PermissionDenied, deliberately: whether somebody else's
// ticket exists is not this caller's to learn. The same rule internal/baas's
// requireSelf follows.
func TestOnePlayerCannotTrackOrCancelAnothersTicket(t *testing.T) {
	h := newHarness(t)
	h.createUser("owner", "device-owner")
	h.createUser("snoop", "device-snoop")

	owner := h.ctx(h.sessionFor(t, "owner", "device-owner"), "")
	snoop := h.ctx(h.sessionFor(t, "snoop", "device-snoop"), "")

	client := mmpb.NewMatchmakerClient(h.conn)
	ticket, err := client.CreateMatchmakingTicket(owner, ticketFor("salmon-run"))
	if err != nil {
		t.Fatalf("CreateMatchmakingTicket: %v", err)
	}

	stream, err := client.TrackMatchmakingTicket(snoop,
		&mmpb.TrackMatchmakingTicketRequest{Name: ticket.GetName()})
	if err != nil {
		t.Fatalf("TrackMatchmakingTicket: %v", err)
	}
	if _, err := stream.Recv(); status.Code(err) != codes.NotFound {
		t.Errorf("tracking another player's ticket: got %v, want NotFound", err)
	}

	_, err = client.CancelMatchmakingTicket(snoop,
		&mmpb.CancelMatchmakingTicketRequest{Name: ticket.GetName()})
	if status.Code(err) != codes.NotFound {
		t.Errorf("cancelling another player's ticket: got %v, want NotFound", err)
	}

	// And the ticket really is untouched, not merely refused loudly.
	if got, _ := h.srv.queue.Get(lastSegment(ticket.GetName())); got.State != 0 {
		t.Errorf("the ticket's state changed to %v", got.State)
	}
}

// TestAGameSessionIsNotReadableByAPlayerWhoIsNotInIt keeps the players list --
// which names other people -- from any authenticated caller who guesses an id.
func TestAGameSessionIsNotReadableByAPlayerWhoIsNotInIt(t *testing.T) {
	h := newHarness(t)
	h.createUser("in-one", "device-in-one")
	h.createUser("in-two", "device-in-two")
	h.createUser("outsider", "device-outsider")

	one := h.ctx(h.sessionFor(t, "in-one", "device-in-one"), "")
	two := h.ctx(h.sessionFor(t, "in-two", "device-in-two"), "")
	outsider := h.ctx(h.sessionFor(t, "outsider", "device-outsider"), "")

	client := mmpb.NewMatchmakerClient(h.conn)
	if _, err := client.CreateMatchmakingTicket(one, ticketFor("turf-war")); err != nil {
		t.Fatalf("CreateMatchmakingTicket (one): %v", err)
	}
	matched, err := client.CreateMatchmakingTicket(two, ticketFor("turf-war"))
	if err != nil {
		t.Fatalf("CreateMatchmakingTicket (two): %v", err)
	}
	name := matched.GetGameSession().GetName()
	if name == "" {
		t.Fatal("no session was formed")
	}

	sessions := mmpb.NewGameSessionServiceClient(h.conn)
	if _, err := sessions.GetGameSession(outsider,
		&mmpb.GetGameSessionRequest{Name: name}); status.Code(err) != codes.NotFound {
		t.Errorf("GetGameSession by an outsider: got %v, want NotFound", err)
	}
	if _, err := sessions.JoinGameSession(outsider,
		&mmpb.JoinGameSessionRequest{Name: name}); status.Code(err) != codes.NotFound {
		t.Errorf("JoinGameSession by an outsider: got %v, want NotFound", err)
	}

	// A player who WAS matched in can still join, so the refusal above is the
	// membership check and not the whole call being broken.
	joined, err := sessions.JoinGameSession(two, &mmpb.JoinGameSessionRequest{Name: name})
	if err != nil {
		t.Fatalf("JoinGameSession by a matched player: %v", err)
	}
	if len(joined.GetMatchedUserSessions()) != 2 {
		t.Errorf("matched user sessions = %d, want 2", len(joined.GetMatchedUserSessions()))
	}
}

// TestSyncGameSessionFromTheHostDoesNotMoveTheSessionEndpoint: the host may
// report a port, a joiner may read the session without host privileges, and
// neither changes where consoles open their gamesync session.
func TestSyncGameSessionFromTheHostDoesNotMoveTheSessionEndpoint(t *testing.T) {
	h := newHarness(t)
	h.createUser("host0000000000001", "device-host")
	h.createUser("joiner00000000001", "device-joiner")

	hostCtx := h.ctx(h.sessionFor(t, "host0000000000001", "device-host"), "")
	joinerCtx := h.ctx(h.sessionFor(t, "joiner00000000001", "device-joiner"), "")

	client := mmpb.NewMatchmakerClient(h.conn)
	if _, err := client.CreateMatchmakingTicket(hostCtx, ticketFor("turf-war")); err != nil {
		t.Fatalf("CreateMatchmakingTicket (host): %v", err)
	}
	matched, err := client.CreateMatchmakingTicket(joinerCtx, ticketFor("turf-war"))
	if err != nil {
		t.Fatalf("CreateMatchmakingTicket (joiner): %v", err)
	}
	name := matched.GetGameSession().GetName()

	sessions := mmpb.NewGameSessionServiceClient(h.conn)

	// A joiner reading before the host has published anything gets a
	// well-formed answer, not an error -- host privileges are not required
	// just to read.
	if _, err := sessions.SyncGameSession(joinerCtx, &mmpb.SyncGameSessionRequest{
		GameSession: &mmpb.GameSession{Name: name}, KeepAliveOnly: true,
	}); err != nil {
		t.Fatalf("SyncGameSession (joiner, keep-alive): %v", err)
	}

	if _, err := sessions.SyncGameSession(hostCtx, &mmpb.SyncGameSessionRequest{
		GameSession: &mmpb.GameSession{Name: name, Port: 45000},
	}); err != nil {
		t.Fatalf("SyncGameSession (host): %v", err)
	}

	got, err := sessions.GetGameSession(joinerCtx, &mmpb.GetGameSessionRequest{Name: name})
	if err != nil {
		t.Fatalf("GetGameSession (joiner, after sync): %v", err)
	}
	// The host's own port is recorded, but it does not redirect anybody:
	// every console's session channel still goes to the session server.
	if got.GetHost() != testSessionHost || got.GetPort() != 443 {
		t.Errorf("after the host's sync the session endpoint is %s:%d, want %s:443 unchanged",
			got.GetHost(), got.GetPort(), testSessionHost)
	}
}

// TestSyncGameSessionRefusesAPortUpdateFromANonHostPlayer is the ownership
// rule: a listening port belongs to whichever console actually opened it,
// and a non-host participant publishing one would tell joiners to dial the
// wrong machine.
func TestSyncGameSessionRefusesAPortUpdateFromANonHostPlayer(t *testing.T) {
	h := newHarness(t)
	h.createUser("host0000000000002", "device-host-2")
	h.createUser("joiner00000000002", "device-joiner-2")

	hostCtx := h.ctx(h.sessionFor(t, "host0000000000002", "device-host-2"), "")
	joinerCtx := h.ctx(h.sessionFor(t, "joiner00000000002", "device-joiner-2"), "")

	client := mmpb.NewMatchmakerClient(h.conn)
	if _, err := client.CreateMatchmakingTicket(hostCtx, ticketFor("turf-war")); err != nil {
		t.Fatalf("CreateMatchmakingTicket (host): %v", err)
	}
	matched, err := client.CreateMatchmakingTicket(joinerCtx, ticketFor("turf-war"))
	if err != nil {
		t.Fatalf("CreateMatchmakingTicket (joiner): %v", err)
	}
	name := matched.GetGameSession().GetName()

	sessions := mmpb.NewGameSessionServiceClient(h.conn)
	_, err = sessions.SyncGameSession(joinerCtx, &mmpb.SyncGameSessionRequest{
		GameSession: &mmpb.GameSession{Name: name, Port: 9999},
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("a non-host port update: got %v, want PermissionDenied", err)
	}

	// The refused update must not have taken effect.
	got, err := sessions.GetGameSession(hostCtx, &mmpb.GetGameSessionRequest{Name: name})
	if err != nil {
		t.Fatalf("GetGameSession: %v", err)
	}
	if got.GetPort() != 443 {
		t.Errorf("port = %d after a refused update, want the session server's 443", got.GetPort())
	}
}

// TestSyncGameSessionRefusesAStrangerTheSameWayGetGameSessionDoes matches
// GetGameSession/JoinGameSession's own rule: whether a session even exists is
// not a stranger's to learn.
func TestSyncGameSessionRefusesAStrangerTheSameWayGetGameSessionDoes(t *testing.T) {
	h := newHarness(t)
	h.createUser("host0000000000003", "device-host-3")
	h.createUser("joiner00000000003", "device-joiner-3")
	h.createUser("outsider000000003", "device-outsider-3")

	hostCtx := h.ctx(h.sessionFor(t, "host0000000000003", "device-host-3"), "")
	joinerCtx := h.ctx(h.sessionFor(t, "joiner00000000003", "device-joiner-3"), "")
	outsiderCtx := h.ctx(h.sessionFor(t, "outsider000000003", "device-outsider-3"), "")

	client := mmpb.NewMatchmakerClient(h.conn)
	if _, err := client.CreateMatchmakingTicket(hostCtx, ticketFor("turf-war")); err != nil {
		t.Fatalf("CreateMatchmakingTicket (host): %v", err)
	}
	matched, err := client.CreateMatchmakingTicket(joinerCtx, ticketFor("turf-war"))
	if err != nil {
		t.Fatalf("CreateMatchmakingTicket (joiner): %v", err)
	}
	name := matched.GetGameSession().GetName()

	sessions := mmpb.NewGameSessionServiceClient(h.conn)
	_, err = sessions.SyncGameSession(outsiderCtx, &mmpb.SyncGameSessionRequest{
		GameSession: &mmpb.GameSession{Name: name}, KeepAliveOnly: true,
	})
	if status.Code(err) != codes.NotFound {
		t.Errorf("SyncGameSession by an outsider: got %v, want NotFound", err)
	}
}

// TestAFormedMatchIsRecordedToHistory is the dashboard's match history
// source: a match forming because the queue filled, not because an operator
// forced it, must still show up -- and unforced, since nobody forced it.
func TestAFormedMatchIsRecordedToHistory(t *testing.T) {
	h := newHarness(t)
	h.createUser("history-one", "device-history-one")
	h.createUser("history-two", "device-history-two")

	one := h.ctx(h.sessionFor(t, "history-one", "device-history-one"), "")
	two := h.ctx(h.sessionFor(t, "history-two", "device-history-two"), "")
	client := mmpb.NewMatchmakerClient(h.conn)

	if _, err := client.CreateMatchmakingTicket(one, ticketFor("turf-war")); err != nil {
		t.Fatalf("CreateMatchmakingTicket (one): %v", err)
	}
	matched, err := client.CreateMatchmakingTicket(two, ticketFor("turf-war"))
	if err != nil {
		t.Fatalf("CreateMatchmakingTicket (two): %v", err)
	}
	if matched.GetState() != mmpb.MatchmakingTicket_SUCCEEDED {
		t.Fatalf("no match formed: %v", matched.GetState())
	}

	history, err := h.store.ListMatches(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListMatches: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("got %d matches in history, want 1", len(history))
	}
	if history[0].Forced {
		t.Error("a match that formed on its own is recorded as forced")
	}
	if history[0].Mode != "turf-war" {
		t.Errorf("mode = %q, want turf-war", history[0].Mode)
	}
	if len(history[0].PlayerIDs) != 2 {
		t.Errorf("player_ids = %v, want both players", history[0].PlayerIDs)
	}
}

// TestAllocateIceServerSetReturnsWhatWasConfigured covers TODO.md §1.2's
// config-only path, including the empty default.
//
// An empty set is a valid answer, not a failure: consoles on one LAN connect
// directly. 'switchnetctl check' is what warns about it, because the failure it
// causes on hardware -- the lobby fills and the match never starts -- reads as
// a matchmaking bug from this side.
func TestAllocateIceServerSetReturnsWhatWasConfigured(t *testing.T) {
	h := newHarness(t)
	h.createUser("player", "device")
	ctx := h.ctx(h.sessionFor(t, "player", "device"), "")

	got, err := mmpb.NewGameSessionServiceClient(h.conn).
		AllocateIceServerSet(ctx, &mmpb.AllocateIceServerSetRequest{})
	if err != nil {
		t.Fatalf("AllocateIceServerSet: %v", err)
	}
	if got.GetStunServer() != nil || len(got.GetTurnServers()) != 0 {
		t.Errorf("an unconfigured server offered ICE servers: %v", got)
	}
	if got.GetTtl().AsDuration() <= 0 {
		t.Error("ttl is not set; a zero reads as already stale")
	}

	// Now with servers configured.
	h.srv.iceServers = iceServersFrom([]config.IceServer{
		{Kind: "stun", Host: "10.0.0.5", Port: 3478},
		{Kind: "turn", Host: "10.0.0.5", Port: 3478, Protocol: "tcp",
			Username: "switchnet", Password: "secret"},
		// Dropped: no host. A half-specified server handed to a console is
		// worse than one not offered, because the console tries it.
		{Kind: "turn", Port: 3478},
	})

	got, err = mmpb.NewGameSessionServiceClient(h.conn).
		AllocateIceServerSet(ctx, &mmpb.AllocateIceServerSetRequest{})
	if err != nil {
		t.Fatalf("AllocateIceServerSet: %v", err)
	}
	if got.GetStunServer().GetHost() != "10.0.0.5" {
		t.Errorf("stun host = %q", got.GetStunServer().GetHost())
	}
	if got.GetStunServer().GetProtocol() != mmpb.StunServer_UDP {
		t.Errorf("an unset protocol should default to udp, got %v",
			got.GetStunServer().GetProtocol())
	}
	if len(got.GetTurnServers()) != 1 {
		t.Fatalf("turn servers = %d, want 1 (the incomplete one dropped)",
			len(got.GetTurnServers()))
	}
	turn := got.GetTurnServers()[0]
	if turn.GetProtocol() != mmpb.TurnServer_TCP {
		t.Errorf("turn protocol = %v, want TCP", turn.GetProtocol())
	}
	if turn.GetUsername() != "switchnet" || turn.GetPassword() != "secret" {
		t.Error("turn credentials were not passed through")
	}
}

// TestListLatencyMeasurementServersFallsBackToPublicIPWhenUnconfigured is the
// regression test for the crash-loop this RPC being Unimplemented caused on
// real hardware (2026-09-23): the console called it right after
// IssuePrearrangedUserToken, before matchmaking, and treated the refusal as a
// communication error, reloading the hall forever. Unlike AllocateIceServerSet,
// an empty answer here is not a valid "LAN only" default -- see the RPC's own
// doc comment -- so an unconfigured deployment must still get a non-empty list
// from network.public_ip.
func TestListLatencyMeasurementServersFallsBackToPublicIPWhenUnconfigured(t *testing.T) {
	h := newHarness(t)
	h.createUser("player", "device")
	ctx := h.ctx(h.sessionFor(t, "player", "device"), "")
	h.srv.publicIP = "203.0.113.9"

	got, err := mmpb.NewGameSessionServiceClient(h.conn).
		ListLatencyMeasurementServers(ctx, &mmpb.ListLatencyMeasurementServersRequest{})
	if err != nil {
		t.Fatalf("ListLatencyMeasurementServers: %v", err)
	}
	if len(got.GetLatencyMeasurementServers()) != 1 {
		t.Fatalf("servers = %d, want 1 (the public_ip fallback)", len(got.GetLatencyMeasurementServers()))
	}
	if got := got.GetLatencyMeasurementServers()[0].GetHost(); got != "203.0.113.9" {
		t.Errorf("host = %q, want the configured public_ip", got)
	}

	// Configuring a real stun server takes over from the fallback.
	h.srv.iceServers = iceServersFrom([]config.IceServer{
		{Kind: "stun", Host: "10.0.0.5", Port: 3478},
	})
	got, err = mmpb.NewGameSessionServiceClient(h.conn).
		ListLatencyMeasurementServers(ctx, &mmpb.ListLatencyMeasurementServersRequest{})
	if err != nil {
		t.Fatalf("ListLatencyMeasurementServers: %v", err)
	}
	if len(got.GetLatencyMeasurementServers()) != 1 {
		t.Fatalf("servers = %d, want 1 (the configured stun server)", len(got.GetLatencyMeasurementServers()))
	}
	if got := got.GetLatencyMeasurementServers()[0].GetHost(); got != "10.0.0.5" {
		t.Errorf("host = %q, want the configured stun server, not the public_ip fallback", got)
	}
}

func TestRegionalLatencyServersMatchReferenceResourceNames(t *testing.T) {
	h := newHarness(t)
	h.createUser("player", "device")
	h.srv.publicIP = "203.0.113.9"
	h.srv.regionalLatencyServers = true
	ctx := h.ctx(h.sessionFor(t, "player", "device"), "")

	got, err := mmpb.NewGameSessionServiceClient(h.conn).
		ListLatencyMeasurementServers(ctx, &mmpb.ListLatencyMeasurementServersRequest{})
	if err != nil {
		t.Fatalf("ListLatencyMeasurementServers: %v", err)
	}
	want := []struct{ id, region string }{
		{"eu-01-udp", "europe-central2"},
		{"eu-02-udp", "europe-west2"},
		{"us-01-udp", "northamerica-northeast1"},
		{"us-02-udp", "us-west2"},
		{"jp-01-udp", "asia-northeast1"},
		{"sg-01-udp", "asia-southeast1"},
		{"au-01-udp", "australia-southeast1"},
	}
	if len(got.GetLatencyMeasurementServers()) != len(want) {
		t.Fatalf("servers = %d, want %d", len(got.GetLatencyMeasurementServers()), len(want))
	}
	for i, server := range got.GetLatencyMeasurementServers() {
		if server.GetName() != h.srv.tenantPath()+"/latencyMeasurementServers/"+want[i].id {
			t.Errorf("server %d name = %q, want id %q", i, server.GetName(), want[i].id)
		}
		if server.GetRegion() != want[i].region {
			t.Errorf("server %d region = %q, want %q", i, server.GetRegion(), want[i].region)
		}
		if server.GetHost() != "203.0.113.9" || server.GetPort() != 3478 || server.GetProtocol() != mmpb.LatencyMeasurementServer_UDP {
			t.Errorf("server %d endpoint = %s:%d/%v, want 203.0.113.9:3478/UDP", i, server.GetHost(), server.GetPort(), server.GetProtocol())
		}
	}
}

// TestQueryGameSessionsAnswersEmptyRatherThanUnimplemented is the regression
// test for the failure NextendoNetwork/splatoon-3 measured on real hardware
// against a different NPLN title: a client that calls this RPC at all reads
// Unimplemented as a hard failure of whatever screen triggered the call, where
// an empty page reads as "nothing to show" and lets the console carry on.
// SwitchNet forms every session through matchmaking tickets and never has a
// public one to list, so the empty answer is also simply true here.
func TestQueryGameSessionsAnswersEmptyRatherThanUnimplemented(t *testing.T) {
	h := newHarness(t)
	h.createUser("browser", "device-browser")
	ctx := h.ctx(h.sessionFor(t, "browser", "device-browser"), "")

	got, err := mmpb.NewGameSessionServiceClient(h.conn).
		QueryGameSessions(ctx, &mmpb.QueryGameSessionsRequest{})
	if err != nil {
		t.Fatalf("QueryGameSessions: %v", err)
	}
	if len(got.GetGameSessions()) != 0 || got.GetNextPageToken() != "" {
		t.Errorf("expected an empty page, got %v", got)
	}
}

// TestMatchmakingRequiresAToken is the allowlist working: a service added later
// is authenticated unless somebody says otherwise.
func TestMatchmakingRequiresAToken(t *testing.T) {
	h := newHarness(t)

	_, err := mmpb.NewMatchmakerClient(h.conn).
		CreateMatchmakingTicket(h.ctx("", ""), ticketFor("turf-war"))
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("got %v, want Unauthenticated", err)
	}

	_, err = mmpb.NewGameSessionServiceClient(h.conn).
		AllocateIceServerSet(h.ctx("", ""), &mmpb.AllocateIceServerSetRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("got %v, want Unauthenticated", err)
	}

	_, err = mmpb.NewGameSessionServiceClient(h.conn).
		QueryGameSessions(h.ctx("", ""), &mmpb.QueryGameSessionsRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("got %v, want Unauthenticated", err)
	}
}

// ---- helpers --------------------------------------------------------------

func ticketFor(mode string) *mmpb.CreateMatchmakingTicketRequest {
	return &mmpb.CreateMatchmakingTicketRequest{
		MatchmakingTicket: &mmpb.MatchmakingTicket{MatchmakingConfig: mode},
	}
}

// awaitSucceeded reads a ticket stream until the ticket succeeds.
//
// Bounded by the test's own deadline rather than looping forever: a stream that
// never delivers is exactly the bug this is here to catch, and a hung test
// reports it far less clearly than a failed one.
// TestTrackMatchmakingTicketStaysOpenAfterSucceeded is the regression test
// for a real-hardware finding this project's own Nextendo-vs-SwitchNet
// comparison pass surfaced: closing this stream server-side the instant a
// SUCCEEDED ticket is sent was found (Nextendo, 2026-08-13) to race the
// console's own gRPC transport, dropping whatever it was about to send next
// on that same stream. The fix is to keep the stream open until the CLIENT
// closes it. This asserts the server-side half of that: Recv on a succeeded
// ticket's stream must not return (server has not closed it) until this
// test cancels its own context, at which point it must return promptly.
func TestTrackMatchmakingTicketStaysOpenAfterSucceeded(t *testing.T) {
	h := newHarness(t)
	h.createUser("player-one", "device-one")
	h.createUser("player-two", "device-two")

	oneCtx, cancelOne := context.WithCancel(h.ctx(h.sessionFor(t, "player-one", "device-one"), ""))
	defer cancelOne()
	two := h.ctx(h.sessionFor(t, "player-two", "device-two"), "")

	client := mmpb.NewMatchmakerClient(h.conn)

	first, err := client.CreateMatchmakingTicket(oneCtx, ticketFor("turf-war"))
	if err != nil {
		t.Fatalf("CreateMatchmakingTicket (one): %v", err)
	}
	streamOne, err := client.TrackMatchmakingTicket(oneCtx,
		&mmpb.TrackMatchmakingTicketRequest{Name: first.GetName()})
	if err != nil {
		t.Fatalf("TrackMatchmakingTicket (one): %v", err)
	}

	if _, err := client.CreateMatchmakingTicket(two, ticketFor("turf-war")); err != nil {
		t.Fatalf("CreateMatchmakingTicket (two): %v", err)
	}

	awaitSucceeded(t, streamOne)

	// The server must not have closed its end: a further Recv should not
	// return within a short window.
	recvDone := make(chan error, 1)
	go func() {
		_, err := streamOne.Recv()
		recvDone <- err
	}()
	select {
	case err := <-recvDone:
		t.Fatalf("stream closed itself right after SUCCEEDED (Recv returned %v), want it held open", err)
	case <-time.After(200 * time.Millisecond):
		// Expected: still open.
	}

	// Now the client closes its end -- the stream must end promptly.
	cancelOne()
	select {
	case <-recvDone:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not close after the client cancelled its context")
	}
}

func awaitSucceeded(
	t *testing.T, stream grpc_ClientStream,
) *mmpb.MatchmakingTicket {
	t.Helper()

	done := make(chan *mmpb.MatchmakingTicket, 1)
	errc := make(chan error, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				errc <- err
				return
			}
			if msg.GetState() == mmpb.MatchmakingTicket_SUCCEEDED {
				done <- msg
				return
			}
		}
	}()

	select {
	case msg := <-done:
		return msg
	case err := <-errc:
		if err == io.EOF {
			t.Fatal("the ticket stream ended without the ticket succeeding")
		}
		t.Fatalf("ticket stream: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the ticket to succeed")
	}
	return nil
}

// grpc_ClientStream is the little of a ticket stream these tests use.
type grpc_ClientStream interface {
	Recv() (*mmpb.MatchmakingTicket, error)
}

// TestStreamingResponsesCarryTheirNPLNGRPCType: Nintendo's gateway stamps
// npln-grpc-type on every response, streams included (ServerStreaming,
// BidirectionalStreaming), per a 704-response capture of the real tenant host.
// Unary calls always had it here; streams had nothing.
func TestStreamingResponsesCarryTheirNPLNGRPCType(t *testing.T) {
	h := newHarness(t)
	h.createUser("stream-one", "device-stream-one")
	ctx := h.ctx(h.sessionFor(t, "stream-one", "device-stream-one"), "")

	client := mmpb.NewMatchmakerClient(h.conn)
	ticket, err := client.CreateMatchmakingTicket(ctx, ticketFor("turf-war"))
	if err != nil {
		t.Fatalf("CreateMatchmakingTicket: %v", err)
	}
	stream, err := client.TrackMatchmakingTicket(ctx,
		&mmpb.TrackMatchmakingTicketRequest{Name: ticket.GetName()})
	if err != nil {
		t.Fatalf("TrackMatchmakingTicket: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("first update: %v", err)
	}
	md, err := stream.Header()
	if err != nil {
		t.Fatalf("header: %v", err)
	}
	if got := md.Get("npln-grpc-type"); len(got) != 1 || got[0] != "ServerStreaming" {
		t.Errorf("npln-grpc-type = %v, want [ServerStreaming]", got)
	}
}
