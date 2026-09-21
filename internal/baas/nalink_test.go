package baas

import (
	"bytes"
	"image/jpeg"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/n-popescu/switchnet/internal/db"
	sjwt "github.com/n-popescu/switchnet/internal/jwt"
	"golang.org/x/crypto/bcrypt"
)

// registerLoginUser creates a person via CreateUserWithLogin directly
// (bypassing the website handler, which has its own tests).
func registerLoginUser(t *testing.T, h *harness, username, password string) db.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	now := h.clock()
	id, err := newHexID(8)
	if err != nil {
		t.Fatal(err)
	}
	u := db.User{
		ID:                id,
		Nickname:          username,
		Birthday:          "0000-00-00",
		Permissions:       db.DefaultPermissions(now),
		Extras:            db.EmptyExtras(),
		BlocksUpdatedAt:   now,
		FriendsUpdatedAt:  now,
		CreatedAt:         now,
		UpdatedAt:         now,
		LoginUsername:     username,
		LoginPasswordHash: string(hash),
	}
	if err := h.store.CreateUserWithLogin(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

// noRedirectClient returns a client that does not follow redirects —
// naAuthorizeSubmit's redirect target is a custom URI scheme
// (nintendo://...) that Go's http.Client cannot dial, so it has to be
// inspected via the Location header instead of followed.
func noRedirectClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

func TestNAAuthorizePageRenders(t *testing.T) {
	h := newHarness(t)
	resp, err := http.Get(h.server.URL + "/connect/1.0.0/authorize?client_id=abc&redirect_uri=nintendo%3A%2F%2Flhub.nx.sys&state=xyz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	requireStatus(t, resp, http.StatusOK)
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `value="abc"`) {
		t.Error("page does not echo client_id back as a hidden field")
	}
	if !strings.Contains(string(body), "nintendo://lhub.nx.sys") {
		t.Error("page does not echo redirect_uri back as a hidden field")
	}
	// A real capture of this exact page showed both present; missing prior to
	// 2026-08-28.
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want %q", got, "no-store")
	}
	if got := resp.Header.Get("Pragma"); got != "no-cache" {
		t.Errorf("Pragma = %q, want %q", got, "no-cache")
	}
}

func TestNAAuthorizeCorrectCredentialsRedirectWithAToken(t *testing.T) {
	h := newHarness(t)
	registerLoginUser(t, h, "alice", "hunter2222")

	client := noRedirectClient()
	form := url.Values{
		"client_id":    {"f4e5f2f3e022208b"},
		"redirect_uri": {"nintendo://lhub.nx.sys"},
		"state":        {"c14c7273054e5894"},
		"nonce":        {"a-console-chosen-nonce"},
		"username":     {"alice"},
		"password":     {"hunter2222"},
	}
	resp, err := client.PostForm(h.server.URL+"/connect/1.0.0/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	requireStatus(t, resp, http.StatusFound)

	// This redirect carries the code and id_token, so a real capture showed
	// it with the same no-cache headers every JSON response gets -- missing
	// prior to 2026-08-28, because this is the one response in nalink.go
	// that bypasses writeJSON (no body, just a Location header).
	if got := resp.Header.Get("Cache-Control"); got != "no-store, no-cache" {
		t.Errorf("Cache-Control = %q, want %q", got, "no-store, no-cache")
	}
	if got := resp.Header.Get("Pragma"); got != "no-cache" {
		t.Errorf("Pragma = %q, want %q", got, "no-cache")
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}

	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "nintendo://lhub.nx.sys#") {
		t.Fatalf("Location = %q, want the nintendo:// scheme with a fragment", loc)
	}
	frag, err := url.ParseQuery(strings.TrimPrefix(loc, "nintendo://lhub.nx.sys#"))
	if err != nil {
		t.Fatal(err)
	}
	if frag.Get("state") != "c14c7273054e5894" {
		t.Errorf("state = %q", frag.Get("state"))
	}
	code := frag.Get("code")
	if code == "" {
		t.Error("no code in the redirect fragment")
	}
	if frag.Get("id_token") == "" {
		t.Fatal("no id_token in the redirect fragment")
	}

	// The id_token has to round-trip through the exact parsing federation
	// uses (ParseUnverified into BAASTokenClaims, reading .Subject) — that is
	// the one invariant the whole design depends on.
	var claims sjwt.BAASTokenClaims
	if err := sjwt.ParseUnverified(frag.Get("id_token"), &claims); err != nil {
		t.Fatalf("id_token does not parse as BAASTokenClaims: %v", err)
	}
	if claims.Subject == "" {
		t.Error("id_token has no subject")
	}

	// c_hash binds this id_token to the code alongside it, per the OIDC spec.
	// nonce echoes what the console sent on the original authorize request.
	// Both verified byte-for-byte against a real capture (oidcHash's doc
	// comment); neither existed before 2026-08-27.
	_, payload := decodeTokenParts(t, frag.Get("id_token"))
	if got, want := payload["c_hash"], oidcHash(code); got != want {
		t.Errorf("id_token c_hash = %v, want %v", got, want)
	}
	if got, want := payload["nonce"], "a-console-chosen-nonce"; got != want {
		t.Errorf("id_token nonce = %v, want %q", got, want)
	}
	if _, present := payload["typ"]; present {
		t.Errorf("id_token payload carries typ = %v, want it entirely absent", payload["typ"])
	}
}

