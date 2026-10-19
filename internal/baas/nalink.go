package baas

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	sjwt "github.com/n-popescu/grid0plus/internal/jwt"
	"golang.org/x/crypto/bcrypt"
)

// oidcHash implements the OIDC 'at_hash'/'c_hash' claim: SHA-256 of the ASCII
// value, left half, base64url without padding. Both id_tokens in this file
// are always signed RS256, so SHA-256 (rather than the SHA-384/512 the spec
// uses for other algs) is always the right hash here.
func oidcHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:len(sum)/2])
}

// The Nintendo Account link stub: what accounts.nintendo.com becomes once it is
// redirected here instead of null-routed.
//
// This does not replicate Nintendo's real OAuth wire protocol, which isn't published and
// which this project won't invent. Instead it serves a custom login page in place of
// Nintendo's, and redirects back through the custom URI scheme the console's system
// software is listening on (`nintendo://lhub.nx.sys`) with a self-issued id_token in the
// fragment. The console does not verify this against anything Nintendo controls: it
// fetches the verification key from whichever host the token's own `jku` claim names,
// and every Nintendo hostname is already ours, so that resolves back to us regardless.
//
// The login itself checks a person's own GRID0+ login credentials, not a shared
// passphrase: this attaches the link to one persistent identity, not an anonymous one per
// attempt.
//
// POST /1.0.0/users fires automatically right after the redirect, driven by the
// account SDK, with no console-local confirmation step in between. accounts.go's
// federation handler consumes the token this file issues specially: a successful
// verification against naIDIssuer's own key (only ever true for a token this file
// signed) is what tells completeLogin to reassign the fresh device account to the
// existing person, rather than leaving it attached to the disposable identity
// registerUser had to invent before any id token existed. See federation's own doc
// comment in accounts.go.

// authCode is a short-lived, single-use authorization code minted by
// naAuthorize and redeemed by naToken. In-memory and TTL'd: this is
// ephemeral hand-off state between two requests seconds apart, not something
// that needs to survive a restart or be shared across processes.
type authCode struct {
	userID    string
	expiresAt time.Time
}

const authCodeTTL = 5 * time.Minute

// naIssuer is the 'iss' every token minted by this file carries.
//
// NAIDTokenJWKSURL is that token's 'jku' -- its OWN JWK set URL, distinct from
// the BAAS host's (internal/baas/certificates.go), even though both currently
// resolve to the same handler (the mux does not route on Host). Exported so
// cmd/baas can hand it to the naIDIssuer it constructs, rather than that URL
// existing as a second, independently-typed literal that could drift from this
// one. A real accounts.nintendo.com id_token has
// jku=https://accounts.nintendo.com/1.0.0/certificates, never the BAAS host's:
// an id_token whose iss names one host but whose jku names a different one for
// where to verify it would be wrong.
const (
	naIssuer         = "https://accounts.nintendo.com"
	NAIDTokenJWKSURL = naIssuer + "/1.0.0/certificates"
)

func (s *Server) storeAuthCode(code, userID string, now time.Time) {
	s.naAuthCodesMu.Lock()
	defer s.naAuthCodesMu.Unlock()
	// Sweep expired codes opportunistically rather than running a ticker for
	// what is, in practice, a handful of entries at a time.
	for c, ac := range s.naAuthCodes {
		if now.After(ac.expiresAt) {
			delete(s.naAuthCodes, c)
		}
	}
	s.naAuthCodes[code] = authCode{userID: userID, expiresAt: now.Add(authCodeTTL)}
}

// consumeAuthCode returns the user id for a code and deletes it — single use,
// same as a real authorization code.
func (s *Server) consumeAuthCode(code string, now time.Time) (string, bool) {
	s.naAuthCodesMu.Lock()
	defer s.naAuthCodesMu.Unlock()
	ac, ok := s.naAuthCodes[code]
	delete(s.naAuthCodes, code)
	if !ok || now.After(ac.expiresAt) {
		return "", false
	}
	return ac.userID, true
}

// naRefreshTokenTTL is not settled by any capture -- refresh_token is opaque,
// so nothing about its lifetime is visible on the wire. Long enough to
// outlive the access tokens it refreshes by a wide margin, which is the one
// property a refresh token needs to be useful at all; 30 days is a
// defensible default rather than a confirmed one.
const naRefreshTokenTTL = 30 * 24 * time.Hour

