package npln

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/n-popescu/switchnet/internal/toyohr"
	authpb "github.com/n-popescu/switchnet/proto/auth/v1"
	friendspb "github.com/n-popescu/switchnet/proto/friends/v1"
	maintpb "github.com/n-popescu/switchnet/proto/maintenance/v1"
	mmpb "github.com/n-popescu/switchnet/proto/matchmaking/v1"
)

// register attaches the implemented services to the gRPC server.
//
// Services are added as they are implemented. An RPC that is registered but not
// implemented answers codes.Unimplemented and logs, which is the to-do list
// ROADMAP §2.4 describes; an RPC on a service that is not registered at all
// fails the same way without the log line, so registering early is worth it.
func (s *Server) register() {
	authpb.RegisterAuthServer(s.grpc, &authService{s: s})
	maintpb.RegisterMaintenanceScheduleServiceServer(s.grpc, &maintenanceService{s: s})
	mmpb.RegisterMatchmakerServer(s.grpc, &matchmakerService{s: s})
	mmpb.RegisterGameSessionServiceServer(s.grpc, &gameSessionService{s: s})
	friendspb.RegisterFriendsServer(s.grpc, &friendsService{s: s})
	friendspb.RegisterPresenceServiceServer(s.grpc, &presenceService{s: s})

	// Splatoon 3's own services. They authenticate through the same
	// interceptor as everything else -- a fest is announced to a console that
	// proved who it is, not to anyone who asks.
	toyohr.NewService(s.store, s.log, s.tenant, s.now).Register(s.grpc)
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

// userResourceName renders a user id the way NPLN's resource names are shaped.
//
// TODO(capture-needed): the exact prefix. Every NPLN resource name in the
// definitions is a path ("tenants/<t>/users/<u>"-shaped), and this follows that
// pattern, but no capture confirms what Splatoon 3 receives here or whether it
// parses it at all. A capture of IssueToken's response would settle it.
// Change: this function.
func userResourceName(userID string) string {
	return "users/" + userID
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