// TestNAAuthorizeAlwaysIncludesIDTokenAndSessionState guards against
// re-introducing a bug that shipped 2026-08-26 and cost a full day to find:
// making id_token/session_state conditional on response_type, on the guess
// that a plain response_type=code request wants a bare code and gets
// confused by an unrequested id_token. A 2026-08-27 capture of a genuinely
// successful link
// (docs/CAPTURES/raw/nextendo-working-nalink-2026-08-27.flow) proved that
// guess wrong: response_type=code and response_type=code+id_token both got
// back the identical four-field fragment (state, session_state, code,
// id_token) from a real, working private server. Always include both,
// regardless of response_type.
func TestNAAuthorizeAlwaysIncludesIDTokenAndSessionState(t *testing.T) {
	h := newHarness(t)
	registerLoginUser(t, h, "alice", "hunter2222")

	client := noRedirectClient()
	form := url.Values{
		"client_id":     {"6ffd70c434d303c8"},
		"redirect_uri":  {"nintendo://account.nx.sys"},
		"response_type": {"code"},
		"state":         {"c14c7273054e5894"},
		"username":      {"alice"},
		"password":      {"hunter2222"},
	}
	resp, err := client.PostForm(h.server.URL+"/connect/1.0.0/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	requireStatus(t, resp, http.StatusFound)

	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "nintendo://account.nx.sys#") {
		t.Fatalf("Location = %q, want the nintendo:// scheme with a fragment", loc)
	}
	frag, err := url.ParseQuery(strings.TrimPrefix(loc, "nintendo://account.nx.sys#"))
	if err != nil {
		t.Fatal(err)
	}
	if frag.Get("code") == "" {
		t.Error("no code in the redirect fragment")
	}
	if frag.Get("id_token") == "" {
		t.Error("no id_token in the redirect fragment for response_type=code -- " +
			"a real working server sends one even here, see the test comment")
	}
	if frag.Get("session_state") == "" {
		t.Error("no session_state in the redirect fragment for response_type=code -- " +
			"a real working server sends one even here, see the test comment")
	}
}

