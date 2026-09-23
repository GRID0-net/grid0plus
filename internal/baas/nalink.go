package baas

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	sjwt "github.com/n-popescu/switchnet/internal/jwt"
	"golang.org/x/crypto/bcrypt"
)

// oidcHash implements the OIDC 'at_hash'/'c_hash' claim: SHA-256 of the ASCII
// value, left half, base64url without padding. Both id_tokens in this file
// are always signed RS256, so SHA-256 (rather than the SHA-384/512 the spec
// uses for other algs) is always the right hash here. Verified byte-for-byte
// against a 2026-08-27 capture of a real working exchange.
func oidcHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:len(sum)/2])
}

// The Nintendo Account link stub: what accounts.nintendo.com becomes once it
// is redirected here instead of null-routed.
//
// This does not replicate Nintendo's real OAuth wire protocol — nobody has a
// capture of the real thing, and CLAUDE.md's own rule is not to invent one.
// What it replicates is Nextendo Network's approach, confirmed from a real,
// decrypted capture of their working implementation on 2026-08-25: serve a
// custom login page instead of Nintendo's, and redirect back through the
// custom URI scheme the console's system software is listening on
// (`nintendo://lhub.nx.sys`, observed directly in that capture) with a
// self-issued id_token in the fragment. The console does not verify this
// against anything Nintendo controls — it fetches the verification key from
// whichever host the token's own `jku` claim names, and every Nintendo
// hostname is already ours, so that resolves back to us regardless.
//
// The login itself checks a person's own SwitchNet login credentials
// (website.go), not a shared passphrase — this is meant to attach the link
// to one persistent identity, not mint an anonymous one per attempt.
//
// Both questions this paragraph used to leave open are now settled, by a
// 2026-08-27 capture of a genuinely successful Nextendo link
// (docs/CAPTURES/nintendo-account-link.md): yes, POST /1.0.0/users fires
// automatically right after the redirect, driven by the nnAccount SDK, no
// console-local confirmation step in between; and accounts.go's federation
// handler DOES consume the token this file issues specially -- a successful
// verification against naIDIssuer's own key (only ever true for a token
// this file signed) is what tells completeLogin to reassign the fresh
// device account to the existing person, rather than leaving it attached to
// the disposable identity registerUser had to invent before any id token
// existed. See federation's own doc comment in accounts.go for the real-
// hardware failure that fix was found against.

// authCode is a short-lived, single-use authorization code minted by
// naAuthorize and redeemed by naToken. In-memory and TTL'd: this is
// ephemeral hand-off state between two requests seconds apart, not something
// that needs to survive a restart or be shared across processes.
type authCode struct {
	userID    string
	expiresAt time.Time
	// scope is what a refresh token was granted, restated on every refresh.
	// Unused for authorization codes.
	scope []string
}

const authCodeTTL = 5 * time.Minute

// naIssuer is the 'iss' every token minted by this file carries.
//
// NAIDTokenJWKSURL is that token's 'jku' -- its OWN JWK set URL, distinct from
// the BAAS host's (internal/baas/certificates.go), even though both currently
// resolve to the same handler (the mux does not route on Host). Exported so
// cmd/baas can hand it to the naIDIssuer it constructs, rather than that URL
// existing as a second, independently-typed literal that could drift from
// this one. A real, working private server's capture (2026-08-27) showed
// exactly this: its accounts.nintendo.com id_token had
// jku=https://accounts.nintendo.com/1.0.0/certificates, never the BAAS host's.
// Before this, naIDIssuer (see sjwt.IssuerNAID) reused cfg.BAAS.IDTokenJWKSURL
// -- a different key from the BAAS id_token (correct), published at the *BAAS
// host's* URL regardless (wrong): an id_token whose iss names one host but
// whose jku names a different one for where to verify it.
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

// storeNARefreshToken and consumeNARefreshToken hold naToken's refresh
// tokens the same way storeAuthCode/consumeAuthCode hold its authorization
// codes -- except a refresh token is multi-use (consuming one issues a new
// access token but does not invalidate the refresh token itself, matching
// ordinary OAuth refresh semantics), so consuming does not delete it.
func (s *Server) storeNARefreshToken(token, userID string, scope []string, now time.Time) {
	s.naRefreshTokensMu.Lock()
	defer s.naRefreshTokensMu.Unlock()
	for t, rt := range s.naRefreshTokens {
		if now.After(rt.expiresAt) {
			delete(s.naRefreshTokens, t)
		}
	}
	s.naRefreshTokens[token] = authCode{userID: userID, expiresAt: now.Add(naRefreshTokenTTL), scope: scope}
}

// consumeNARefreshToken returns the refresh token's user and the scopes it was
// granted.
func (s *Server) consumeNARefreshToken(token string, now time.Time) (string, []string, bool) {
	s.naRefreshTokensMu.Lock()
	defer s.naRefreshTokensMu.Unlock()
	rt, ok := s.naRefreshTokens[token]
	if !ok || now.After(rt.expiresAt) {
		return "", nil, false
	}
	return rt.userID, rt.scope, true
}