// storeNARefreshToken and consumeNARefreshToken keep naToken's refresh
// tokens in the database.
//
// The token is a plain opaque string, not a JWT: refresh_token from this
// endpoint is 64 characters, unlike every other token minted alongside it, so
// there is nothing in it to verify and the server has to remember it. Keeping
// it in process memory would log every console out of its Nintendo Account on
// every restart or deploy: the next refresh answers 400 invalid_grant and the
// console shows the login page again. Only its SHA-256 is stored.
//
// A refresh token is multi-use (consuming one issues a new access token but
// does not invalidate it, ordinary OAuth refresh semantics), so consuming does
// not delete it.
func (s *Server) storeNARefreshToken(ctx context.Context, token, userID string, scope []string, now time.Time) error {
	return s.store.CreateNARefreshToken(ctx, db.NARefreshToken{
		TokenSHA256: naRefreshTokenHash(token),
		UserID:      userID,
		Scope:       scope,
		ExpiresAt:   now.Add(naRefreshTokenTTL),
		CreatedAt:   now,
	})
}

// consumeNARefreshToken returns the refresh token's user and the scopes it was
// granted, or ok=false if it is unknown or expired.
func (s *Server) consumeNARefreshToken(ctx context.Context, token string, now time.Time) (userID string, scope []string, ok bool, err error) {
	if token == "" {
		return "", nil, false, nil
	}
	rt, err := s.store.GetNARefreshToken(ctx, naRefreshTokenHash(token), now)
	switch {
	case errors.Is(err, db.ErrNotFound):
		return "", nil, false, nil
	case err != nil:
		return "", nil, false, err
	}
	return rt.UserID, rt.Scope, true, nil
}

func naRefreshTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// naAuthorizePage renders the login form. GET /connect/1.0.0/authorize.
//
// client_id, redirect_uri, state, response_type and nonce are echoed back
// verbatim as hidden fields -- nonce so it can round-trip into the id_token's
// own 'nonce' claim, matching the OIDC spec (present, but empty, when the console
// sent none). scope, prompt and theme are accepted and discarded -- nothing else
// changes what this page does. redirect_uri is trusted as given rather than
// checked against an allowlist: this is a private, operator-run server reachable
// only by consoles the operator already controls DNS for, not a public OAuth
// provider guarding against open-redirect abuse from arbitrary third parties.
//
// The console can construct other authorize URLs than the ordinary link flow:
// prompt=login (plain re-auth), scope=phone_verification with prompt=phone
// (2FA), and scope=transferNaToken with prompt=email_transfer_upload/download
// (the account-email-transfer step of console migration -- see
// internal/nintendo/hosts.go's migration.%.scsi... entry). None of these change
// what this page needs to do: scope/prompt are already accepted-and-discarded,
// and nothing depends on a specific prompt value rather than on whether an
// Authorization header is present (see naAuthorizeSubmit's own comment for why
// that is the one signal branched on).
func (s *Server) naAuthorizePage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	writeHTML(w, http.StatusOK, naAuthorizeHTML(q.Get("client_id"), q.Get("redirect_uri"), q.Get("state"), q.Get("response_type"), q.Get("nonce"), ""))
}

