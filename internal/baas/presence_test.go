package baas

import (
	"testing"
	"time"

	"github.com/n-popescu/switchnet/internal/db"
)

// Presence is what the friends feature is judged on: a friend list where
// everybody is permanently offline looks broken even though every other endpoint
// works. These tests cover the two rules that make it behave, and the failure
// each one prevents.

// TestAnIdleConsoleReadsAsOnlineNotOffline is the liveness rule.
//
// The console does not report anything while it sits in the Home Menu, which is
// exactly where players look at each other's status. But it keeps polling its
// friend list. Without treating that traffic as a heartbeat, the normal state of
// a friend is offline.
func TestAnIdleConsoleReadsAsOnlineNotOffline(t *testing.T) {
	h := newHarness(t)
	alice := h.newAccount()
	bob := h.newAccount()
	h.befriend(alice, bob)

	// Alice never reports a state. She only polls her own friend list, the way a
	// console in the Home Menu does.
	h.advance(2 * time.Minute)
	resp := h.do(request{
		method: "GET", path: "/2.0.0/users/" + alice.UserID + "/friends",
		token: alice.AccessToken,
	})
	requireStatus(t, resp, 200)

	if got := h.friends(bob)[0].Presence.State; got != db.StateOnline {
		t.Errorf("an idle console reads as %q; want ONLINE. Without the liveness "+
			"path, every friend is permanently offline.", got)
	}
}

// TestAReportedPlayingStateExpires is the TTL rule.
//
// The console reports PLAYING when a game starts and does not report again.
// Without a TTL, a console that was switched off mid-game shows as playing
// forever.
func TestAReportedPlayingStateExpires(t *testing.T) {
	h := newHarness(t)
	alice := h.newAccount()
	bob := h.newAccount()
	h.befriend(alice, bob)

	resp := h.reportPresence(alice, db.StatePlaying, splatoon3TitleID, `{"mode":"regular"}`)
	requireStatus(t, resp, 200)

	if got := h.friends(bob)[0].Presence.State; got != db.StatePlaying {
		t.Fatalf("presence right after the report = %q, want PLAYING", got)
	}

	// Past both TTLs: the console has said nothing and has made no request.
	h.advance(testPresenceTTL + testConsoleAliveTTL + time.Minute)

	after := h.friends(bob)[0].Presence
	if after.State != db.StateOffline {
		t.Errorf("presence after both TTLs expired = %q, want OFFLINE", after.State)
	}
	// The app fields have to be cleared too, or a friend list shows "playing X"
	// for a game that was closed an hour ago.
	if got := after.Extras[db.ScopeFriends][keyAppID]; got != nil {
		t.Errorf("appInfo:appId is still %v after the state expired", got)
	}
	if got := after.Extras[db.ScopeFriends][keyAppField]; got != nil {
		t.Errorf("appField is still %v after the state expired", got)
	}
}

// TestALiveConsoleWhoseReportWentStaleReadsAsOnlineNotPlaying is the interaction
// between the two rules.
//
// The reported state has gone stale but the console is plainly still there. It
// is online, and it is not in a game as far as the server knows — reporting
// PLAYING would be a guess, and reporting OFFLINE would be wrong.
func TestALiveConsoleWhoseReportWentStaleReadsAsOnlineNotPlaying(t *testing.T) {
	h := newHarness(t, func(c *Config) {
		// A short report TTL and a long liveness TTL, so the two can be crossed
		// independently.
		c.PresenceTTL = time.Minute
		c.ConsolePresenceTTL = time.Hour
	})
	alice := h.newAccount()
	bob := h.newAccount()
	h.befriend(alice, bob)

	resp := h.reportPresence(alice, db.StatePlaying, splatoon3TitleID, `{}`)
	requireStatus(t, resp, 200)

	h.advance(5 * time.Minute)

	// Alice makes a request: any authenticated request is proof of life.
	resp = h.do(request{
		method: "GET", path: "/1.0.0/users/" + alice.UserID, token: alice.AccessToken,
	})
	requireStatus(t, resp, 200)

	got := h.friends(bob)[0].Presence
	if got.State != db.StateOnline {
		t.Errorf("state = %q, want ONLINE: the report is stale but the console is live",
			got.State)
	}
	if v := got.Extras[db.ScopeFriends][keyAppID]; v != nil {
		t.Errorf("appInfo:appId = %v, want it cleared once the report went stale", v)
	}
}

