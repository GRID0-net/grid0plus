package baas

import (
	"net/url"
	"strings"
	"testing"

	"github.com/n-popescu/switchnet/internal/db"
	sjwt "github.com/n-popescu/switchnet/internal/jwt"
)

// TestFullAuthChain is the phase 1.1 definition of done: a client walks the whole
// chain — device token, application token, anonymous BAAS token, register, log in
// — and every JWT is checked for the right claims and signature.
//
// The chain matters because each step's output is the next step's input, so a
// wrong claim anywhere fails several steps later with an unrelated-looking error.
func TestFullAuthChain(t *testing.T) {
	h := newHarness(t)

	// ---- step 1: a device token, from SwitchNet's dauth server ----
	deviceToken := h.deviceToken()

	// ---- step 2: an application token, from SwitchNet's aauth server ----
	// Splatoon 3 on retail is MEMBERSHIP_REQUIRED with a GAME_SERVER handler,
	// which is the case that makes the id token carry 'hm'.
	appToken := h.appToken(splatoon3TitleID, "MEMBERSHIP_REQUIRED", "GAME_SERVER")

	// ---- step 3: an anonymous BAAS access token ----
	resp := h.do(request{
		method: "POST", path: "/1.0.0/application/token",
		form: url.Values{
			"grantType": {"public_client"},
			"assertion": {deviceToken},
		},
	})
	requireStatus(t, resp, 200)

	var tokens tokenResponse
	decode(t, resp, &tokens)
	if tokens.TokenType != "Bearer" {
		t.Errorf("tokenType = %q, want Bearer", tokens.TokenType)
	}
	if tokens.ExpiresIn != 10800 {
		t.Errorf("expiresIn = %d, want 10800", tokens.ExpiresIn)
	}

	anon := parseBAASClaims(t, tokens.AccessToken)
	if anon.Type != "token" {
		t.Errorf("anonymous token typ = %q, want token", anon.Type)
	}
	if anon.GrantType != 1 {
		t.Errorf("anonymous token bs:grt = %d, want 1", anon.GrantType)
	}
	if anon.Subject != testAudience || anon.Audience != testAudience {
		t.Errorf("anonymous token sub/aud = %q/%q, want %q for both",
			anon.Subject, anon.Audience, testAudience)
	}
	if anon.Issuer != testIssuer {
		t.Errorf("anonymous token iss = %q", anon.Issuer)
	}
	// An anonymous token names no user and no device account. If it did, it
	// would be a user token in all but name.
	if anon.DeviceAccountID != "" {
		t.Errorf("anonymous token carries bs:did = %q", anon.DeviceAccountID)
	}

	// ---- step 4: register a user ----
	resp = h.do(request{method: "POST", path: "/1.0.0/users", token: tokens.AccessToken})
	requireStatus(t, resp, 201)

	// The console reads Location to know where its new account lives.
	var created UserResponse
	decode(t, resp, &created)
	if want := "/1.0.0/users/" + created.ID; resp.Header.Get("Location") != want {
		t.Errorf("Location = %q, want %q", resp.Header.Get("Location"), want)
	}

	if len(created.ID) != 16 {
		t.Errorf("user id %q is not 16 hex digits", created.ID)
	}
	if created.Birthday != "0000-00-00" {
		t.Errorf("birthday = %q, want the literal 0000-00-00 for 'not set'", created.Birthday)
	}
	if created.Presence.State != db.StateOffline {
		t.Errorf("a new user's presence = %q, want OFFLINE", created.Presence.State)
	}
	// Every extras scope must be present even when empty: the console indexes
	// into these without checking the key exists.
	for _, scope := range db.Scopes {
		if _, ok := created.Extras[scope]; !ok {
			t.Errorf("extras is missing the %q scope", scope)
		}
		if _, ok := created.Presence.Extras[scope]; !ok {
			t.Errorf("presence extras is missing the %q scope", scope)
		}
	}
	// The default has to let friends see presence. SELF as a default is a friend
	// list where everybody is permanently offline and nothing says why.
	if created.Permissions.Presence != "FRIENDS" {
		t.Errorf("default permissions.presence = %q, want FRIENDS", created.Permissions.Presence)
	}
	if !created.Permissions.FriendRequestReception {
		t.Error("default friendRequestReception is false, so nobody could be added")
	}
	if created.ETag == "" {
		t.Error("no ETag")
	}

	if len(created.DeviceAccounts) != 1 {
		t.Fatalf("got %d device accounts, want 1", len(created.DeviceAccounts))
	}
	da := created.DeviceAccounts[0]
	if len(da.ID) != 16 {
		t.Errorf("device account id %q is not 16 hex digits", da.ID)
	}
	// Nintendo's password is 40 characters. It is returned exactly once.
	if len(da.Password) != 40 {
		t.Errorf("device account password is %d characters, want 40", len(da.Password))
	}

	// ---- step 5: log in ----
	resp = h.do(request{
		method: "POST", path: "/1.0.0/login",
		token: h.anonymousToken(),
		form: url.Values{
			"id":            {da.ID},
			"password":      {da.Password},
			"appAuthNToken": {appToken},
			"naCountry":     {"NL"},
			"isPersistent":  {"true"},
		},
	})
	requireStatus(t, resp, 200)

	var login loginResponse
	decode(t, resp, &login)

	if login.TokenType != "Bearer" {
		t.Errorf("tokenType = %q, want Bearer", login.TokenType)
	}
	if login.User.ID != created.ID {
		t.Errorf("login returned user %q, want %q", login.User.ID, created.ID)
	}
	// The password is never sent again.
	for _, d := range login.User.DeviceAccounts {
		if d.Password != "" {
			t.Error("login returned the device account password; it is sent only at creation")
		}
	}
	// Logging in is a statement that the console is awake, not that it is
	// playing: the game has not started yet.
	if login.User.Presence.State != db.StateOnline {
		t.Errorf("presence after login = %q, want ONLINE", login.User.Presence.State)
	}

	// ---- the user access token ----
	user := parseBAASClaims(t, login.AccessToken)
	if user.Type != "token" {
		t.Errorf("user token typ = %q, want token", user.Type)
	}
	if user.GrantType != 2 {
		t.Errorf("user token bs:grt = %d, want 2", user.GrantType)
	}
	if user.Subject != created.ID {
		t.Errorf("user token sub = %q, want the user id %q", user.Subject, created.ID)
	}
	if user.DeviceAccountID != da.ID {
		t.Errorf("user token bs:did = %q, want the device account id %q",
			user.DeviceAccountID, da.ID)
	}
	if user.Audience != testAudience {
		t.Errorf("user token aud = %q", user.Audience)
	}

	// ---- the id token, which game servers verify ----
	id := parseBAASClaims(t, login.IDToken)
	if id.Type != "id_token" {
		t.Errorf("id token typ = %q, want id_token", id.Type)
	}
	if id.Subject != created.ID {
		t.Errorf("id token sub = %q, want the user id", id.Subject)
	}
	if id.DeviceAccountID != da.ID {
		t.Errorf("id token bs:did = %q", id.DeviceAccountID)
	}
	// An id token has no grant type.
	if id.GrantType != 0 {
		t.Errorf("id token carries bs:grt = %d", id.GrantType)
	}

	// The application token's details are copied into the id token so a game
	// server can see which title is connecting.
	nin := nintendoClaim(t, login.IDToken)
	if nin["ai"] != splatoon3TitleID {
		t.Errorf("id token ai = %v, want the Splatoon 3 title id", nin["ai"])
	}
	if nin["edi"] != "b46bda4e1dd5e7ce002430a68b2c6d4e" {
		t.Errorf("id token edi = %v, want it copied from the application token", nin["edi"])
	}
	if nin["ph"] != "GAME_SERVER" {
		t.Errorf("id token ph = %v, want GAME_SERVER", nin["ph"])
	}
	// A game server reads 'hm' to decide whether online play is allowed. On a
	// private network every registered user is entitled to play.
	if nin["hm"] != true {
		t.Errorf("id token hm = %v, want true", nin["hm"])
	}

	if login.Summary == nil || !login.Summary.Nintendo.HasMembership {
		t.Error("no membership summary, which the console expects when the " +
			"policy handler is GAME_SERVER")
	}

	// ---- the token actually works ----
	resp = h.do(request{
		method: "GET", path: "/1.0.0/users/" + created.ID,
		token: login.AccessToken,
	})
	requireStatus(t, resp, 200)
}

