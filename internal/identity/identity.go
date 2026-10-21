// Package identity decides who a game server is talking to, from a BAAS id
// token, for every game server GRID0+ runs (NPLN for Splatoon 3, NEX for
// Splatoon 2).
//
// It exists as its own package so that there is exactly one copy of the rule
// this project is built around: a lookup that cannot resolve the caller is an
// error, never a default. The bug it prevents -- a server that fell back to
// "the last account that authenticated" and made every console the same
// person -- is written up in CLAUDE.md. Two game servers each carrying their
// own copy of the check is how one of them drifts.
package identity

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	sjwt "github.com/n-popescu/grid0plus/internal/jwt"
)

// ErrUnauthenticated is every refusal. Deliberately one error: the caller
// learns nothing about which check failed, and the log says.
var ErrUnauthenticated = errors.New("identity: unauthenticated")

// Caller is a resolved identity. It is produced only by this package, only
// from a token GRID0+ signed, and only after the user was confirmed to
// still exist.
type Caller struct {
	// UserID is the GRID0+ (BAAS) user: 16 lowercase hex digits.
	UserID string
	// DeviceAccountID is the console the token was issued to, when it names one.
	DeviceAccountID string
}

// Store is what resolution reads.
type Store interface {
	GetUser(ctx context.Context, id string) (db.User, error)
	GetDeviceAccount(ctx context.Context, id string) (db.DeviceAccount, error)
}

// Resolve is the "is this identity still good" check: a user that still
// exists and is not deleted, and -- if the token names a console -- a device
// account that still belongs to that user and whose tokens were not revoked
// after this one was minted.
func Resolve(ctx context.Context, store Store, log *slog.Logger, userID, deviceAccountID string, issuedAt int64) (Caller, error) {
	user, err := store.GetUser(ctx, userID)
	if err != nil {
		if !errors.Is(err, db.ErrNotFound) {
			log.Error("identity: load user", "user", userID, "error", err)
		}
		return Caller{}, ErrUnauthenticated
	}
	if user.Deleted {
		return Caller{}, ErrUnauthenticated
	}

	if deviceAccountID != "" {
		da, err := store.GetDeviceAccount(ctx, deviceAccountID)
		switch {
		case errors.Is(err, db.ErrNotFound):
			return Caller{}, ErrUnauthenticated
		case err != nil:
			log.Error("identity: load device account", "error", err)
			return Caller{}, ErrUnauthenticated
		}
		if da.DeletedAt != nil || da.UserID != userID {
			return Caller{}, ErrUnauthenticated
		}
		if da.TokensRevokedBefore != nil && time.Unix(issuedAt, 0).Before(*da.TokensRevokedBefore) {
			// A token minted before an operator revoked this device account's
			// tokens stays cryptographically valid until it expires; it must
			// still not be accepted.
			log.Warn("identity: refused a token minted before its device account's tokens were revoked",
				"device_account", deviceAccountID)
			return Caller{}, ErrUnauthenticated
		}
	}

	return Caller{UserID: userID, DeviceAccountID: deviceAccountID}, nil
}

// IDTokens verifies BAAS id tokens and resolves them to a caller.
type IDTokens struct {
	verifier *sjwt.Verifier
	store    Store
	log      *slog.Logger
}

// NewIDTokens verifies with key, the BAAS id-token key every service reads
// from the shared keys directory.
func NewIDTokens(key sjwt.Key, store Store, log *slog.Logger) *IDTokens {
	return &IDTokens{verifier: sjwt.NewVerifier(key), store: store, log: log}
}

// Resolve verifies an id token and resolves its subject.
//
// Only an id token is accepted. An access token is for the account API, not
// for a game server; accepting one here would let a game server act as the
// player against their own account, which is the reason the two keys are
// separate.
func (t *IDTokens) Resolve(ctx context.Context, idToken string) (Caller, error) {
	if idToken == "" {
		return Caller{}, ErrUnauthenticated
	}
	var claims sjwt.BAASTokenClaims
	if err := t.verifier.Verify(idToken, &claims); err != nil {
		// Info, not Debug: Splatoon 3 sign-ins fail this way a few times a day
		// and the reason is the only thing that tells expiry from a bad key.
		t.log.Info("identity: rejected id token", "error", err)
		return Caller{}, ErrUnauthenticated
	}
	if claims.Type != "id_token" {
		t.log.Debug("identity: refused a non-id token at a game-server seam", "typ", claims.Type)
		return Caller{}, ErrUnauthenticated
	}
	if claims.Subject == "" {
		return Caller{}, ErrUnauthenticated
	}
	return Resolve(ctx, t.store, t.log, claims.Subject, claims.DeviceAccountID, claims.IssuedAt)
}
