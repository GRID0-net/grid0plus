package baas

import "testing"

// TestUpdatePushChannelEchoesTheRealShape covers PUT
// /1.0.0/push_channels/{uid}/{daid}.
//
// Previously this route answered a bare 204 (acknowledge). A real, working
// Nextendo capture (2026-08-27) shows a real server answers 200 with a JSON
// body echoing the request plus createdAt/updatedAt/userId/deviceAccountId
// and an always-empty userAttributes -- see devices.go's own comment.
func TestUpdatePushChannelEchoesTheRealShape(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()

	resp := h.do(request{
		method: "PUT", path: "/1.0.0/push_channels/" + a.UserID + "/" + a.DeviceAccountID,
		token: a.AccessToken,
		json:  `{"service":"fcm","deviceToken":"some-token","deviceAttributes":{"osVersion":"1.0.0"}}`,
	})
	requireStatus(h.t, resp, 200)

	var body struct {
		Service          string         `json:"service"`
		DeviceToken      string         `json:"deviceToken"`
		DeviceAttributes map[string]any `json:"deviceAttributes"`
		UserAttributes   map[string]any `json:"userAttributes"`
		UserID           string         `json:"userId"`
		DeviceAccountID  string         `json:"deviceAccountId"`
		CreatedAt        int64          `json:"createdAt"`
		UpdatedAt        int64          `json:"updatedAt"`
	}
	decode(h.t, resp, &body)

	if body.Service != "fcm" {
		t.Errorf("service = %q, want fcm", body.Service)
	}
	if body.DeviceToken != "some-token" {
		t.Errorf("deviceToken = %q, want some-token", body.DeviceToken)
	}
	if body.DeviceAttributes["osVersion"] != "1.0.0" {
		t.Errorf("deviceAttributes not echoed back: %#v", body.DeviceAttributes)
	}
	if body.UserAttributes == nil || len(body.UserAttributes) != 0 {
		t.Errorf("userAttributes = %#v, want an empty object", body.UserAttributes)
	}
	if body.UserID != a.UserID {
		t.Errorf("userId = %q, want %q", body.UserID, a.UserID)
	}
	if body.DeviceAccountID != a.DeviceAccountID {
		t.Errorf("deviceAccountId = %q, want %q", body.DeviceAccountID, a.DeviceAccountID)
	}
	if body.CreatedAt == 0 || body.UpdatedAt == 0 {
		t.Errorf("createdAt/updatedAt not set: %d/%d", body.CreatedAt, body.UpdatedAt)
	}
}

// TestUpdatePushChannelRefusesAnotherAccount covers the same identity guard
// every other user-scoped endpoint has -- see CLAUDE.md's note on the bug
// where a console acted as the wrong person.
func TestUpdatePushChannelRefusesAnotherAccount(t *testing.T) {
	h := newHarness(t)
	alice := h.newAccount()
	bob := h.newAccount()

	resp := h.do(request{
		method: "PUT", path: "/1.0.0/push_channels/" + bob.UserID + "/" + bob.DeviceAccountID,
		token: alice.AccessToken,
		json:  `{"service":"fcm","deviceToken":"some-token"}`,
	})
	requireStatus(h.t, resp, 403)
}

// TestDevicesSnapshotSplitsPersistentAndDeletedUsers covers POST
// /1.0.0/devices/snapshot.
//
// Previously this route answered a bare 204 (acknowledge). A real, working
// Nextendo capture (2026-08-27) shows a real server answers 200 with
// {"deletedUserIds": [...], "persistentUserIds": [...]} -- see devices.go's
// own comment for why that split costs nothing extra to implement for real.
func TestDevicesSnapshotSplitsPersistentAndDeletedUsers(t *testing.T) {
	h := newHarness(t)
	alice := h.newAccount()
	bob := h.newAccount()
	const unknown = "0000000000000000"

	resp := h.do(request{
		method: "POST", path: "/1.0.0/devices/snapshot",
		token: h.anonymousToken(),
		json:  `{"userIds":["` + alice.UserID + `","` + bob.UserID + `","` + unknown + `"]}`,
	})
	requireStatus(h.t, resp, 200)

	var body struct {
		PersistentUserIDs []string `json:"persistentUserIds"`
		DeletedUserIDs    []string `json:"deletedUserIds"`
	}
	decode(h.t, resp, &body)

	if len(body.PersistentUserIDs) != 2 ||
		!containsID(body.PersistentUserIDs, alice.UserID) ||
		!containsID(body.PersistentUserIDs, bob.UserID) {
		t.Errorf("persistentUserIds = %v, want [%s %s]", body.PersistentUserIDs, alice.UserID, bob.UserID)
	}
	if len(body.DeletedUserIDs) != 1 || body.DeletedUserIDs[0] != unknown {
		t.Errorf("deletedUserIds = %v, want [%s]", body.DeletedUserIDs, unknown)
	}
}

func containsID(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// The token is how a push to this user finds the console's npns.
func TestAPushChannelIsRememberedForItsOwnUser(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	resp := h.do(request{
		method: "PUT", path: "/1.0.0/push_channels/" + a.UserID + "/" + a.DeviceAccountID,
		token: a.AccessToken,
		json:  `{"service":"npns","deviceToken":"0009c95be1ac9b2c1e9d9d9fc8bf3302c7f5"}`,
	})
	requireStatus(h.t, resp, 200)
	chans, err := h.store.PushChannels(t.Context(), []string{a.UserID})
	if err != nil || len(chans) != 1 || chans[0].DeviceToken != "0009c95be1ac9b2c1e9d9d9fc8bf3302c7f5" ||
		chans[0].DeviceAccountID != a.DeviceAccountID {
		t.Fatalf("push channels = %+v, %v", chans, err)
	}
}
