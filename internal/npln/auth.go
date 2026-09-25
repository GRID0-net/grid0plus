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
	Subject string `json:"sub"`
	Issuer  string `json:"iss"`
	// Audience is empty on a session token, and absent from the wire when it
	// is: a real one (see issueSessionToken) carries five members and 'aud' is
	// not among them. An access token still always sets it -- the tenant.
	Audience string `json:"aud,omitempty"`
	Expires  int64  `json:"exp"`
	IssuedAt int64  `json:"iat"`
	// TokenID is empty on a session token for the same reason: the capture has
	// no 'jti' either, and a claim the client has never seen is a difference
	// worth not introducing.
	TokenID string `json:"jti,omitempty"`

	// DeviceAccountID is the console this token was issued to, carried so that
	// the same device-account ownership check BAAS makes on every request can
	// be made here too.
	DeviceAccountID string `json:"npln:did,omitempty"`

	// SessionID is the game session a matchmaking id token was issued for.
	// Absent on an access token, which is not about one match.
	SessionID string `json:"npln:gsid,omitempty"`

	// Refresh marks a refresh token, so exchangeRefreshToken can refuse an
	// access token presented in its place even though both are signed by the
	// same key and verify with the same claim shape.
	Refresh bool `json:"npln:refresh,omitempty"`

	// NPLN is field-for-field what a real access token's own 'npln' claim
	// carries -- see nplnRights' doc comment. Present only on an access
	// token: it is what npl1 reads to decide what the token may do, and a
	// session token is a different credential for a different reader.
	NPLN *nplnRights `json:"npln,omitempty"`

	// Gamesync is the session block a session token carries, and it is there
	// for the console rather than for this server: the client reads gsid, usid,
	// uid and tid out of it to open its gamesync session. Present only on a
	// session token -- see issueSessionToken.
	Gamesync *gamesyncClaim `json:"gamesync,omitempty"`
}

// gamesyncClaim is the 'gamesync' object a session token carries.
//
// Field for field the shape a hardware capture of a working server shows
// (NextendoNetwork/splatoon-3's capture of TrackGameSessionCreationTicket,
// MatchedUserSession field 3). The two fields SwitchNet has no source for are
// present and empty rather than absent or invented: 'team' is a Splatfest team,
// which this server announces but does not assign, and 'attr'/'ltcy' are the
// client's own attribute and latency JSON, which the capture shows as "{}" and
// {"latencies":{}} for a session that has not gathered either yet.
// nplnRights is the 'npln' object a real access token's payload carries.
//
// Field for field a real Nintendo access token's payload, from a decrypted
// MITM capture NextendoNetwork/splatoon-3 records taking on 2026-06-28 (facts
// only, not its code):
//
//	"npln":{"aid":"a-…","app_id":"0100c2500fc20000",
//	        "authorization":{"allow":["**"],"deny":[],"nso_restricted":false},
//	        "ext_id":"…","ext_id_type":1,"tid":"t-dce9377b-lp1"}
//
// Their own write-up is explicit about why this matters more than it looks:
// npl1 reads these claims to know what it may do, and a token it cannot
// decode this way "finds no rights at all" -- which reproduces exactly the
// class of symptom that motivated writing this down: authentication that
// reports success, calls that return OK, and the client still behaving as
// though it were offline. An access token that never carried this object at
// all (SwitchNet's own case until this was found) is the same failure by
// construction, not merely a risk of it.
//
// Authorization is the one field this project cannot get wrong by omission:
// SwitchNet has no notion of a title-scoped rights system to report honestly,
// so it grants the token every right (allow: ["**"]) rather than reporting
// none -- the same shape the capture itself shows for an ordinary session,
// and the opposite of what an absent claim leaves the client concluding on
// its own. AID and ExtID echo this server's own identifiers (the caller's
// user id and device account id) under the field names the capture uses,
// not fabricated Nintendo internals -- the same reasoning IssuePrearrangedUserToken
// already gives for echoing user_index back as ShortId.
type nplnRights struct {
	AID           string            `json:"aid"`
	AppID         string            `json:"app_id"`
	Authorization nplnAuthorization `json:"authorization"`
	ExtID         string            `json:"ext_id"`
	ExtIDType     int               `json:"ext_id_type"`
	TID           string            `json:"tid"`
}

type nplnAuthorization struct {
	Allow         []string `json:"allow"`
	Deny          []string `json:"deny"`
	NSORestricted bool     `json:"nso_restricted"`
}