// naAuthorizeSubmit checks the submitted credentials and redirects through the custom
// URI scheme.
//
// It always includes id_token and session_state in the fragment, regardless of the
// requested response_type: response_type=code and response_type=code+id_token both get
// the identical four-field fragment (state, session_state, code, id_token) back from a
// working server, and the code exchange at POST /connect/1.0.0/api/token happens either way.
//
// A second shape reaches this same endpoint: a silent, credential-less
// re-authorization for a DIFFERENT client_id (one whose redirect_uri is
// nintendo://e-license.nx.sys, not the account-link client this file was built around),
// carrying no username/password form fields at all, only an `Authorization: Bearer
// <access_token>` header (the same access_token naToken's authorization_code grant just
// minted) and prompt=none, the OIDC value meaning "do not show any UI; if the caller is
// not already authenticated, fail rather than prompt." An empty username there would
// reach GetUserByLoginUsername("") and answer "Wrong username or password", a login
// PAGE's error, wrong for a request whose whole point was never showing one. So this
// branches on the Authorization header's presence rather than on the literal string
// "none" in prompt, since that is the one signal actually seen on the wire; a request
// bearing neither credentials nor a bearer token still gets the credential path below,
// and correctly fails it.
//
// POST /connect/1.0.0/authorize.
func (s *Server) naAuthorizeSubmit(w http.ResponseWriter, r *http.Request) {
	form, err := parseForm(r)
	if err != nil {
		writeHTML(w, http.StatusBadRequest, naAuthorizeHTML("", "", "", "", "", "Could not read that form submission."))
		return
	}

	clientID := form.Get("client_id")
	redirectURI := form.Get("redirect_uri")
	state := form.Get("state")
	responseType := form.Get("response_type")
	nonce := form.Get("nonce")
	username := form.Get("username")
	password := form.Get("password")

	if redirectURI == "" {
		writeHTML(w, http.StatusBadRequest, naAuthorizeHTML(clientID, redirectURI, state, responseType, nonce, "Missing redirect_uri."))
		return
	}

	var userID string
	if bearer := r.Header.Get("Authorization"); username == "" && password == "" && bearer != "" {
		// The silent, already-authenticated path: an existing na access_token
		// stands in for fresh credentials, exactly as naUsersMe and
		// naQRCodeParam already accept one in place of a session cookie.
		scheme, token, found := strings.Cut(bearer, " ")
		var claims sjwt.BAASTokenClaims
		if !found || !strings.EqualFold(scheme, "Bearer") ||
			s.idVerifier.Verify(token, &claims) != nil || claims.Type != "token" {
			s.log.Warn("nalink: silent re-authorization with an invalid or expired access token", "client_id", clientID)
			s.redirectNAOAuthError(w, redirectURI, state, "login_required")
			return
		}
		userID = claims.Subject
	} else {
		user, err := s.store.GetUserByLoginUsername(r.Context(), username)
		if err != nil {
			s.log.Warn("nalink: login for unknown username", "username", username)
			writeHTML(w, http.StatusUnauthorized, naAuthorizeHTML(clientID, redirectURI, state, responseType, nonce, "Wrong username or password."))
			return
		}
		if subtle.ConstantTimeCompare(
			[]byte(bcryptOK(user.LoginPasswordHash, password)), []byte("1")) != 1 {
			s.log.Warn("nalink: login with wrong password", "username", username)
			writeHTML(w, http.StatusUnauthorized, naAuthorizeHTML(clientID, redirectURI, state, responseType, nonce, "Wrong username or password."))
			return
		}
		userID = user.ID
	}

	now := s.now()
	code, err := newHexID(16)
	if err != nil {
		writeInternalError(s.log, w, r, "generate authorization code", err)
		return
	}
	s.storeAuthCode(code, userID, now)

	idToken, err := s.naIDIssuer.Sign(sjwt.BAASTokenClaims{
		Subject:  userID,
		Audience: clientID,
		IssuedAt: now.Unix(),
		Expires:  now.Add(5 * time.Minute).Unix(),
		Issuer:   naIssuer,
		TokenID:  sjwt.NewTokenID(),
		CHash:    oidcHash(code),
		Nonce:    &nonce,
	})
	if err != nil {
		writeInternalError(s.log, w, r, "sign na id token", err)
		return
	}
	sessionState, err := newHexID(16)
	if err != nil {
		writeInternalError(s.log, w, r, "generate session state", err)
		return
	}

	frag := url.Values{}
	if state != "" {
		frag.Set("state", state)
	}
	frag.Set("code", code)
	frag.Set("session_state", sessionState)
	frag.Set("id_token", idToken)

	location := redirectURI + "#" + frag.Encode()
	s.log.Info("nalink: login succeeded, redirecting", "user", userID, "redirect_uri", redirectURI, "response_type", responseType)
	// This redirect carries the code and id_token, so it gets the same
	// no-cache headers writeJSON gives every other response in this package
	// -- a real capture showed them present here too, and this is the one
	// response in nalink.go that bypasses writeJSON entirely (no body, just
	// a Location header), so it never got them automatically.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store, no-cache")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Location", location)
	w.WriteHeader(http.StatusFound)
}

