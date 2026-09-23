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
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
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

	// LoginAttemptsPerWindow caps login attempts per client address per
	// LoginWindow. 0 disables the limit.
	LoginAttemptsPerWindow int
	// LoginWindow is the sliding window LoginAttemptsPerWindow counts over.
	LoginWindow time.Duration

	// LogBodies logs request and response bodies on the access log line.
	// Useful exactly once, to see the real bytes a console sent or received
	// without a console-side capture -- far too chatty (and carries real
	// tokens/passwords) to leave on. See internal/stubs.Config.LogBodies for
	// the same pattern elsewhere in this codebase.
	LogBodies bool
}

// Server is the BAAS HTTP server.
type Server struct {
	cfg   Config
	store db.Store
	bus   presencebus.Bus
	log   *slog.Logger

	// accessIssuer signs access and user tokens; idIssuer signs the id tokens
	// embedded in a login/federation response (iss = the BAAS host, read by
	// game servers); naIDIssuer signs the Nintendo Account link flow's id
	// tokens (iss = "https://accounts.nintendo.com", nalink.go). All three
	// are separate keys: idIssuer and naIDIssuer are published together at
	// the same JWK set URL but must not share a key -- see
	// sjwt.IssuerNAID's doc comment for why.
	accessIssuer *sjwt.Issuer
	idIssuer     *sjwt.Issuer
	naIDIssuer   *sjwt.Issuer

	accessVerifier *sjwt.Verifier
	// idVerifier wraps naIDIssuer's key for verification. Two uses:
	//   - the id_token presented to /1.0.0/federation. A REAL Nintendo
	//     Account id_token can never verify here — SwitchNet does not have
	//     Nintendo's key — so this only ever succeeds for a token nalink.go
	//     minted itself. That is exactly what makes it safe to treat a
	//     successful verification as proof the subject names a real,
	//     existing SwitchNet person: see completeLogin's use of it.
	//   - the refresh_token presented to naToken's "refresh_token" grant,
	//     which nalink.go also signs with naIDIssuer's key (see
	//     NAIDTokenJWKSURL's doc comment for why every token minted at
	//     accounts.nintendo.com shares one key).
	idVerifier *sjwt.Verifier
	// deviceVerifier checks the device token a console presents to
	// /1.0.0/application/token. SwitchNet's own dauth server issued it.
	deviceVerifier *sjwt.Verifier
	// appVerifier checks the application token a console may present to
	// /1.0.0/login. SwitchNet's own aauth server issued it.
	appVerifier *sjwt.Verifier

	// now is time.Now, overridable in tests so presence TTL behaviour can be
	// tested without sleeping.
	now func() time.Time

	// naAuthCodes holds the Nintendo Account link stub's short-lived
	// authorization codes (nalink.go). Scoped to the Server instance rather
	// than a package-level variable: this codebase's one hard rule about
	// mutable state is not to reach for a global that outlives a single
	// caller, and there is no reason for this map to be the exception.
	naAuthCodes   map[string]authCode
	naAuthCodesMu sync.Mutex

	// loginLimiter throttles /1.0.0/login and /1.0.0/federation by client
	// address. See ratelimit.go.
	loginLimiter *loginLimiter
}

