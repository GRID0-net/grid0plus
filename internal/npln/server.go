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
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/n-popescu/switchnet/config"
	"github.com/n-popescu/switchnet/internal/db"
	sjwt "github.com/n-popescu/switchnet/internal/jwt"
	"github.com/n-popescu/switchnet/internal/matchmaking"
	"github.com/n-popescu/switchnet/internal/messaging"
	"github.com/n-popescu/switchnet/internal/presence"
	"github.com/n-popescu/switchnet/internal/presencebus"
	"github.com/n-popescu/switchnet/internal/splatdiag"
	"github.com/n-popescu/switchnet/internal/toyohr"
)

// Server is the NPLN gRPC gateway.
type Server struct {
	store db.Store
	log   *slog.Logger

	// sessionHost and sessionPort are where a match sends each console to open
	// its gamesync session: GameSession.host and .port. See
	// config.NPLN.SessionHost.
	sessionHost string
	sessionPort int32

	// tenantSeen logs, once, the npln-tenant-id form the console really sends.
	tenantSeen sync.Once

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
	appID          string
	accessTokenTTL int
	env            Environment

	// queue forms matches. In memory: a ticket is worth nothing once the
	// process restarts, because the console that owned it has already given up
	// and made a new one.
	queue *matchmaking.Queue
	// matchModes are the modes with a configured size, used only to decide
	// whether an arriving mode is worth logging.
	matchModes map[string]int
	// unknownModes remembers which modes have already been logged, so a console
	// re-queuing every thirty seconds does not fill the log with the same line.
	unknownModes sync.Map
	// unimplemented remembers which RPCs have already been reported as
	// unanswered, for the same reason: the console retries.
	unimplemented sync.Map
	// seenMethods backs noteFirstSeen -- see its own doc comment.
	seenMethods sync.Map

	// iceServers are the STUN/TURN servers handed out by AllocateIceServerSet.
	iceServers []config.IceServer
	// regionalLatencyServers enables the seven-region hardware experiment.
	regionalLatencyServers bool
	// publicIP is network.public_ip -- ListLatencyMeasurementServers' fallback
	// target when no ice_servers are configured (the LAN-only default).
	publicIP string
	// adminToken guards the operator API. Empty means it is not served at all.
	adminToken string

	// messages is every user's mailbox for the Messaging service. In memory,
	// like queue above -- see internal/messaging's package doc for why.
	messages *messaging.Queue

	// presence resolves a user's presence through the exact same TTL rules
	// BAAS's HTTP endpoints do -- see internal/presence's package doc.
	presence presence.Resolver
	// bus is where a fresh BAAS presence report arrives, so the Friends and
	// PresenceService streams can push a change instead of only reporting what
	// was already true when the stream opened. Nil is a valid value: the
	// streams still work, they just only refresh on their own keep-alive timer
	// -- the same graceful degradation internal/dashboard has without
	// redis.url set (see cmd/npln/main.go and ROADMAP §2.6, and the same
	// reason: this process is not the one BAAS's HTTP handlers run in, so the
	// in-process bus variant delivers nothing across the boundary).
	bus presencebus.Bus
	// attrs is what each player's game publishes over PresenceService.KeepAlive
	// -- see presenceAttributes.
	attrs *presenceAttributes
	// rooms holds private rooms' creation tickets, properties, passwords and
	// codes -- see rooms.go. The rooms themselves are sessions in queue.
	rooms *rooms
	// roomModesSeen remembers which room configs have been logged once.
	roomModesSeen sync.Map

	// rotation is the stage and mode rotation internal/toyohr's Schedule
	// service serves. Parsed in New, so a typo in config fails at startup
	// rather than on the first request from a console.
	rotation toyohr.Rotation
	// A private, single-user response comparison, nil in normal operation.
	diagnostic *splatdiag.Bundle

	// now is overridable so tests can control expiry without sleeping.
	now func() time.Time

	grpc *grpc.Server

	// stop closes when the server is shutting down, ending the background
	// sweeper. Separate from a request context because the sweeper outlives
	// every request.
	stop     chan struct{}
	stopOnce sync.Once
}