// bcryptOK returns "1" if password matches hash, "" otherwise — shaped this
// way so the caller can compare it with subtle.ConstantTimeCompare rather
// than branching directly on bcrypt's own result, matching this codebase's
// existing style for credential checks (see completeLogin in accounts.go).
func bcryptOK(hash, password string) string {
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil {
		return "1"
	}
	return ""
}

// naToken issues tokens for POST /connect/1.0.0/api/token.
//
// access_token, id_token and refresh_token here are all signed with naIDIssuer's key,
// the same one naAuthorizeSubmit signs its id_token with -- not accessIssuer, the BAAS
// host's key. A real server's access_token and id_token from this exact endpoint share
// one kid, both published at accounts.nintendo.com's own JWKS. Signing access_token with
// accessIssuer gives it iss=accounts.nintendo.com but a jku pointing at the BAAS host,
// the same iss/jku mismatch already fixed once for the id_token (see NAIDTokenJWKSURL's
// doc comment).
//
// Two grant types, two different response shapes:
//
//   - authorization_code: exchanges the code minted by naAuthorizeSubmit for
//     access_token, id_token, refresh_token, session_token, expires_in, scope and
//     token_type. The scope returned is whatever the request's own "scope" form field
//     asked for (a space-separated OAuth2 scope string), not the "scope=nx" on the
//     front-channel authorize call: those are two independent parameters in the real
//     flow. id_token is required here, not just on the front-channel redirect:
//     LoginApplet's first-time-link request uses response_type=code only, so no
//     id_token ever arrives via the redirect fragment, and omitting it from this
//     response produced error 2124-3122.
//   - refresh_token: the response is deliberately smaller -- access_token, expires_in,
//     scope, token_type only, no new refresh_token or id_token. Looks the presented
//     refresh_token up in naRefreshTokens rather than verifying a signature:
//     refresh_token is a plain opaque string here (see naRefreshTokens' doc comment),
//     not a JWT, so there is no signature to check.
//
// naScopeIDs maps an OAuth scope string to the numeric id accounts.nintendo.com uses for
// it in an access_token's 'ac:scp' claim. See BAASTokenClaims.ACScopes for where these
// came from and what is still unknown.
var naScopeIDs = map[string]int{
	"openid":             0,
	"offline":            1,
	"napps":              4,
	"urn:oauth:init-sso": 5,
	"user":               8,
	"user.birthday":      9,
	"user.email":         10,
	"user.links":         11,
	"user.links[].id":    12,
	"user.terms":         13,
	"user.screenName":    23,
	"user.loginId":       28,
}

