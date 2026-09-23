package npln

import (
	"context"
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
// for a squad, everyone but the leader is currently dropped. SwitchNet issues
// no delegation tokens, so it cannot honour the field without inventing one.
// A capture of a squad entering matchmaking would settle it.
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

	proto, err := m.s.ticketProto(ticket)
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

// GetGameSession returns a match the caller was placed into.
//
// Only a match they are actually in. A session id is not a secret worth much,
// but the players list inside one names other people, and handing that to any
// authenticated caller who guesses an id is a leak for no gain.
func (g *gameSessionService) GetGameSession(
	ctx context.Context, req *mmpb.GetGameSessionRequest,
) (*mmpb.GameSession, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}

	id := lastSegment(req.GetName())
	if !g.s.queue.IsPlayer(id, caller.UserID) {
		return nil, ErrNotFound
	}
	sess, found := g.s.queue.Session(id)
	if !found {
		return nil, ErrNotFound
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
// Always empty. SwitchNet forms every session through matchmaking tickets and
// sessionProto always sets IsPublic false, so there is nothing a browse query
// could ever turn up that GetGameSession/JoinGameSession's ticket-driven path
// does not already cover. An empty page rather than leaving this Unimplemented
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

// JoinGameSession confirms the caller into a session they were matched into.
//
// Only that. SwitchNet forms every session through matchmaking, so the players
// are already known by the time this is called and joining is an
// acknowledgement rather than a mutation. Joining a session you were NOT
// matched into is backfill or a short alias -- neither is implemented, and
// neither shape is documented, so this refuses rather than inventing one.
func (g *gameSessionService) JoinGameSession(
	ctx context.Context, req *mmpb.JoinGameSessionRequest,
) (*mmpb.JoinGameSessionResponse, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}

	id := lastSegment(req.GetName())
	if !g.s.queue.IsPlayer(id, caller.UserID) {
		g.s.log.Warn("npln: join by a user who was not matched into the session",
			"session", id, "caller", caller.UserID)
		return nil, ErrNotFound
	}
	sess, found := g.s.queue.Session(id)
	if !found {
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
			{User: userResourceName(t.UserID)},
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

// sessionProto renders a formed match as the wire message.
func (s *Server) sessionProto(sess matchmaking.Session) *mmpb.GameSession {
	return &mmpb.GameSession{
		Name:                s.tenantPath() + "/gameSessions/" + sess.ID,
		MaxParticipantCount: int32(len(sess.Players)),
		// Everyone matched is a participant. There is no lobby to fill here:
		// the session is created at the moment it is already full.
		CurrentParticipantCount: int32(len(sess.Players)),
		CanParticipate:          false,
		IsPublic:                false,
		State:                   mmpb.GameSession_ACTIVE,
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

// userSessions renders one entry per matched player.
func (s *Server) userSessions(sess matchmaking.Session) []*mmpb.UserSession {
	out := make([]*mmpb.UserSession, 0, len(sess.Players))
	for _, userID := range sess.Players {
		out = append(out, &mmpb.UserSession{
			Name:       s.userSessionName(sess.ID, userID),
			User:       userResourceName(userID),
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
			UserDefinition:     &mmpb.UserDefinition{User: userResourceName(userID)},
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
		PlayersPerMatch: c.PlayersPerMatch,
		TicketTTL:       time.Duration(c.TicketTTL) * time.Second,
	}
	if len(c.ModePlayers) > 0 {
		cfg.ModeSizes = make(map[string]int, len(c.ModePlayers))
		for mode, n := range c.ModePlayers {
			cfg.ModeSizes[mode] = n
		}
	}
	return cfg
}
