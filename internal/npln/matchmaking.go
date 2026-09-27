package npln

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/n-popescu/switchnet/config"
	"github.com/n-popescu/switchnet/internal/db"
	"github.com/n-popescu/switchnet/internal/matchmaking"
	mmpb "github.com/n-popescu/switchnet/proto/matchmaking/v1"
)

// ---- nn.npln.matchmaking.v1.Matchmaker ------------------------------------

type matchmakerService struct {
	mmpb.UnimplementedMatchmakerServer
	s *Server
}

// CreateMatchmakingTicket puts the caller in the queue.
//
// The ticket is created for the AUTHENTICATED caller, never for the user the
// request names. A request's UserDefinitions are the client saying who it
// believes it is; the token is what proves it. Accepting the former would let
// any authenticated console queue as anybody else, which is the same class of
// bug as the device-account fallback internal/baas is shaped around.
//
// TODO(capture-needed): whether Splatoon 3 ever sends more than one
// UserDefinition here. NPLN supports delegation -- one console queuing on
// behalf of a party, carrying UserDelegationTokens -- and if the game uses it
// for a squad, everyone but the leader is currently dropped. SwitchNet now
// issues delegation tokens (delegation.go) but does not yet read them here:
// nothing shows how a squad's ticket carries its members, and placing players
// on one team from a guess is how a squad ends up split or stuck. A capture
// of a squad entering matchmaking would settle it.
//
// A 2026-09-20 decompile of Splatoon 3 v11.3.0's own main confirms
// GameSessionService.IssueUserDelegationToken is a real, compiled-in RPC the
// client links against (see internal/npln/README.md's "confirmed RPC
// surface" table) -- so the mechanism this TODO worries about does exist on
// the wire. That is not proof the game calls it outside a squad flow; only a
// capture of one would be.
// Change: this function, and Queue.Enqueue's one-ticket-per-player rule.
func (m *matchmakerService) CreateMatchmakingTicket(
	ctx context.Context, req *mmpb.CreateMatchmakingTicketRequest,
) (*mmpb.MatchmakingTicket, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}

	// The mode is the one thing here that IS the client's to choose: it is what
	// they want to play, not who they are.
	mode := req.GetMatchmakingTicket().GetMatchmakingConfig()
	if mode == "" {
		// A ticket with no mode would match against every other modeless
		// ticket, which is every mode at once. Refusing is better than forming
		// a match nobody asked for.
		m.s.log.Debug("npln: matchmaking ticket with no config", "user", caller.UserID)
		return nil, errMalformed(m.s.env)
	}

	ticket, sessions, err := m.s.queue.Enqueue(caller.UserID, mode)
	if err != nil {
		m.s.log.Error("npln: enqueue", "user", caller.UserID, "error", err)
		return nil, errMalformed(m.s.env)
	}
	for _, sess := range sessions {
		m.s.log.Info("npln: match formed",
			"session", sess.ID, "mode", sess.Mode,
			"players", len(sess.Players), "host", sess.Host)
		m.s.recordMatch(ctx, sess, false)
	}

	// Always answer SEARCHING, with no session, even when this ticket completed a match on
	// arrival. The console reads its result from TrackMatchmakingTicket, which it only opens
	// after a SEARCHING answer: given SUCCEEDED here (2026-09-27 hardware, the second of two
	// players) it never tracked the ticket, never called AllocateIceServerSet, and the match
	// never started. Nintendo's CreateMatchmakingTicket answers SEARCHING and the result
	// arrives on Track (NextendoNetwork's capture notes, facts only). Track starts with the
	// ticket's current state, so a ticket matched here is SUCCEEDED on its first frame.
	searching := ticket
	searching.State = matchmaking.StateSearching
	searching.SessionID = ""
	proto, err := m.s.ticketProto(searching)
	if err != nil {
		return nil, err
	}
	return proto, nil
}

// recordMatch persists a formed session to the match history, for the
// dashboard. Best-effort: a match already formed in the live queue either
// way, and failing the caller's request over a history write it did not ask
// for would turn a cosmetic gap into a dropped ticket.
func (s *Server) recordMatch(ctx context.Context, sess matchmaking.Session, forced bool) {
	err := s.store.CreateMatch(ctx, db.Match{
		ID: sess.ID, Tenant: s.tenant, Mode: sess.Mode,
		HostUserID: sess.Host, PlayerIDs: sess.Players,
		Forced: forced, CreatedAt: sess.CreatedAt,
	})
	if err != nil {
		s.log.Warn("npln: record match history", "session", sess.ID, "error", err)
	}
}

