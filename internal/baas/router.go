// Package baas reimplements Nintendo's account and friends server.
//
// Endpoints: https://e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com
//
//	https://m-lp1.baas.nintendo.com (accounts, 21.0.0 and later)
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/BAAS-Server
//
// This is the server the stock Nintendo friends applet talks to. It owns
// accounts, friend codes, friendships, friend requests, blocks and presence, and
// it is the only place identity is decided: every other server in SwitchNet
// resolves a player by verifying an id token this server issued.
//
// # Identity is fail-closed
//
// A request is authorised by exactly one thing: an access token this server
// signed, whose subject is a user that exists. A device account resolves to one
// user or the request is refused. There is no default account, no "last console
// that logged in", and no lookup that falls back to a global.
//
// That is not paranoia. A production deployment of this same stack shipped a
// Switch-facing account server whose device-account lookup fell back to a
// process-wide variable holding the last authenticated account. The result was
// that every console acted as one person: they all sent friend requests as him
// and all displayed his friend code, and regenerating the code changed nothing
// because the code was never the identity. See docs/ARCHITECTURE.md.
package baas

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/n-popescu/switchnet/internal/db"
	sjwt "github.com/n-popescu/switchnet/internal/jwt"
	"github.com/n-popescu/switchnet/internal/presencebus"
)

// Config is what the server needs to run.
type Config struct {
	// Issuer is the 'iss' claim of access and id tokens.
	Issuer string
	// Audience is the 'aud' claim.
	Audience string

	AccessTokenJWKSURL string
	IDTokenJWKSURL     string

	AccessTokenTTL time.Duration
	IDTokenTTL     time.Duration

	FriendCodeCooldown time.Duration

	// PresenceTTL is how long a reported state stays valid without a refresh.
	PresenceTTL time.Duration
	// ConsolePresenceTTL is how long a console counts as ONLINE after its last
	// authenticated request.
	ConsolePresenceTTL time.Duration

	ImageDir      string
	ImageBaseURL  string
	MaxImageBytes int64

	// AllowRegistration lets an unknown console create an account.
	AllowRegistration bool
}

// Server is the BAAS HTTP server.
type Server struct {
	cfg   Config
	store db.Store
	bus   presencebus.Bus
	log   *slog.Logger

	// accessIssuer signs access and user tokens; idIssuer signs id tokens.
	// They are separate keys because they are published at separate JWK set
	// URLs and verified by different consumers.
	accessIssuer *sjwt.Issuer
	idIssuer     *sjwt.Issuer

	accessVerifier *sjwt.Verifier
	// deviceVerifier checks the device token a console presents to
	// /1.0.0/application/token. SwitchNet's own dauth server issued it.
	deviceVerifier *sjwt.Verifier
	// appVerifier checks the application token a console may present to
	// /1.0.0/login. SwitchNet's own aauth server issued it.
	appVerifier *sjwt.Verifier

	// now is time.Now, overridable in tests so presence TTL behaviour can be
	// tested without sleeping.
	now func() time.Time
}

// Deps are the collaborators a Server needs.
type Deps struct {
	Store          db.Store
	Bus            presencebus.Bus
	Log            *slog.Logger
	AccessIssuer   *sjwt.Issuer
	IDIssuer       *sjwt.Issuer
	AccessVerifier *sjwt.Verifier
	DeviceVerifier *sjwt.Verifier
	AppVerifier    *sjwt.Verifier
}

// New returns a BAAS server.
func New(cfg Config, d Deps) *Server {
	return &Server{
		cfg:            cfg,
		store:          d.Store,
		bus:            d.Bus,
		log:            d.Log,
		accessIssuer:   d.AccessIssuer,
		idIssuer:       d.IDIssuer,
		accessVerifier: d.AccessVerifier,
		deviceVerifier: d.DeviceVerifier,
		appVerifier:    d.AppVerifier,
		now:            func() time.Time { return time.Now().UTC() },
	}
}

// SetClock replaces the server's clock. For tests only.
func (s *Server) SetClock(now func() time.Time) { s.now = now }

