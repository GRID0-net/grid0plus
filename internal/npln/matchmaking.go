package npln

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/n-popescu/switchnet/config"
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
// The player is the AUTHENTICATED caller, never the user the request names.
// CreateMatchmakingTicketRequest carries user delegation tokens for the case
// where one console queues on behalf of several accounts, and honouring those
// without verifying them would let any console queue as anybody — the same
// shape as the bug this repository is built around. They are refused rather
// than ignored, because a console that asked to queue two players and got one
// back sits in a lobby waiting for a player who was never queued.
func (m *matchmakerService) CreateMatchmakingTicket(
	ctx context.Context, req *mmpb.CreateMatchmakingTicketRequest,
) (*mmpb.MatchmakingTicket, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}
	if len(req.GetUserDelegationTokens()) > 0 {
		m.s.log.Warn("npln: refused a delegated matchmaking ticket",
			"user", caller.UserID, "delegations", len(req.GetUserDelegationTokens()))
		return nil, status.Error(codes.Unimplemented, "")
	}

	mode := lastSegment(req.GetMatchmakingTicket().GetMatchmakingConfig())
	if mode == "" {
		// A ticket with no config is a ticket for nothing. Matching it against
		// other configless tickets would put two players queuing for different
		// modes into one match.
		return nil, status.Error(codes.InvalidArgument, "no matchmaking config")
	}
	if _, known := m.s.matchModes[mode]; !known {
		// Logged at info, once per mode per process, because this log line is
		// how an operator learns the mode names to put in npln.mode_players —
		// nothing public documents them.
		m.s.noteUnknownMode(mode)
	}

	ticket, sessions, err := m.s.queue.Enqueue(caller.UserID, mode)
	if err != nil {
		m.s.log.Error("npln: enqueue", "user", caller.UserID, "mode", mode, "error", err)
		return nil, status.Error(codes.Internal, "")
	}
	for _, sess := range sessions {
		m.s.log.Info("npln: match formed", "session", sess.ID, "mode", sess.Mode,
			"players", sess.Players, "host", sess.Host)
	}

	return m.s.ticketProto(ticket), nil
}

// TrackMatchmakingTicket streams a ticket's state until it resolves.
//
// **The console lives on this stream.** It is how it learns a match was formed;
// there is no polling path. So the stream stays open until the ticket reaches a
// terminal state or the console goes away, and it sends the current state
// immediately rather than waiting for the next change — a stream that attached
// after its ticket already matched would otherwise wait for an event that has
// already happened.
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
	if ticket.UserID != caller.UserID {
		// Somebody else's ticket. Refused rather than served read-only: the
		// stream reveals who they are matched with and when.
		m.s.log.Warn("npln: refused tracking another player's ticket",
			"caller", caller.UserID, "owner", ticket.UserID)
		return ErrPermissionDenied
	}

	updates, stop := m.s.queue.Watch(id)
	defer stop()

	for {
		select {
		case <-ctx.Done():
			// The console hung up. Not an error: it happens whenever a player
			// backs out of matchmaking.
			return nil
		case t, open := <-updates:
			if !open {
				return nil
			}
			if err := stream.Send(m.s.ticketProto(t)); err != nil {
				return err
			}
			if terminal(t.State) {
				// Ending the stream here rather than holding it open: the
				// ticket cannot change again, and the console reads the
				// terminal state as its answer.
				return nil
			}
		}
	}
}

// CancelMatchmakingTicket withdraws the caller's ticket.
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
		return nil, ErrPermissionDenied
	}

	// A ticket that already matched cannot be cancelled, and that is not an
	// error: the other players are being told to connect, and answering
	// anything but "done" makes the console retry a cancellation that will
	// never succeed. Queue.Cancel reports false; the state the console reads
	// back on its stream is the truth.
	m.s.queue.Cancel(id)
	return &emptypb.Empty{}, nil
}