// Options configures a Server.
type Options struct {
	Store  db.Store
	Log    *slog.Logger
	Config config.NPLN

	// Bus carries the live matchmaking gauge to the dashboard, and delivers
	// live presence changes for the Friends and PresenceService streams to
	// push. Optional: nil means the dashboard's tile reports no data rather
	// than a zero that reads as "nobody is queuing", and the streams still
	// work, just refreshed only on their own keep-alive timer. See
	// Server.bus's doc comment.
	Bus presencebus.Bus

	// AccessKey signs and verifies NPLN access tokens (ES256).
	AccessKey sjwt.ECKey
	// BAASIDKey verifies BAAS id tokens (RS256). This is the same key BAAS
	// publishes at /1.0.0/certificates — the seam between the two phases.
	BAASIDKey sjwt.Key

	// Env selects how malformed metadata is refused. Production by default.
	Env Environment

	// PresenceTTL and ConsolePresenceTTL apply the exact same TTL rules
	// BAAS's presence read path does -- pass the same values as BAAS's own
	// config (cfg.BAAS.PresenceTTL, cfg.BAAS.ConsolePresenceTTL), not a
	// separate NPLN setting: presence is one user's state, not one per
	// service, and a second pair of knobs is a second way for the two to
	// disagree about who is online.
	PresenceTTL        time.Duration
	ConsolePresenceTTL time.Duration

	// Now is overridable in tests. Defaults to time.Now.
	Now func() time.Time

	// PublicIP is network.public_ip -- see Server.publicIP.
	PublicIP string
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

	rotation, err := toyohr.RotationFromStrings(
		opts.Config.Rotation.RegularStages,
		opts.Config.Rotation.BankaraRules,
		opts.Config.Rotation.BankaraStages,
		opts.Config.Rotation.StagesPerSlot,
	)
	if err != nil {
		return nil, err
	}
	diagnostic, err := splatdiag.FromEnvironment()
	if err != nil {
		return nil, fmt.Errorf("npln: private diagnostic: %w", err)
	}

	s := &Server{
		store:                  opts.Store,
		log:                    opts.Log,
		tokens:                 sjwt.NewECVerifier(opts.AccessKey),
		issuer:                 sjwt.NewECIssuer(opts.AccessKey, opts.Config.JWKSURL),
		idTokens:               sjwt.NewVerifier(opts.BAASIDKey),
		tenant:                 opts.Config.Tenant,
		sessionHost:            opts.Config.SessionHost,
		sessionPort:            int32(opts.Config.SessionPort),
		issuerName:             opts.Config.Issuer,
		appID:                  opts.Config.AppID,
		accessTokenTTL:         opts.Config.AccessTokenTTL,
		env:                    opts.Env,
		queue:                  matchmaking.New(queueConfig(opts.Config), opts.Now),
		matchModes:             opts.Config.ModePlayers,
		iceServers:             iceServersFrom(opts.Config.IceServers),
		regionalLatencyServers: opts.Config.RegionalLatencyServers,
		publicIP:               opts.PublicIP,
		adminToken:             opts.Config.AdminToken,
		messages:               messaging.New(opts.Now),
		presence: presence.Resolver{
			Store:      opts.Store,
			ReportTTL:  opts.PresenceTTL,
			ConsoleTTL: opts.ConsolePresenceTTL,
		},
		bus:        opts.Bus,
		attrs:      newPresenceAttributes(),
		rooms:      newRooms(),
		rotation:   rotation,
		diagnostic: diagnostic,
		now:        opts.Now,
		stop:       make(chan struct{}),
	}

	s.grpc = grpc.NewServer(
		grpc.ChainUnaryInterceptor(s.unaryInterceptor),
		grpc.ChainStreamInterceptor(s.streamInterceptor),
		grpc.UnknownServiceHandler(s.unknownService),
	)
	s.register()
	go s.sweepTickets()
	return s, nil
}

// statsInterval is how often the matchmaking gauge is republished.
//
// Comfortably inside presencebus.StatsTTL, so a running server's gauge never
// looks stale to the dashboard -- and a server that stops is noticed within one
// TTL rather than reading as an empty queue forever.
const statsInterval = 10 * time.Second

// sweepInterval is how often expired matchmaking tickets are swept.
//
// Sweeping on a timer rather than when a ticket arrives, because a queue nobody
// is joining is exactly the one whose tickets need to expire: doing it only on
// arrival means the last player to give up waits forever.
const sweepInterval = 15 * time.Second