// Handler returns the routed, middleware-wrapped HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// ---- no token required ----
	mux.HandleFunc("POST /1.0.0/application/token", s.applicationToken)
	// The two JWK set endpoints. See certificates.go for why they publish
	// different keys.
	s.registerCertificateRoutes(mux)

	// ---- anonymous token required ----
	mux.Handle("POST /1.0.0/users", s.anonymous(s.registerUser))
	mux.Handle("POST /1.0.0/login", s.anonymous(s.login))
	mux.Handle("POST /1.0.0/federation", s.anonymous(s.federation))

	// ---- user token required ----
	mux.Handle("GET /1.0.0/users", s.authenticated(s.findUsers))
	mux.Handle("GET /1.0.0/users/{uid}", s.authenticated(s.getUser))
	mux.Handle("PATCH /1.0.0/users/{uid}", s.authenticated(s.patchUser))

	mux.Handle("POST /1.0.0/users/{uid}/generate_code", s.authenticated(s.generateFriendCode))
	mux.Handle("POST /1.0.0/users/{uid}/link", s.authenticated(s.linkAccount))
	mux.Handle("POST /1.0.0/users/{uid}/unlink", s.authenticated(s.unlinkAccount))

	mux.Handle("PATCH /1.0.0/users/{uid}/device_accounts/{daid}", s.authenticated(s.patchDeviceAccount))
	mux.Handle("DELETE /1.0.0/users/{uid}/device_accounts/{daid}", s.authenticated(s.deleteDeviceAccount))

	mux.Handle("GET /1.0.0/users/{uid}/blocks", s.authenticated(s.listBlocks))
	mux.Handle("POST /1.0.0/users/{uid}/blocks", s.authenticated(s.addBlock))
	mux.Handle("DELETE /1.0.0/users/{uid}/blocks/{other}", s.authenticated(s.removeBlock))

	mux.Handle("POST /1.0.0/image_upload", s.authenticated(s.uploadImage))
	mux.Handle("GET /images/{name}", http.StripPrefix("/images/", s.imageFileServer()))

	mux.Handle("POST /2.0.0/friend_requests", s.authenticated(s.createFriendRequest))
	mux.Handle("PATCH /2.0.0/friend_requests/{rid}", s.authenticated(s.resolveFriendRequest))
	mux.Handle("GET /2.0.0/users/{uid}/friend_requests/inbox", s.authenticated(s.listInbox))
	mux.Handle("GET /2.0.0/users/{uid}/friend_requests/outbox", s.authenticated(s.listOutbox))
	mux.Handle("GET /2.0.0/users/{uid}/friends", s.authenticated(s.listFriends))
	mux.Handle("PATCH /2.0.0/users/{uid}/friends/{other}", s.authenticated(s.patchFriend))
	mux.Handle("DELETE /2.0.0/users/{uid}/friends/{other}", s.authenticated(s.removeFriend))
	mux.Handle("GET /2.0.0/users/{uid}/relationships/{other}", s.authenticated(s.getRelationship))

	// ---- endpoints answered but not implemented ----
	// The console calls these and treats a failure as a fault. None of them
	// affects accounts or friends, so an empty success is the correct answer —
	// but it is logged, so an operator can see what is being asked for.
	mux.Handle("PUT /1.0.0/push_channels/{a}/{b}", s.authenticated(s.acknowledge))
	mux.Handle("POST /1.0.0/devices/me/delete", s.authenticated(s.acknowledge))
	mux.Handle("POST /1.0.0/devices/snapshot", s.authenticated(s.acknowledge))
	mux.Handle("GET /1.0.0/users/{uid}/device_histories", s.authenticated(s.acknowledge))

	// An unrouted path must still answer in BAAS's error shape.
	mux.HandleFunc("/", s.notFound)

	return s.withLogging(mux)
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.log.Warn("baas: unhandled request", "method", r.Method, "path", r.URL.Path)
	writeError(w, r, errResourceNotFound)
}

// acknowledge answers an endpoint SwitchNet does not implement.
func (s *Server) acknowledge(w http.ResponseWriter, r *http.Request, _ *caller) {
	s.log.Info("baas: acknowledged unimplemented endpoint",
		"method", r.Method, "path", r.URL.Path)
	w.WriteHeader(http.StatusNoContent)
}

// ---- authentication ------------------------------------------------------

// caller is the authenticated identity behind a request.
//
// It is built only from a verified token. Nothing else — not a header, not a
// path parameter, not the last request seen — can produce one.
type caller struct {
	// UserID is the authenticated user. Empty for an anonymous token.
	UserID string
	// DeviceAccountID is the device account the token was minted for. Empty for
	// an anonymous token.
	DeviceAccountID string
	// DeviceID is the console, when the token carries one.
	DeviceID string
	// Anonymous is true for a token from /1.0.0/application/token.
	Anonymous bool
}

// handlerFunc is a handler that has an authenticated caller.
type handlerFunc func(w http.ResponseWriter, r *http.Request, c *caller)

// anonymous requires any valid access token: anonymous or user.
//
// Registration and login need a token but cannot need a user token, because the
// point of them is to get one.
func (s *Server) anonymous(next handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := s.authenticate(w, r)
		if !ok {
			return
		}
		next(w, r, c)
	})
}