// naScopeIDsFor maps the requested scope strings to their numeric ids, dropping
// any this server has no id for rather than inventing one -- an unknown scope
// is a gap in the capture, not a value to guess at.
func naScopeIDsFor(scope []string) []int {
	ids := make([]int, 0, len(scope))
	for _, s := range scope {
		if id, ok := naScopeIDs[s]; ok {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return ids
}

func (s *Server) naToken(w http.ResponseWriter, r *http.Request) {
	form, err := parseForm(r)
	if err != nil {
		writeNAOAuthError(w, http.StatusBadRequest, "invalid_request")
		return
	}

	now := s.now()
	clientID := form.Get("client_id")
	scope := strings.Fields(form.Get("scope"))

	switch form.Get("grant_type") {
	case "authorization_code":
		userID, ok := s.consumeAuthCode(form.Get("code"), now)
		if !ok {
			s.log.Warn("nalink: token exchange with an unknown or expired code")
			writeNAOAuthError(w, http.StatusBadRequest, "invalid_grant")
			return
		}

		accessToken, err := s.naIDIssuer.Sign(sjwt.BAASTokenClaims{
			Subject:  userID,
			Audience: clientID,
			IssuedAt: now.Unix(),
			Expires:  now.Add(15 * time.Minute).Unix(),
			Issuer:   naIssuer,
			TokenID:  sjwt.NewTokenID(),
			Type:     "token",
			// 4 is the only value ever observed, for exactly this grant.
			// UNCONFIRMED: what other grants map to.
			ACGrantType: 4,
			ACScopes:    naScopeIDsFor(scope),
		})
		if err != nil {
			writeInternalError(s.log, w, r, "sign na access token", err)
			return
		}

		// at_hash binds this id_token to the accessToken just signed above, so
		// it has to be computed from that exact compact JWT string -- see
		// oidcHash's doc comment for the algorithm.
		idToken, err := s.naIDIssuer.Sign(sjwt.BAASTokenClaims{
			Subject:  userID,
			Audience: clientID,
			IssuedAt: now.Unix(),
			Expires:  now.Add(5 * time.Minute).Unix(),
			Issuer:   naIssuer,
			TokenID:  sjwt.NewTokenID(),
			ATHash:   oidcHash(accessToken),
		})
		if err != nil {
			writeInternalError(s.log, w, r, "sign na id token", err)
			return
		}

		refreshToken, err := newHexID(32)
		if err != nil {
			writeInternalError(s.log, w, r, "generate na refresh token", err)
			return
		}
		if err := s.storeNARefreshToken(r.Context(), refreshToken, userID, scope, now); err != nil {
			writeInternalError(s.log, w, r, "store na refresh token", err)
			return
		}

		sessionToken, err := newHexID(32)
		if err != nil {
			writeInternalError(s.log, w, r, "generate na session token", err)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"access_token":  accessToken,
			"id_token":      idToken,
			"refresh_token": refreshToken,
			"session_token": sessionToken,
			"expires_in":    900,
			"scope":         scope,
			"token_type":    "Bearer",
		})

	case "refresh_token":
		userID, granted, ok, err := s.consumeNARefreshToken(r.Context(), form.Get("refresh_token"), now)
		if err != nil {
			writeInternalError(s.log, w, r, "load na refresh token", err)
			return
		}
		if !ok {
			s.log.Warn("nalink: token refresh with an unknown or expired refresh_token")
			writeNAOAuthError(w, http.StatusBadRequest, "invalid_grant")
			return
		}
		// The console's refresh request carries no scope at all, and a real answer
		// restates the original grant's twelve scopes, with the matching ac:scp in the
		// access token. Echoing the empty request would answer scope: [] and an access
		// token with no ac:scp.
		if len(scope) == 0 && len(granted) > 0 {
			scope = granted
		}

		accessToken, err := s.naIDIssuer.Sign(sjwt.BAASTokenClaims{
			Subject:  userID,
			Audience: clientID,
			IssuedAt: now.Unix(),
			Expires:  now.Add(15 * time.Minute).Unix(),
			Issuer:   naIssuer,
			TokenID:  sjwt.NewTokenID(),
			Type:     "token",
			// Same shape as the authorization_code branch's access_token:
			// the grant that minted it differs, what it authorises does not.
			ACGrantType: 4,
			ACScopes:    naScopeIDsFor(scope),
		})
		if err != nil {
			writeInternalError(s.log, w, r, "sign na access token", err)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": accessToken,
			"expires_in":   900,
			"scope":        scope,
			"token_type":   "Bearer",
		})

	default:
		writeNAOAuthError(w, http.StatusBadRequest, "invalid_grant")
	}
}