// TrackMatchmakingTicket streams a ticket until it reaches a terminal state.
//
// The console lives on this stream: it opens one after creating a ticket and
// reads its own progress off it, so closing it early is how a player ends up
// staring at a spinner. It returns only when the ticket is finished one way or
// another, or when the console itself goes away.
func (m *matchmakerService) TrackMatchmakingTicket(
	req *mmpb.TrackMatchmakingTicketRequest,
	stream grpc.ServerStreamingServer[mmpb.MatchmakingTicket],
) error {
	ctx := stream.Context()
	caller, ok := CallerFrom(ctx)
	if !ok {
		return ErrUnauthenticated
	}

	id := lastSegment(req.GetName())
	ticket, found := m.s.queue.Get(id)
	if !found {
		return ErrNotFound
	}
	// Somebody else's ticket is NotFound rather than PermissionDenied: whether
	// a given ticket exists is not this caller's to learn. Same rule as
	// internal/baas's requireSelf.
	if ticket.UserID != caller.UserID {
		m.s.log.Warn("npln: ticket tracked by another user",
			"ticket", id, "owner", ticket.UserID, "caller", caller.UserID)
		return ErrNotFound
	}

	// Watch sends the current state immediately, so a stream that attaches
	// after the ticket already succeeded does not wait for a change that has
	// already happened.
	updates, stop := m.s.queue.Watch(id)
	defer stop()

	for {
		select {
		case <-ctx.Done():
			// The console hung up, or the whole server is shutting down.
			return ctx.Err()
		case t, open := <-updates:
			if !open {
				return nil
			}
			proto, err := m.s.ticketProto(t)
			if err != nil {
				return err
			}
			if err := stream.Send(proto); err != nil {
				return err
			}
			if t.State == matchmaking.StateSucceeded {
				// Do NOT return here. A real-hardware finding (Nextendo,
				// 2026-08-13, cited in this project's own Nextendo-vs-
				// SwitchNet comparison pass) found that closing this stream
				// server-side the instant SUCCEEDED is sent races the
				// console's own gRPC transport: its poll loop had nothing
				// queued to send yet (still read-only) when the server hung
				// up, so IssueToken/KeepUserSession -- whatever the console
				// was about to send on this same session -- never left the
				// transport before the socket was torn down roughly 1.2s
				// later. Same symptom class as matchedUserSessions' own
				// empty-MatchmakingIdToken bug this project already fixed
				// (2321-4992): the console's session-channel request dies
				// before it is ever sent. Blocking here until the CONSOLE
				// closes its end, instead of the server closing it first,
				// is what Nextendo's fix does. TIMED_OUT and CANCELLED keep
				// the old eager-close behaviour below -- Nextendo's finding
				// was specific to SUCCEEDED, and nothing confirms the same
				// race applies to a ticket that never matched.
				<-ctx.Done()
				return ctx.Err()
			}
			if terminal(t.State) {
				return nil
			}
		}
	}
}

// CancelMatchmakingTicket withdraws the caller's ticket.
//
// Cancelling something that already matched is deliberately not an error: the
// console may have sent it before the success arrived on its stream, and the
// answer to that race is the ticket's own state, which the stream already
// carries.
func (m *matchmakerService) CancelMatchmakingTicket(
	ctx context.Context, req *mmpb.CancelMatchmakingTicketRequest,
) (*emptypb.Empty, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}

	id := lastSegment(req.GetName())
	ticket, found := m.s.queue.Get(id)
	if !found {
		return nil, ErrNotFound
	}
	if ticket.UserID != caller.UserID {
		m.s.log.Warn("npln: ticket cancelled by another user",
			"ticket", id, "owner", ticket.UserID, "caller", caller.UserID)
		return nil, ErrNotFound
	}

	m.s.queue.Cancel(id)
	return &emptypb.Empty{}, nil
}

// ---- nn.npln.matchmaking.v1.GameSessionService ----------------------------

type gameSessionService struct {
	mmpb.UnimplementedGameSessionServiceServer
	s *Server
}

// GetGameSession returns a match the caller was placed into, or a room.
//
// A match only to its own players. A session id is not a secret worth much,
// but the players list inside one names other people, and handing that to any
// authenticated caller who guesses an id is a leak for no gain.
//
// A room to anybody who names it, because a joiner reads it before entering:
// the join screen shows the host and "1/10" from this answer. The room's
// random id is the capability here, the same one JoinGameSession accepts;
// anyone who has it was given it by the room's code or a friend's presence.
func (g *gameSessionService) GetGameSession(
	ctx context.Context, req *mmpb.GetGameSessionRequest,
) (*mmpb.GameSession, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}

	id := lastSegment(req.GetName())
	sess, found := g.s.queue.Session(id)
	if !found {
		return nil, ErrNotFound
	}
	if !sess.Room && !g.s.queue.IsPlayer(id, caller.UserID) {
		return nil, ErrNotFound
	}
	if sess.Room {
		g.s.queue.TouchRoom(id)
	}
	return g.s.sessionProto(sess), nil
}