type gamesyncClaim struct {
	Attr string `json:"attr"`
	GSID string `json:"gsid"`
	LTCY string `json:"ltcy"`
	Team string `json:"team"`
	TID  string `json:"tid"`
	Typ  int    `json:"typ"`
	UID  string `json:"uid"`
	USID string `json:"usid"`
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

	user, err := s.store.GetUser(ctx, claims.Subject)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			// The user was deleted after the token was signed. The token is
			// still valid; the user is not.
			return Caller{}, ErrUnauthenticated
		}
		s.log.Error("npln: load user", "user", claims.Subject, "error", err)
		return Caller{}, ErrUnauthenticated
	}
	if user.Deleted {
		// switchnetctl account delete-user soft-deletes: the row stays, and
		// only this flag says the person was removed. BAAS's authenticate
		// already refuses it on every request; without the same check here a
		// removed player's game session kept working until its token expired.
		s.log.Warn("npln: refused a call from a deleted user", "user", claims.Subject)
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

	return s.resolveCaller(ctx, claims.Subject, claims.DeviceAccountID, claims.IssuedAt)
}

// resolveCaller is the "is this identity still good" check shared by every
// token this server accepts as proof of a player: a user that still exists,
// and -- if the token names a console -- a device account that still belongs
// to that user and was not revoked after the token was minted. exchangeIDToken
// and exchangeRefreshToken both reduce to this once they've verified their
// own token's signature and picked a subject out of it.
func (s *Server) resolveCaller(ctx context.Context, userID, deviceAccountID string, issuedAt int64) (Caller, error) {
	user, err := s.store.GetUser(ctx, userID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return Caller{}, ErrUnauthenticated
		}
		s.log.Error("npln: load user", "user", userID, "error", err)
		return Caller{}, ErrUnauthenticated
	}
	if user.Deleted {
		return Caller{}, ErrUnauthenticated
	}

	if deviceAccountID != "" {
		da, err := s.store.GetDeviceAccount(ctx, deviceAccountID)
		switch {
		case errors.Is(err, db.ErrNotFound):
			return Caller{}, ErrUnauthenticated
		case err != nil:
			s.log.Error("npln: load device account", "error", err)
			return Caller{}, ErrUnauthenticated
		}
		if da.DeletedAt != nil || da.UserID != userID {
			return Caller{}, ErrUnauthenticated
		}
		if da.TokensRevokedBefore != nil && time.Unix(issuedAt, 0).Before(*da.TokensRevokedBefore) {
			// Same check as internal/baas/router.go's authenticate, and for the
			// same reason: a token minted before an operator revoked this
			// device account's tokens stays cryptographically valid, but must
			// not seed or extend an NPLN session either.
			s.log.Warn("npln: refused a token minted before its device account's tokens were revoked",
				"device_account", deviceAccountID)
			return Caller{}, ErrUnauthenticated
		}
	}

	return Caller{UserID: userID, DeviceAccountID: deviceAccountID}, nil
}

// refreshTokenTTL is how long a minted refresh token stays valid before the
// console must go back through BAAS for a fresh id token. Nextendo's own
// deployed server hands a refresh token back on every IssueToken/
// IssuePrearrangedUserToken response (confirmed by reading, not copying,
// their newTokenPID helper); this server previously minted none at all. A
// hardware crash right after IssuePrearrangedUserToken -- with no further
// call ever logged, so the client never even got as far as asking for
// anything else -- surfaced this as a real gap rather than a cosmetic one:
// see docs/CAPTURES/README.md. Their server never expires it; SwitchNet gives
// it a TTL instead, consistent with every other token this server issues.
const refreshTokenTTL = 30 * 24 * time.Hour

// issueRefreshToken mints an opaque, self-verifying refresh token for c. It is
// this server's own token, not a reconstruction of a Nintendo one: the format
// only has to be something exchangeRefreshToken can verify later.
func (s *Server) issueRefreshToken(c Caller, now time.Time) (string, error) {
	tok, err := s.issuer.Sign(nplnClaims{
		Subject:         c.UserID,
		Issuer:          s.issuerName,
		Audience:        s.tenant,
		IssuedAt:        now.Unix(),
		Expires:         now.Add(refreshTokenTTL).Unix(),
		TokenID:         sjwt.NewTokenID(),
		DeviceAccountID: c.DeviceAccountID,
		Refresh:         true,
	})
	if err != nil {
		return "", fmt.Errorf("sign npln refresh token: %w", err)
	}
	return tok, nil
}

// exchangeRefreshToken is issueRefreshToken's inverse: it verifies a refresh
// token and, if the identity it names is still good, resolves it to a Caller.
//
// Rejects an access token presented in its place -- the two share a verifier
// and claim shape, so the Refresh claim is what tells them apart.
func (s *Server) exchangeRefreshToken(ctx context.Context, refreshToken string) (Caller, error) {
	if refreshToken == "" {
		return Caller{}, ErrUnauthenticated
	}

	var claims nplnClaims
	if err := s.tokens.Verify(refreshToken, &claims); err != nil {
		s.log.Debug("npln: rejected refresh token", "error", err)
		return Caller{}, ErrUnauthenticated
	}
	if !claims.Refresh {
		s.log.Debug("npln: refused an access token presented as a refresh token")
		return Caller{}, ErrUnauthenticated
	}
	if claims.Subject == "" {
		return Caller{}, ErrUnauthenticated
	}

	return s.resolveCaller(ctx, claims.Subject, claims.DeviceAccountID, claims.IssuedAt)
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
		NPLN: &nplnRights{
			AID:   "a-" + c.UserID,
			AppID: s.appID,
			Authorization: nplnAuthorization{
				Allow: []string{"**"},
				Deny:  []string{},
			},
			ExtID:     c.DeviceAccountID,
			ExtIDType: 1,
			TID:       s.tenant,
		},
	})
	if err != nil {
		return "", 0, fmt.Errorf("sign npln access token: %w", err)
	}
	return tok, ttl, nil
}

