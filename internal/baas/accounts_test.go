package baas

import (
	"net/url"
	"strings"
	"testing"
	"time"

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
	// A 2026-08-27 capture of a real registerUser response has no 'presence'
	// key at all -- only a friend-visible read of someone else's user object
	// does. Nickname is blank at registration (the console sets it later), so
	// 'nicknameUpdatedAt' is absent too, the same "not set yet" treatment as
	// Birthday's sentinel.
	if created.Presence != nil {
		t.Errorf("a freshly registered user's own view carries a presence block: %+v", created.Presence)
	}
	if created.NicknameUpdatedAt != 0 {
		t.Errorf("nicknameUpdatedAt = %d for a user with no nickname yet, want omitted (0)", created.NicknameUpdatedAt)
	}
	// Every extras scope must be present even when empty: the console indexes
	// into these without checking the key exists.
	for _, scope := range db.Scopes {
		if _, ok := created.Extras[scope]; !ok {
			t.Errorf("extras is missing the %q scope", scope)
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
	// A 2026-08-27 capture of a real /1.0.0/login response has no 'presence'
	// key in its own self-view user object at all -- only a friend-visible
	// read of someone else's does. Whether logging in actually marks the user
	// online for a friend is presence_test.go's job
	// (TestAnIdleConsoleReadsAsOnlineNotOffline and friends); this is only
	// about the response shape.
	if login.User.Presence != nil {
		t.Errorf("login's own user object carries a presence block: %+v", login.User.Presence)
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

// TestFederationRecordsAndReturnsTheLinkedNintendoAccount guards against a
// real bug found on real hardware 2026-08-26: recordNintendoAccountLink
// stored the linked id in extras, but userResponse never read it back into
// Links.NintendoAccount, so a console that just federated with a real id
// token got a 200 whose links object was still completely empty. The
// console gave up right there (error 2124-3121) without ever calling
// /1.0.0/users/{id}/link, because nothing in the response confirmed the
// federation it had just done actually took effect.
func TestFederationRecordsAndReturnsTheLinkedNintendoAccount(t *testing.T) {
	h := newHarness(t)
	a := h.register()

	now := h.clock()
	idToken, err := h.srv.naIDIssuer.Sign(sjwt.BAASTokenClaims{
		Subject:  "some-nintendo-account-id",
		Audience: "test",
		IssuedAt: now.Unix(),
		Expires:  now.Add(time.Minute).Unix(),
		Issuer:   "https://accounts.nintendo.com",
		TokenID:  sjwt.NewTokenID(),
		Type:     "id_token",
	})
	if err != nil {
		t.Fatal(err)
	}

	resp := h.do(request{
		method: "POST", path: "/1.0.0/federation",
		token: h.anonymousToken(),
		form: url.Values{
			"idp":      {"nintendoAccount"},
			"id":       {a.DeviceAccountID},
			"password": {a.Password},
			"idToken":  {idToken},
		},
	})
	requireStatus(t, resp, 200)

	var body loginResponse
	decode(t, resp, &body)
	// A 2026-08-27 capture of a real working federation response has no
	// 'presence' key in its own embedded user object at all -- SwitchNet used
	// to always include one. Sending it anyway is an extra key the console's
	// reader has not been confirmed to tolerate.
	if body.User.Presence != nil {
		t.Errorf("federation's own embedded user object carries a presence block: %+v", body.User.Presence)
	}
	if body.User.Links.NintendoAccount == nil {
		t.Fatal("federation succeeded but links.nintendoAccount is absent from the response")
	}
	if body.User.Links.NintendoAccount.ID != "some-nintendo-account-id" {
		t.Errorf("links.nintendoAccount.id = %q, want %q",
			body.User.Links.NintendoAccount.ID, "some-nintendo-account-id")
	}

	// A plain /1.0.0/login afterwards must also carry the link: it was
	// recorded on the user, not just returned once by federation itself.
	login := h.login(&a, "")
	if login.User.Links.NintendoAccount == nil {
		t.Error("a later /1.0.0/login lost the recorded Nintendo Account link")
	}

	// registerUser's own defaults ("0000-00-00", "") are for an anonymous
	// device profile, not a person who just federated with a real Nintendo
	// Account id. Sending those straight through in this same response
	// produced a real, undocumented hardware failure (2124-0292, 2026-08-26).
	if body.User.Birthday == "" || body.User.Birthday == "0000-00-00" {
		t.Errorf("federation's own embedded user.birthday = %q, want a real-looking date", body.User.Birthday)
	}
	if body.User.Country == "" {
		t.Error("federation's own embedded user.country is empty, want a real-looking ISO country code")
	}

	// A real working account's federation response always carries both
	// thumbnail sizes and a non-empty extras.self.nxAccount -- see
	// recordNintendoAccountLink's TODO(capture-needed) comment for what is
	// and is not known about the latter.
	if body.User.Thumbnail2URL == "" {
		t.Error("federation's own embedded user.thumbnail2Url is empty")
	}
	nxAccount, _ := body.User.Extras[db.ScopeSelf]["nxAccount"].(string)
	if nxAccount == "" {
		t.Error("federation's own embedded user.extras.self.nxAccount is empty")
	}
}

// TestNicknameUpdatedAtAppearsOnlyOnceANicknameIsSet guards the other half of
// the presence/nicknameUpdatedAt shape fix: registerUser's own response never
// carries 'nicknameUpdatedAt' (nobody has set a nickname yet, the same
// "not set yet" treatment as Birthday's own sentinel), but a real capture of a
// working account -- which always has a nickname by the time it logs in --
// always carries it. A fix that only ever exercises the "absent" branch could
// silently never populate the "present" one.
func TestNicknameUpdatedAtAppearsOnlyOnceANicknameIsSet(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()

	before := h.login(&a, "")
	if before.User.NicknameUpdatedAt != 0 {
		t.Errorf("nicknameUpdatedAt = %d before any nickname was set, want 0 (omitted)", before.User.NicknameUpdatedAt)
	}

	h.setNickname(a, "donut")

	after := h.login(&a, "")
	if after.User.NicknameUpdatedAt == 0 {
		t.Error("nicknameUpdatedAt is still 0 (omitted) after setting a nickname")
	}
}

// TestFederationBackfillsCountryFromTheRequestNotAHardcodedDefault guards a
// real gap found 2026-08-27: recordNintendoAccountLink always backfilled
// "US" regardless of the naCountry the console actually sent in the request
// -- silently wrong for anyone not in the US, and a real capture of a
// working account showed a French naCountry ("FR") end to end.
func TestFederationBackfillsCountryFromTheRequestNotAHardcodedDefault(t *testing.T) {
	h := newHarness(t)
	a := h.register()

	now := h.clock()
	idToken, err := h.srv.naIDIssuer.Sign(sjwt.BAASTokenClaims{
		Subject:  "some-other-nintendo-account-id",
		Audience: "test",
		IssuedAt: now.Unix(),
		Expires:  now.Add(time.Minute).Unix(),
		Issuer:   "https://accounts.nintendo.com",
		TokenID:  sjwt.NewTokenID(),
		Type:     "id_token",
	})
	if err != nil {
		t.Fatal(err)
	}

	resp := h.do(request{
		method: "POST", path: "/1.0.0/federation",
		token: h.anonymousToken(),
		form: url.Values{
			"idp":       {"nintendoAccount"},
			"id":        {a.DeviceAccountID},
			"password":  {a.Password},
			"idToken":   {idToken},
			"naCountry": {"FR"},
		},
	})
	requireStatus(t, resp, 200)

	var body loginResponse
	decode(t, resp, &body)
	if body.User.Country != "FR" {
		t.Errorf("federation's own embedded user.country = %q, want the naCountry it was sent, %q",
			body.User.Country, "FR")
	}
}

// TestFederationBackfillsBirthdayAndCountryEvenWhenTheLinkAlreadyExisted
// guards the early-return that shipped alongside the very first version of
// this feature: recordNintendoAccountLink skipped its whole body, birthday
// and country included, whenever the Nintendo Account id was already
// recorded from an earlier federation call. On real hardware every retry
// after the first hit exactly this path.
func TestFederationBackfillsBirthdayAndCountryEvenWhenTheLinkAlreadyExisted(t *testing.T) {
	h := newHarness(t)
	person := h.register()
	firstDevice := h.register()

	now := h.clock()
	sign := func() string {
		idToken, err := h.srv.naIDIssuer.Sign(sjwt.BAASTokenClaims{
			Subject:  person.UserID,
			Audience: "test",
			IssuedAt: now.Unix(),
			Expires:  now.Add(time.Minute).Unix(),
			Issuer:   "https://accounts.nintendo.com",
			TokenID:  sjwt.NewTokenID(),
			Type:     "id_token",
		})
		if err != nil {
			t.Fatal(err)
		}
		return idToken
	}

	// First federation: records the link, and should already backfill.
	first := h.do(request{
		method: "POST", path: "/1.0.0/federation",
		token: h.anonymousToken(),
		form: url.Values{
			"idp": {"nintendoAccount"}, "id": {firstDevice.DeviceAccountID},
			"password": {firstDevice.Password}, "idToken": {sign()},
		},
	})
	requireStatus(t, first, 200)

	// Reset to the sentinels directly, as if backfilling had never run, so
	// the second federation call is the only thing that could have fixed
	// them -- proving the early-return path backfills too, not just the
	// first-ever call.
	sentinelBirthday, sentinelCountry := "0000-00-00", ""
	if _, err := h.store.UpdateUser(t.Context(), person.UserID,
		db.UserPatch{Birthday: &sentinelBirthday, Country: &sentinelCountry}); err != nil {
		t.Fatal(err)
	}

	// A second console federating to the same, already-linked person.
	secondDevice := h.register()
	second := h.do(request{
		method: "POST", path: "/1.0.0/federation",
		token: h.anonymousToken(),
		form: url.Values{
			"idp": {"nintendoAccount"}, "id": {secondDevice.DeviceAccountID},
			"password": {secondDevice.Password}, "idToken": {sign()},
		},
	})
	requireStatus(t, second, 200)

	var body loginResponse
	decode(t, second, &body)
	if body.User.Birthday == "" || body.User.Birthday == "0000-00-00" {
		t.Errorf("birthday = %q after a repeat federation call, want it backfilled", body.User.Birthday)
	}
	if body.User.Country == "" {
		t.Error("country is empty after a repeat federation call, want it backfilled")
	}
}

// TestFederationAttachesAFreshDeviceAccountToAnExistingPerson is the design
// this whole reassignment mechanism exists for, described by the operator
// 2026-08-26 after real hardware showed the gap: signing in on the Nintendo
// Account link page is signing in as an existing person (here: someone who
// registered on the website), not minting a new one. Before this, the
// console's "Transfer" flow registered a brand new, empty SwitchNet user for
// every console it ran on and merely labelled it with the real person's id —
// no friend code, no history, nothing carried over. This asserts the device
// account /1.0.0/users just created ends up truly owned by the person who
// signed in, not just decorated with their id.
func TestFederationAttachesAFreshDeviceAccountToAnExistingPerson(t *testing.T) {
	h := newHarness(t)

	// The person: registered independently, has their own friend code and
	// history already — standing in for someone who signed up on the
	// website before ever touching this console.
	person := h.register()
	h.login(&person, "")
	friendCode := h.generateFriendCode(person)

	// The console: a brand new, otherwise-unrelated device account, exactly
	// what /1.0.0/users hands back before any id token exists.
	fresh := h.register()

	now := h.clock()
	idToken, err := h.srv.naIDIssuer.Sign(sjwt.BAASTokenClaims{
		Subject:  person.UserID,
		Audience: "test",
		IssuedAt: now.Unix(),
		Expires:  now.Add(time.Minute).Unix(),
		Issuer:   "https://accounts.nintendo.com",
		TokenID:  sjwt.NewTokenID(),
		Type:     "id_token",
	})
	if err != nil {
		t.Fatal(err)
	}

	resp := h.do(request{
		method: "POST", path: "/1.0.0/federation",
		token: h.anonymousToken(),
		form: url.Values{
			"idp":      {"nintendoAccount"},
			"id":       {fresh.DeviceAccountID},
			"password": {fresh.Password},
			"idToken":  {idToken},
		},
	})
	requireStatus(t, resp, 200)

	var body loginResponse
	decode(t, resp, &body)
	if body.User.ID != person.UserID {
		t.Errorf("federation logged in as %q, want the person it verified, %q",
			body.User.ID, person.UserID)
	}
	if body.User.Links.FriendCode == nil || body.User.Links.FriendCode.ID != friendCode {
		t.Error("the attached identity lost its friend code, so this is a fresh person, not the real one")
	}

	// The device account genuinely moved: logging into it again authenticates
	// as the person, not the fresh empty user /1.0.0/users originally made.
	loginResp := h.do(request{
		method: "POST", path: "/1.0.0/login",
		token: h.anonymousToken(),
		form: url.Values{
			"id": {fresh.DeviceAccountID}, "password": {fresh.Password},
			"naCountry": {"NL"}, "isPersistent": {"true"},
		},
	})
	requireStatus(t, loginResp, 200)
	var loginBody loginResponse
	decode(t, loginResp, &loginBody)
	if loginBody.User.ID != person.UserID {
		t.Errorf("a later plain login still authenticates as %q, want %q",
			loginBody.User.ID, person.UserID)
	}
}

// TestFederationWithAnUnverifiableIDTokenDoesNotReassign guards the fail-safe
// side of attaching: an id token this server cannot verify — a real Nintendo
// Account token, or anything else that is not one nalink.go itself signed —
// must never move a device account. Only its subject gets recorded as a
// label, exactly as before this feature existed.
func TestFederationWithAnUnverifiableIDTokenDoesNotReassign(t *testing.T) {
	h := newHarness(t)
	person := h.register()
	fresh := h.register()

	// Signed with a key this server never issued, so it looks like a
	// plausible id token but cannot verify against idVerifier.
	foreignRing := sjwt.NewKeyring(t.TempDir(), true)
	otherKey, err := foreignRing.Key("foreign")
	if err != nil {
		t.Fatal(err)
	}
	foreignIssuer := sjwt.NewIssuer(otherKey, "https://accounts.nintendo.com/keys", false)
	idToken, err := foreignIssuer.Sign(sjwt.BAASTokenClaims{
		Subject:  person.UserID,
		Audience: "test",
		IssuedAt: h.clock().Unix(),
		Expires:  h.clock().Add(time.Minute).Unix(),
		Issuer:   "https://accounts.nintendo.com",
		TokenID:  sjwt.NewTokenID(),
		Type:     "id_token",
	})
	if err != nil {
		t.Fatal(err)
	}

	resp := h.do(request{
		method: "POST", path: "/1.0.0/federation",
		token: h.anonymousToken(),
		form: url.Values{
			"idp":      {"nintendoAccount"},
			"id":       {fresh.DeviceAccountID},
			"password": {fresh.Password},
			"idToken":  {idToken},
		},
	})
	requireStatus(t, resp, 200)

	var body loginResponse
	decode(t, resp, &body)
	if body.User.ID != fresh.UserID {
		t.Errorf("an unverifiable id token reassigned the device account to %q, want it to stay %q",
			body.User.ID, fresh.UserID)
	}
	// The subject is still recorded as a label, same as before this feature.
	if body.User.Links.NintendoAccount == nil || body.User.Links.NintendoAccount.ID != person.UserID {
		t.Error("an unverifiable id token's subject should still be recorded as a label")
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

	// /1.0.0/certificates publishes two keys: idIssuer's (the id token in a
	// login/federation response) and naIDIssuer's (the Nintendo Account link
	// flow's id token, nalink.go) -- see sjwt.IssuerNAID's doc comment for why
	// they must be different keys despite sharing this one URL.
	if len(idSet.Keys) != 2 || len(accessSet.Keys) != 1 {
		t.Fatalf("key set sizes = %d and %d, want 2 and 1", len(idSet.Keys), len(accessSet.Keys))
	}
	if idSet.Keys[0].KeyID == idSet.Keys[1].KeyID {
		t.Error("idIssuer and naIDIssuer publish the same key at /1.0.0/certificates")
	}
	// The id and access sets must publish different keys. Sharing one would
	// mean an id token verifies as an access token.
	for _, k := range idSet.Keys {
		if k.KeyID == accessSet.Keys[0].KeyID {
			t.Error("/1.0.0/certificates and /1.0.0/internal_certificates publish the same key")
		}
		// Nintendo marks these with a 'usage' field.
		if k.Usage != "developer" {
			t.Errorf("id token key usage = %q, want developer", k.Usage)
		}
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

	// /1.0.0/certificates now publishes two keys (idIssuer's and naIDIssuer's,
	// see sjwt.IssuerNAID) -- login's id token is signed with idIssuer's, and
	// its kid must be *one of* the published set, not necessarily the first.
	set := h.jwks(t, "/1.0.0/certificates")
	if len(set.Keys) != 2 {
		t.Fatalf("key set has %d keys, want 2", len(set.Keys))
	}

	claims := parseBAASClaims(t, login.IDToken)
	if claims.Subject != a.UserID {
		t.Errorf("id token sub = %q, want %q", claims.Subject, a.UserID)
	}

	// The 'kid' in the token has to be in the published set, or a game server
	// cannot pick the right key.
	header, _ := decodeTokenParts(t, login.IDToken)
	found := false
	for _, k := range set.Keys {
		if k.KeyID == header["kid"] {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("id token kid = %v, not among the published kids", header["kid"])
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