// BatchGetGameSessions is GetGameSession, batched.
//
// Confirmed real and linked by the same 2026-09-20 Splatoon 3 v11.3.0
// decompile as every other RPC in this file's "confirmed RPC surface"
// section (see internal/npln/README.md); unlike most of that list, this one
// needs no new server-side concept to answer -- it is exactly
// GetGameSession's own authorization and lookup, once per requested name.
//
// A name the caller cannot see (wrong session, or one that does not exist)
// is silently omitted rather than failing the whole batch: the same
// "partial visibility is not an error" reasoning ListFriendUsers already
// applies when a friendship names a since-deleted user. Request.View is
// ignored -- nothing here distinguishes detail levels, so every session
// already comes back at full detail regardless of what a caller asked for.
func (g *gameSessionService) BatchGetGameSessions(
	ctx context.Context, req *mmpb.BatchGetGameSessionsRequest,
) (*mmpb.BatchGetGameSessionsResponse, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}

	out := make([]*mmpb.GameSession, 0, len(req.GetNames()))
	for _, name := range req.GetNames() {
		id := lastSegment(name)
		if !g.s.queue.IsPlayer(id, caller.UserID) {
			continue
		}
		sess, found := g.s.queue.Session(id)
		if !found {
			continue
		}
		out = append(out, g.s.sessionProto(sess))
	}
	return &mmpb.BatchGetGameSessionsResponse{GameSessions: out}, nil
}

// QueryGameSessions answers a browse-style search for public sessions.
//
// Always empty. Matches are never public, and a room -- though marked
// public, as a real server marks even a private one -- is reached by its code
// or through a friend's presence, never by browsing, as far as anything shows
// Splatoon 3 doing. An empty page rather than leaving this Unimplemented
// still matters: a real NPLN client that calls it at all reads Unimplemented as
// a hard failure of whatever screen triggered the call, where an empty result
// set reads as "nothing to show" -- which is always true here.
//
// TODO(capture-needed): whether Splatoon 3 ever calls QueryGameSessions, and
// for what -- a private-battle room browser is the plausible candidate, since
// ticket-based matchmaking never needs one. A capture of that flow would
// settle it. Change: this function, and sessionProto's IsPublic if the answer
// is that some SwitchNet-formed sessions should be browsable after all.
func (g *gameSessionService) QueryGameSessions(
	ctx context.Context, _ *mmpb.QueryGameSessionsRequest,
) (*mmpb.QueryGameSessionsResponse, error) {
	if _, ok := CallerFrom(ctx); !ok {
		return nil, ErrUnauthenticated
	}
	return &mmpb.QueryGameSessionsResponse{}, nil
}

// JoinGameSession confirms the caller into a match they were matched into, or
// puts them into a room.
//
// For a match it is an acknowledgement rather than a mutation: the players are
// known from the moment it formed. For a room it adds the caller -- see
// joinRoom for who may. Joining a match you were NOT matched into is backfill,
// which is not implemented and not documented, so it is refused rather than
// invented.
func (g *gameSessionService) JoinGameSession(
	ctx context.Context, req *mmpb.JoinGameSessionRequest,
) (*mmpb.JoinGameSessionResponse, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}

	id := lastSegment(req.GetName())
	sess, found := g.s.queue.Session(id)
	if !found {
		return nil, ErrNotFound
	}
	if sess.Room {
		return g.s.joinRoom(ctx, sess, req, caller.UserID)
	}
	if !g.s.queue.IsPlayer(id, caller.UserID) {
		g.s.log.Warn("npln: join by a user who was not matched into the session",
			"session", id, "caller", caller.UserID)
		return nil, ErrNotFound
	}

	matched, err := g.s.matchedUserSessions(sess)
	if err != nil {
		return nil, err
	}
	return &mmpb.JoinGameSessionResponse{
		GameSession:         g.s.sessionProto(sess),
		MatchedUserSessions: matched,
	}, nil
}

