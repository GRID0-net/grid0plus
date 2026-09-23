package npln

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/n-popescu/switchnet/internal/toyohr"
	"github.com/n-popescu/switchnet/internal/ugcstore"
	authpb "github.com/n-popescu/switchnet/proto/auth/v1"
	friendspb "github.com/n-popescu/switchnet/proto/friends/v1"
	globalcounterpb "github.com/n-popescu/switchnet/proto/globalcounter/v1"
	leaderboardpb "github.com/n-popescu/switchnet/proto/leaderboard/v1"
	maintpb "github.com/n-popescu/switchnet/proto/maintenance/v1"
	mmpb "github.com/n-popescu/switchnet/proto/matchmaking/v1"
	messagingpb "github.com/n-popescu/switchnet/proto/messaging/v1"
)

// register attaches the implemented services to the gRPC server.
//
// Services are added as they are implemented. An RPC that is registered but not
// implemented answers codes.Unimplemented and logs, which is the to-do list
// ROADMAP §2.4 describes; an RPC on a service that is not registered at all
// fails the same way without the log line, so registering early is worth it.
func (s *Server) register() {
	authpb.RegisterAuthServer(s.grpc, &authService{s: s})
	authpb.RegisterUserServiceServer(s.grpc, &userService{s: s})
	maintpb.RegisterMaintenanceScheduleServiceServer(s.grpc, &maintenanceService{s: s})
	mmpb.RegisterMatchmakerServer(s.grpc, &matchmakerService{s: s})
	mmpb.RegisterGameSessionServiceServer(s.grpc, &gameSessionService{s: s})
	friendspb.RegisterFriendsServer(s.grpc, &friendsService{s: s})
	friendspb.RegisterPresenceServiceServer(s.grpc, &presenceService{s: s})
	messagingpb.RegisterMessagingServer(s.grpc, &messagingService{s: s})

	// Registered for log visibility only (see this function's own doc comment):
	// a 2026-09-20 decompile of Splatoon 3 v11.3.0 confirms both are real,
	// linked services, but each is a substantial subsystem (achievement-style
	// counters and holders; leaderboard scores, charts and categories) with no
	// capture or documentation to build real behaviour from. Implementing any
	// method from the proto shape alone would be inventing the exact kind of
	// plausible-looking Nintendo internal CLAUDE.md's first rule forbids.
	globalcounterpb.RegisterGlobalCounterServiceServer(s.grpc, &globalcounterpb.UnimplementedGlobalCounterServiceServer{})
	leaderboardpb.RegisterLeaderboardServiceServer(s.grpc, &leaderboardpb.UnimplementedLeaderboardServiceServer{})

	callerID := func(ctx context.Context) (string, bool) {
		c, ok := CallerFrom(ctx)
		return c.UserID, ok
	}

	// The document store Splatoon 3 keeps its per-player records in. Shared
	// with toyohr: GameRecord/InitializeAttributes creates documents the
	// console then reads through this service.
	docs := ugcstore.NewService(ugcstore.Options{
		Store: s.store, Log: s.log, Tenant: s.tenant, Now: s.now, CallerID: callerID,
	})
	docs.Register(s.grpc)

	// Splatoon 3's own services. They authenticate through the same
	// interceptor as everything else -- a fest is announced to a console that
	// proved who it is, not to anyone who asks.
	toyohr.NewService(toyohr.Options{
		Store:     s.store,
		Log:       s.log,
		Tenant:    s.tenant,
		Now:       s.now,
		Rotation:  s.rotation,
		Documents: docs,
		// The identity comes from this package's interceptor. toyohr cannot
		// import it back without a cycle, so it is handed in.
		CallerID: callerID,
	}).Register(s.grpc)
}

// ---- nn.npln.auth.v1.Auth -------------------------------------------------

type authService struct {
	authpb.UnimplementedAuthServer
	s *Server
}

// IssueToken exchanges a BAAS id token for an NPLN session.
//
// This is the phase-1 → phase-2 seam. The game sends the id token BAAS gave it
// in ExternalIdToken.nsa_id_token; SwitchNet verifies it against the same key
// it publishes at /1.0.0/certificates and issues an ES256 access token.
//
// The request's own `user` field is ignored for identity. It is the client
// naming a user, and the token is what proves one — the same distinction the
// 'uid' metadata gets, for the same reason.
func (a *authService) IssueToken(
	ctx context.Context, req *authpb.IssueTokenRequest,
) (*authpb.IssueTokenResponse, error) {
	caller, err := a.s.exchangeIDToken(ctx, req.GetExternalIdToken().GetNsaIdToken())
	if err != nil {
		return nil, err
	}

	access, ttl, err := a.s.issueAccessToken(caller, a.s.now())
	if err != nil {
		a.s.log.Error("npln: issue access token", "user", caller.UserID, "error", err)
		return nil, ErrUnauthenticated
	}

	a.s.log.Info("npln: issued a session", "user", caller.UserID)

	return &authpb.IssueTokenResponse{
		Token: &authpb.Token{
			User:        userResourceName(caller.UserID),
			AccessToken: access,
			Ttl:         durationpb.New(ttl),
			// No refresh token. TODO(capture-needed): whether Splatoon 3 ever
			// calls RefreshToken, and what it expects back when it does. The
			// game re-authenticates from its BAAS id token on reconnect, which
			// is a path SwitchNet already serves, so an absent refresh token is
			// the defensible reading rather than a fabricated one. A capture of
			// a real session lasting past the access token's TTL would settle
			// it. Change: this function and RefreshToken below.
			//
			// A 2026-09-20 decompile of Splatoon 3 v11.3.0 confirms
			// "/nn.npln.auth.v1.Auth/RefreshToken" is a real, compiled-in RPC
			// this client links against -- the mechanism this TODO asks about
			// does exist on the wire. That narrows "does it exist" but not
			// "does this game actually call it instead of just re-authenticating
			// from BAAS"; only a capture answers that.
		},
	}, nil
}

