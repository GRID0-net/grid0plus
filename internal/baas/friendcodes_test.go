package baas

import (
	"net/url"
	"regexp"
	"testing"
	"time"
)

var friendCodePattern = regexp.MustCompile(`^\d{4}-\d{4}-\d{4}$`)

func TestFriendCodeShapeAndUniqueness(t *testing.T) {
	h := newHarness(t)

	seen := map[string]bool{}
	for range 10 {
		a := h.newAccount()
		code := h.friendCode(a)
		if !friendCodePattern.MatchString(code) {
			t.Fatalf("friend code %q is not dddd-dddd-dddd", code)
		}
		if seen[code] {
			t.Fatalf("friend code %q was issued twice", code)
		}
		seen[code] = true
	}
}

// TestFriendCodeExistsFromAccountCreation checks that a fresh account has a
// friend code from the moment it exists (see friendcodes.go's
// assignFriendCode), the same as System Settings shows one immediately with no
// Nintendo Account link required. A federation/login response without
// links.friendCode makes the console abandon the link, and it never calls
// generate_code itself to fix that.
func TestFriendCodeExistsFromAccountCreation(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()

	if h.self(t, a).Links.FriendCode == nil {
		t.Error("a fresh account has no friend code")
	}
}

// TestFriendCodeCooldown covers the 24-hour regeneration window.
//
// The cooldown is not decoration: without it, anybody who was given a code can be
// locked out of using it by the owner regenerating on a loop.
func TestFriendCodeCooldown(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()

	// The account already has a code from creation -- read it rather than
	// generating one, which would immediately hit the cooldown below.
	first := h.friendCode(a)

	// Immediately after, the code is not regenerable and the endpoint says so
	// with Nintendo's dedicated 422.
	resp := h.do(request{
		method: "POST", path: "/1.0.0/users/" + a.UserID + "/generate_code",
		token: a.AccessToken, form: url.Values{"type": {"NX"}},
	})
	requireError(t, resp, 422, "friend_code_unregenerable_state")

	// The user object has to tell the console when it may ask again, or the
	// friends applet has nothing to show for the greyed-out button.
	fc := h.self(t, a).Links.FriendCode
	if fc == nil {
		t.Fatal("no friend code")
	}
	if fc.Regenerable {
		t.Error("regenerable is true while the cooldown is running")
	}
	if fc.RegenerableAt <= fc.UpdatedAt {
		t.Errorf("regenerableAt (%d) is not after updatedAt (%d)",
			fc.RegenerableAt, fc.UpdatedAt)
	}

	// Just short of the window: still refused.
	h.advance(testFriendCodeCooldn - time.Minute)
	resp = h.do(request{
		method: "POST", path: "/1.0.0/users/" + a.UserID + "/generate_code",
		token: a.AccessToken, form: url.Values{"type": {"NX"}},
	})
	requireError(t, resp, 422, "friend_code_unregenerable_state")

	// Past it: allowed, and the code changes.
	h.advance(2 * time.Minute)
	if fc := h.self(t, a).Links.FriendCode; !fc.Regenerable {
		t.Error("regenerable is false after the cooldown elapsed")
	}
	second := h.generateFriendCode(a)
	if second == first {
		t.Error("regenerating returned the same code")
	}
}

func TestGenerateCodeRejectsAnUnknownType(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()

	resp := h.do(request{
		method: "POST", path: "/1.0.0/users/" + a.UserID + "/generate_code",
		token: a.AccessToken, form: url.Values{"type": {"WII"}},
	})
	requireError(t, resp, 400, "invalid_params")

	// An absent type is tolerated: the parameter is trivially defaulted, and
	// refusing it would block the feature on a firmware that stopped sending it.
	// Past the account-creation cooldown first, since this account already has
	// a code from creation -- otherwise this second call would 422 on the
	// cooldown rather than exercising the type validation this test is for.
	h.advance(testFriendCodeCooldn + time.Minute)
	resp = h.do(request{
		method: "POST", path: "/1.0.0/users/" + a.UserID + "/generate_code",
		token: a.AccessToken, form: url.Values{},
	})
	requireStatus(t, resp, 200)
}

func TestNormaliseFriendCode(t *testing.T) {
	cases := map[string]string{
		"7779-8151-4794":     "7779-8151-4794",
		"777981514794":       "7779-8151-4794",
		"SW-7779-8151-4794":  "7779-8151-4794",
		"sw-7779-8151-4794":  "7779-8151-4794",
		"  7779-8151-4794  ": "7779-8151-4794",
		"7779 8151 4794":     "7779-8151-4794",

		// Not twelve digits: not a code.
		"":                  "",
		"7779-8151":         "",
		"7779-8151-47945":   "",
		"not-a-code":        "",
		"SW-abcd-efgh-ijkl": "",
	}
	for input, want := range cases {
		if got := normaliseFriendCode(input); got != want {
			t.Errorf("normaliseFriendCode(%q) = %q, want %q", input, got, want)
		}
	}
}