// TestAFreshReportBeatsLiveness proves a weak signal never downgrades a strong
// one. Liveness must not turn a PLAYING friend into a merely-online one.
func TestAFreshReportBeatsLiveness(t *testing.T) {
	h := newHarness(t)
	alice := h.newAccount()
	bob := h.newAccount()
	h.befriend(alice, bob)

	resp := h.reportPresence(alice, db.StatePlaying, splatoon3TitleID, `{}`)
	requireStatus(t, resp, 200)

	// Several ordinary requests, each of which bumps the liveness timestamp.
	for range 3 {
		h.advance(10 * time.Second)
		resp = h.do(request{
			method: "GET", path: "/1.0.0/users/" + alice.UserID, token: alice.AccessToken,
		})
		requireStatus(t, resp, 200)
	}

	if got := h.friends(bob)[0].Presence.State; got != db.StatePlaying {
		t.Errorf("state = %q, want PLAYING to survive the liveness heartbeat", got)
	}
}

// TestAnExplicitOfflineReportIsHonoured is the counterpart to the liveness rule.
//
// A console reporting OFFLINE is signing out. The liveness heartbeat exists for
// the case where nothing was reported, and it must not override a console that
// just told us it is going away — a user who logged out showing as online is
// its own kind of wrong.
func TestAnExplicitOfflineReportIsHonoured(t *testing.T) {
	h := newHarness(t)
	alice := h.newAccount()
	bob := h.newAccount()
	h.befriend(alice, bob)

	resp := h.reportPresence(alice, db.StatePlaying, splatoon3TitleID, `{}`)
	requireStatus(t, resp, 200)

	resp = h.reportPresence(alice, db.StateOffline, "", "")
	requireStatus(t, resp, 200)

	// Alice keeps making requests, so the liveness timestamp stays fresh.
	resp = h.do(request{
		method: "GET", path: "/1.0.0/users/" + alice.UserID, token: alice.AccessToken,
	})
	requireStatus(t, resp, 200)

	if got := h.friends(bob)[0].Presence.State; got != db.StateOffline {
		t.Errorf("state = %q after an explicit OFFLINE report, want OFFLINE: the "+
			"liveness heartbeat must not override what the console said", got)
	}
}

// TestAnOnlineReportClearsTheAppFields covers a console that closes a game.
//
// The console reports ONLINE but does not always clear the app fields itself, so
// a friend list would keep showing "playing X" for a game that is no longer
// running.
func TestAnOnlineReportClearsTheAppFields(t *testing.T) {
	h := newHarness(t)
	alice := h.newAccount()
	bob := h.newAccount()
	h.befriend(alice, bob)

	resp := h.reportPresence(alice, db.StatePlaying, splatoon3TitleID, `{"mode":"regular"}`)
	requireStatus(t, resp, 200)

	// Only the state changes: the app fields are left in place, the way a
	// console that closed a game does.
	resp = h.do(request{
		method: "PATCH",
		path:   "/1.0.0/users/" + alice.UserID + "/device_accounts/" + alice.DeviceAccountID,
		token:  alice.AccessToken,
		patch:  `[{"op":"replace","path":"/presence/state","value":"ONLINE"}]`,
	})
	requireStatus(t, resp, 200)

	got := h.friends(bob)[0].Presence
	if got.State != db.StateOnline {
		t.Fatalf("state = %q, want ONLINE", got.State)
	}
	if v := got.Extras[db.ScopeFriends][keyAppID]; v != nil {
		t.Errorf("appInfo:appId = %v, want it cleared once the state is not PLAYING", v)
	}
}

