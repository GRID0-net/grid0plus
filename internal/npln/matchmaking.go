package npln

import (
	"context"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/n-popescu/switchnet/config"
	"github.com/n-popescu/switchnet/internal/matchmaking"
	mmpb "github.com/n-popescu/switchnet/proto/matchmaking/v1"
)

// sweepInterval is how often expired tickets are timed out and finished
// sessions discarded.
//
// A ticket's TTL is the promise made to the console; this is the resolution
// that promise is kept at, so it wants to be well under the TTL. Ten seconds
// against a three-minute default means a player waits at most ten seconds past
// the point the server had already given up on them.
const sweepInterval = 10 * time.Second

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
	}

	return m.s.ticketProto(ticket), nil
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
			if err := stream.Send(m.s.ticketProto(t)); err != nil {
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

	return &mmpb.JoinGameSessionResponse{
		GameSession:         g.s.sessionProto(sess),
		MatchedUserSessions: g.s.matchedUserSessions(sess),
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
// TODO(capture-needed): the resource-name shape. Every NPLN resource name in
// the definitions is a path, and this follows the AIP-style convention the rest
// of them do, but nothing confirms what Splatoon 3 receives here. lastSegment
// deliberately parses whatever comes back leniently, so a wrong prefix costs a
// cosmetic mismatch rather than a broken Track or Cancel.
// Change: this function and sessionProto.
func (s *Server) ticketProto(t matchmaking.Ticket) *mmpb.MatchmakingTicket {
	out := &mmpb.MatchmakingTicket{
		Name:              s.tenantPath() + "/matchmakingTickets/" + t.ID,
		MatchmakingConfig: t.Mode,
		State:             ticketState(t.State),
		UserDefinitions: []*mmpb.UserDefinition{
			{User: userResourceName(t.UserID)},
		},
	}

	if t.SessionID == "" {
		return out
	}
	sess, ok := s.queue.Session(t.SessionID)
	if !ok {
		// The session aged out from under a ticket somebody is still reading.
		// The state is still the truth, so the ticket is worth sending without
		// it rather than failing the stream.
		return out
	}
	out.GameSession = s.sessionProto(sess)
	out.MatchedUserSessions = s.matchedUserSessions(sess)
	return out
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
		// Host names the player whose console the others connect to, not an
		// address. Gameplay is peer-to-peer and SwitchNet is not in the path
		// once a match starts -- all this does is say who to reach, and ICE
		// works out how.
		Host:         userResourceName(sess.Host),
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

// matchedUserSessions names each player's session in the match.
//
// MatchmakingIdToken is deliberately left empty. It is NPLN's mechanism for one
// player to prove to another that the server placed them in this match, and
// SwitchNet issues no such token: peers here are consoles on one operator's own
// network, and a token nobody verifies is a token that only looks like a
// safeguard.
//
// TODO(capture-needed): whether Splatoon 3 refuses to start a match when this
// is absent. If it does, this becomes a real signed token rather than a gap,
// and the thing it has to be signed with is the same key IssueToken uses.
// Change: this function.
func (s *Server) matchedUserSessions(sess matchmaking.Session) []*mmpb.MatchedUserSession {
	out := make([]*mmpb.MatchedUserSession, 0, len(sess.Players))
	for _, userID := range sess.Players {
		out = append(out, &mmpb.MatchedUserSession{
			UserDefinition: &mmpb.UserDefinition{User: userResourceName(userID)},
			UserSession:    s.userSessionName(sess.ID, userID),
		})
	}
	return out
}

func (s *Server) userSessionName(sessionID, userID string) string {
	return s.tenantPath() + "/gameSessions/" + sessionID + "/userSessions/" + userID
}

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
// Lenient on purpose. The exact name shapes are TODO(capture-needed), so this
// accepts whatever the console echoes back rather than insisting on the one
// this server happened to mint -- including a bare id. Getting the prefix wrong
// then costs a cosmetic mismatch instead of every Track and Cancel answering
// NotFound. Same reasoning as internal/toyohr's festIDFromResourceName.
func lastSegment(name string) string {
	name = strings.TrimSuffix(strings.TrimSpace(name), "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// iceServersFrom copies the configured ICE servers, dropping any the validator
// would have rejected.
//
// Defensive rather than redundant: a Server can be built without going through
// config.Validate -- the tests do -- and a half-specified server handed to a
// console is worse than one not offered at all, because the console tries it.
func iceServersFrom(cfg []config.ICEServer) []config.ICEServer {
	out := make([]config.ICEServer, 0, len(cfg))
	for _, s := range cfg {
		if (s.Kind != "stun" && s.Kind != "turn") || s.Host == "" || s.Port <= 0 {
			continue
		}
		out = append(out, s)
	}
	return out
}