func (s *Server) sweepTickets() {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			expired, formed := s.queue.Sweep()
			for _, t := range expired {
				// Logged because a ticket that timed out is the visible half of
				// "nobody else was queuing", and an operator reading this
				// wants to know it happened rather than infer it.
				s.log.Info("npln: matchmaking ticket timed out",
					"ticket", t.ID, "user", t.UserID, "mode", t.Mode)
			}
			for _, sess := range formed {
				// The other half of forming a match on a timer rather than on a
				// request: CreateMatchmakingTicket's own handler logs and
				// records a match it formed inline, and a match a grace period
				// finishes between requests needs the same treatment or it is
				// invisible to the operator and missing from the history.
				s.log.Info("npln: match formed after its grace period",
					"session", sess.ID, "mode", sess.Mode,
					"players", len(sess.Players), "host", sess.Host)
				s.recordMatch(context.Background(), sess, false)
			}
			s.publishStats()
		}
	}
}

// publishStats republishes the matchmaking gauge.
//
// On a timer as well as on every change, because the gauge expires: a running
// server with an idle queue has to keep saying "still nobody" or the dashboard
// cannot tell it apart from a server that stopped.
func (s *Server) publishStats() {
	if s.bus == nil {
		return
	}
	// A short bound rather than the caller's context, and the error is only
	// logged: the gauge is cosmetic, and a dashboard tile must never be able to
	// slow down or fail a console's request.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := s.bus.PublishStats(ctx, presencebus.Stats{
		Matchmaking: s.queue.Waiting(),
		At:          s.now(),
	}); err != nil {
		s.log.Debug("npln: publish matchmaking stats", "error", err)
	}
}

// noteUnknownMode logs a mode with no configured size, once per mode.
//
// This log line has a job: nothing public documents the strings Splatoon 3 puts
// in a ticket's matchmaking_config, so it is how an operator learns what to put
// in npln.mode_players. Once per mode, because a console re-queues often enough
// to bury everything else.
func (s *Server) noteUnknownMode(mode string) {
	if _, seen := s.unknownModes.LoadOrStore(mode, struct{}{}); seen {
		return
	}
	s.log.Info("npln: matchmaking mode has no configured size, using npln.players_per_match",
		"mode", mode, "players", s.queue.PlayersFor(mode))
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
//
// Idempotent: cmd/npln stops it on a signal and a test's cleanup stops it
// again, and closing a closed channel panics.
func (s *Server) Stop() {
	s.stopOnce.Do(func() { close(s.stop) })
	s.grpc.GracefulStop()
}

// Queue exposes the match queue, for the dashboard's live view.
func (s *Server) Queue() *matchmaking.Queue { return s.queue }

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
	// GetDocument can return either a document or a zero-body NotFound. Wait
	// for the handler's result before sending its opening headers so only the
	// absent case declares content-length: 0, but still send them before grpc
	// writes the status trailers (avoiding Trailers-Only framing).
	const getDocument = "/nn.npln.ugcstore.v1.Ugcstore/GetDocument"
	if info.FullMethod != getDocument {
		sendNPLNHeader(ctx, s.log, info.FullMethod)
	}
	resp, err := handler(ctx, req)
	if info.FullMethod == getDocument {
		sendNPLNHeader(ctx, s.log, info.FullMethod, status.Code(err) == codes.NotFound)
	}
	s.noteUnimplemented(info.FullMethod, err)
	s.noteFirstSeen(info.FullMethod, err)
	// Every call, not just the first: while this codebase's own hall-entry
	// sequence is still being worked out on hardware, a call repeating (as
	// ValidateToken did, invisibly, during the 2026-09-23 KeepAlive
	// misdiagnosis -- noteFirstSeen's dedup hid the retry loop entirely) is
	// exactly the kind of thing worth seeing without another deploy-and-retry
	// cycle. Cheap at this deployment's traffic; revisit if it ever isn't.
	s.log.Info("npln: call", "method", info.FullMethod, "code", status.Code(err))
	return resp, err
}

// noteFirstSeen logs the first time any given unary RPC is actually reached,
// once, regardless of outcome -- the counterpart to noteUnimplemented, for
// every call that IS implemented. Without it, a handler that runs and returns
// successfully (or with an ordinary, expected error like NotFound) leaves no
// trace at all: exactly the gap that made ListLatencyMeasurementServers
// invisible on 2026-09-23 right up until it was found to be Unimplemented --
// there was no way to tell "never called" from "called and answered" for
// anything already implemented.
func (s *Server) noteFirstSeen(method string, err error) {
	if _, seen := s.seenMethods.LoadOrStore(method, struct{}{}); seen {
		return
	}
	s.log.Info("npln: first call to this RPC", "method", method, "code", status.Code(err))
}