// naUsersMe answers the profile fetch nnAccount makes immediately after the
// token exchange. GET /2.0.0/users/me, on api.accounts.nintendo.com -- a different
// host from the rest of this file. It is authenticated the same way as the rest
// of BAAS (Authorization: Bearer <access_token>).
//
// The response must carry the full shape (~20 fields), not only the 6 fields
// GRID0+ tracks: a partial response is a 200 that nginx logs as successful, but
// the console still errors (2124-3122) and never makes the further calls that
// follow a complete one (a second token refresh, another /2.0.0/users/me, then
// /api/1.0.0/users/{id}/qrcode_param). Two fields specifically are substituted
// rather than passed through: birthday and country. accounts.go/provision.go's
// "0000-00-00" and "" are BAAS's own established sentinels for "unset" on a
// console-registered device profile, correct there -- but this is a different,
// stricter API surface (a real Nintendo Account profile, not an anonymous BAAS
// device profile), and those sentinels are exactly the kind of value a parser
// expecting a real date and a real ISO country code would reject.
func (s *Server) naUsersMe(w http.ResponseWriter, r *http.Request) {
	header := r.Header.Get("Authorization")
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
		writeNAOAuthError(w, http.StatusUnauthorized, "invalid_token")
		return
	}

	// This Bearer token is the access_token naToken minted, signed with
	// naIDIssuer's key (accounts.nintendo.com), not accessIssuer's (the BAAS
	// host) -- see naToken's doc comment for why. Verify against the same
	// key it was actually signed with.
	var claims sjwt.BAASTokenClaims
	if err := s.idVerifier.Verify(token, &claims); err != nil || claims.Type != "token" {
		writeNAOAuthError(w, http.StatusUnauthorized, "invalid_token")
		return
	}

	user, err := s.store.GetUser(r.Context(), claims.Subject)
	if err != nil {
		writeNAOAuthError(w, http.StatusUnauthorized, "invalid_token")
		return
	}

	birthday := user.Birthday
	if birthday == "" || birthday == "0000-00-00" {
		birthday = "2000-01-01"
	}
	country := user.Country
	if country == "" {
		country = "US"
	}
	createdAt := user.CreatedAt.Unix()
	updatedAt := user.UpdatedAt.Unix()

	iconURI := user.ThumbnailURL
	if iconURI == "" {
		iconURI, _ = s.defaultAvatarURLOrEmpty()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":       user.ID,
		"nickname": user.Nickname,
		"birthday": birthday,
		"country":  country,
		"iconUri":  iconURI,
		"language": "en-US",
		"region":   nil,
		"gender":   "unknown",
		"isChild":  false,

		"email":         "switchnet@example.com",
		"emailVerified": true,
		"loginId":       nil,
		// The screenName is a masked email, "ne•••@e••••", not a plain literal. Matching that
		// shape here rather than a bare word, in case the console validates it as one before
		// locally recording the link (real accounts persist isNnLinked and this profile data in
		// _user.json, System Save Data 8000000000000010 -- a step our server-side response can
		// influence but never observe failing).
		"screenName": "sw•••@e••••",

		"createdAt": createdAt,
		"updatedAt": updatedAt,

		"links":                  map[string]any{},
		"phoneNumberEnabled":     false,
		"termsAgreementRequired": false,
		"agreedTerms": map[string]any{
			"EULA":          map[string]any{"agreedAt": createdAt, "country": country, "version": 1},
			"privacyPolicy": map[string]any{"agreedAt": createdAt, "country": country, "version": 1},
		},

		"clientFriendsOptedIn":          true,
		"clientFriendsOptedInUpdatedAt": createdAt,
		"analyticsOptedIn":              false,
		"analyticsOptedInUpdatedAt":     createdAt,
		"analyticsPermissions": map[string]any{
			"dataCollection":   naPermissionBlock(createdAt, false),
			"internalAnalysis": naPermissionBlock(createdAt, true),
			"targetMarketing":  naPermissionBlock(createdAt, false),
		},
		"emailOptedIn":          false,
		"emailOptedInUpdatedAt": createdAt,
		"eachEmailOptedIn": map[string]any{
			"deals":  map[string]any{"optedIn": false, "updatedAt": createdAt},
			"survey": map[string]any{"optedIn": false, "updatedAt": createdAt},
		},

		"timezone": map[string]any{
			"id": "UTC", "name": "UTC", "utcOffset": "+00:00", "utcOffsetSeconds": 0,
		},
	})
}

// naPermissionBlock is one entry of /2.0.0/users/me's analyticsPermissions,
// repeated three times in the real capture with only the permitted value
// differing.
func naPermissionBlock(updatedAt int64, permitted bool) map[string]any {
	return map[string]any{
		"editable":      map[string]any{"admin": false, "self": true},
		"permitted":     permitted,
		"updatedAt":     updatedAt,
		"userConfirmed": true,
	}
}