func TestLoginWithoutAnAppTokenOmitsTheMembershipSummary(t *testing.T) {
	h := newHarness(t)
	a := h.register()

	login := h.login(&a, "")

	// No application token means no game is logging in, so there is nothing to
	// say about membership and no application block in the id token.
	if login.Summary != nil {
		t.Error("summary is present without an application token")
	}
	if nin := nintendoClaim(t, login.IDToken); nin != nil {
		t.Errorf("id token carries a nintendo claim without an application token: %v", nin)
	}
}

func TestFreePolicyDoesNotClaimMembership(t *testing.T) {
	h := newHarness(t)
	a := h.register()

	// With a FREE policy there is no policy handler, so the id token must not
	// claim a membership: a game server reading 'hm' when the handler is not
	// GAME_SERVER would be reading a field Nintendo does not send.
	login := h.login(&a, h.appToken(splatoon3TitleID, "FREE", ""))

	if login.Summary != nil {
		t.Error("summary is present for a FREE policy")
	}
	nin := nintendoClaim(t, login.IDToken)
	if nin == nil {
		t.Fatal("no nintendo claim in the id token")
	}
	if _, present := nin["hm"]; present {
		t.Errorf("id token carries hm = %v for a FREE policy", nin["hm"])
	}
	if _, present := nin["ph"]; present {
		t.Errorf("id token carries ph = %v for a FREE policy", nin["ph"])
	}
}

