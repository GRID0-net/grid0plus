package npln

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"

	"github.com/n-popescu/switchnet/internal/db"
	sjwt "github.com/n-popescu/switchnet/internal/jwt"
)

// Caller is the resolved identity behind one request.
//
// It is produced only by authenticate, only from a token SwitchNet signed, and
// only after the user has been confirmed to still exist. There is no way to
// construct one from anything the client said about itself.
type Caller struct {
	// UserID is the SwitchNet user. It comes from the token's subject, never
	// from the 'uid' metadata field.
	UserID string
	// DeviceAccountID is the console the token was issued to.
	DeviceAccountID string
}

// nplnClaims is the payload of an NPLN access token.
//
// Deliberately small. It exists to name a user and expire; anything else the
// server needs it reads from the database, where it can still be correct after
// the token was signed. A claim baked into a token is a fact frozen at issue
// time, and this project has already been bitten by treating one as current.
type nplnClaims struct {
	Subject  string `json:"sub"`
	Issuer   string `json:"iss"`
	Audience string `json:"aud"`
	Expires  int64  `json:"exp"`
	IssuedAt int64  `json:"iat"`
	TokenID  string `json:"jti"`

	// DeviceAccountID is the console this token was issued to, carried so that
	// the same device-account ownership check BAAS makes on every request can
	// be made here too.
	DeviceAccountID string `json:"npln:did,omitempty"`

	// SessionID is the game session a matchmaking id token was issued for.
	// Absent on an access token, which is not about one match.
	SessionID string `json:"npln:gsid,omitempty"`
}

func (c nplnClaims) GetExpirationTime() (*gojwt.NumericDate, error) {
	return &gojwt.NumericDate{Time: time.Unix(c.Expires, 0)}, nil
}
func (c nplnClaims) GetIssuedAt() (*gojwt.NumericDate, error) {
	return &gojwt.NumericDate{Time: time.Unix(c.IssuedAt, 0)}, nil
}
func (c nplnClaims) GetNotBefore() (*gojwt.NumericDate, error) { return nil, nil }
func (c nplnClaims) GetIssuer() (string, error)                { return c.Issuer, nil }
func (c nplnClaims) GetSubject() (string, error)               { return c.Subject, nil }
func (c nplnClaims) GetAudience() (gojwt.ClaimStrings, error) {
	if c.Audience == "" {
		return nil, nil
	}
	return gojwt.ClaimStrings{c.Audience}, nil
}

// authenticate resolves a call's bearer token to a caller, or fails.
//
// This is the same shape as internal/baas's authenticate, for the same reason,
// and the reason is the bug this whole project is built around: a server that
// resolved a console by a cached credential and fell back to a process-wide
// "last account seen" when the lookup missed. Every console then acted as one
// person.
//
// So: one lookup, and any miss is an error. No fallback, no default user, no
// "the uid metadata said who they were". A token stays cryptographically valid
// until it expires, which is why the user is re-read on every request rather
// than trusted from the token alone.
func (s *Server) authenticate(ctx context.Context, md callMetadata) (Caller, error) {
	if md.bearer == "" {
		return Caller{}, ErrUnauthenticated
	}

	var claims nplnClaims
	if err := s.tokens.Verify(md.bearer, &claims); err != nil {
		s.log.Debug("npln: rejected access token", "error", err)
		return Caller{}, ErrUnauthenticated
	}
	if claims.Subject == "" {
		return Caller{}, ErrUnauthenticated
	}

	// The 'uid' metadata is the client telling us who it thinks it is. It is
	// checked against the token and otherwise ignored. A mismatch is refused
	// rather than resolved in the token's favour: a client sending someone
	// else's uid is either broken or probing, and neither should be served.
	if md.uid != "" && md.uid != claims.Subject {
		s.log.Warn("npln: refused a call whose uid does not match its token",
			"uid", md.uid, "subject", claims.Subject)
		return Caller{}, ErrPermissionDenied
	}

	if _, err := s.store.GetUser(ctx, claims.Subject); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			// The user was deleted after the token was signed. The token is
			// still valid; the user is not.
			return Caller{}, ErrUnauthenticated
		}
		s.log.Error("npln: load user", "user", claims.Subject, "error", err)
		return Caller{}, ErrUnauthenticated
	}

	// Same device-account check BAAS makes: a console whose device account was
	// deleted, or reassigned to somebody else, must stop working immediately
	// rather than when its token happens to expire.
	if claims.DeviceAccountID != "" {
		da, err := s.store.GetDeviceAccount(ctx, claims.DeviceAccountID)
		switch {
		case errors.Is(err, db.ErrNotFound):
			return Caller{}, ErrUnauthenticated
		case err != nil:
			s.log.Error("npln: load device account", "error", err)
			return Caller{}, ErrUnauthenticated
		}
		if da.DeletedAt != nil || da.UserID != claims.Subject {
			s.log.Warn("npln: refused token whose device account does not match its subject",
				"subject", claims.Subject, "device_account", claims.DeviceAccountID,
				"owner", da.UserID, "deleted", da.DeletedAt != nil)
			return Caller{}, ErrUnauthenticated
		}
		if da.TokensRevokedBefore != nil && time.Unix(claims.IssuedAt, 0).Before(*da.TokensRevokedBefore) {
			// Re-checked on every call, the same as the deleted/reassigned checks
			// above -- this is what actually ends an already-active NPLN session
			// the moment an operator revokes it (switchnetctl account
			// revoke-tokens), rather than only stopping a NEW one from being
			// minted at exchangeIDToken below.
			s.log.Warn("npln: refused an access token minted before its device account's tokens were revoked",
				"device_account", claims.DeviceAccountID)
			return Caller{}, ErrUnauthenticated
		}
	}

	return Caller{UserID: claims.Subject, DeviceAccountID: claims.DeviceAccountID}, nil
}