// SyncGameSession is how the host publishes its reachable port, and how any
// matched player -- host or joiner -- reads the session's current
// participants back.
//
// TODO(capture-needed): whether Splatoon 3 dials GameSession.Port directly at
// all. Gameplay is peer-to-peer over ICE (see AllocateIceServerSet), and a
// real client that connects entirely through negotiated ICE candidates may
// never read this field. Persisting and returning whatever the host sends is
// the defensible reading either way -- a field the client ignores costs
// nothing, where refusing to store it risks dropping something a real client
// does depend on -- but nothing here confirms which. A capture of a match
// actually connecting would settle it. Change: this function and
// matchmaking.Session's HostPort field.
//
// A 2026-09-20 decompile of Splatoon 3 v11.3.0's own main leans further
// toward "never": every other GameSessionService RPC this server implements
// appears as a literal path string in the binary (proof this client links
// against it), but "/nn.npln.matchmaking.v1.GameSessionService/SyncGameSession"
// does not, despite SyncGameSession being defined in the same upstream proto
// set as the rest of the service. That is evidence this specific game never
// calls it, not proof the RPC itself is fictional or wrong -- other NPLN
// titles may use it, and a string absent from .rodata is consistent with but
// does not prove "never linked, never called". Still short of a capture.
//
// Only the port moves through here. Everything else GameSession carries
// (participant counts, state, who the host is) is already authoritative on
// this server; a client-supplied value for any of those is ignored rather
// than trusted, the same reasoning AllocateIceServerSet's doc comment gives
// for ignoring the request's own tenant and user fields. A listening port is
// different: it is the one piece of session state that genuinely lives on
// the host's console and nowhere else, so this is the one field this server
// has no other way to learn.
//
// KeepAliveOnly is honoured as a read: any matched player may call this to
// read the current session back without needing host privileges to do so --
// useful for a joiner checking whether the host has published a port yet.
func (g *gameSessionService) SyncGameSession(
	ctx context.Context, req *mmpb.SyncGameSessionRequest,
) (*mmpb.SyncGameSessionResponse, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}

	id := lastSegment(req.GetGameSession().GetName())
	if !g.s.queue.IsPlayer(id, caller.UserID) {
		return nil, ErrNotFound
	}

	sess, found := g.s.queue.Session(id)
	if !found {
		return nil, ErrNotFound
	}

	if !req.GetKeepAliveOnly() {
		if caller.UserID != sess.Host {
			// The caller is a real participant -- IsPlayer already confirmed
			// that -- so refusing this is a permission problem, not a "does
			// this exist" one, unlike JoinGameSession's stranger case.
			g.s.log.Warn("npln: SyncGameSession update attempted by a non-host participant",
				"session", id, "host", sess.Host, "caller", caller.UserID)
			return nil, ErrPermissionDenied
		}
		updated, found := g.s.queue.SetHostPort(id, req.GetGameSession().GetPort())
		if !found {
			// The session aged out between the two lookups above.
			return nil, ErrNotFound
		}
		sess = updated
		g.s.log.Info("npln: host published its game session port",
			"session", id, "host", sess.Host, "port", sess.HostPort)
	}

	return &mmpb.SyncGameSessionResponse{
		UserSessions: g.s.userSessions(sess),
	}, nil
}

// AllocateIceServerSet hands the console the STUN/TURN servers to use.
//
// Splatoon 3's gameplay is peer-to-peer and it uses ICE rather than the Pia
// NAT-check pair, so these addresses are how two consoles behind different NATs
// find a path to each other. They must be reachable FROM THE CONSOLE.
//
// An empty set is a valid answer and the default one: consoles on the same LAN
// connect directly, which is the whole of what a small private network needs.
// 'switchnetctl check' warns when it is empty, because the failure it causes
// elsewhere -- the lobby fills and the match never starts -- reads as a
// matchmaking bug rather than a connectivity one.
func (g *gameSessionService) AllocateIceServerSet(
	ctx context.Context, _ *mmpb.AllocateIceServerSetRequest,
) (*mmpb.IceServerSet, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}

	// The request names a tenant and a user. Both are ignored: the tenant was
	// already checked against this server's own on the way in, and the user is
	// the caller's token, not the request's field.
	set := &mmpb.IceServerSet{
		Name: g.s.tenantPath() + "/users/" + caller.UserID + "/iceServerSet",
		Ttl:  durationpb.New(iceServerSetTTL),
		// Cached for the same span it is valid for. A console that re-asks
		// every match adds a round trip to something that has not changed since
		// the operator last edited their configuration file.
		ClientCacheDuration: durationpb.New(iceServerSetTTL),
		UpdateTime:          timestamppb.New(g.s.now()),
	}

	for _, cfgServer := range g.s.iceServers {
		switch cfgServer.Kind {
		case "stun":
			set.StunServer = &mmpb.StunServer{
				Host:     cfgServer.Host,
				Port:     int32(cfgServer.Port),
				Protocol: stunProtocol(cfgServer.Protocol),
			}
		case "turn":
			set.TurnServers = append(set.TurnServers, &mmpb.TurnServer{
				Host:     cfgServer.Host,
				Port:     int32(cfgServer.Port),
				Protocol: turnProtocol(cfgServer.Protocol),
				Username: cfgServer.Username,
				Password: cfgServer.Password,
			})
		}
	}
	return set, nil
}