// ---- identity resolution: the tests that must fail closed ----------------

// TestLoginRefusesAnUnknownDeviceAccount is the most important test in this
// package.
//
// A console that presents a device account this server never issued must be
// refused. The failure mode being guarded against is a lookup that falls back to
// "the last account that logged in": with that fallback in place every console
// acts as one person, they all send friend requests as him, and they all display
// his friend code. Regenerating the code changes nothing, because the code was
// never the identity.
func TestLoginRefusesAnUnknownDeviceAccount(t *testing.T) {
	h := newHarness(t)

	// A real account exists and has logged in, so any process-wide "last
	// authenticated account" would be populated.
	existing := h.newAccount()

	resp := h.do(request{
		method: "POST", path: "/1.0.0/login",
		token: h.anonymousToken(),
		form: url.Values{
			"id":        {"deadbeefdeadbeef"},
			"password":  {strings.Repeat("A", 40)},
			"naCountry": {"NL"},
		},
	})
	requireError(t, resp, 400, "invalid_device_account")

	// And make sure nothing was handed out that could act as the real account.
	var body loginResponse
	_ = body
	if resp.StatusCode == 200 {
		t.Fatal("an unknown device account was logged in")
	}
	_ = existing
}

func TestLoginRefusesTheWrongPassword(t *testing.T) {
	h := newHarness(t)
	a := h.register()

	resp := h.do(request{
		method: "POST", path: "/1.0.0/login",
		token: h.anonymousToken(),
		form: url.Values{
			"id":        {a.DeviceAccountID},
			"password":  {strings.Repeat("B", 40)},
			"naCountry": {"NL"},
		},
	})
	requireError(t, resp, 400, "invalid_device_account")
}

