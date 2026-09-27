package scsi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	sjwt "github.com/n-popescu/grid0plus/internal/jwt"
)

// ErrUnauthenticated means the request carried no usable BAAS access token.
var ErrUnauthenticated = errors.New("scsi: unauthenticated")

// Caller is the resolved identity behind one request — the BAAS user id and
// the device account the console authenticated with.
type Caller struct {
	NSAID           string
	DeviceAccountID string
}

// authenticate verifies the Authorization header against BAAS's id-token
// key -- the same seam internal/npln's exchangeIDToken checks, not
// internal/baas's own access-token verification.
//
// Confirmed on real hardware, not guessed: a 2026-09-23 loopback capture of
// the plaintext request nginx forwards to this service (between it and a
// console that had already completed BAAS login) shows the Authorization
// header's 'jku' pointing at /1.0.0/certificates -- the id-token JWKS,
// distinct from /1.0.0/internal_certificates' access-token one -- and the
// payload's own 'typ' as "id_token". This server previously verified against
// the access-token key instead (an untested, and wrong, guess: the account
// sysmodule reaches this host over a system-level credential separate from
// any per-app access token, exactly the reasoning that makes an id token the
// right one here too, in hindsight). Every request from that console 401ed
// with "unknown key id" until this was found, and it retried continuously
// rather than backing off.
func (s *Server) authenticate(r *http.Request) (Caller, error) {
	header := r.Header.Get("Authorization")
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return Caller{}, ErrUnauthenticated
	}

	var claims sjwt.BAASTokenClaims
	if err := s.idVerifier.Verify(token, &claims); err != nil {
		s.log.Debug("scsi: rejected id token", "error", err)
		return Caller{}, ErrUnauthenticated
	}
	if claims.Type != "id_token" || claims.Subject == "" || claims.DeviceAccountID == "" {
		return Caller{}, ErrUnauthenticated
	}

	da, err := s.store.GetDeviceAccount(r.Context(), claims.DeviceAccountID)
	switch {
	case errors.Is(err, db.ErrNotFound):
		return Caller{}, ErrUnauthenticated
	case err != nil:
		s.log.Error("scsi: load device account", "error", err)
		return Caller{}, ErrUnauthenticated
	}
	if da.DeletedAt != nil || da.UserID != claims.Subject {
		s.log.Warn("scsi: refused token whose device account does not match its subject",
			"subject", claims.Subject, "device_account", claims.DeviceAccountID)
		return Caller{}, ErrUnauthenticated
	}
	if da.TokensRevokedBefore != nil && time.Unix(claims.IssuedAt, 0).Before(*da.TokensRevokedBefore) {
		return Caller{}, ErrUnauthenticated
	}

	if _, err := s.store.GetUser(r.Context(), claims.Subject); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return Caller{}, ErrUnauthenticated
		}
		s.log.Error("scsi: load user", "error", err)
		return Caller{}, ErrUnauthenticated
	}

	return Caller{NSAID: claims.Subject, DeviceAccountID: claims.DeviceAccountID}, nil
}

type callerKey struct{}

func withCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

func callerFrom(ctx context.Context) Caller {
	c, _ := ctx.Value(callerKey{}).(Caller)
	return c
}

// authenticated wraps a handler, refusing any request with no valid BAAS
// access token before it reaches business logic.
func (s *Server) authenticated(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := s.authenticate(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid_token", "Token is invalid")
			return
		}
		next(w, r.WithContext(withCaller(r.Context(), c)))
	}
}