// sendNPLNHeader forces this call's HTTP/2 response headers out before the
// handler below returns, so a handler that answers with no message or with a
// bare error (NotFound, Unimplemented -- most of internal/toyohr's backlog)
// never collapses into a single "Trailers-Only" frame.
//
// grpc-go's default, when a unary handler returns without ever having sent
// anything, IS Trailers-Only: headers and the grpc-status trailer folded into
// one HEADERS frame, an optimisation for the common "failed before doing
// anything" case. Nintendo's own NPLN gateway never does this -- every
// response, empty or not, arrives as two frames: ordinary headers
// (content-type, npln-grpc-type: Unary), then a separate trailers frame
// carrying grpc-status. NextendoNetwork/splatoon-3 (a from-scratch NPLN
// reimplementation tested against real hardware; PolyForm Shield-licensed,
// so only this fact is taken, never its code) measured a real console
// crashing specifically on the single-frame shape for an otherwise-correct
// NotFound: nn.npln.Worker aborting with 2162-0001, immediately, the same
// abort this repository's own ground rules exist to avoid shipping blind.
// Calling SendHeader up front -- before the handler has a chance to return
// -- is what keeps grpc-go from folding the two frames together, for every
// RPC this gateway serves, not only the ones already known to answer empty.
//
// A no-op outside a real call (grpc.SendHeader errors when the context
// carries no live stream, e.g. a test invoking a handler directly): that
// failure is expected and not this call's to report.
func sendNPLNHeader(ctx context.Context, log *slog.Logger, method string, emptyDocument ...bool) {
	header := metadata.Pairs("npln-grpc-type", "Unary")
	if method == "/nn.npln.ugcstore.v1.Ugcstore/GetDocument" {
		// Both preserved working captures mark a populated GetDocument as
		// ServerStreaming. A missing document has that value followed by
		// Unary, even though the generated RPC is unary.
		header = metadata.Pairs("npln-grpc-type", "ServerStreaming")
		if len(emptyDocument) > 0 && emptyDocument[0] {
			header.Append("npln-grpc-type", "Unary")
		}
	}
	// No content-length: 0, although Nintendo's opening headers carry one.
	// Behind nginx's grpc_pass it ends the response after these headers and
	// the trailers never reach the console: on hardware (2026-09-25) the
	// game read these NotFounds as status 2 (UNKNOWN), not 5, and failed its
	// point-card job at the hall. Nextendo sat behind Traefik, which passes
	// the trailers through.
	if err := grpc.SendHeader(ctx, header); err != nil {
		log.Debug("npln: send header", "method", method, "error", err)
	}
}

func (s *Server) streamInterceptor(
	srv any, ss grpc.ServerStream,
	info *grpc.StreamServerInfo, handler grpc.StreamHandler,
) error {
	// SetHeader, not SendHeader: the header joins the first frame when it
	// goes, without forcing one out early.
	_ = ss.SetHeader(metadata.Pairs("npln-grpc-type", grpcKind(info)))
	ctx, err := s.enter(ss.Context(), info.FullMethod)
	if err != nil {
		return err
	}
	// Logged on entry, not after handler() returns: a stream like
	// TrackMatchmakingTicket is meant to stay open until a match forms, so
	// waiting for it to close before noting it was ever reached would often
	// mean never.
	s.noteFirstSeen(info.FullMethod, nil)
	err = handler(srv, wrappedStream{ServerStream: ss, ctx: ctx})
	s.noteUnimplemented(info.FullMethod, err)
	return err
}

// grpcKind is the value Nintendo's NPLN gateway puts in npln-grpc-type: the
// kind of call. A capture of 704 responses from the real tenant host carries it
// on every one -- Unary, ServerStreaming or BidirectionalStreaming -- per
// NextendoNetwork/splatoon-3's measurement (facts only, not its code).
func grpcKind(info *grpc.StreamServerInfo) string {
	switch {
	case info.IsClientStream && info.IsServerStream:
		return "BidirectionalStreaming"
	case info.IsServerStream:
		return "ServerStreaming"
	case info.IsClientStream:
		return "ClientStreaming"
	}
	return "Unary"
}

// sameTenant reports whether a call's npln-tenant-id names the configured
// tenant, accepting the id in either of the forms that circulate for it: the
// full id Nintendo's own tokens carry ("t-dce9377b-lp1", the tid of a real
// access token, and what NextendoNetwork/splatoon-3 records the console
// sending) and the short hex id ("dce9377b") this project used until
// 2026-09-23. Which one the console sends has not been seen by this server
// yet -- the first call logs it -- and refusing the other would refuse every
// call the game makes.
func sameTenant(got, want string) bool {
	return got != "" && shortTenant(got) == shortTenant(want)
}