// exchangeIDToken turns a BAAS id token into an NPLN session.
//
// This is the seam between phase 1 and phase 2. The game presents the id token
// BAAS issued it — carried in the protobuf as ExternalIdToken.nsa_id_token —
// and gets back an NPLN access token signed with a different key entirely.
//
// The id token is verified against SwitchNet's own BAAS id key, not accepted on
// its face. That key is the one published at /1.0.0/certificates, which is
// exactly the seam docs/ARCHITECTURE.md describes: a game server learns which
// player is connecting without trusting anything the game told it.
func (s *Server) exchangeIDToken(ctx context.Context, idToken string) (Caller, error) {
	if idToken == "" {
		return Caller{}, ErrUnauthenticated
	}

	var claims sjwt.BAASTokenClaims
	if err := s.idTokens.Verify(idToken, &claims); err != nil {
		s.log.Debug("npln: rejected id token", "error", err)
		return Caller{}, ErrUnauthenticated
	}
	if claims.Type != "id_token" {
		// An access token is for the account API, not for a game server.
		// Accepting one here would let a game server act as the player against
		// their own account — which is the reason the two keys are separate.
		s.log.Debug("npln: refused a non-id token at the game-server seam", "typ", claims.Type)
		return Caller{}, ErrUnauthenticated
	}
	if claims.Subject == "" {
		return Caller{}, ErrUnauthenticated
	}

	if _, err := s.store.GetUser(ctx, claims.Subject); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return Caller{}, ErrUnauthenticated
		}
		s.log.Error("npln: load user", "user", claims.Subject, "error", err)
		return Caller{}, ErrUnauthenticated
	}

	if claims.DeviceAccountID != "" {
		da, err := s.store.GetDeviceAccount(ctx, claims.DeviceAccountID)
		switch {
		case errors.Is(err, db.ErrNotFound):
			return Caller{}, ErrUnauthenticated
		case err != nil:
			s.log.Error("npln: load device account", "error", err)
			return Caller{}, ErrUnauthenticated
		}
		if da.DeletedAt != nil || da.UserID != claims.Subject {
			return Caller{}, ErrUnauthenticated
		}
		if da.TokensRevokedBefore != nil && time.Unix(claims.IssuedAt, 0).Before(*da.TokensRevokedBefore) {
			// Same check as internal/baas/router.go's authenticate, and for the
			// same reason: an id token minted before an operator revoked this
			// device account's tokens stays cryptographically valid, but must
			// not seed a new NPLN session either.
			s.log.Warn("npln: refused an id token minted before its device account's tokens were revoked",
				"device_account", claims.DeviceAccountID)
			return Caller{}, ErrUnauthenticated
		}
	}

	return Caller{UserID: claims.Subject, DeviceAccountID: claims.DeviceAccountID}, nil
}