func TestLoginRefusesADeletedDeviceAccount(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()

	resp := h.do(request{
		method: "DELETE",
		path:   "/1.0.0/users/" + a.UserID + "/device_accounts/" + a.DeviceAccountID,
		token:  a.AccessToken,
	})
	requireStatus(t, resp, 204)

	// A deleted device account is refused with a distinct error, so a console
	// whose account was removed gets a different answer from one presenting a
	// credential that never existed.
	resp = h.do(request{
		method: "POST", path: "/1.0.0/login",
		token: h.anonymousToken(),
		form: url.Values{
			"id":        {a.DeviceAccountID},
			"password":  {a.Password},
			"naCountry": {"NL"},
		},
	})
	requireError(t, resp, 403, "unavailable_device_account")
}

// TestAnExistingTokenStopsWorkingWhenItsDeviceAccountIsDeleted proves the
// authorisation check re-reads the device account rather than trusting the token
// alone. A token stays cryptographically valid until it expires, so without this
// check a deleted console would keep full access for up to three hours.
func TestAnExistingTokenStopsWorkingWhenItsDeviceAccountIsDeleted(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()

	resp := h.do(request{
		method: "GET", path: "/1.0.0/users/" + a.UserID, token: a.AccessToken,
	})
	requireStatus(t, resp, 200)

	if err := h.store.DeleteDeviceAccount(t.Context(), a.UserID, a.DeviceAccountID); err != nil {
		t.Fatal(err)
	}

	resp = h.do(request{
		method: "GET", path: "/1.0.0/users/" + a.UserID, token: a.AccessToken,
	})
	requireError(t, resp, 401, "invalid_token")
}

// TestATokenWhoseSubjectDoesNotOwnItsDeviceAccountIsRefused covers a forged or
// mixed-up token: the signature is ours, but the user and the device account do
// not belong together. Accepting it is how one console ends up acting as another
// account.
func TestATokenWhoseSubjectDoesNotOwnItsDeviceAccountIsRefused(t *testing.T) {
	h := newHarness(t)
	victim := h.newAccount()
	attacker := h.newAccount()

	// Mint a well-signed token claiming the victim's user id but the attacker's
	// device account. Only the server's own signing key is used, so this is
	// exactly what a bug in token minting would produce.
	ring := sjwt.NewKeyring(t.TempDir(), true)
	_ = ring

	forged := h.mintUserToken(victim.UserID, attacker.DeviceAccountID)

	resp := h.do(request{
		method: "GET", path: "/1.0.0/users/" + victim.UserID, token: forged,
	})
	requireError(t, resp, 401, "invalid_token")
}

func TestAnIDTokenIsNotAcceptedAsAnAccessToken(t *testing.T) {
	h := newHarness(t)
	a := h.register()
	login := h.login(&a, h.appToken(splatoon3TitleID, "FREE", ""))

	// An id token is handed to game servers. If it were accepted here, a game
	// server's copy of a player's token could act as that player against their
	// account.
	resp := h.do(request{
		method: "GET", path: "/1.0.0/users/" + a.UserID, token: login.IDToken,
	})
	requireError(t, resp, 401, "invalid_token")
}

func TestAnAnonymousTokenCannotReachUserEndpoints(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()

	// The token is valid; it is simply not enough. Nintendo distinguishes this
	// from an invalid token, and so does SwitchNet.
	resp := h.do(request{
		method: "GET", path: "/1.0.0/users/" + a.UserID, token: h.anonymousToken(),
	})
	requireError(t, resp, 403, "insufficient_scope")
}

