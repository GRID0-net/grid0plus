// Package npln is the NPLN gRPC gateway: the game server Splatoon 3 talks to
// once it has authenticated.
//
// NPLN is gRPC over HTTP/2 with protobuf payloads, one tenant per title. It is
// not the JSON front end the game contacts first — that is internal/nplnrest —
// and the two must not be routed to each other. See internal/nintendo/hosts.go.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers
// Definitions: https://github.com/kinnay/NPLN-Protocols, generated into proto/
//
// SwitchNet signs its own NPLN tokens and verifies its own signatures. It does
// not reproduce or validate any Nintendo credential.
package npln

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"google.golang.org/grpc"

	"github.com/n-popescu/switchnet/config"
	"github.com/n-popescu/switchnet/internal/db"
	sjwt "github.com/n-popescu/switchnet/internal/jwt"
)

// Server is the NPLN gRPC gateway.
type Server struct {
	store db.Store
	log   *slog.Logger

	// tokens verifies the ES256 access tokens this server issues; issuer signs
	// them. Both wrap the same key.
	tokens *sjwt.ECVerifier
	issuer *sjwt.ECIssuer

	// idTokens verifies the RS256 BAAS id tokens presented at the phase-1 seam.
	// A different key AND a different algorithm from the one above, which is
	// what stops either being accepted where the other belongs.
	idTokens *sjwt.Verifier

	tenant         string
	issuerName     string
	accessTokenTTL int
	env            Environment

	// now is overridable so tests can control expiry without sleeping.
	now func() time.Time

	grpc *grpc.Server
}

// Options configures a Server.
type Options struct {
	Store  db.Store
	Log    *slog.Logger
	Config config.NPLN

	// AccessKey signs and verifies NPLN access tokens (ES256).
	AccessKey sjwt.ECKey
	// BAASIDKey verifies BAAS id tokens (RS256). This is the same key BAAS
	// publishes at /1.0.0/certificates — the seam between the two phases.
	BAASIDKey sjwt.Key

	// Env selects how malformed metadata is refused. Production by default.
	Env Environment

	// Now is overridable in tests. Defaults to time.Now.
	Now func() time.Time
}

// New builds the gateway.
func New(opts Options) (*Server, error) {
	if opts.Store == nil {
		return nil, fmt.Errorf("npln: no store")
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Config.Tenant == "" {
		// The tenant is what the game stamps on every call. A server that
		// accepts any tenant would answer another title's traffic as if it were
		// Splatoon 3, which is a confusing way to find out the config is wrong.
		return nil, fmt.Errorf("npln: no tenant configured")
	}

	s := &Server{
		store:          opts.Store,
		log:            opts.Log,
		tokens:         sjwt.NewECVerifier(opts.AccessKey),
		issuer:         sjwt.NewECIssuer(opts.AccessKey, opts.Config.JWKSURL),
		idTokens:       sjwt.NewVerifier(opts.BAASIDKey),
		tenant:         opts.Config.Tenant,
		issuerName:     opts.Config.Issuer,
		accessTokenTTL: opts.Config.AccessTokenTTL,
		env:            opts.Env,
		now:            opts.Now,
	}

	s.grpc = grpc.NewServer(
		grpc.ChainUnaryInterceptor(s.unaryInterceptor),
		grpc.ChainStreamInterceptor(s.streamInterceptor),
	)
	s.register()
	return s, nil
}

// GRPCServer exposes the underlying server, for tests and for serving on a
// listener the caller owns.
func (s *Server) GRPCServer() *grpc.Server { return s.grpc }

// Serve accepts connections until the listener is closed.
//
// TLS is terminated at the nginx edge, not here: the edge already holds the
// certificate for every Nintendo hostname, and terminating in two places would
// mean two certificate configurations that can disagree. nginx reaches this
// server with grpc_pass over cleartext HTTP/2 on the loopback interface.
func (s *Server) Serve(l net.Listener) error { return s.grpc.Serve(l) }

// Stop stops the server, waiting for in-flight RPCs.
func (s *Server) Stop() { s.grpc.GracefulStop() }

// callerKey is the context key the interceptor stores the resolved caller
// under. Unexported and of a private type, so nothing outside this package can
// put a Caller into a context — an identity must come from authenticate.
type callerKey struct{}

// CallerFrom returns the authenticated caller, if the RPC required one.
func CallerFrom(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok
}

// unauthenticatedMethods are the RPCs that may be called without a token,
// because they are how a token is obtained.
//
// An allowlist, not a denylist: a new RPC is authenticated until somebody says
// otherwise. The reverse — open unless listed — means every service added later
// is public until someone remembers.
var unauthenticatedMethods = map[string]bool{
	"/nn.npln.auth.v1.Auth/IssueToken":                                                 true,
	"/nn.npln.auth.v1.Auth/IssuePrearrangedUserToken":                                  true,
	"/nn.npln.auth.v1.Auth/IssueAnonymousUserToken":                                    true,
	"/nn.npln.auth.v1.Auth/RefreshToken":                                               true,
	"/nn.npln.auth.v1.Auth/RefreshAnonymousUserToken":                                  true,
	"/nn.npln.auth.v1.Auth/CreateUser":                                                 true,
	"/nn.npln.maintenance.v1.MaintenanceScheduleService/SubscribeMaintenanceSchedules": true,
}

func (s *Server) unaryInterceptor(
	ctx context.Context, req any,
	info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
) (any, error) {
	ctx, err := s.enter(ctx, info.FullMethod)
	if err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

func (s *Server) streamInterceptor(
	srv any, ss grpc.ServerStream,
	info *grpc.StreamServerInfo, handler grpc.StreamHandler,
) error {
	ctx, err := s.enter(ss.Context(), info.FullMethod)
	if err != nil {
		return err
	}
	return handler(srv, wrappedStream{ServerStream: ss, ctx: ctx})
}

// enter parses and authenticates one call, returning the context the handler
// should see.
func (s *Server) enter(ctx context.Context, method string) (context.Context, error) {
	md, ok := parseMetadata(ctx)
	if !ok {
		s.log.Debug("npln: malformed metadata", "method", method)
		return nil, errMalformed(s.env)
	}
	if md.tenant != s.tenant {
		// Another title's traffic, or a misconfigured tenant. Refused the same
		// way malformed metadata is: it is not this server's to answer.
		s.log.Debug("npln: wrong tenant", "method", method, "tenant", md.tenant)
		return nil, errMalformed(s.env)
	}

	if unauthenticatedMethods[method] {
		return ctx, nil
	}

	c, err := s.authenticate(ctx, md)
	if err != nil {
		logAuthFailure(s.log, method, err)
		return nil, err
	}
	return context.WithValue(ctx, callerKey{}, c), nil
}

// wrappedStream carries a replaced context through a streaming RPC.
type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w wrappedStream) Context() context.Context { return w.ctx }
