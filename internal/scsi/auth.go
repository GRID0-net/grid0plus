package scsi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/n-popescu/switchnet/internal/db"
	sjwt "github.com/n-popescu/switchnet/internal/jwt"
)

// ErrUnauthenticated means the request carried no usable BAAS access token.
var ErrUnauthenticated = errors.New("scsi: unauthenticated")

// Caller is the resolved identity behind one request — the BAAS user id and
// the device account the console authenticated with.
type Caller struct {
	NSAID           string
	DeviceAccountID string
}

// authenticate verifies the Authorization header the same way
// internal/baas/router.go's own authenticate does, against the same
// baas_access key, rather than importing baas's package: this server reads
// identity off the token BAAS already issued, exactly the seam
// internal/npln's exchangeIDToken documents for its own equivalent check.
//
// Real OLSC traffic authenticates this way because it is system-level
// account-state synchronization from the account sysmodule, which already
// holds a BAAS access token — not a separate token type of its own. No
// capture confirms this; it is the only reading consistent with there being
// no separate token-issuance endpoint anywhere in OLSC's documented surface.
func (s *Server) authenticate(r *http.Request) (Caller, error) {
	header := r.Header.Get("Authorization")
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return Caller{}, ErrUnauthenticated
	}

	var claims sjwt.BAASTokenClaims
	if err := s.accessVerifier.Verify(token, &claims); err != nil {
		s.log.Debug("scsi: rejected access token", "error", err)
		return Caller{}, ErrUnauthenticated
	}
	if claims.Type != "token" || claims.Subject == "" || claims.DeviceAccountID == "" {
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