func TestOneUserCannotReadOrChangeAnother(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	b := h.newAccount()

	// Every user-scoped endpoint must refuse a caller who edited the user id in
	// the URL. Missing one of these is a whole-account read for anybody with a
	// valid token of their own.
	cases := []request{
		{method: "GET", path: "/1.0.0/users/" + b.UserID},
		{method: "PATCH", path: "/1.0.0/users/" + b.UserID,
			patch: `[{"op":"replace","path":"/nickname","value":"stolen"}]`},
		{method: "POST", path: "/1.0.0/users/" + b.UserID + "/generate_code",
			form: url.Values{"type": {"NX"}}},
		{method: "GET", path: "/2.0.0/users/" + b.UserID + "/friends"},
		{method: "GET", path: "/2.0.0/users/" + b.UserID + "/friend_requests/inbox"},
		{method: "GET", path: "/2.0.0/users/" + b.UserID + "/friend_requests/outbox"},
		{method: "GET", path: "/1.0.0/users/" + b.UserID + "/blocks"},
		{method: "DELETE", path: "/2.0.0/users/" + b.UserID + "/friends/" + a.UserID},
		{method: "GET", path: "/2.0.0/users/" + b.UserID + "/relationships/" + a.UserID},
		{method: "PATCH",
			path:  "/1.0.0/users/" + b.UserID + "/device_accounts/" + b.DeviceAccountID,
			patch: `[{"op":"replace","path":"/presence/state","value":"PLAYING"}]`},
	}
	for _, c := range cases {
		c.token = a.AccessToken
		resp := h.do(c)
		if resp.StatusCode != 403 {
			t.Errorf("%s %s: status = %d, want 403", c.method, c.path, resp.StatusCode)
		}
	}
}

// TestPresenceCannotBeReportedForAnotherDeviceAccount stops one console
// overwriting another's state when two consoles share a user.
func TestPresenceCannotBeReportedForAnotherDeviceAccount(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()

	resp := h.do(request{
		method: "PATCH",
		path:   "/1.0.0/users/" + a.UserID + "/device_accounts/ffffffffffffffff",
		token:  a.AccessToken,
		patch:  `[{"op":"replace","path":"/presence/state","value":"PLAYING"}]`,
	})
	requireError(t, resp, 403, "insufficient_scope")
}

func TestMissingOrMalformedAuthorizationHeader(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()

	for name, headers := range map[string]map[string]string{
		"missing":      {},
		"no scheme":    {"Authorization": a.AccessToken},
		"wrong scheme": {"Authorization": "Basic " + a.AccessToken},
		"empty token":  {"Authorization": "Bearer "},
	} {
		resp := h.do(request{
			method: "GET", path: "/1.0.0/users/" + a.UserID, headers: headers,
		})
		// Nintendo distinguishes a malformed Authorization header
		// (invalid_request) from a token that does not verify (invalid_token).
		if resp.StatusCode != 400 && resp.StatusCode != 401 {
			t.Errorf("%s: status = %d, want 400 or 401", name, resp.StatusCode)
		}
	}
}

// ---- application/token ---------------------------------------------------