// issueAccessToken signs an NPLN access token for a caller.
func (s *Server) issueAccessToken(c Caller, now time.Time) (string, time.Duration, error) {
	ttl := time.Duration(s.accessTokenTTL) * time.Second
	tok, err := s.issuer.Sign(nplnClaims{
		Subject:         c.UserID,
		Issuer:          s.issuerName,
		Audience:        s.tenant,
		IssuedAt:        now.Unix(),
		Expires:         now.Add(ttl).Unix(),
		TokenID:         sjwt.NewTokenID(),
		DeviceAccountID: c.DeviceAccountID,
	})
	if err != nil {
		return "", 0, fmt.Errorf("sign npln access token: %w", err)
	}
	return tok, ttl, nil
}

// matchmakingIDTokenAudience is the 'aud' of a matchmaking id token.
//
// A distinct audience from an access token's, which is the tenant. That is what
// stops one being used where the other belongs: gamesync accepts only this
// audience, so an access token presented to it is refused even though both are
// ES256 and signed by the same key.
const matchmakingIDTokenAudience = "gamesync"

// matchmakingIDTokenTTL is how long a matchmaking id token is good for.
//
// Five minutes. It exists to be handed straight to gamesync, which exchanges it
// for a session of its own, so its whole life is one round trip — and a
// short-lived credential that leaks is worth much less than a long-lived one.
const matchmakingIDTokenTTL = 5 * time.Minute

// issueMatchmakingIDToken signs a token a player presents to gamesync.
func (s *Server) issueMatchmakingIDToken(c Caller, sessionID string) (string, error) {
	now := s.now()
	tok, err := s.issuer.Sign(nplnClaims{
		Subject:  c.UserID,
		Issuer:   s.issuerName,
		Audience: matchmakingIDTokenAudience,
		IssuedAt: now.Unix(),
		Expires:  now.Add(matchmakingIDTokenTTL).Unix(),
		TokenID:  sjwt.NewTokenID(),
		// The session, so a token issued for one match cannot be replayed into
		// another. Not enforced by gamesync today -- it has no view of matches
		// -- but present so that becoming enforced is a change in one place
		// rather than a new token format.
		SessionID: sessionID,
		// No device account. The device-account ownership check belongs to the
		// gateway, which re-reads it on every call; carrying it here would
		// invite gamesync to make the same check against a fact frozen at issue
		// time.
	})
	if err != nil {
		return "", fmt.Errorf("sign matchmaking id token: %w", err)
	}
	return tok, nil
}

// VerifyMatchmakingIDToken resolves a matchmaking id token to its player.
//
// Exported because internal/gamesync needs it and must not import this package's
// internals: cmd/npln hands this method across as a function. It is the same
// key, so there is nothing to distribute — but it is a different audience, which
// is what makes an access token unusable there.
func (s *Server) VerifyMatchmakingIDToken(idToken string) (string, error) {
	if idToken == "" {
		return "", errors.New("no token")
	}

	var claims nplnClaims
	if err := s.tokens.Verify(idToken, &claims); err != nil {
		return "", err
	}
	if claims.Audience != matchmakingIDTokenAudience {
		// An access token, or a token for something else. Refused rather than
		// accepted on its subject: the audience is the only thing separating a
		// credential for the account API from one for a game service.
		return "", fmt.Errorf("wrong audience %q", claims.Audience)
	}
	if claims.Subject == "" {
		return "", errors.New("no subject")
	}
	return claims.Subject, nil
}

// logAuthFailure records why a call was refused, at the level that makes the
// log useful without making it noise.
//
// Refusals are expected traffic here: a console reconnects with an expired
// token routinely. What is not expected is a refusal whose reason is a
// mismatch rather than an expiry, so those are warnings and the rest debug.
func logAuthFailure(log *slog.Logger, method string, err error) {
	if errors.Is(err, ErrPermissionDenied) {
		log.Warn("npln: refused", "method", method, "error", err)
		return
	}
	log.Debug("npln: refused", "method", method, "error", err)
}