// sessionTokenIssuer is the 'iss' of a session token: literally "gss".
//
// Not s.issuerName, which is what an access token carries. The console is the
// reader of this one (see issueSessionToken), and "gss" is the value a real one
// carries -- the game's own name for the session service it is about to call.
//
// It is also what keeps the two credentials apart now that neither is separated
// by an audience: an access token's issuer is the tenant URL and it carries no
// gamesync block, so VerifyMatchmakingIDToken refuses it.
const sessionTokenIssuer = "gss"

// sessionTokenTTL is how long a session token is good for.
//
// An hour, from the capture: exp - iat = 3600 there. Longer than one match's
// bring-up needs and no longer than that: the token's whole life is "open the
// gamesync session for this match".
const sessionTokenTTL = time.Hour

// issueSessionToken mints the token a match hands one player.
//
// Signed with the same ES256 key as every other NPLN token — gamesync verifies
// it through VerifyMatchmakingIDToken — but shaped for the CONSOLE to read,
// because the console is what reads it: npl1 takes gsid, usid, uid and tid out
// of the 'gamesync' object here to build Gamesync/IssueToken and then
// KeepUserSession. That is measured, not inferred, and so is the cost of
// getting it wrong: with this token absent or shaped differently the console
// opens TCP+TLS+h2 to the session host and never sends a HEADERS frame at all,
// the connection drops after about a second, and the game reports 2321-4992 on
// KeepUserSession with nothing in this server's log — because from this side
// nothing was ever called. See docs/CAPTURES/npln-session-token-2026-09-23.md.
//
// The signature is what makes the ids trustworthy: a console cannot write its
// own gsid, so a match cannot be joined by naming yourself into it.
func (s *Server) issueSessionToken(userID, sessionID string) (string, error) {
	now := s.now()
	tok, err := s.issuer.SignSessionToken(nplnClaims{
		Subject:  userID,
		Issuer:   sessionTokenIssuer,
		IssuedAt: now.Unix(),
		Expires:  now.Add(sessionTokenTTL).Unix(),
		Gamesync: &gamesyncClaim{
			Attr: "{}",
			GSID: sessionID,
			LTCY: `{"latencies":{}}`,
			// A Splatfest team. SwitchNet announces fests but does not place a
			// player on a team, so this is empty rather than guessed.
			Team: "",
			TID:  s.tenant,
			Typ:  1,
			UID:  userID,
			// The user session's own id, the last segment of
			// userSessionName: a UUID distinct from uid, as in the capture.
			USID: userSessionID(sessionID, userID),
		},
	})
	if err != nil {
		// Logged here rather than at each caller: this is the one place that
		// knows which player and which session the failure belongs to, and the
		// status itself says nothing (see errInternal).
		s.log.Error("npln: sign session token",
			"user", userID, "session", sessionID, "error", err)
		return "", errInternal()
	}
	return tok, nil
}

// issueMatchmakingIDToken signs the same token by name, for the RPC that will
// hand one out on request.
//
// The same credential as a match hands its players, not a second kind:
// IssueMatchmakingIdToken's own field is the one the ticket's
// MatchedUserSession carries, so a console that asked for it there and a
// console that read it off a ticket hold identical tokens. The RPC itself is
// not registered yet (see README's table), which is why nothing calls this.
func (s *Server) issueMatchmakingIDToken(c Caller, sessionID string) (string, error) {
	return s.issueSessionToken(c.UserID, sessionID)
}

// VerifyMatchmakingIDToken resolves a session token to its player.
//
// Exported because internal/gamesync needs it and must not import this package's
// internals: cmd/npln hands this method across as a function. The same key signs
// every token here, so the issuer is what separates them: a session token says
// 'iss' "gss" and carries the gamesync block the console reads. An access token
// — which acts on the account API, not on a match — has neither, and is refused
// rather than accepted on its subject.
func (s *Server) VerifyMatchmakingIDToken(idToken string) (string, error) {
	if idToken == "" {
		return "", errors.New("no token")
	}

	var claims nplnClaims
	if err := s.tokens.Verify(idToken, &claims); err != nil {
		return "", err
	}
	if claims.Issuer != sessionTokenIssuer || claims.Gamesync == nil {
		return "", fmt.Errorf("not a session token (iss %q, gamesync %t)",
			claims.Issuer, claims.Gamesync != nil)
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