// ListLatencyMeasurementServers hands the console what it pings, before
// matchmaking even starts, to pick which of several server regions to
// search in.
//
// Splatoon 3 calls this right after IssuePrearrangedUserToken -- confirmed by
// a hardware capture of a real console (2026-09-23), where it was the very
// next RPC and the one this server did not yet implement, refused
// Unimplemented, and the console read that as a communication error and
// retried the hall forever rather than surfacing a Nintendo error code.
// NextendoNetwork/splatoon-3's own hardware capture (facts only, not its
// code) confirms the shape of the fix: the console needs a NON-EMPTY list to
// proceed past this screen at all -- an empty one aborts earlier than a
// list whose pings simply time out, which the console tolerates fine. So,
// unlike AllocateIceServerSet just above (where an empty ICE set is a valid
// answer for a LAN-only deployment), this answers with the operator's own
// configured ICE servers reused as latency targets rather than an empty
// list: real addresses this deployment already advertises as reachable from
// a console, not an invented Nintendo region table.
//
// TODO(capture-needed): whether the console actually requires a *successful*
// ping to at least one server, rather than merely a non-empty list. If it
// does, an operator with no ice_servers configured (the LAN-only default)
// would need a real answer here too -- change this function.
//
// Corroborating (not resolving) evidence: this project's own Nextendo-vs-
// SwitchNet comparison pass found Nextendo's own handler comment states a
// non-empty list is what lets the console proceed, and that the pings
// themselves timing out does not stop it -- only an empty list aborts
// early. That is Nextendo's own testing note, not a decoded wire capture of
// Nintendo's real server, so it does not on its own meet this project's own
// bar for removing this TODO (see CLAUDE.md rule 6) -- but it is a second,
// independent signal the current non-empty-list answer is already correct
// and this function likely needs no change.
func (g *gameSessionService) ListLatencyMeasurementServers(
	ctx context.Context, _ *mmpb.ListLatencyMeasurementServersRequest,
) (*mmpb.ListLatencyMeasurementServersResponse, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}

	out := &mmpb.ListLatencyMeasurementServersResponse{}
	if g.s.regionalLatencyServers {
		host, port, protocol, ok := g.latencyTarget()
		if ok {
			for _, region := range latencyServerRegions {
				out.LatencyMeasurementServers = append(out.LatencyMeasurementServers, &mmpb.LatencyMeasurementServer{
					Name:     g.s.tenantPath() + "/latencyMeasurementServers/" + region.id,
					Region:   region.region,
					Host:     host,
					Port:     port,
					Protocol: protocol,
				})
			}
		}
	} else {
		for i, cfgServer := range g.s.iceServers {
			if cfgServer.Kind != "stun" {
				continue
			}
			out.LatencyMeasurementServers = append(out.LatencyMeasurementServers, &mmpb.LatencyMeasurementServer{
				Name:     g.s.tenantPath() + "/latencyMeasurementServers/" + latencyServerID(i),
				Region:   latencyServerRegion,
				Host:     cfgServer.Host,
				Port:     int32(cfgServer.Port),
				Protocol: latencyProtocol(cfgServer.Protocol),
			})
		}

		// No stun entries -- the LAN-only default AllocateIceServerSet documents
		// as fine for ICE, but which this RPC's own doc comment says the console
		// does not tolerate as an empty list. network.public_ip is already this
		// deployment's one required "the console can reach this" address (natcheck
		// advertises it the same way), so it is the fallback target here rather
		// than leaving the list empty.
		if len(out.LatencyMeasurementServers) == 0 && g.s.publicIP != "" {
			out.LatencyMeasurementServers = append(out.LatencyMeasurementServers, &mmpb.LatencyMeasurementServer{
				Name:     g.s.tenantPath() + "/latencyMeasurementServers/" + latencyServerID(0),
				Region:   latencyServerRegion,
				Host:     g.s.publicIP,
				Port:     3478,
				Protocol: mmpb.LatencyMeasurementServer_UDP,
			})
		}
	}
	g.s.log.Info("npln: answered ListLatencyMeasurementServers",
		"user", caller.UserID, "servers", len(out.LatencyMeasurementServers))
	return out, nil
}

// latencyServerRegions are the resource ids and region labels observed in the
// working reference implementation. The experiment changes list shape while
// keeping every address and port pointed at this deployment's one reachable
// endpoint.
const latencyServerRegion = "europe-west2"

var latencyServerRegions = []struct{ id, region string }{
	{"eu-01-udp", "europe-central2"},
	{"eu-02-udp", "europe-west2"},
	{"us-01-udp", "northamerica-northeast1"},
	{"us-02-udp", "us-west2"},
	{"jp-01-udp", "asia-northeast1"},
	{"sg-01-udp", "asia-southeast1"},
	{"au-01-udp", "australia-southeast1"},
}

// latencyTarget chooses the one endpoint repeated for the regional-list
// experiment. It reflects measured server behavior without claiming there are
// seven physical latency servers in this deployment.
func (g *gameSessionService) latencyTarget() (string, int32, mmpb.LatencyMeasurementServer_Protocol, bool) {
	for _, cfgServer := range g.s.iceServers {
		if cfgServer.Kind == "stun" {
			return cfgServer.Host, int32(cfgServer.Port), latencyProtocol(cfgServer.Protocol), true
		}
	}
	if g.s.publicIP != "" {
		return g.s.publicIP, 3478, mmpb.LatencyMeasurementServer_UDP, true
	}
	return "", 0, mmpb.LatencyMeasurementServer_UDP, false
}

// latencyServerID renders a dot-free slug for the resource name's last
// segment -- an IP address there would put dots where the console's
// resource-name parser does not expect them (the same class of strictness
// ugcstore's README documents for document names).
func latencyServerID(i int) string {
	return fmt.Sprintf("switchnet-%02d-udp", i+1)
}