// CreateAcceptance is deliberately left to the embedded Unimplemented.
//
// Acceptance is the flow where a formed match is offered to each player and
// waits for them to agree — the REQUIRING_ACCEPTANCE and DECLINED ticket
// states. SwitchNet never puts a ticket into that state, so there is nothing an
// acceptance could apply to, and answering one would mean inventing a response
// for a flow that cannot occur.

// ---- nn.npln.matchmaking.v1.GameSessionService ----------------------------

type gameSessionService struct {
	mmpb.UnimplementedGameSessionServiceServer
	s *Server
}

// GetGameSession describes a match to one of its players.
//
// Only to one of its players: a session names who is in it and which console
// hosts, so it is not public information on a server whose whole point is a
// private group.
func (g *gameSessionService) GetGameSession(
	ctx context.Context, req *mmpb.GetGameSessionRequest,
) (*mmpb.GameSession, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}

	sess, found := g.s.queue.Session(lastSegment(req.GetName()))
	if !found {
		return nil, ErrNotFound
	}
	if !participates(sess, caller.UserID) {
		// NotFound rather than PermissionDenied: a caller who is not in the
		// session should not learn that it exists.
		return nil, ErrNotFound
	}
	return g.s.sessionProto(sess), nil
}

// JoinGameSession puts the caller into a session and tells them who else is in
// it.
func (g *gameSessionService) JoinGameSession(
	ctx context.Context, req *mmpb.JoinGameSessionRequest,
) (*mmpb.JoinGameSessionResponse, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}
	if len(req.GetUserDelegationTokens()) > 0 {
		// Same reasoning as CreateMatchmakingTicket: an unverified delegation
		// is a console joining as somebody else.
		return nil, status.Error(codes.Unimplemented, "")
	}

	sess, err := g.s.queue.Join(lastSegment(req.GetName()), caller.UserID)
	switch {
	case errors.Is(err, matchmaking.ErrNoSession):
		return nil, ErrNotFound
	case errors.Is(err, matchmaking.ErrSessionFull):
		return nil, status.Error(codes.FailedPrecondition, "session is full")
	case err != nil:
		g.s.log.Error("npln: join session", "user", caller.UserID, "error", err)
		return nil, status.Error(codes.Internal, "")
	}

	matched := g.s.matchedSessionsProto(sess)

	return &mmpb.JoinGameSessionResponse{
		MatchedUserSessions: matched,
		GameSession:         g.s.sessionProto(sess),
	}, nil
}

// iceServerSetTTL is how long a console may use an allocated ICE server set.
//
// One hour, and the client is told to cache it for the same period. Both are
// SwitchNet's own numbers: the servers are static configuration here rather
// than allocated per session, so there is nothing to expire — but answering
// with no TTL at all would leave a console free to cache addresses across an
// operator changing them.
const iceServerSetTTL = time.Hour

// AllocateIceServerSet hands the console the STUN and TURN servers to use.
//
// **The addresses must be reachable from the CONSOLE.** They come straight from
// config rather than from anything this server can discover, because a server
// behind NAT cannot know its own reachable address and a guess here produces
// the single most common Splatoon 3 failure: the lobby fills and the match
// never starts, which from the server side is indistinguishable from a
// matchmaking bug.
//
// An empty list is answered with an empty set rather than an error. Peers on one
// LAN reach each other without ICE, so a small deployment is legitimately
// configured this way; `switchnetctl check` warns about it instead.
func (g *gameSessionService) AllocateIceServerSet(
	ctx context.Context, _ *mmpb.AllocateIceServerSetRequest,
) (*mmpb.IceServerSet, error) {
	if _, ok := CallerFrom(ctx); !ok {
		return nil, ErrUnauthenticated
	}

	out := &mmpb.IceServerSet{
		Name:                g.s.iceServerSetName(),
		Ttl:                 durationpb.New(iceServerSetTTL),
		UpdateTime:          timestamppb.New(g.s.now()),
		ClientCacheDuration: durationpb.New(iceServerSetTTL),
	}

	for _, srv := range g.s.iceServers {
		switch srv.Kind {
		case "stun":
			// One stun_server, not a list — the message declares a single
			// field. A second one in config is a configuration mistake worth
			// naming rather than silently dropping.
			if out.StunServer != nil {
				g.s.log.Warn("npln: more than one stun server configured; using the first",
					"ignored", srv.Host)
				continue
			}
			out.StunServer = &mmpb.StunServer{
				Host:     srv.Host,
				Port:     int32(srv.Port),
				Protocol: stunProtocol(srv.Protocol),
			}
		case "turn":
			out.TurnServers = append(out.TurnServers, &mmpb.TurnServer{
				Host:     srv.Host,
				Port:     int32(srv.Port),
				Protocol: turnProtocol(srv.Protocol),
				Username: srv.Username,
				Password: srv.Password,
			})
		}
	}
	return out, nil
}