// naAuthorizePage renders the login form. GET /connect/1.0.0/authorize.
//
// client_id, redirect_uri, state, response_type and nonce are echoed back
// verbatim as hidden fields — nonce so it can round-trip into the id_token's
// own 'nonce' claim, matching the OIDC spec and a real capture (present, but
// empty, when the console sent none). scope, prompt and theme are accepted
// and discarded — nothing else changes what this page does. redirect_uri is
// trusted as given rather than checked against an allowlist: this is a
// private, operator-run server reachable only by consoles the operator
// already controls DNS for, not a public OAuth provider guarding against
// open-redirect abuse from arbitrary third parties.
//
// A 2026-09-20 decompile of the operator's own console's account sysmodule
// found the complete set of authorize URLs it can construct, confirming
// prompt/scope combinations beyond the ordinary link flow this file was
// built around: prompt=login (plain re-auth), scope=phone_verification
// with prompt=phone (2FA), and scope=transferNaToken with
// prompt=email_transfer_upload/download (the account-email-transfer step
// of console migration -- see internal/nintendo/hosts.go's migration.%.
// scsi... entry for the rest of that flow). None of these change what this
// page needs to do: scope/prompt are already accepted-and-discarded, and
// nothing here observed depends on a specific prompt value rather than on
// whether an Authorization header is present (see naAuthorizeSubmit's own
// comment for why that is the one signal branched on). Recorded so a
// future prompt value showing up on the wire is recognized rather than
// mistaken for something new.
func (s *Server) naAuthorizePage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	writeHTML(w, http.StatusOK, naAuthorizeHTML(q.Get("client_id"), q.Get("redirect_uri"), q.Get("state"), q.Get("response_type"), q.Get("nonce"), ""))
}

// naAuthorizeSubmit checks the submitted credentials and redirects through
// the custom URI scheme.
//
// Always includes id_token and session_state in the fragment, regardless of
// the requested response_type. This was made conditional on 2026-08-26,
// guessing that a plain response_type=code request wants a bare code and
// gets confused by an unrequested id_token -- a real, fully decrypted
// capture of a genuinely working link (2026-08-27,
// docs/CAPTURES/raw/nextendo-working-nalink-2026-08-27.flow) proved that
// guess wrong: response_type=code and response_type=code+id_token both got
// the identical four-field fragment (state, session_state, code, id_token)
// back from a real, working private server. Reverted to unconditional.
// through the code exchange at POST /connect/1.0.0/api/token either way.
//
// A SECOND shape reaches this same endpoint, found in the same capture and
// not previously handled at all: a silent, credential-less re-authorization
// for a DIFFERENT client_id (in the capture, one whose redirect_uri was
// nintendo://e-license.nx.sys -- e-license, not the account-link client this
// file was originally built around), carrying no username/password form
// fields at all, only an `Authorization: Bearer <access_token>` header
// (the same access_token naToken's authorization_code grant just minted)
// and prompt=none, the OIDC standard value meaning "do not show any UI; if
// the caller is not already authenticated, fail rather than prompt." Before
// this pass, an empty username here reached GetUserByLoginUsername("") and
// answered "Wrong username or password" -- a login PAGE's error, wrong for a
// request whose whole point was never showing one. Branches on the
// Authorization header's presence rather than on the literal string
// "none" in prompt, since that is the one signal this project has actually
// seen on the wire; a request bearing neither credentials nor a bearer
// token still gets the credential path below, and correctly fails it.
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
// access_token, id_token and refresh_token here are all signed with
// naIDIssuer's key, the same one naAuthorizeSubmit signs its id_token with —
// not accessIssuer, the BAAS host's key. Found 2026-08-27 by decoding a real
// working capture's response
// (docs/CAPTURES/raw/nextendo-working-nalink-2026-08-27.flow): its
// access_token and id_token from this exact endpoint share one kid, both
// published at accounts.nintendo.com's own JWKS. Signing access_token with
// accessIssuer gave it iss=accounts.nintendo.com but a jku pointing at the
// BAAS host — the same iss/jku mismatch already found and fixed once for the
// id_token (see NAIDTokenJWKSURL's doc comment), just missed on this sibling
// field the first time.
//
// Two grant types, two different response shapes — both confirmed byte-for-
// byte against a 2026-08-26 mitmproxy capture of a real first-time
// interactive link against Nextendo (docs/CAPTURES has the full exchange):
//
//   - authorization_code: exchanges the code minted by naAuthorizeSubmit for
//     access_token, id_token, refresh_token, session_token, expires_in,
//     scope and token_type. The scope returned is whatever the request's own
//     "scope" form field asked for (a space-separated OAuth2 scope string),
//     not the "scope=nx" on the front-channel authorize call — those are two
//     independent parameters in the real flow, confirmed by the capture
//     showing a much longer scope list at this step than at authorize time.
//     id_token is required here, not just on the front-channel redirect:
//     LoginApplet's real first-time-link request uses response_type=code
//     only, so no id_token ever arrives via the redirect fragment, and
//     omitting it from this response produced error 2124-3122 on real
//     hardware before this was fixed.
//   - refresh_token: the captured response is deliberately smaller —
//     access_token, expires_in, scope, token_type only, no new refresh_token
//     or id_token. Looks the presented refresh_token up in naRefreshTokens
//     rather than verifying a signature: refresh_token is a plain opaque
//     string here (see naRefreshTokens' doc comment), not a JWT, so there is
//     no signature to check.
//
// naScopeIDs maps an OAuth scope string to the numeric id accounts.nintendo.com
// uses for it in an access_token's 'ac:scp' claim. See BAASTokenClaims.ACScopes
// for where these came from and what is still unknown.
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
			// TODO(capture-needed): what other grants map to.
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
		s.storeNARefreshToken(refreshToken, userID, scope, now)

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
		userID, granted, ok := s.consumeNARefreshToken(form.Get("refresh_token"), now)
		if !ok {
			s.log.Warn("nalink: token refresh with an unknown or expired refresh_token")
			writeNAOAuthError(w, http.StatusBadRequest, "invalid_grant")
			return
		}
		// The console's refresh request carries no scope at all, and a
		// working server's answer (2026-08-27 capture, the refresh right
		// after federation) restates the original grant's twelve scopes,
		// with the matching ac:scp in the access token. Echoing the empty
		// request answered scope: [] and an access token with no ac:scp.
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
// token exchange. GET /2.0.0/users/me, on api.accounts.nintendo.com — a
// different host from the rest of this file, not previously routed here at
// all. Confirmed necessary and confirmed authenticated the same way as the
// rest of BAAS (Authorization: Bearer <access_token>) by the 2026-08-26
// mitmproxy capture.
//
// A first version of this handler returned only the 6 fields SwitchNet
// actually tracks, on the theory that nothing had been observed to need the
// rest. On real hardware that produced a 200 nginx logged as successful, but
// the console still errored (2124-3122) and never made the further calls
// (a second token refresh, another /2.0.0/users/me, then
// /api/1.0.0/users/{id}/qrcode_param) that the same real capture showed
// following a real 1407-byte response with ~20 fields. Matching the full
// shape here is not a guess: every key below is one the capture actually
// showed, not one reasoned about — it is what "the two hardest bugs in this
// codebase were caused by trusting a partial response" would predict in
// hindsight. Two fields specifically are substituted rather than passed
// through: birthday and country. accounts.go/provision.go's "0000-00-00"
// and "" are BAAS's own established sentinels for "unset" on a
// console-registered device profile, correct there — but this is a
// different, stricter API surface (a real Nintendo Account profile, not an
// anonymous BAAS device profile), and those sentinels are exactly the kind
// of value a parser expecting a real date and a real ISO country code would
// reject.
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
		// The real capture's screenName was a masked email, "ne•••@e••••" for
		// nextendo@example.com -- not the plain literal it looked like at a
		// glance. Matching that shape here rather than a bare word, in case
		// the console validates it as one before locally recording the link
		// (real accounts persist isNnLinked and this profile data in
		// _user.json, System Save Data 8000000000000010 -- a step our
		// server-side response can influence but never observe failing).
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