func latencyProtocol(name string) mmpb.LatencyMeasurementServer_Protocol {
	switch name {
	case "tcp", "tls":
		return mmpb.LatencyMeasurementServer_TCP
	default:
		return mmpb.LatencyMeasurementServer_UDP
	}
}

// ListUserSessions returns every matched player's session for one game
// session -- the same list GetGameSession already embeds in
// GameSession.UserSessions, as its own paginated resource.
//
// Confirmed real and linked by the same Splatoon 3 v11.3.0 decompile as
// BatchGetGameSessions above. Pagination is not implemented: a game session
// here is exactly as large as its match size (SwitchNet's own
// matchmaking.Session), which is small enough that a second page never
// exists to ask for. PageSize/PageToken are accepted and ignored rather than
// answered with an error, the same reasoning ListFriendUsers gives for
// capping at one page instead of refusing a request for more.
func (g *gameSessionService) ListUserSessions(
	ctx context.Context, req *mmpb.ListUserSessionsRequest,
) (*mmpb.ListUserSessionsResponse, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}

	id := lastSegment(req.GetParent())
	if !g.s.queue.IsPlayer(id, caller.UserID) {
		return nil, ErrNotFound
	}
	sess, found := g.s.queue.Session(id)
	if !found {
		return nil, ErrNotFound
	}
	return &mmpb.ListUserSessionsResponse{UserSessions: g.s.userSessions(sess)}, nil
}

// GetUserSession returns one matched player's session within a game session.
//
// Confirmed real and linked by the same decompile as ListUserSessions above.
// Built from the same userSessions() list rather than a separate lookup, so
// the two RPCs can never disagree about one player's session.
func (g *gameSessionService) GetUserSession(
	ctx context.Context, req *mmpb.GetUserSessionRequest,
) (*mmpb.UserSession, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}

	name := req.GetName()
	id := gameSessionIDFromPath(name)
	if !g.s.queue.IsPlayer(id, caller.UserID) {
		return nil, ErrNotFound
	}
	sess, found := g.s.queue.Session(id)
	if !found {
		return nil, ErrNotFound
	}
	target := lastSegment(name)
	for _, us := range g.s.userSessions(sess) {
		if lastSegment(us.GetName()) == target {
			return us, nil
		}
	}
	return nil, ErrNotFound
}

// iceServerSetTTL is how long an allocated ICE server set is good for.
//
// SwitchNet's set does not actually expire -- it is read straight from a
// configuration file and changes only when the operator edits one -- but the
// field is not optional and a zero would read as "already stale". An hour is
// long enough not to churn and short enough that an edit takes effect within
// one session.
const iceServerSetTTL = time.Hour

func stunProtocol(name string) mmpb.StunServer_Protocol {
	switch name {
	case "tcp":
		return mmpb.StunServer_TCP
	case "tls":
		return mmpb.StunServer_TLS
	default:
		return mmpb.StunServer_UDP
	}
}

func turnProtocol(name string) mmpb.TurnServer_Protocol {
	switch name {
	case "tcp":
		return mmpb.TurnServer_TCP
	case "tls":
		return mmpb.TurnServer_TLS
	default:
		return mmpb.TurnServer_UDP
	}
}

// ---- rendering ------------------------------------------------------------

// tenantPath is the resource-name prefix every matchmaking resource hangs off.
func (s *Server) tenantPath() string { return "tenants/" + s.tenant }

// ticketProto renders a queue ticket as the wire message.
//
// The resource-name shape is confirmed, not guessed: a 2026-09-20 decompile
// of Splatoon 3 v11.3.0 found the function that BUILDS a ticket resource
// name (string-concatenating "tenants" + "/" + tenant + "/matchmakingTickets"
// + "/" + id) and, separately, the function that PARSES one back apart
// looking for that exact "tenants/<t>/matchmakingTickets/<id>" segment
// sequence via literal comparison against "tenants" and "matchmakingTickets".
// Round-trip agreement in the client's own code, not just one side of it.
// The same evidence exists for "gameSessions" (see sessionProto). lastSegment
// still parses leniently regardless, since a wrong prefix elsewhere in this
// package would otherwise cost a cosmetic mismatch instead of a broken Track
// or Cancel -- this confirmation does not change that defensive choice.
func (s *Server) ticketProto(t matchmaking.Ticket) (*mmpb.MatchmakingTicket, error) {
	out := &mmpb.MatchmakingTicket{
		Name:              s.tenantPath() + "/matchmakingTickets/" + t.ID,
		MatchmakingConfig: t.Mode,
		State:             ticketState(t.State),
		UserDefinitions: []*mmpb.UserDefinition{
			{User: s.userResourceName(t.UserID)},
		},
	}

	if t.SessionID == "" {
		return out, nil
	}
	sess, ok := s.queue.Session(t.SessionID)
	if !ok {
		// The session aged out from under a ticket somebody is still reading.
		// The state is still the truth, so the ticket is worth sending without
		// it rather than failing the stream.
		return out, nil
	}
	out.GameSession = s.sessionProto(sess)
	matched, err := s.matchedUserSessions(sess)
	if err != nil {
		// The session exists and is named, but no player can open it without a
		// token. Sending the ticket without them would be the 2321-4992 shape
		// with extra steps, so the call fails instead.
		return nil, err
	}
	out.MatchedUserSessions = matched
	return out, nil
}