// ---- finding a user -----------------------------------------------------

func TestFindUserByFriendCode(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	b := h.newAccount()
	h.setNickname(b, "Bob")
	code := h.friendCode(b)

	resp := h.do(request{
		method: "GET", path: "/1.0.0/users?friendCode=" + url.QueryEscape(code),
		token: a.AccessToken,
	})
	requireStatus(t, resp, 200)

	var found CollectionEnvelope[UserResponse]
	decode(t, resp, &found)
	if len(found.Items) != 1 || found.Items[0].ID != b.UserID {
		t.Fatalf("lookup returned %+v, want just B", found)
	}
	if found.Items[0].Nickname != "Bob" {
		t.Errorf("nickname = %q, want Bob", found.Items[0].Nickname)
	}
	// A stranger sees enough to be added and nothing more.
	if len(found.Items[0].DeviceAccounts) != 0 {
		t.Error("a stranger lookup exposed device accounts")
	}
}

// TestFindUserByFriendCodeRealQueryParam is TestFindUserByFriendCode's own
// query, "?friendCode=", replaced with the real parameter name: the console
// filters on "filter.links.friendCode.id.$in", a MongoDB-style filter, not a
// plain key.
func TestFindUserByFriendCodeRealQueryParam(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	b := h.newAccount()
	h.setNickname(b, "Bob")
	code := h.friendCode(b)

	resp := h.do(request{
		method: "GET", path: "/1.0.0/users?filter.links.friendCode.id.$in=" + url.QueryEscape(code),
		token: a.AccessToken,
	})
	requireStatus(t, resp, 200)

	var found CollectionEnvelope[UserResponse]
	decode(t, resp, &found)
	if len(found.Items) != 1 || found.Items[0].ID != b.UserID {
		t.Fatalf("lookup returned %+v, want just B", found)
	}
}

// TestUserLookupByFilterIDIN reads the id form the console actually sends.
//
// Its builder formats "filter.id.$in=" followed by "%016llx" per id, comma
// separated, after de-duplicating; and the response parsers beside it read
// $.items[*]. A server that answers either with a 400 (an unfiltered query) or
// with a bare array fails the call the applet is built around.
func TestUserLookupByFilterIDIN(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	b := h.newAccount()
	c := h.newAccount()
	h.setNickname(b, "Bob")
	h.setNickname(c, "Carol")

	resp := h.do(request{
		method: "GET",
		path:   "/1.0.0/users?filter.id.$in=" + b.UserID + "," + c.UserID + "," + b.UserID,
		token:  a.AccessToken,
	})
	requireStatus(t, resp, 200)

	var found CollectionEnvelope[UserResponse]
	decode(t, resp, &found)
	// Two, not three: the console de-duplicates before it asks, so it never
	// sends a repeat, but a server that echoes one back twice would make the
	// applet's own list disagree with its cache.
	if len(found.Items) != 2 {
		t.Fatalf("got %d users for an $in of two ids, want 2", len(found.Items))
	}
	got := map[string]bool{}
	for _, u := range found.Items {
		got[u.ID] = true
	}
	if !got[b.UserID] || !got[c.UserID] {
		t.Errorf("lookup by $in returned %+v, want both B and C", got)
	}
}

func TestFindUsersRefusesAnUnfilteredQuery(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	for range 3 {
		h.newAccount()
	}

	// An unfiltered query would enumerate every account on the server to any
	// authenticated console, which is the member list of a private group.
	resp := h.do(request{
		method: "GET", path: "/1.0.0/users", token: a.AccessToken,
	})
	requireError(t, resp, 400, "invalid_params")
}

func TestFindUsersDoesNotReturnTheCaller(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	code := h.friendCode(a)

	// Finding yourself by your own code is not useful, and if the console acts
	// on it the result is a self-friend-request.
	resp := h.do(request{
		method: "GET", path: "/1.0.0/users?friendCode=" + url.QueryEscape(code),
		token: a.AccessToken,
	})
	requireStatus(t, resp, 200)

	var found CollectionEnvelope[UserResponse]
	decode(t, resp, &found)
	if len(found.Items) != 0 {
		t.Errorf("the caller's own account came back: %+v", found)
	}
}