// shortTenant reduces "t-<id>-<env>" to "<id>"; anything else is returned as
// is.
func shortTenant(t string) string {
	t = strings.TrimPrefix(t, "tenants/")
	if rest, ok := strings.CutPrefix(t, "t-"); ok {
		if i := strings.LastIndexByte(rest, '-'); i > 0 {
			return rest[:i]
		}
	}
	return t
}

// unknownService answers a call to a service this server does not register at
// all (Ugcstore, Datastore, Appconfig... -- see services.go).
//
// Without it grpc-go answers Unimplemented as a single Trailers-Only frame,
// and runs no interceptor, so nothing is logged either. That single-frame shape
// is the one NextendoNetwork/splatoon-3 measured crashing Splatoon 3
// (2162-0001 in nn.npln.Worker) -- see sendNPLNHeader. Here the call still goes
// through streamInterceptor (authentication, npln-grpc-type), the headers go
// out on their own, and the Unimplemented follows in a separate trailers frame,
// logged once per method like any other unimplemented RPC.
func (s *Server) unknownService(_ any, stream grpc.ServerStream) error {
	if err := stream.SendHeader(nil); err != nil {
		s.log.Debug("npln: send header", "error", err)
	}
	return status.Error(codes.Unimplemented, "not implemented")
}

// noteUnimplemented logs an RPC the console asked for and this server does not
// answer, once per method.
//
// **This log is the to-do list**, which is why it is worth a mechanism rather
// than a line in each handler. ROADMAP §2.4 says to let the console drive the
// order of the remaining toyohr services; that only works if an unanswered RPC
// says so, and it only says so if its service is registered. So: register
// everything, and catch the refusals here.
//
// Matched on the generated message rather than on the code alone. codes
// .Unimplemented is also what this server deliberately answers for a flow it
// will not serve — a delegated matchmaking ticket, malformed metadata — and
// those are decisions, not gaps. protoc-gen-go-grpc's stubs all say "method X
// not implemented"; SwitchNet's own refusals never do.
func (s *Server) noteUnimplemented(method string, err error) {
	if status.Code(err) != codes.Unimplemented {
		return
	}
	if !strings.HasSuffix(status.Convert(err).Message(), "not implemented") {
		return
	}
	if _, seen := s.unimplemented.LoadOrStore(method, struct{}{}); seen {
		return
	}
	s.log.Info("npln: the console asked for an RPC this server does not implement",
		"method", method)
}

// enter parses and authenticates one call, returning the context the handler
// should see.
func (s *Server) enter(ctx context.Context, method string) (context.Context, error) {
	md, ok := parseMetadata(ctx)
	if !ok {
		s.log.Debug("npln: malformed metadata", "method", method)
		return nil, errMalformed(s.env)
	}
	if !sameTenant(md.tenant, s.tenant) {
		// Another title's traffic, or a misconfigured tenant. Refused the same
		// way malformed metadata is: it is not this server's to answer. Warn,
		// not Debug: when this fires for the one title configured, every call
		// the game makes is refused, and at Debug that was invisible.
		s.log.Warn("npln: wrong tenant", "method", method, "tenant", md.tenant, "want", s.tenant)
		return nil, errMalformed(s.env)
	}
	s.tenantSeen.Do(func() {
		s.log.Info("npln: first call's npln-tenant-id", "tenant", md.tenant, "configured", s.tenant)
	})

	if unauthenticatedMethods[method] {
		return ctx, nil
	}

	c, err := s.authenticate(ctx, md)
	if err != nil {
		logAuthFailure(s.log, method, err)
		return nil, err
	}

	// The console is talking to us over NPLN, so it is online -- the same
	// liveness signal internal/baas/router.go's authenticated middleware
	// records for every HTTP request. Without this, a console that only ever
	// touches BAAS at login (everything else being Splatoon 3's own gRPC
	// traffic) would never look alive to PresenceService's TTL rules.
	if err := s.store.TouchPresence(ctx, c.UserID, s.now()); err != nil {
		// Liveness is a nicety. Failing the call over it would turn a
		// cosmetic problem into every RPC failing.
		s.log.Warn("npln: touch presence", "user", c.UserID, "error", err)
	}

	return context.WithValue(ctx, callerKey{}, c), nil
}

// wrappedStream carries a replaced context through a streaming RPC.
type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w wrappedStream) Context() context.Context { return w.ctx }