// naQRCodeParam answers GET /api/1.0.0/users/{id}/qrcode_param -- note the
// path: /api/1.0.0/, not /1.0.0/, confirmed real by a 2026-08-27 capture of
// a genuinely successful Nextendo link, not a typo carried over from
// elsewhere in this file. nnAccount calls this right after naUsersMe,
// exactly the "further call" this file's own naUsersMe comment predicted
// once that handler's response matched the real shape -- this pass is what
// confirms the prediction and closes it.
//
// Real response shape, from that same capture: {"exp", "param", "userId"}.
// exp is a real Unix timestamp 604800 seconds (exactly 7 days) after the
// response's own issue time -- an exact duration, safe to reuse; "param" is
// NOT an opaque blob the way nxAccount is. It decoded as plain text: a URL,
// https://accounts.nintendo.com/qr_verify?sig=...&iat=...&aud=...&sub=...
// &exp=..., evidently meant to become a QR code a second console scans to
// add the same account without retyping credentials. The shape (which query
// parameters, in what order) is real; "sig" is not -- it is presumably an
// HMAC keyed with a secret only the account server holds, and CLAUDE.md's
// second ground rule (never forge a Nintendo-issued credential) applies
// exactly as much to Nextendo's own private signing key as to Nintendo's:
// this project does not have it and does not invent a plausible-looking
// stand-in for it. sig is left an honest opaque placeholder.
//
// TODO(capture-needed): whether anything ever calls qr_verify with this
// value -- unconfirmed, since a single console's own capture cannot show a
// SECOND console scanning its QR code. No qr_verify route exists here for
// exactly that reason: building one to accept a signature this project can
// never produce correctly would be answering a question with an invented
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
		`<input name="username" placeholder="SwitchNet username" autocomplete="username" required>`+
		`<input name="password" type="password" placeholder="Password" `+
		`autocomplete="current-password" required>`+
		`<button type="submit">Sign in</button>`+
		`</form></body></html>`,
		htmlPageStyle, problemHTML,
		html.EscapeString(clientID), html.EscapeString(redirectURI), html.EscapeString(state), html.EscapeString(responseType),
		html.EscapeString(nonce))
}
