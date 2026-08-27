package baas

import (
	"crypto/subtle"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	sjwt "github.com/n-popescu/switchnet/internal/jwt"
	"golang.org/x/crypto/bcrypt"
)

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
// What is NOT yet confirmed by that capture, and is deliberately not guessed
// at here: whether POST /1.0.0/users fires automatically after the redirect,
// and how the resulting device account is meant to attach to the existing
// person rather than becoming a new one. See the plan this was built from
// and docs/CAPTURES for the open item. This file only issues the token; nothing
// in accounts.go has been changed to consume it specially.

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

// naAuthorizePage renders the login form. GET /connect/1.0.0/authorize.
//
// client_id, redirect_uri, state and response_type are echoed back verbatim
// as hidden fields; scope, prompt, nonce and theme are accepted and
// discarded — none of them change what this page does. redirect_uri is
// trusted as given rather than checked against an allowlist: this is a
// private, operator-run server reachable only by consoles the operator
// already controls DNS for, not a public OAuth provider guarding against
// open-redirect abuse from arbitrary third parties.
func (s *Server) naAuthorizePage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	writeHTML(w, http.StatusOK, naAuthorizeHTML(q.Get("client_id"), q.Get("redirect_uri"), q.Get("state"), q.Get("response_type"), ""))
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
// POST /connect/1.0.0/authorize.
func (s *Server) naAuthorizeSubmit(w http.ResponseWriter, r *http.Request) {
	form, err := parseForm(r)
	if err != nil {
		writeHTML(w, http.StatusBadRequest, naAuthorizeHTML("", "", "", "", "Could not read that form submission."))
		return
	}

	clientID := form.Get("client_id")
	redirectURI := form.Get("redirect_uri")
	state := form.Get("state")
	responseType := form.Get("response_type")
	username := form.Get("username")
	password := form.Get("password")

	if redirectURI == "" {
		writeHTML(w, http.StatusBadRequest, naAuthorizeHTML(clientID, redirectURI, state, responseType, "Missing redirect_uri."))
		return
	}

	user, err := s.store.GetUserByLoginUsername(r.Context(), username)
	if err != nil {
		s.log.Warn("nalink: login for unknown username", "username", username)
		writeHTML(w, http.StatusUnauthorized, naAuthorizeHTML(clientID, redirectURI, state, responseType, "Wrong username or password."))
		return
	}
	if subtle.ConstantTimeCompare(
		[]byte(bcryptOK(user.LoginPasswordHash, password)), []byte("1")) != 1 {
		s.log.Warn("nalink: login with wrong password", "username", username)
		writeHTML(w, http.StatusUnauthorized, naAuthorizeHTML(clientID, redirectURI, state, responseType, "Wrong username or password."))
		return
	}

	now := s.now()
	code, err := newHexID(16)
	if err != nil {
		writeInternalError(s.log, w, r, "generate authorization code", err)
		return
	}
	s.storeAuthCode(code, user.ID, now)

	idToken, err := s.naIDIssuer.Sign(sjwt.BAASTokenClaims{
		Subject:  user.ID,
		Audience: clientID,
		IssuedAt: now.Unix(),
		Expires:  now.Add(5 * time.Minute).Unix(),
		Issuer:   naIssuer,
		TokenID:  sjwt.NewTokenID(),
		Type:     "id_token",
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
	s.log.Info("nalink: login succeeded, redirecting", "user", user.ID, "redirect_uri", redirectURI, "response_type", responseType)
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
//     or id_token. Verifies the presented refresh_token was signed by
//     SwitchNet (sjwt gives no other way for one to exist) rather than
//     tracking sessions server-side.
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

		accessToken, err := s.accessIssuer.Sign(sjwt.BAASTokenClaims{
			Subject:  userID,
			Audience: clientID,
			IssuedAt: now.Unix(),
			Expires:  now.Add(15 * time.Minute).Unix(),
			Issuer:   naIssuer,
			TokenID:  sjwt.NewTokenID(),
			Type:     "token",
		})
		if err != nil {
			writeInternalError(s.log, w, r, "sign na access token", err)
			return
		}

		idToken, err := s.naIDIssuer.Sign(sjwt.BAASTokenClaims{
			Subject:  userID,
			Audience: clientID,
			IssuedAt: now.Unix(),
			Expires:  now.Add(5 * time.Minute).Unix(),
			Issuer:   naIssuer,
			TokenID:  sjwt.NewTokenID(),
			Type:     "id_token",
		})
		if err != nil {
			writeInternalError(s.log, w, r, "sign na id token", err)
			return
		}

		refreshToken, err := s.accessIssuer.Sign(sjwt.BAASTokenClaims{
			Subject:  userID,
			Audience: clientID,
			IssuedAt: now.Unix(),
			Expires:  now.Add(15 * time.Minute).Unix(),
			Issuer:   naIssuer,
			TokenID:  sjwt.NewTokenID(),
			Type:     "refresh_token",
		})
		if err != nil {
			writeInternalError(s.log, w, r, "sign na refresh token", err)
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
		var claims sjwt.BAASTokenClaims
		if err := s.accessVerifier.Verify(form.Get("refresh_token"), &claims); err != nil || claims.Type != "refresh_token" {
			s.log.Warn("nalink: token refresh with an invalid refresh_token", "error", err)
			writeNAOAuthError(w, http.StatusBadRequest, "invalid_grant")
			return
		}

		accessToken, err := s.accessIssuer.Sign(sjwt.BAASTokenClaims{
			Subject:  claims.Subject,
			Audience: clientID,
			IssuedAt: now.Unix(),
			Expires:  now.Add(15 * time.Minute).Unix(),
			Issuer:   naIssuer,
			TokenID:  sjwt.NewTokenID(),
			Type:     "token",
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

	var claims sjwt.BAASTokenClaims
	if err := s.accessVerifier.Verify(token, &claims); err != nil || claims.Type != "token" {
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
		iconURI = s.defaultAvatarURLOrEmpty()
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

func writeNAOAuthError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":%q}`, code)
}

func naAuthorizeHTML(clientID, redirectURI, state, responseType, problem string) string {
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
		`<input name="username" placeholder="SwitchNet username" autocomplete="username" required>`+
		`<input name="password" type="password" placeholder="Password" `+
		`autocomplete="current-password" required>`+
		`<button type="submit">Sign in</button>`+
		`</form></body></html>`,
		htmlPageStyle, problemHTML,
		html.EscapeString(clientID), html.EscapeString(redirectURI), html.EscapeString(state), html.EscapeString(responseType))
}