// ---- rendering ------------------------------------------------------------

func (s *Server) ticketProto(t matchmaking.Ticket) *mmpb.MatchmakingTicket {
	out := &mmpb.MatchmakingTicket{
		Name:              s.ticketName(t.ID),
		MatchmakingConfig: s.tenantName() + "/matchmakingConfigs/" + t.Mode,
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
		return out
	}
	out.GameSession = s.sessionProto(sess)
	// Who else is in the match, carried on the ticket rather than left for a
	// follow-up call: the ticket stream is the only thing the console is
	// guaranteed to be reading when a match forms.
	out.MatchedUserSessions = s.matchedSessionsProto(sess)
	return out
}

// matchedSessionsProto names every player in a session.
//
// No matchmaking id tokens. The console asks for those per player through
// include_id_token_users, so that peers can prove who they are to each other;
// issuing one unasked would hand every player a credential for every other.
func (s *Server) matchedSessionsProto(sess matchmaking.Session) []*mmpb.MatchedUserSession {
	out := make([]*mmpb.MatchedUserSession, 0, len(sess.Players))
	for _, p := range sess.Players {
		out = append(out, &mmpb.MatchedUserSession{
			UserDefinition: &mmpb.UserDefinition{User: userResourceName(p)},
			UserSession:    s.userSessionName(sess.ID, p),
		})
	}
	return out
}

func (s *Server) sessionProto(sess matchmaking.Session) *mmpb.GameSession {
	return &mmpb.GameSession{
		Name:                    s.sessionName(sess.ID),
		MaxParticipantCount:     int32(sess.MaxPlayers),
		CurrentParticipantCount: int32(len(sess.Players)),
		CanParticipate:          len(sess.Players) < sess.MaxPlayers,
		// Not public: a session exists because SwitchNet matched these players
		// into it, and there is no browse-and-join path for anybody else.
		IsPublic:   false,
		State:      mmpb.GameSession_ACTIVE,
		Host:       userResourceName(sess.Host),
		CreateTime: timestamppb.New(sess.CreatedAt),
		// No Port. Gameplay is peer-to-peer and the port belongs to the host's
		// console, which this server never learns: it is negotiated over ICE.
		// Sending a plausible number would point every peer at a port nothing
		// is listening on.
	}
}

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

// terminal reports whether a ticket can still change.
func terminal(s matchmaking.State) bool {
	switch s {
	case matchmaking.StateSucceeded, matchmaking.StateTimedOut, matchmaking.StateCancelled:
		return true
	}
	return false
}

func participates(sess matchmaking.Session, userID string) bool {
	for _, p := range sess.Players {
		if p == userID {
			return true
		}
	}
	return false
}

func stunProtocol(p string) mmpb.StunServer_Protocol {
	switch p {
	case "tcp":
		return mmpb.StunServer_TCP
	case "tls":
		return mmpb.StunServer_TLS
	default:
		return mmpb.StunServer_UDP
	}
}

func turnProtocol(p string) mmpb.TurnServer_Protocol {
	switch p {
	case "tcp":
		return mmpb.TurnServer_TCP
	case "tls":
		return mmpb.TurnServer_TLS
	default:
		return mmpb.TurnServer_UDP
	}
}

// queueConfig turns the NPLN configuration into the queue's own.
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