// sessionProto renders a formed match or a room as the wire message.
func (s *Server) sessionProto(sess matchmaking.Session) *mmpb.GameSession {
	if sess.Room {
		return s.roomProto(sess)
	}
	return &mmpb.GameSession{
		Name:                s.tenantPath() + "/gameSessions/" + sess.ID,
		MaxParticipantCount: int32(len(sess.Players)),
		// Everyone matched is a participant. There is no lobby to fill here:
		// the session is created at the moment it is already full.
		CurrentParticipantCount: int32(len(sess.Players)),
		// This project's own Nextendo-vs-SwitchNet comparison pass found
		// Nextendo sets CanParticipate true on every GameSession it builds,
		// with no exception for an already-full ticket match -- this used to
		// be false here with no capture or comment backing that choice, an
		// unexamined default rather than a considered one. Matching
		// Nextendo's real-hardware-tested behaviour until a capture says
		// otherwise.
		CanParticipate: true,
		IsPublic:       false,
		State:          mmpb.GameSession_ACTIVE,
		// Host and Port are the address each console opens its gamesync
		// session to for this match -- a server, not a player. This used to
		// name the host player ("users/<id>") with the port it published via
		// SyncGameSession, a reading nothing on the wire supported; a console
		// given a user name to dial opens no session and reports 2321-4992.
		// The working reference (NextendoNetwork/splatoon-3) announces its
		// session server here, and measured that the tenant name on :443 is
		// the form that works. See config.NPLN.SessionHost.
		Host:         s.sessionHost,
		Port:         s.sessionPort,
		CreateTime:   timestamppb.New(sess.CreatedAt),
		UserSessions: s.userSessions(sess),
	}
}

// roomProto renders a room. Unlike a match, a room is not full when it
// exists: capacity is the mode's size and the count is whoever is in it now,
// derived from the player list rather than counted up, so a player retrying a
// join never inflates it. is_public is true even for a private room, as in
// the capture of one.
func (s *Server) roomProto(sess matchmaking.Session) *mmpb.GameSession {
	meta, _ := s.roomMetaFor(sess.ID)
	return &mmpb.GameSession{
		Name:                    s.tenantPath() + "/gameSessions/" + sess.ID,
		MaxParticipantCount:     int32(sess.MaxPlayers),
		CurrentParticipantCount: int32(len(sess.Players)),
		CanParticipate:          len(sess.Players) < sess.MaxPlayers,
		IsPublic:                true,
		State:                   mmpb.GameSession_ACTIVE,
		Host:                    s.sessionHost,
		Port:                    s.sessionPort,
		CreateTime:              timestamppb.New(sess.CreatedAt),
		Properties:              meta.properties,
		UserSessions:            s.userSessions(sess),
	}
}

// userSessions renders one entry per matched player.
func (s *Server) userSessions(sess matchmaking.Session) []*mmpb.UserSession {
	out := make([]*mmpb.UserSession, 0, len(sess.Players))
	for _, userID := range sess.Players {
		out = append(out, &mmpb.UserSession{
			Name:       s.userSessionName(sess.ID, userID),
			User:       s.userResourceName(userID),
			State:      mmpb.UserSession_ACTIVE,
			CreateTime: timestamppb.New(sess.CreatedAt),
		})
	}
	return out
}

// matchedUserSessions names each player's session in the match, and carries the
// session token that player's console opens its gamesync channel with.
//
// The token was left empty here until 2026-09-23, on the reasoning that
// SwitchNet verified no such token, so issuing one would only look like a
// safeguard. That reasoning was wrong about who the reader is, and a real
// console paid for it: the console reads gsid, usid, uid and tid out of the
// token's 'gamesync' object to build Gamesync/IssueToken and then
// KeepUserSession (see issueSessionToken), so with this field empty the console
// opens its session channel to the session host and never sends a request at
// all -- the connection dies after about a second and the game reports
// 2321-4992.
//
// One token per player rather than one per match, because each names its own
// player as the subject: a shared token would let the first console that
// received it open a gamesync session as somebody else.
//
// The whole set is minted before any of it is returned. A match where some
// players can open their session and others cannot is the failure this codebase
// is shaped around, one phase later: the ones who did get in sit in a lobby
// that never fills.
func (s *Server) matchedUserSessions(sess matchmaking.Session) ([]*mmpb.MatchedUserSession, error) {
	out := make([]*mmpb.MatchedUserSession, 0, len(sess.Players))
	for _, userID := range sess.Players {
		token, err := s.issueSessionToken(userID, sess.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, &mmpb.MatchedUserSession{
			UserDefinition:     &mmpb.UserDefinition{User: s.userResourceName(userID)},
			UserSession:        s.userSessionName(sess.ID, userID),
			MatchmakingIdToken: token,
		})
	}
	return out, nil
}