// IssuePrearrangedUserToken is the first call a Splatoon 3 client makes once
// its gRPC channel is up -- the game already knows which Nintendo Account it
// wants (from its own BAAS login, "prearranged"), so this exists to trade
// that identity for an NPLN session in one round trip instead of CreateUser
// then IssueToken.
//
// The request also carries its own "tenant" and "user_index" fields. Neither
// feeds identity: tenant is checked once, for every call, by the gateway
// interceptor before a handler is ever reached (see enter()/sameTenant), and
// user_index has no defensible reading here -- nothing in this codebase
// tracks a per-tenant short id for a user, so inventing one would be exactly
// the kind of plausible-looking Nintendo internal CLAUDE.md rules out. It is
// read but not used, same as IssueToken's own now-documented "user" field.
func (a *authService) IssuePrearrangedUserToken(
	ctx context.Context, req *authpb.IssuePrearrangedUserTokenRequest,
) (*authpb.IssuePrearrangedUserTokenResponse, error) {
	caller, err := a.s.exchangeIDToken(ctx, req.GetExternalIdToken().GetNsaIdToken())
	if err != nil {
		return nil, err
	}

	access, ttl, err := a.s.issueAccessToken(caller, a.s.now())
	if err != nil {
		a.s.log.Error("npln: issue access token", "user", caller.UserID, "error", err)
		return nil, ErrUnauthenticated
	}

	a.s.log.Info("npln: issued a prearranged session", "user", caller.UserID)

	return &authpb.IssuePrearrangedUserTokenResponse{
		User: &authpb.User{
			Name: userResourceName(caller.UserID),
			// Account, CreateTime, LastLoginTime and ShortId are left unset:
			// nothing in internal/db.Store tracks them today, and TODO(capture-
			// needed) below covers whether the client actually reads any of
			// them here rather than from a later call. See docs/CAPTURES/README.md.
		},
		Token: &authpb.Token{
			User:        userResourceName(caller.UserID),
			AccessToken: access,
			Ttl:         durationpb.New(ttl),
			// TODO(capture-needed): same open question as IssueToken's Token
			// above -- whether a refresh token belongs here. Change: this
			// function and RefreshToken.
		},
	}, nil
}

// ValidateToken answers whether the caller's token is still good.
//
// Reaching the handler at all means the interceptor already authenticated the
// call, so there is nothing left to check: an invalid token never gets here.
func (a *authService) ValidateToken(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	if _, ok := CallerFrom(ctx); !ok {
		return nil, ErrUnauthenticated
	}
	return &emptypb.Empty{}, nil
}

// ---- nn.npln.maintenance.v1.MaintenanceScheduleService --------------------

type maintenanceService struct {
	maintpb.UnimplementedMaintenanceScheduleServiceServer
	s *Server
}

// maintenanceKeepAlive is how often a keep-alive is sent on an otherwise idle
// maintenance stream.
//
// The stream carries a KeepAlive message in its own oneof, which is the
// protocol saying out loud that silence is not expected. A stream that goes
// quiet is treated as dead and the client stops trusting it — ROADMAP §2.5
// records this for the friends streams, and it is the same rule here.
//
// Thirty seconds is well inside any reasonable idle timeout, including the
// nginx defaults the edge is generated with.
const maintenanceKeepAlive = 30 * time.Second

// SubscribeMaintenanceSchedules reports that there is no maintenance, and keeps
// saying so.
//
// SwitchNet has no maintenance windows: it is one operator's server, and when
// it is down it is down rather than scheduled. So this never sends a schedule.
// It cannot simply return, though — the RPC is a stream, and returning closes
// it, which the client reads as the connection failing rather than as "nothing
// to report".
func (m *maintenanceService) SubscribeMaintenanceSchedules(
	req *maintpb.SubscribeMaintenanceSchedulesRequest,
	stream grpc.ServerStreamingServer[maintpb.SubscribeMaintenanceSchedulesResponse],
) error {
	ctx := stream.Context()

	// One keep-alive immediately, so the client has something in hand rather
	// than an open stream that has never spoken.
	if err := stream.Send(keepAlive()); err != nil {
		return err
	}

	ticker := time.NewTicker(maintenanceKeepAlive)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// The console went away. Not an error.
			return nil
		case <-ticker.C:
			if err := stream.Send(keepAlive()); err != nil {
				return err
			}
		}
	}
}

func keepAlive() *maintpb.SubscribeMaintenanceSchedulesResponse {
	return &maintpb.SubscribeMaintenanceSchedulesResponse{
		Response: &maintpb.SubscribeMaintenanceSchedulesResponse_KeepAlive{
			KeepAlive: &maintpb.KeepAlive{},
		},
	}
}