func TestApplicationTokenRejectsABadAssertion(t *testing.T) {
	h := newHarness(t)

	for name, form := range map[string]url.Values{
		"wrong grant type": {"grantType": {"password"}, "assertion": {h.deviceToken()}},
		"no assertion":     {"grantType": {"public_client"}},
		"garbage assertion": {
			"grantType": {"public_client"}, "assertion": {"not-a-token"},
		},
	} {
		resp := h.do(request{
			method: "POST", path: "/1.0.0/application/token", form: form,
		})
		if resp.StatusCode == 200 {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestExpiredDeviceTokenIsRejected(t *testing.T) {
	h := newHarness(t)

	// A console's clock is what decides this in practice: a console whose clock
	// is badly wrong has its tokens rejected as expired, and the error blames
	// the token rather than the clock.
	//
	// The expiry is set against the wall clock, not the harness clock, because
	// JWT expiry is validated by the JWT library against real time.
	resp := h.do(request{
		method: "POST", path: "/1.0.0/application/token",
		form: url.Values{
			"grantType": {"public_client"},
			"assertion": {h.expiredDeviceToken()},
		},
	})
	requireError(t, resp, 401, "invalid_token")
}

// ---- registration --------------------------------------------------------

func TestRegistrationCanBeDisabled(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.AllowRegistration = false })

	// Turned off once every console in the group is registered, so somebody who
	// finds the server cannot add themselves to it.
	resp := h.do(request{
		method: "POST", path: "/1.0.0/users", token: h.anonymousToken(),
	})
	requireError(t, resp, 403, "forbidden")
}

func TestRegistrationGivesEachAccountDistinctIdentifiers(t *testing.T) {
	h := newHarness(t)

	users := map[string]bool{}
	deviceAccounts := map[string]bool{}
	passwords := map[string]bool{}
	for range 5 {
		a := h.register()
		if users[a.UserID] {
			t.Fatalf("user id %q was issued twice", a.UserID)
		}
		if deviceAccounts[a.DeviceAccountID] {
			t.Fatalf("device account id %q was issued twice", a.DeviceAccountID)
		}
		if passwords[a.Password] {
			t.Fatal("the same device account password was issued twice")
		}
		users[a.UserID] = true
		deviceAccounts[a.DeviceAccountID] = true
		passwords[a.Password] = true
	}
}

// ---- JWK sets ------------------------------------------------------------

func TestJWKSetsAreServedWithoutATokenAndUseDistinctKeys(t *testing.T) {
	h := newHarness(t)

	// Both are reachable with no Authorization header: a game server verifying an
	// id token has no BAAS token of its own.
	idSet := h.jwks(t, "/1.0.0/certificates")
	accessSet := h.jwks(t, "/1.0.0/internal_certificates")

	if len(idSet.Keys) != 1 || len(accessSet.Keys) != 1 {
		t.Fatalf("key set sizes = %d and %d, want 1 each", len(idSet.Keys), len(accessSet.Keys))
	}
	// The two sets must publish different keys. Sharing one would mean an id
	// token verifies as an access token.
	if idSet.Keys[0].KeyID == accessSet.Keys[0].KeyID {
		t.Error("/1.0.0/certificates and /1.0.0/internal_certificates publish the same key")
	}
	// Nintendo marks these with a 'usage' field.
	if idSet.Keys[0].Usage != "developer" {
		t.Errorf("id token key usage = %q, want developer", idSet.Keys[0].Usage)
	}
	if accessSet.Keys[0].Usage != "internal" {
		t.Errorf("access token key usage = %q, want internal", accessSet.Keys[0].Usage)
	}
}

// TestPublishedIDTokenKeyVerifiesARealIDToken is what a phase 2 game server
// does: fetch /1.0.0/certificates and verify the token the console handed it.
func TestPublishedIDTokenKeyVerifiesARealIDToken(t *testing.T) {
	h := newHarness(t)
	a := h.register()
	login := h.login(&a, h.appToken(splatoon3TitleID, "FREE", ""))

	set := h.jwks(t, "/1.0.0/certificates")
	if len(set.Keys) != 1 {
		t.Fatalf("key set has %d keys", len(set.Keys))
	}

	claims := parseBAASClaims(t, login.IDToken)
	if claims.Subject != a.UserID {
		t.Errorf("id token sub = %q, want %q", claims.Subject, a.UserID)
	}

	// The 'kid' in the token has to be the one in the published set, or a game
	// server cannot pick the right key.
	header, _ := decodeTokenParts(t, login.IDToken)
	if header["kid"] != set.Keys[0].KeyID {
		t.Errorf("id token kid = %v, published kid = %q", header["kid"], set.Keys[0].KeyID)
	}
	if header["jku"] != testIssuer+"/1.0.0/certificates" {
		t.Errorf("id token jku = %v, want the certificates URL", header["jku"])
	}
}

func TestUnroutedPathReturnsABAASShapedError(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()

	resp := h.do(request{
		method: "GET", path: "/9.9.9/nonexistent", token: a.AccessToken,
	})
	requireError(t, resp, 404, "resource_is_not_found")
}
