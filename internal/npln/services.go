package npln

import (
	"context"
	"errors"
	"net/http"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/n-popescu/grid0plus/internal/toyohr"
	"github.com/n-popescu/grid0plus/internal/ugcstore"
	authpb "github.com/n-popescu/grid0plus/proto/auth/v1"
	friendspb "github.com/n-popescu/grid0plus/proto/friends/v1"
	globalcounterpb "github.com/n-popescu/grid0plus/proto/globalcounter/v1"
	leaderboardpb "github.com/n-popescu/grid0plus/proto/leaderboard/v1"
	maintpb "github.com/n-popescu/grid0plus/proto/maintenance/v1"
	mmpb "github.com/n-popescu/grid0plus/proto/matchmaking/v1"
	messagingpb "github.com/n-popescu/grid0plus/proto/messaging/v1"
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

	// GlobalCounterService answers GetSeason only, which a special Big Run
	// reads its community golden-egg total from (see globalcounter.go); every
	// other method stays Unimplemented. LeaderboardService answers
	// BrowseLeaderboard with an empty board (see leaderboard.go); the rest of
	// it stays Unimplemented.
	globalcounterpb.RegisterGlobalCounterServiceServer(s.grpc, &globalCounterService{s: s})
	leaderboardpb.RegisterLeaderboardServiceServer(s.grpc, &leaderboardService{s: s})

	callerID := func(ctx context.Context) (string, bool) {
		c, ok := CallerFrom(ctx)
		return c.UserID, ok
	}

	// The document store Splatoon 3 keeps its per-player records in. Shared
	// with toyohr: GameRecord/InitializeAttributes creates documents the
	// console then reads through this service.
	docs := ugcstore.NewService(ugcstore.Options{
		Store: s.store, Log: s.log, Tenant: s.tenant, Now: s.now, CallerID: callerID,
		Diagnostic:        s.diagnostic,
		Signer:            ugcstore.URLSigner{Sign: s.signAttachmentURL, Verify: s.verifyAttachmentURL},
		AttachmentBaseURL: "https://" + attachmentHost,
	})
	docs.Register(s.grpc)
	s.docs = docs

	// Splatoon 3's own services. They authenticate through the same
	// interceptor as everything else -- a fest is announced to a console that
	// proved who it is, not to anyone who asks.
	toyohr.NewService(toyohr.Options{
		Store:      s.store,
		Log:        s.log,
		Tenant:     s.tenant,
		Now:        s.now,
		Rotation:   s.rotation,
		Documents:  docs,
		Diagnostic: s.diagnostic,
		// The identity comes from this package's interceptor. toyohr cannot
		// import it back without a cycle, so it is handed in.
		CallerID: callerID,
		OnEntry:  s.onFestEntry,
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
// in ExternalIdToken.nsa_id_token; GRID0+ verifies it against the same key
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

	now := a.s.now()
	access, ttl, err := a.s.issueAccessToken(caller, now)
	if err != nil {
		a.s.log.Error("npln: issue access token", "user", caller.UserID, "error", err)
		return nil, ErrUnauthenticated
	}
	refresh, err := a.s.issueRefreshToken(caller, now)
	if err != nil {
		a.s.log.Error("npln: issue refresh token", "user", caller.UserID, "error", err)
		return nil, ErrUnauthenticated
	}

	a.s.log.Info("npln: issued a session", "user", caller.UserID)

	return &authpb.IssueTokenResponse{
		Token: &authpb.Token{
			User:         a.s.userResourceName(caller.UserID),
			AccessToken:  access,
			RefreshToken: refresh,
			Ttl:          durationpb.New(ttl),
		},
	}, nil
}

// IssuePrearrangedUserToken is the first call a Splatoon 3 client makes once
// its gRPC channel is up -- the game already knows which Nintendo Account it
// wants (from its own BAAS login, "prearranged"), so this exists to trade
// that identity for an NPLN session in one round trip instead of CreateUser
// then IssueToken.
//
// The request's own "tenant" field does not feed identity: it is checked
// once, for every call, by the gateway interceptor before a handler is ever
// reached (see enter()/sameTenant). "user_index" is echoed back as the
// response's short_id -- not this server's own id scheme (nothing here
// tracks one), just handing back what the console itself supplied, the same
// way IssueToken's "user" field would be if this server ever needed to
// restate it.
func (a *authService) IssuePrearrangedUserToken(
	ctx context.Context, req *authpb.IssuePrearrangedUserTokenRequest,
) (*authpb.IssuePrearrangedUserTokenResponse, error) {
	caller, err := a.s.exchangeIDToken(ctx, req.GetExternalIdToken().GetNsaIdToken())
	if err != nil {
		return nil, err
	}

	now := a.s.now()
	access, ttl, err := a.s.issueAccessToken(caller, now)
	if err != nil {
		a.s.log.Error("npln: issue access token", "user", caller.UserID, "error", err)
		return nil, ErrUnauthenticated
	}
	refresh, err := a.s.issueRefreshToken(caller, now)
	if err != nil {
		a.s.log.Error("npln: issue refresh token", "user", caller.UserID, "error", err)
		return nil, ErrUnauthenticated
	}

	a.s.log.Info("npln: issued a prearranged session", "user", caller.UserID)

	return &authpb.IssuePrearrangedUserTokenResponse{
		User: &authpb.User{
			Name:    a.s.userResourceName(caller.UserID),
			ShortId: int64(req.GetUserIndex()),
			// Account, CreateTime and LastLoginTime are left unset: nothing in
			// internal/db.Store tracks them today. See docs/CAPTURES/README.md.
		},
		Token: &authpb.Token{
			User:         a.s.userResourceName(caller.UserID),
			AccessToken:  access,
			RefreshToken: refresh,
			Ttl:          durationpb.New(ttl),
		},
	}, nil
}

// RefreshToken trades a still-good refresh token for a fresh access token,
// without the console needing to go back through BAAS for a new id token.
//
// Splatoon 3 links this RPC in (confirmed by a 2026-09-20 decompile of
// v11.3.0's main), and Nextendo's own deployed server always hands a refresh
// token back from IssueToken/IssuePrearrangedUserToken -- so this server now
// does the same, and answers this RPC rather than leaving it Unimplemented.
func (a *authService) RefreshToken(
	ctx context.Context, req *authpb.RefreshTokenRequest,
) (*authpb.RefreshTokenResponse, error) {
	caller, err := a.s.exchangeRefreshToken(ctx, req.GetRefreshToken())
	if err != nil {
		return nil, err
	}

	now := a.s.now()
	access, ttl, err := a.s.issueAccessToken(caller, now)
	if err != nil {
		a.s.log.Error("npln: issue access token", "user", caller.UserID, "error", err)
		return nil, ErrUnauthenticated
	}
	refresh, err := a.s.issueRefreshToken(caller, now)
	if err != nil {
		a.s.log.Error("npln: issue refresh token", "user", caller.UserID, "error", err)
		return nil, ErrUnauthenticated
	}

	a.s.log.Info("npln: refreshed a session", "user", caller.UserID)

	return &authpb.RefreshTokenResponse{
		Token: &authpb.Token{
			User:         a.s.userResourceName(caller.UserID),
			AccessToken:  access,
			RefreshToken: refresh,
			Ttl:          durationpb.New(ttl),
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
// Both working 2026-09-23 captures send this response every 20 seconds.
const maintenanceKeepAlive = 20 * time.Second

// SubscribeMaintenanceSchedules reports that there is no maintenance, and keeps
// saying so.
//
// GRID0+ has no maintenance windows: it is one operator's server, and when
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

// attachmentHost is where the console uploads and downloads Ugcstore
// attachments (internal/ugcstore/attachments.go). Nintendo's are on
// storage.googleapis.com, which GRID0+ does not and must not redirect; this
// is a vermillion host the console already resolves to GRID0+ and trusts,
// whose nginx block proxies to this process's JSON front end, where
// AttachmentHandler is mounted.
const attachmentHost = "gw.hac.lp1.vermillion.srv.nintendo.net"

// attachmentTokenIssuer prefixes the issuer of an attachment URL's token.
// authenticate accepts only an access token's issuer, so a URL token can
// never be used as a bearer.
const attachmentTokenIssuer = "npln-ugc-"

func (s *Server) signAttachmentURL(kind, owner, blobID string, expires time.Time) (string, error) {
	return s.issuer.Sign(nplnClaims{
		Subject:  owner,
		Issuer:   attachmentTokenIssuer + kind,
		Audience: blobID,
		IssuedAt: s.now().Unix(),
		Expires:  expires.Unix(),
	})
}

func (s *Server) verifyAttachmentURL(kind, token string) (string, string, error) {
	var c nplnClaims
	if err := s.tokens.Verify(token, &c); err != nil {
		return "", "", err
	}
	if c.Issuer != attachmentTokenIssuer+kind || c.Subject == "" || c.Audience == "" {
		return "", "", errors.New("not an attachment token of this kind")
	}
	return c.Subject, c.Audience, nil
}

// AttachmentHandler serves Ugcstore attachment uploads and downloads, for
// the JSON front end to mount under /grid0/ugc/.
func (s *Server) AttachmentHandler() http.Handler { return s.docs.AttachmentHandler() }