// naQRCodeParam answers GET /api/1.0.0/users/{id}/qrcode_param. Note the path:
// /api/1.0.0/, not /1.0.0/; that is real, not a typo carried over from elsewhere in
// this file. The account SDK calls this right after naUsersMe.
//
// The real response shape is {"exp", "param", "userId"}. exp is a Unix timestamp
// 604800 seconds (exactly 7 days) after the response's own issue time -- an exact
// duration, safe to reuse. "param" is NOT an opaque blob the way nxAccount is: it is
// plain text, a URL, https://accounts.nintendo.com/qr_verify?sig=...&iat=...&aud=...&sub=...
// &exp=..., evidently meant to become a QR code a second console scans to add the same
// account without retyping credentials. The shape (which query parameters, in what
// order) is real; "sig" is not -- it is presumably an HMAC keyed with a secret only the
// account server holds, and never forging a Nintendo-issued credential applies exactly as
// much to a private signing key as to Nintendo's: this project does not have it and does
// not invent a plausible-looking stand-in for it. sig is left an honest opaque
// placeholder.
//
// UNCONFIRMED: whether anything ever calls qr_verify with this value. No qr_verify
// route exists here for exactly that reason: building one to accept a signature this
// project can never produce correctly would be answering a question with an invented
// fact, not a real capability.
func (s *Server) naQRCodeParam(w http.ResponseWriter, r *http.Request) {
	header := r.Header.Get("Authorization")
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
		writeNAOAuthError(w, http.StatusUnauthorized, "invalid_token")
		return
	}
	var claims sjwt.BAASTokenClaims
	if err := s.idVerifier.Verify(token, &claims); err != nil || claims.Type != "token" {
		writeNAOAuthError(w, http.StatusUnauthorized, "invalid_token")
		return
	}

	id := r.PathValue("id")
	if id == "" || !strings.EqualFold(id, claims.Subject) {
		writeNAOAuthError(w, http.StatusForbidden, "access_denied")
		return
	}

	sig, err := newHexID(32)
	if err != nil {
		writeInternalError(s.log, w, r, "generate qrcode_param signature placeholder", err)
		return
	}
	now := s.now()
	exp := now.Add(7 * 24 * time.Hour)
	param := fmt.Sprintf(
		"https://accounts.nintendo.com/qr_verify?sig=%s&iat=%d&aud=%s&sub=%s&exp=%d",
		sig, now.Unix(), claims.Audience, id, exp.Unix(),
	)

	writeJSON(w, http.StatusOK, map[string]any{
		"exp":    exp.Unix(),
		"param":  param,
		"userId": id,
	})
}

func writeNAOAuthError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":%q}`, code)
}

// redirectNAOAuthError answers a failed silent (prompt=none) re-
// authorization the OIDC-standard way: the error goes into the SAME
// fragment position a success would have used state/code/id_token, per the
// spec's own "Authentication Error Response" -- not this project's own
// invention, unlike the fields nxAccountBlob and naQRCodeParam's "param"
// leave as honest placeholders. No real capture shows this exact failure
// (the one capture this file is built from only shows the success path),
// so this is the one piece of naAuthorizeSubmit's new branch that rests on
// a public specification rather than a byte-for-byte real response.
func (s *Server) redirectNAOAuthError(w http.ResponseWriter, redirectURI, state, code string) {
	frag := url.Values{}
	if state != "" {
		frag.Set("state", state)
	}
	frag.Set("error", code)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store, no-cache")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Location", redirectURI+"#"+frag.Encode())
	w.WriteHeader(http.StatusFound)
}

func naAuthorizeHTML(clientID, redirectURI, state, responseType, nonce, problem string) string {
	var problemHTML string
	if problem != "" {
		problemHTML = fmt.Sprintf(`<p class="problem">%s</p>`, html.EscapeString(problem))
	}
	return fmt.Sprintf(`<!doctype html><html><head><meta charset="utf-8">`+
		`<title>Nintendo Account</title><style>%s</style></head><body>`+
		`<h1>Sign in to link this profile</h1>%s`+
		`<form method="post" action="/connect/1.0.0/authorize">`+
		`<input type="hidden" name="client_id" value="%s">`+
		`<input type="hidden" name="redirect_uri" value="%s">`+
		`<input type="hidden" name="state" value="%s">`+
		`<input type="hidden" name="response_type" value="%s">`+
		`<input type="hidden" name="nonce" value="%s">`+
		`<input name="username" placeholder="GRID0+ username" autocomplete="username" required>`+
		`<input name="password" type="password" placeholder="Password" `+
		`autocomplete="current-password" required>`+
		`<button type="submit">Sign in</button>`+
		`</form></body></html>`,
		htmlPageStyle, problemHTML,
		html.EscapeString(clientID), html.EscapeString(redirectURI), html.EscapeString(state), html.EscapeString(responseType),
		html.EscapeString(nonce))
}