func TestNAAuthorizeWrongPasswordDoesNotRedirect(t *testing.T) {
	h := newHarness(t)
	registerLoginUser(t, h, "alice", "hunter2222")

	client := noRedirectClient()
	form := url.Values{
		"redirect_uri": {"nintendo://lhub.nx.sys"},
		"username":     {"alice"},
		"password":     {"wrongpassword"},
	}
	resp, err := client.PostForm(h.server.URL+"/connect/1.0.0/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	requireStatus(t, resp, http.StatusUnauthorized)
	if resp.Header.Get("Location") != "" {
		t.Error("a wrong password produced a Location header")
	}
}

func TestNAAuthorizeUnknownUsernameDoesNotRedirect(t *testing.T) {
	h := newHarness(t)

	client := noRedirectClient()
	form := url.Values{
		"redirect_uri": {"nintendo://lhub.nx.sys"},
		"username":     {"nobody"},
		"password":     {"whatever1"},
	}
	resp, err := client.PostForm(h.server.URL+"/connect/1.0.0/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	requireStatus(t, resp, http.StatusUnauthorized)
}

func TestNATokenExchangesACodeForAnAccessToken(t *testing.T) {
	h := newHarness(t)
	registerLoginUser(t, h, "alice", "hunter2222")

	client := noRedirectClient()
	authorizeResp, err := client.PostForm(h.server.URL+"/connect/1.0.0/authorize", url.Values{
		"redirect_uri": {"nintendo://lhub.nx.sys"},
		"username":     {"alice"},
		"password":     {"hunter2222"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer authorizeResp.Body.Close()
	loc := authorizeResp.Header.Get("Location")
	frag, err := url.ParseQuery(strings.TrimPrefix(loc, "nintendo://lhub.nx.sys#"))
	if err != nil {
		t.Fatal(err)
	}
	code := frag.Get("code")
	if code == "" {
		t.Fatal("no code to exchange")
	}

	tokenResp, err := http.PostForm(h.server.URL+"/connect/1.0.0/api/token", url.Values{
		"grant_type": {"authorization_code"},
		"code":       {code},
		"client_id":  {"f4e5f2f3e022208b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tokenResp.Body.Close()
	requireStatus(t, tokenResp, http.StatusOK)

	var body struct {
		AccessToken  string   `json:"access_token"`
		IDToken      string   `json:"id_token"`
		RefreshToken string   `json:"refresh_token"`
		SessionToken string   `json:"session_token"`
		ExpiresIn    int      `json:"expires_in"`
		Scope        []string `json:"scope"`
		TokenType    string   `json:"token_type"`
	}
	decode(t, tokenResp, &body)
	if body.AccessToken == "" {
		t.Error("no access_token")
	}
	// A real console's first-time interactive link uses response_type=code
	// only, so no id_token ever arrives via the redirect fragment — it has to
	// come from here, or the console has an access token but no way to learn
	// which account it belongs to (this was error 2124-3122 on real
	// hardware, 2026-08-26, before id_token was added to this response).
	if body.IDToken == "" {
		t.Error("no id_token")
	}
	if body.RefreshToken == "" {
		t.Error("no refresh_token")
	}
	// refresh_token must be opaque, not a JWT: a real working capture showed
	// one 64 characters long with no dots, unlike every other token minted
	// alongside it. Signing it as a JWT here shipped 2026-08-26 and was found
	// and fixed 2026-08-27.
	if strings.Count(body.RefreshToken, ".") == 2 {
		t.Errorf("refresh_token looks like a JWT (%q), want an opaque string", body.RefreshToken)
	}
	if len(body.RefreshToken) != 64 {
		t.Errorf("refresh_token is %d characters, want 64 (newHexID(32))", len(body.RefreshToken))
	}
	if body.SessionToken == "" {
		t.Error("no session_token")
	}
	if body.TokenType != "Bearer" {
		t.Errorf("token_type = %q, want Bearer", body.TokenType)
	}

	// access_token and id_token must share a key: a real working capture
	// (docs/CAPTURES/raw/nextendo-working-nalink-2026-08-27.flow) showed both
	// tokens from this exact endpoint published at the same kid, both under
	// accounts.nintendo.com's own JWKS -- not the BAAS host's. Signing
	// access_token with the wrong issuer's key here shipped 2026-08-26 and
	// was found and fixed 2026-08-27.
	accessHeader, accessPayload := decodeTokenParts(t, body.AccessToken)
	idHeader, idPayload := decodeTokenParts(t, body.IDToken)
	if accessHeader["kid"] != idHeader["kid"] {
		t.Errorf("access_token kid (%v) != id_token kid (%v), want them to match",
			accessHeader["kid"], idHeader["kid"])
	}
	if accessHeader["jku"] != naIssuer+"/1.0.0/certificates" {
		t.Errorf("access_token jku = %v, want %s/1.0.0/certificates", accessHeader["jku"], naIssuer)
	}

	// at_hash binds id_token to access_token per the OIDC spec: SHA-256 of the
	// access_token, left half, base64url. Verified byte-for-byte against a
	// real capture (oidcHash's doc comment).
	if got, want := idPayload["at_hash"], oidcHash(body.AccessToken); got != want {
		t.Errorf("id_token at_hash = %v, want %v", got, want)
	}
	// A real working capture's id_token from this exact endpoint has no 'typ'
	// claim in its payload at all -- unlike a BAAS id_token, which always
	// does. Reusing sjwt.BAASTokenClaims for both must not leak BAAS's shape
	// onto this OIDC-flavoured token.
	if _, present := idPayload["typ"]; present {
		t.Errorf("id_token payload carries typ = %v, want it entirely absent", idPayload["typ"])
	}
	// ac:grt is accounts.nintendo.com's own grant-type flavour, separate from
	// BAAS's bs:grt. 4 is the only value ever observed, for exactly this
	// grant (see ACGrantType's doc comment).
	if got, want := accessPayload["ac:grt"], float64(4); got != want {
		t.Errorf("access_token ac:grt = %v, want %v", got, want)
	}

	// The code is single-use: exchanging it again is refused.
	secondResp, err := http.PostForm(h.server.URL+"/connect/1.0.0/api/token", url.Values{
		"grant_type": {"authorization_code"},
		"code":       {code},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer secondResp.Body.Close()
	if secondResp.StatusCode == http.StatusOK {
		t.Error("a code was accepted twice")
	}
}

// TestNAIDTokenAndBAASIDTokenUseDifferentKeys guards a real hardware failure
// found 2026-08-26 (error 2124-0292): nalink.go's Nintendo Account link id
// token (iss = "https://accounts.nintendo.com") and a login/federation
// response's embedded id token (iss = the BAAS host) were signed with the
// same key, both published at the same /1.0.0/certificates JWK set. Per a
// real, working private server's own operational notes (Nextendo Network's
// baas-jwks), the console links a token's kid to its iss and silently
// rejects the whole Nintendo Account import the moment it sees one kid used
// under two different issuers -- with no further network call, exactly the
// symptom this bug produced. sjwt.IssuerNAID is the fix: a key of its own.
func TestNAIDTokenAndBAASIDTokenUseDifferentKeys(t *testing.T) {
	h := newHarness(t)
	registerLoginUser(t, h, "alice", "hunter2222")

	client := noRedirectClient()
	authorizeResp, err := client.PostForm(h.server.URL+"/connect/1.0.0/authorize", url.Values{
		"redirect_uri": {"nintendo://lhub.nx.sys"},
		"username":     {"alice"},
		"password":     {"hunter2222"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer authorizeResp.Body.Close()
	loc := authorizeResp.Header.Get("Location")
	frag, err := url.ParseQuery(strings.TrimPrefix(loc, "nintendo://lhub.nx.sys#"))
	if err != nil {
		t.Fatal(err)
	}

	naHeader, naPayload := decodeTokenParts(t, frag.Get("id_token"))
	if naPayload["iss"] != "https://accounts.nintendo.com" {
		t.Fatalf("na id_token iss = %v, want accounts.nintendo.com", naPayload["iss"])
	}

	// An ordinary account's login response also embeds an id token, this one
	// for the BAAS issuer.
	a := h.register()
	login := h.login(&a, "")
	baasHeader, baasPayload := decodeTokenParts(t, login.IDToken)
	if baasPayload["iss"] == naPayload["iss"] {
		t.Fatal("test setup: the BAAS and NA id tokens must have different issuers")
	}

	if naHeader["kid"] == baasHeader["kid"] {
		t.Errorf("na id_token kid (%v) == baas id_token kid (%v) despite different issuers -- "+
			"this is exactly what makes the console reject the Nintendo Account import",
			naHeader["kid"], baasHeader["kid"])
	}
}

// naLogin drives a full authorize-then-exchange round trip and returns the
// authorization_code grant's tokens, for tests that need a real, valid token
// rather than exercising the login form itself.
func naLogin(t *testing.T, h *harness, username, password string) (accessToken, refreshToken string) {
	t.Helper()
	client := noRedirectClient()
	authorizeResp, err := client.PostForm(h.server.URL+"/connect/1.0.0/authorize", url.Values{
		"redirect_uri": {"nintendo://account.nx.sys"},
		"username":     {username},
		"password":     {password},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer authorizeResp.Body.Close()
	loc := authorizeResp.Header.Get("Location")
	frag, err := url.ParseQuery(strings.TrimPrefix(loc, "nintendo://account.nx.sys#"))
	if err != nil {
		t.Fatal(err)
	}
	code := frag.Get("code")
	if code == "" {
		t.Fatal("no code to exchange")
	}

	tokenResp, err := http.PostForm(h.server.URL+"/connect/1.0.0/api/token", url.Values{
		"grant_type": {"authorization_code"},
		"code":       {code},
		"client_id":  {"6ffd70c434d303c8"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tokenResp.Body.Close()
	requireStatus(t, tokenResp, http.StatusOK)

	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	decode(t, tokenResp, &body)
	if body.AccessToken == "" || body.RefreshToken == "" {
		t.Fatal("naLogin: token exchange did not return both tokens")
	}
	return body.AccessToken, body.RefreshToken
}

// TestNAAuthorizeSilentReauthorizationWithABearerTokenSucceeds guards the
// second authorize shape a real capture showed and this project had never
// handled: a credential-less, prompt=none re-authorization for a DIFFERENT
// client_id than the one that first signed the person in, carrying only an
// Authorization: Bearer <access_token> header. Before this, an empty
// username reached GetUserByLoginUsername("") and answered "Wrong username
// or password" -- a login page's error, for a request that must never show
// one.
func TestNAAuthorizeSilentReauthorizationWithABearerTokenSucceeds(t *testing.T) {
	h := newHarness(t)
	registerLoginUser(t, h, "carol", "hunter2222")
	accessToken, _ := naLogin(t, h, "carol", "hunter2222")

	client := noRedirectClient()
	req, err := http.NewRequest("POST", h.server.URL+"/connect/1.0.0/authorize", strings.NewReader(url.Values{
		"response_type": {"code id_token"},
		"prompt":        {"none"},
		"client_id":     {"48d6d1f020427ad7"}, // a different client than naLogin's own
		"redirect_uri":  {"nintendo://e-license.nx.sys"},
		"scope":         {"openid user"},
		"state":         {"a-second-state"},
		"nonce":         {"a-second-nonce"},
	}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	requireStatus(t, resp, http.StatusFound)

	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "nintendo://e-license.nx.sys#") {
		t.Fatalf("Location = %q, want the requested redirect_uri's own scheme", loc)
	}
	frag, err := url.ParseQuery(strings.TrimPrefix(loc, "nintendo://e-license.nx.sys#"))
	if err != nil {
		t.Fatal(err)
	}
	if frag.Get("state") != "a-second-state" {
		t.Errorf("state = %q, want it echoed back", frag.Get("state"))
	}
	if frag.Get("code") == "" || frag.Get("id_token") == "" {
		t.Fatal("silent re-authorization did not mint a code and id_token")
	}
	if frag.Get("error") != "" {
		t.Errorf("got error=%q on what should be a success", frag.Get("error"))
	}
}

// TestNAAuthorizeSilentReauthorizationWithABadBearerTokenFailsWithoutAPage
// checks the failure side: prompt=none forbids showing any UI, so an
// invalid or expired access token here must redirect with an OIDC-standard
// error in the fragment, not the HTML login form a credential mismatch
// gets.
func TestNAAuthorizeSilentReauthorizationWithABadBearerTokenFailsWithoutAPage(t *testing.T) {
	h := newHarness(t)

	client := noRedirectClient()
	req, err := http.NewRequest("POST", h.server.URL+"/connect/1.0.0/authorize", strings.NewReader(url.Values{
		"prompt":       {"none"},
		"client_id":    {"48d6d1f020427ad7"},
		"redirect_uri": {"nintendo://e-license.nx.sys"},
		"state":        {"still-echoed"},
	}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer garbage-not-a-real-token")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	requireStatus(t, resp, http.StatusFound)

	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "nintendo://e-license.nx.sys#") {
		t.Fatalf("Location = %q, want a redirect to the requested redirect_uri, not an HTML page", loc)
	}
	frag, err := url.ParseQuery(strings.TrimPrefix(loc, "nintendo://e-license.nx.sys#"))
	if err != nil {
		t.Fatal(err)
	}
	if frag.Get("error") != "login_required" {
		t.Errorf("error = %q, want login_required", frag.Get("error"))
	}
	if frag.Get("state") != "still-echoed" {
		t.Errorf("state = %q, want it echoed back even on failure", frag.Get("state"))
	}
}

func TestNAQRCodeParamReturnsTheRealShape(t *testing.T) {
	h := newHarness(t)
	user := registerLoginUser(t, h, "dana", "hunter2222")
	accessToken, _ := naLogin(t, h, "dana", "hunter2222")

	resp, err := doAuthorized(h, "GET", "/api/1.0.0/users/"+user.ID+"/qrcode_param", accessToken)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	requireStatus(t, resp, http.StatusOK)

	var body struct {
		Exp    int64  `json:"exp"`
		Param  string `json:"param"`
		UserID string `json:"userId"`
	}
	decode(t, resp, &body)
	if body.UserID != user.ID {
		t.Errorf("userId = %q, want %q", body.UserID, user.ID)
	}
	if !strings.HasPrefix(body.Param, "https://accounts.nintendo.com/qr_verify?") {
		t.Errorf("param = %q, want a qr_verify URL", body.Param)
	}
	for _, key := range []string{"sig=", "iat=", "aud=", "sub=" + user.ID, "exp="} {
		if !strings.Contains(body.Param, key) {
			t.Errorf("param %q is missing %q", body.Param, key)
		}
	}
}

// TestNAQRCodeParamRefusesAnotherAccounts checks the fail-closed identity
// property this project treats as load-bearing everywhere else: a token
// authenticated as one person must not read another's qrcode_param by
// naming them in the path.
func TestNAQRCodeParamRefusesAnotherAccount(t *testing.T) {
	h := newHarness(t)
	registerLoginUser(t, h, "erin", "hunter2222")
	accessToken, _ := naLogin(t, h, "erin", "hunter2222")

	resp, err := doAuthorized(h, "GET", "/api/1.0.0/users/0000000000000000/qrcode_param", accessToken)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	requireStatus(t, resp, http.StatusForbidden)
}

func TestNATokenRefreshGrantSucceedsWithARealRefreshToken(t *testing.T) {
	h := newHarness(t)
	user := registerLoginUser(t, h, "bob", "hunter2222")
	_, refreshToken := naLogin(t, h, "bob", "hunter2222")

	resp, err := http.PostForm(h.server.URL+"/connect/1.0.0/api/token", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {"6ffd70c434d303c8"},
		"scope":         {"openid offline user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	requireStatus(t, resp, http.StatusOK)

	var body struct {
		AccessToken  string   `json:"access_token"`
		IDToken      string   `json:"id_token"`
		RefreshToken string   `json:"refresh_token"`
		Scope        []string `json:"scope"`
		TokenType    string   `json:"token_type"`
	}
	decode(t, resp, &body)
	if body.AccessToken == "" {
		t.Error("no access_token")
	}
	// The captured real response to a refresh grant is deliberately smaller
	// than an authorization_code response: no id_token, no new
	// refresh_token. Asserting their absence, not just the access_token's
	// presence, is what would catch a copy-paste from the other branch.
	if body.IDToken != "" {
		t.Error("refresh grant response has an id_token; the real one never does")
	}
	if body.RefreshToken != "" {
		t.Error("refresh grant response has a refresh_token; the real one never does")
	}
	if len(body.Scope) != 3 {
		t.Errorf("scope = %v, want the 3 scopes the request asked for echoed back", body.Scope)
	}

	// Verify the access token this grant issued actually authenticates as
	// the same user the refresh_token belonged to, not an empty subject.
	meResp, err := doAuthorized(h, "GET", "/2.0.0/users/me", body.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	defer meResp.Body.Close()
	requireStatus(t, meResp, http.StatusOK)
	var me struct {
		ID string `json:"id"`
	}
	decode(t, meResp, &me)
	if me.ID != user.ID {
		t.Errorf("refreshed access_token authenticates as %q, want %q", me.ID, user.ID)
	}
}

// doAuthorized issues a request with a Bearer Authorization header — a
// plain http.NewRequest since http.Get/PostForm have no way to add headers.
func doAuthorized(h *harness, method, path, bearer string) (*http.Response, error) {
	req, err := http.NewRequest(method, h.server.URL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	return http.DefaultClient.Do(req)
}

func TestNAUsersMeReturnsTheAuthenticatedPersonsProfile(t *testing.T) {
	h := newHarness(t)
	user := registerLoginUser(t, h, "carol", "hunter2222")
	accessToken, _ := naLogin(t, h, "carol", "hunter2222")

	resp, err := doAuthorized(h, "GET", "/2.0.0/users/me", accessToken)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	requireStatus(t, resp, http.StatusOK)

	var body struct {
		ID       string `json:"id"`
		Nickname string `json:"nickname"`
		Birthday string `json:"birthday"`
		Country  string `json:"country"`
	}
	decode(t, resp, &body)
	if body.ID != user.ID {
		t.Errorf("id = %q, want %q", body.ID, user.ID)
	}
	if body.Nickname != "carol" {
		t.Errorf("nickname = %q, want carol", body.Nickname)
	}
	// registerLoginUser stores "0000-00-00" and an empty country -- BAAS's
	// own sentinels for "unset" on a device profile (accounts.go,
	// provision.go). Passing those straight through here produced a real
	// hardware failure (2124-3122, 2026-08-26): a real Nintendo Account
	// profile is expected to carry an actual date and a real ISO country
	// code, not BAAS's placeholder values.
	if body.Birthday == "" || body.Birthday == "0000-00-00" {
		t.Errorf("birthday = %q, want a real-looking date, not BAAS's unset sentinel", body.Birthday)
	}
	if body.Country == "" {
		t.Error("country is empty, want a real-looking ISO country code")
	}
}

// TestNAUsersMeGivesAFreshPersonADefaultAvatar matches the operator's
// 2026-08-26 description of real Nextendo: the "add this user to this
// console" confirmation screen always has a picture to show, generated the
// first time a person with no uploaded thumbnail is fetched, never blank.
func TestNAUsersMeGivesAFreshPersonADefaultAvatar(t *testing.T) {
	h := newHarness(t)
	registerLoginUser(t, h, "dana", "hunter2222")
	accessToken, _ := naLogin(t, h, "dana", "hunter2222")

	resp, err := doAuthorized(h, "GET", "/2.0.0/users/me", accessToken)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	requireStatus(t, resp, http.StatusOK)

	var body struct {
		IconURI string `json:"iconUri"`
	}
	decode(t, resp, &body)
	if body.IconURI == "" {
		t.Fatal("no iconUri for a person with no uploaded thumbnail")
	}
	if !strings.HasSuffix(body.IconURI, "/default") {
		t.Errorf("iconUri = %q, want it to end with /default", body.IconURI)
	}

	// The harness's ImageBaseURL is not actually routable, so check the file
	// landed on disk (imageFileServer would serve it from here) rather than
	// fetching the URL over HTTP.
	raw, err := os.ReadFile(h.srv.GetImagePath("default"))
	if err != nil {
		t.Fatalf("default avatar was not written to disk: %v", err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(raw)); err != nil {
		t.Errorf("default avatar is not a valid JPEG: %v", err)
	}
}

func TestNAUsersMeRefusesAMissingOrInvalidToken(t *testing.T) {
	h := newHarness(t)

	noAuthResp, err := http.Get(h.server.URL + "/2.0.0/users/me")
	if err != nil {
		t.Fatal(err)
	}
	defer noAuthResp.Body.Close()
	if noAuthResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no Authorization header: status = %d, want 401", noAuthResp.StatusCode)
	}

	badResp, err := doAuthorized(h, "GET", "/2.0.0/users/me", "not-a-real-token")
	if err != nil {
		t.Fatal(err)
	}
	defer badResp.Body.Close()
	if badResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("garbage token: status = %d, want 401", badResp.StatusCode)
	}
}

func TestNATokenRefreshGrantIsRefusedNotCrashed(t *testing.T) {
	h := newHarness(t)
	resp, err := http.PostForm(h.server.URL+"/connect/1.0.0/api/token", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {"whatever"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "invalid_grant") {
		t.Errorf("body = %s, want an invalid_grant OAuth error", body)
	}
}