// authenticated requires a user access token.
func (s *Server) authenticated(next handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := s.authenticate(w, r)
		if !ok {
			return
		}
		if c.Anonymous || c.UserID == "" {
			// An anonymous token on a user endpoint is a scope problem, not an
			// authentication one: the token is valid, it just is not enough.
			writeError(w, r, errInsufficientScope)
			return
		}

		// The console is talking to us, so it is online. This is what stops a
		// console idling in the Home Menu from reading as offline to all its
		// friends: it never reports a state there, but it does keep polling.
		if err := s.store.TouchPresence(r.Context(), c.UserID, s.now()); err != nil {
			// Liveness is a nicety. Failing the request over it would turn a
			// cosmetic problem into an outage.
			s.log.Warn("baas: touch presence", "user", c.UserID, "error", err)
		}

		next(w, r, c)
	})
}

// authenticate verifies the Authorization header.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (*caller, bool) {
	header := r.Header.Get("Authorization")
	if header == "" {
		writeError(w, r, errInvalidRequest)
		return nil, false
	}
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
		writeError(w, r, errInvalidRequest)
		return nil, false
	}

	var claims sjwt.BAASTokenClaims
	if err := s.accessVerifier.Verify(token, &claims); err != nil {
		s.log.Debug("baas: rejected access token", "error", err, "path", r.URL.Path)
		writeError(w, r, errInvalidToken)
		return nil, false
	}
	if claims.Type != "token" {
		// An id token is for game servers, not for this API. Accepting one here
		// would let a game server's copy of a player's token act as that player
		// against their account.
		writeError(w, r, errInvalidToken)
		return nil, false
	}

	switch claims.GrantType {
	case 1:
		return &caller{Anonymous: true}, true
	case 2:
		if claims.Subject == "" || claims.DeviceAccountID == "" {
			writeError(w, r, errInvalidToken)
			return nil, false
		}
		// The token names a user. Confirm that user still exists and that the
		// device account still belongs to them, rather than trusting the token
		// alone: a deleted account's token stays cryptographically valid until
		// it expires.
		da, err := s.store.GetDeviceAccount(r.Context(), claims.DeviceAccountID)
		switch {
		case errors.Is(err, db.ErrNotFound):
			writeError(w, r, errInvalidToken)
			return nil, false
		case err != nil:
			writeInternalError(s.log, w, r, "load device account", err)
			return nil, false
		}
		if da.DeletedAt != nil || da.UserID != claims.Subject {
			// Either the console's device account was deleted, or the token
			// claims a user the device account does not belong to. Refuse. This
			// is the check that has to fail closed.
			s.log.Warn("baas: refused token whose device account does not match its subject",
				"subject", claims.Subject, "device_account", claims.DeviceAccountID,
				"owner", da.UserID, "deleted", da.DeletedAt != nil)
			writeError(w, r, errInvalidToken)
			return nil, false
		}

		return &caller{
			UserID:          claims.Subject,
			DeviceAccountID: claims.DeviceAccountID,
			DeviceID:        da.DeviceID,
		}, true
	default:
		writeError(w, r, errInvalidToken)
		return nil, false
	}
}

// requireSelf checks that the caller is the user named in the path.
//
// Nintendo answers a mismatch with insufficient_scope, and so does SwitchNet.
// Every user-scoped endpoint goes through this: an authenticated console must
// not be able to read or change somebody else's account by editing the URL.
func (s *Server) requireSelf(w http.ResponseWriter, r *http.Request, c *caller) bool {
	uid := r.PathValue("uid")
	if uid == "" || !strings.EqualFold(uid, c.UserID) {
		s.log.Warn("baas: refused cross-user request",
			"caller", c.UserID, "target", uid, "path", r.URL.Path)
		writeError(w, r, errInsufficientScope)
		return false
	}
	return true
}

// ---- logging -------------------------------------------------------------

// statusRecorder captures the response status for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}

		level := slog.LevelInfo
		if rec.status >= 500 {
			level = slog.LevelError
		} else if rec.status >= 400 {
			level = slog.LevelWarn
		}
		s.log.Log(r.Context(), level, "baas request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start).Round(time.Millisecond).String(),
			// The user agent identifies which sysmodule is calling —
			// nnAccount or nnFriends — which matters when a capture and the
			// implementation disagree about a request shape.
			"agent", r.Header.Get("User-Agent"),
		)
	})
}

// publishPresence sends a presence change to the bus.
//
// A failure is logged and swallowed: the bus feeds the dashboard and the phase 2
// NPLN server, and neither is worth failing a console's request for.
func (s *Server) publishPresence(ctx context.Context, e presencebus.Event) {
	if s.bus == nil {
		return
	}
	if err := s.bus.Publish(ctx, e); err != nil {
		s.log.Warn("baas: publish presence event", "user", e.UserID, "error", err)
	}
}