// Deps are the collaborators a Server needs.
type Deps struct {
	Store          db.Store
	Bus            presencebus.Bus
	Log            *slog.Logger
	AccessIssuer   *sjwt.Issuer
	IDIssuer       *sjwt.Issuer
	NAIDIssuer     *sjwt.Issuer
	AccessVerifier *sjwt.Verifier
	IDVerifier     *sjwt.Verifier
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
		naIDIssuer:     d.NAIDIssuer,
		accessVerifier: d.AccessVerifier,
		idVerifier:     d.IDVerifier,
		deviceVerifier: d.DeviceVerifier,
		appVerifier:    d.AppVerifier,
		now:            func() time.Time { return time.Now().UTC() },
		naAuthCodes:    map[string]authCode{},
		loginLimiter:   newLoginLimiter(cfg.LoginAttemptsPerWindow, cfg.LoginWindow),
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

	// Self-service registration for a person, independent of any console.
	// See website.go.
	mux.HandleFunc("GET /register", s.registerPage)
	mux.HandleFunc("POST /register", s.registerSubmit)

	// The username/password login /register creates an account for. Not
	// /1.0.0/login: that path is the real, Nintendo-shaped device-account
	// protocol and stays exactly that shape. This one needs no prior access
	// token, unlike /1.0.0/login -- see loginByUsername's own doc comment.
	mux.HandleFunc("POST /login", s.loginByUsername)

	// The Nintendo Account link stub. See nalink.go. /2.0.0/users/me is on
	// api.accounts.nintendo.com rather than accounts.nintendo.com, but this
	// mux does not route on Host, so one registration covers both.
	mux.HandleFunc("GET /connect/1.0.0/authorize", s.naAuthorizePage)
	mux.HandleFunc("POST /connect/1.0.0/authorize", s.naAuthorizeSubmit)
	mux.HandleFunc("POST /connect/1.0.0/api/token", s.naToken)
	mux.HandleFunc("GET /2.0.0/users/me", s.naUsersMe)
	// /api/1.0.0/, not /1.0.0/ -- a real capture confirms this is the exact
	// path nnAccount calls next, not a typo. See naQRCodeParam.
	mux.HandleFunc("GET /api/1.0.0/users/{id}/qrcode_param", s.naQRCodeParam)

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
	// A 2026-09-19 capture shows the console requests images from the root of
	// the cdn-image host as '/1/<etag>' and '/2/<etag>', not '/images/<name>'
	// -- see images.go's imageURLs doc comment.
	mux.Handle("GET /1/{name}", http.StripPrefix("/1/", s.imageFileServer()))
	mux.Handle("GET /2/{name}", http.StripPrefix("/2/", s.imageFileServer()))

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
	//
	// push_channels and devices/snapshot used to be routed here too, both
	// answering a bare 204 on the same "no real content, so empty success is
	// correct" reasoning as the two below -- reconciled against a real,
	// working Nextendo capture (2026-08-27) into devices.go's own handlers
	// instead, once that capture showed a real server answers 200 with an
	// actual JSON body for both, not empty. See devices.go for what changed
	// and why "no real content" undersold what these needed.
	mux.Handle("POST /1.0.0/devices/me/delete", s.authenticated(s.acknowledge))
	mux.Handle("GET /1.0.0/users/{uid}/device_histories", s.authenticated(s.acknowledge))

	mux.Handle("PUT /1.0.0/push_channels/{uid}/{daid}", s.authenticated(s.updatePushChannel))
	// devices/snapshot has no {uid} in its path, unlike every genuinely
	// user-scoped endpoint above, and a real capture confirms why: the
	// console calls it with the anonymous device-level token straight from
	// /1.0.0/application/token (bs:grt=1), never a logged-in user's. Gating
	// it behind s.authenticated meant every console with a linked account
	// got a hard 403 on this periodic check instead of the no-op success
	// the comment above intends — surfacing as a "needs to be verified
	// online" error on the console, found 2026-08-28.
	mux.Handle("POST /1.0.0/devices/snapshot", s.anonymous(s.devicesSnapshot))

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
		if da.TokensRevokedBefore != nil && time.Unix(claims.IssuedAt, 0).Before(*da.TokensRevokedBefore) {
			// Minted before an operator revoked this device account's sessions
			// (switchnetctl account revoke-tokens) -- cryptographically valid,
			// not expired, refused anyway. The only way to react to a
			// leaked-but-unexpired token before this existed was rotating the
			// key every token for every console is signed with.
			s.log.Warn("baas: refused a token minted before this device account's tokens were revoked",
				"device_account", claims.DeviceAccountID,
				"issued_at", time.Unix(claims.IssuedAt, 0), "revoked_before", *da.TokensRevokedBefore)
			writeError(w, r, errInvalidToken)
			return nil, false
		}

		// A user-level delete (switchnetctl account delete-user) is meant to
		// evict every device account under that person at once, existing
		// tokens included -- not just block new logins. The device account's
		// own DeletedAt, checked above, does not cover this: deleting a user
		// does not touch its device accounts' rows.
		user, err := s.store.GetUser(r.Context(), claims.Subject)
		switch {
		case errors.Is(err, db.ErrNotFound):
			writeError(w, r, errInvalidToken)
			return nil, false
		case err != nil:
			writeInternalError(s.log, w, r, "load user", err)
			return nil, false
		}
		if user.Deleted {
			s.log.Warn("baas: refused token for a deleted user",
				"subject", claims.Subject, "device_account", claims.DeviceAccountID)
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

// maxLoggedBody caps how much of a request/response body LogBodies reads, so
// it cannot be used to make the server buffer without limit.
const maxLoggedBody = 16 << 10

// statusRecorder captures the response status for the access log, and
// optionally a copy of the body written (see LogBodies).
type statusRecorder struct {
	http.ResponseWriter
	status int
	body   *bytes.Buffer // nil unless LogBodies is on
}

func (s *statusRecorder) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	if s.body != nil && s.body.Len() < maxLoggedBody {
		room := maxLoggedBody - s.body.Len()
		if room > len(b) {
			room = len(b)
		}
		s.body.Write(b[:room])
	}
	return s.ResponseWriter.Write(b)
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		var reqBody string
		if s.cfg.LogBodies && r.Body != nil {
			raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxLoggedBody))
			if err == nil {
				reqBody = string(raw)
			}
			// Handlers still need to read the body; give them a fresh reader
			// over what was actually captured (which is everything, unless it
			// hit the cap -- fine, this is a diagnostic path, not a semantic
			// change to request handling).
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}

		rec := &statusRecorder{ResponseWriter: w}
		if s.cfg.LogBodies {
			rec.body = &bytes.Buffer{}
		}
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
		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start).Round(time.Millisecond).String(),
			// The user agent identifies which sysmodule is calling —
			// nnAccount or nnFriends — which matters when a capture and the
			// implementation disagree about a request shape.
			"agent", r.Header.Get("User-Agent"),
		}
		if s.cfg.LogBodies {
			attrs = append(attrs, "reqBody", reqBody, "respBody", rec.body.String())
		}
		s.log.Log(r.Context(), level, "baas request", attrs...)
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