func (s *Server) userSessionName(sessionID, userID string) string {
	return s.tenantPath() + "/gameSessions/" + sessionID + "/userSessions/" + userSessionID(sessionID, userID)
}

// userSessionID is one player's user-session id within one game session.
//
// A UUID, and not the user id it used to be: in the capture of a working
// server the session token's usid is a UUID of its own, distinct from uid,
// and the console builds its gamesync session from both. Name-based (v5)
// over the game session and the user, so it needs no storage, is the same in
// the resource name and in the token, and differs per match.
func userSessionID(sessionID, userID string) string {
	return uuid.NewSHA1(userSessionNamespace, []byte(sessionID+"/"+userID)).String()
}

// userSessionNamespace scopes userSessionID; any fixed UUID would do.
var userSessionNamespace = uuid.MustParse("6f0b4c3e-5d1a-4c2e-9b7a-2a1e8c4d5f60")

// ticketState maps a queue state onto the wire enum.
//
// One-to-one, which is why matchmaking.State mirrors the protocol's own states
// instead of inventing a parallel vocabulary. FAILED, REQUIRING_ACCEPTANCE and
// DECLINED have no queue equivalent: nothing here fails a ticket for a reason
// of its own, and acceptance is not implemented.
func ticketState(s matchmaking.State) mmpb.MatchmakingTicket_State {
	switch s {
	case matchmaking.StateSearching:
		return mmpb.MatchmakingTicket_SEARCHING
	case matchmaking.StatePlacing:
		return mmpb.MatchmakingTicket_PLACING
	case matchmaking.StateSucceeded:
		return mmpb.MatchmakingTicket_SUCCEEDED
	case matchmaking.StateTimedOut:
		return mmpb.MatchmakingTicket_TIMED_OUT
	case matchmaking.StateCancelled:
		return mmpb.MatchmakingTicket_CANCELLED
	default:
		return mmpb.MatchmakingTicket_STATE_UNSPECIFIED
	}
}

// terminal reports whether a ticket has finished, one way or another.
func terminal(s matchmaking.State) bool {
	switch s {
	case matchmaking.StateSucceeded, matchmaking.StateTimedOut, matchmaking.StateCancelled:
		return true
	default:
		return false
	}
}

// lastSegment takes the id off the end of a resource name.
//
// Lenient on purpose, and kept that way even after ticketProto's doc comment
// confirmed the exact name shape this server mints: a console that echoes
// back some other prefix (a stale client, a name copied from a different
// resource type) still gets a working Track/Cancel instead of a cosmetic
// mismatch turning into NotFound. Same reasoning as internal/toyohr's
// festIDFromResourceName.
func lastSegment(name string) string {
	name = strings.TrimSuffix(strings.TrimSpace(name), "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// gameSessionIDFromPath finds the id in a "gameSessions/<id>" segment pair
// anywhere in a resource name -- a GameSession's own name, or a UserSession's
// name nested one level under it. The only path in this package with two
// variable segments rather than one, so lastSegment alone cannot answer it.
func gameSessionIDFromPath(name string) string {
	parts := strings.Split(strings.Trim(strings.TrimSpace(name), "/"), "/")
	for i := 0; i < len(parts)-1; i++ {
		if parts[i] == "gameSessions" {
			return parts[i+1]
		}
	}
	return ""
}

// iceServersFrom copies the configured ICE servers, dropping any the validator
// would have rejected.
//
// Defensive rather than redundant: a Server can be built without going through
// config.Validate -- the tests do -- and a half-specified server handed to a
// console is worse than one not offered at all, because the console tries it.
func iceServersFrom(cfg []config.IceServer) []config.IceServer {
	out := make([]config.IceServer, 0, len(cfg))
	for _, s := range cfg {
		if (s.Kind != "stun" && s.Kind != "turn") || s.Host == "" || s.Port <= 0 {
			continue
		}
		out = append(out, s)
	}
	return out
}

// queueConfig builds the matchmaking queue's config from the NPLN section.
func queueConfig(c config.NPLN) matchmaking.Config {
	cfg := matchmaking.Config{
		PlayersPerMatch:   c.PlayersPerMatch,
		TicketTTL:         time.Duration(c.TicketTTL) * time.Second,
		VersusGracePeriod: time.Duration(c.VersusGracePeriod) * time.Second,
		CoopGracePeriod:   time.Duration(c.CoopGracePeriod) * time.Second,
	}
	if len(c.ModePlayers) > 0 {
		cfg.ModeSizes = make(map[string]int, len(c.ModePlayers))
		for mode, n := range c.ModePlayers {
			cfg.ModeSizes[mode] = n
		}
	}
	if len(c.CoopModes) > 0 {
		cfg.CoopModes = make(map[string]bool, len(c.CoopModes))
		for _, mode := range c.CoopModes {
			cfg.CoopModes[mode] = true
		}
	}
	return cfg
}