func TestReportingOfflineRecordsALogoutTime(t *testing.T) {
	h := newHarness(t)
	alice := h.newAccount()
	bob := h.newAccount()
	h.befriend(alice, bob)

	resp := h.reportPresence(alice, db.StatePlaying, splatoon3TitleID, `{}`)
	requireStatus(t, resp, 200)

	h.advance(time.Minute)
	resp = h.reportPresence(alice, db.StateOffline, "", "")
	requireStatus(t, resp, 200)

	// A console reporting OFFLINE is logging out. Recording when lets the
	// friends applet show "last online" instead of nothing.
	stored, err := h.store.GetPresence(t.Context(), alice.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LogoutAt == nil {
		t.Fatal("no logout time was recorded")
	}
	if !stored.LogoutAt.Equal(h.clock()) {
		t.Errorf("logoutAt = %v, want the time of the report %v", stored.LogoutAt, h.clock())
	}
}

// TestPresenceIsHiddenWhenTheOwnerSetsItToSelf covers the privacy setting.
func TestPresenceIsHiddenWhenTheOwnerSetsItToSelf(t *testing.T) {
	h := newHarness(t)
	alice := h.newAccount()
	bob := h.newAccount()
	h.befriend(alice, bob)

	resp := h.do(request{
		method: "PATCH", path: "/1.0.0/users/" + alice.UserID,
		token: alice.AccessToken,
		patch: `[{"op":"replace","path":"/permissions/presence","value":"SELF"}]`,
	})
	requireStatus(t, resp, 200)

	resp = h.reportPresence(alice, db.StatePlaying, splatoon3TitleID, `{}`)
	requireStatus(t, resp, 200)

	if got := h.friends(bob)[0].Presence.State; got != db.StateOffline {
		t.Errorf("Bob sees %q; a SELF presence setting must read as OFFLINE", got)
	}
	// Alice still sees her own state.
	if got := h.friends(alice); len(got) != 1 {
		t.Fatalf("Alice has %d friends", len(got))
	}
	resp = h.do(request{
		method: "GET", path: "/1.0.0/users/" + alice.UserID, token: alice.AccessToken,
	})
	requireStatus(t, resp, 200)
	var self UserResponse
	decode(t, resp, &self)
	if self.Presence.State != db.StatePlaying {
		t.Errorf("Alice's own view of her presence = %q, want PLAYING", self.Presence.State)
	}
}

func TestFavoriteFriendsOnlyPresence(t *testing.T) {
	h := newHarness(t)
	alice := h.newAccount()
	favourite := h.newAccount()
	ordinary := h.newAccount()
	h.befriend(alice, favourite)
	h.befriend(alice, ordinary)

	// The flag that matters is Alice's own, on Alice's side of the friendship.
	resp := h.do(request{
		method: "PATCH",
		path:   "/2.0.0/users/" + alice.UserID + "/friends/" + favourite.UserID,
		token:  alice.AccessToken,
		patch:  `[{"op":"replace","path":"/isFavorite","value":true}]`,
	})
	requireStatus(t, resp, 204)

	resp = h.do(request{
		method: "PATCH", path: "/1.0.0/users/" + alice.UserID,
		token: alice.AccessToken,
		patch: `[{"op":"replace","path":"/permissions/presence","value":"FAVORITE_FRIENDS"}]`,
	})
	requireStatus(t, resp, 200)

	resp = h.reportPresence(alice, db.StatePlaying, splatoon3TitleID, `{}`)
	requireStatus(t, resp, 200)

	if got := h.friendPresence(t, favourite, alice.UserID); got != db.StatePlaying {
		t.Errorf("a favourite sees %q, want PLAYING", got)
	}
	if got := h.friendPresence(t, ordinary, alice.UserID); got != db.StateOffline {
		t.Errorf("an ordinary friend sees %q, want OFFLINE", got)
	}
}

// ---- the presence patch itself ------------------------------------------

func TestPresencePatchMergesRatherThanReplaces(t *testing.T) {
	h := newHarness(t)
	alice := h.newAccount()
	bob := h.newAccount()
	h.befriend(alice, bob)

	resp := h.reportPresence(alice, db.StatePlaying, splatoon3TitleID, `{"mode":"regular"}`)
	requireStatus(t, resp, 200)

	// A later report that only moves the app field must not lose the app id. The
	// console sends only what changed, so replacing the block instead of merging
	// it would drop the field that makes "Playing X" appear.
	resp = h.do(request{
		method: "PATCH",
		path:   "/1.0.0/users/" + alice.UserID + "/device_accounts/" + alice.DeviceAccountID,
		token:  alice.AccessToken,
		patch: `[{"op":"add","path":"/presence/extras/friends/appField",` +
			`"value":"{\"mode\":\"ranked\"}"}]`,
	})
	requireStatus(t, resp, 200)

	scope := h.friends(bob)[0].Presence.Extras[db.ScopeFriends]
	if scope[keyAppID] != splatoon3TitleID {
		t.Errorf("appInfo:appId = %v after an appField-only report; the merge lost it",
			scope[keyAppID])
	}
	if scope[keyAppField] != `{"mode":"ranked"}` {
		t.Errorf("appField = %v, want the new value", scope[keyAppField])
	}
}

func TestPresenceStateIsValidated(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()

	for _, state := range []string{"PLAYINGGG", "online", "", "42"} {
		resp := h.do(request{
			method: "PATCH",
			path:   "/1.0.0/users/" + a.UserID + "/device_accounts/" + a.DeviceAccountID,
			token:  a.AccessToken,
			patch:  `[{"op":"replace","path":"/presence/state","value":"` + state + `"}]`,
		})
		if resp.StatusCode != 400 {
			t.Errorf("state %q: status = %d, want 400", state, resp.StatusCode)
		}
	}
}

func TestPresencePatchRejectsAnUnknownPath(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()

	// Rejecting rather than ignoring means a firmware that sends something new
	// shows up in the log instead of quietly not taking effect.
	resp := h.do(request{
		method: "PATCH",
		path:   "/1.0.0/users/" + a.UserID + "/device_accounts/" + a.DeviceAccountID,
		token:  a.AccessToken,
		patch:  `[{"op":"replace","path":"/presence/somethingNew","value":"x"}]`,
	})
	requireError(t, resp, 400, "invalid_params")
}

func TestPresencePatchRejectsAnUnknownExtrasScope(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()

	resp := h.do(request{
		method: "PATCH",
		path:   "/1.0.0/users/" + a.UserID + "/device_accounts/" + a.DeviceAccountID,
		token:  a.AccessToken,
		patch: `[{"op":"add","path":"/presence/extras/nobody/appInfo:appId",` +
			`"value":"0100c2500fc20000"}]`,
	})
	requireError(t, resp, 400, "invalid_params")
}

func TestTitleIDIsNormalisedToLowerCase(t *testing.T) {
	h := newHarness(t)
	alice := h.newAccount()
	bob := h.newAccount()
	h.befriend(alice, bob)

	// A title id is 16 hex digits and the console has been seen to send either
	// case. The denormalised column has to be one of them, or a dashboard query
	// on the title id misses half the rows.
	resp := h.reportPresence(alice, db.StatePlaying, "0100C2500FC20000", `{}`)
	requireStatus(t, resp, 200)

	stored, err := h.store.GetPresence(t.Context(), alice.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AppID != splatoon3TitleID {
		t.Errorf("stored app id = %q, want it lower-cased to %q", stored.AppID, splatoon3TitleID)
	}
}

// ---- profile patches ----------------------------------------------------

func TestProfilePatchPathsAndValidation(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()

	accepted := map[string]string{
		"nickname":               `[{"op":"replace","path":"/nickname","value":"Inkling"}]`,
		"country":                `[{"op":"replace","path":"/country","value":"NL"}]`,
		"birthday":               `[{"op":"replace","path":"/birthday","value":"1990-01-01"}]`,
		"personal analytics off": `[{"op":"replace","path":"/permissions/personalAnalytics","value":false}]`,
		"friend visibility":      `[{"op":"replace","path":"/permissions/friends","value":"FRIENDS"}]`,
		"self extras":            `[{"op":"add","path":"/extras/self/nxAccount","value":"abc"}]`,
	}
	for name, patch := range accepted {
		resp := h.do(request{
			method: "PATCH", path: "/1.0.0/users/" + a.UserID,
			token: a.AccessToken, patch: patch,
		})
		if resp.StatusCode != 200 {
			t.Errorf("%s: status = %d, want 200", name, resp.StatusCode)
		}
	}

	rejected := map[string]string{
		"unknown path":          `[{"op":"replace","path":"/somethingNew","value":"x"}]`,
		"nickname not a string": `[{"op":"replace","path":"/nickname","value":42}]`,
		"over-long nickname": `[{"op":"replace","path":"/nickname","value":"` +
			longString(65) + `"}]`,
		"invalid friend visibility": `[{"op":"replace","path":"/permissions/friends","value":"NOBODY"}]`,
		"invalid presence setting":  `[{"op":"replace","path":"/permissions/presence","value":"EVERYONE"}]`,
		"unsupported operation":     `[{"op":"move","from":"/nickname","path":"/country"}]`,
		"not an array":              `{"op":"replace","path":"/nickname","value":"x"}`,
	}
	for name, patch := range rejected {
		resp := h.do(request{
			method: "PATCH", path: "/1.0.0/users/" + a.UserID,
			token: a.AccessToken, patch: patch,
		})
		if resp.StatusCode != 400 {
			t.Errorf("%s: status = %d, want 400", name, resp.StatusCode)
		}
	}
}

func TestETagMovesWhenTheUserChanges(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()

	before := h.self(t, a).ETag
	h.advance(time.Second)
	h.setNickname(a, "Renamed")
	after := h.self(t, a).ETag

	// The console uses the ETag to decide whether its cached copy is stale, so it
	// has to move exactly when the user does.
	if before == after {
		t.Error("the ETag did not change after a profile edit")
	}
	if before == "" || after == "" {
		t.Error("an ETag is empty")
	}
}

// ---- helpers ------------------------------------------------------------

func (h *harness) self(t *testing.T, a account) UserResponse {
	t.Helper()
	resp := h.do(request{
		method: "GET", path: "/1.0.0/users/" + a.UserID, token: a.AccessToken,
	})
	requireStatus(t, resp, 200)

	var out UserResponse
	decode(t, resp, &out)
	return out
}

// friendPresence returns the state that viewer sees for one of their friends.
func (h *harness) friendPresence(t *testing.T, viewer account, friendID string) string {
	t.Helper()
	for _, f := range h.friends(viewer) {
		if f.ID == friendID {
			return f.Presence.State
		}
	}
	t.Fatalf("%s is not in %s's friend list", friendID, viewer.UserID)
	return ""
}

func longString(n int) string {
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = 'a'
	}
	return string(buf)
}
