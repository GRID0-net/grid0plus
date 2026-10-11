package baas

import (
	"net/http"
	"net/url"
	"testing"
)

// emulatorLogin signs a person in the way an emulator does (POST /login)
// and returns the access token its friend service uses.
func emulatorLogin(t *testing.T, h *harness, username, password string) string {
	t.Helper()
	registerWebsiteUser(t, h, username, password)
	resp, err := http.PostForm(h.server.URL+"/login", url.Values{"username": {username}, "password": {password}})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, resp, 200)
	var body loginResponse
	decode(t, resp, &body)
	if body.AccessToken == "" {
		t.Fatal("no access token from /login")
	}
	return body.AccessToken
}

// Two emulators become friends by friend code and then see each other's
// presence, including the game's presence blob a join needs, under the NSA
// id NPLN names friends by.
func TestEmulatorsBecomeFriendsAndSeeEachOthersPresence(t *testing.T) {
	h := newHarness(t)
	alice := emulatorLogin(t, h, "alice", "password-a")
	bob := emulatorLogin(t, h, "bob", "password-b")

	var bobMe emulatorFriend
	resp := h.do(request{method: "GET", path: "/emulator/v1/me", token: bob})
	requireStatus(t, resp, 200)
	decode(t, resp, &bobMe)
	if bobMe.FriendCode == "" || bobMe.ID == "" {
		t.Fatalf("me = %+v", bobMe)
	}

	resp = h.do(request{method: "POST", path: "/emulator/v1/friend-requests", token: alice,
		json: `{"friendCode":"` + bobMe.FriendCode + `"}`})
	requireStatus(t, resp, 201)

	var pending struct {
		Incoming []emulatorRequest `json:"incoming"`
	}
	resp = h.do(request{method: "GET", path: "/emulator/v1/friend-requests", token: bob})
	requireStatus(t, resp, 200)
	decode(t, resp, &pending)
	if len(pending.Incoming) != 1 {
		t.Fatalf("bob's incoming = %+v", pending)
	}
	resp = h.do(request{method: "POST", path: "/emulator/v1/friend-requests/" + pending.Incoming[0].ID + "/accept", token: bob})
	requireStatus(t, resp, 204)

	resp = h.do(request{method: "POST", path: "/emulator/v1/presence", token: bob,
		json: `{"state":"PLAYING","appId":"0100c2500fc20000","appField":"AAEC"}`})
	requireStatus(t, resp, 204)

	var list struct {
		Friends []emulatorFriend `json:"friends"`
	}
	resp = h.do(request{method: "GET", path: "/emulator/v1/friends", token: alice})
	requireStatus(t, resp, 200)
	decode(t, resp, &list)
	if len(list.Friends) != 1 {
		t.Fatalf("alice's friends = %+v", list)
	}
	f := list.Friends[0]
	if f.ID != bobMe.ID || f.State != "PLAYING" || f.AppID != "0100c2500fc20000" || f.AppField != "AAEC" {
		t.Errorf("friend = %+v", f)
	}

	// In a game the forks mostly pass the game's status 1: still playing.
	resp = h.do(request{method: "POST", path: "/emulator/v1/presence", token: bob,
		json: `{"state":"ONLINE","appId":"0100c2500fc20000","appField":"AAEC"}`})
	requireStatus(t, resp, 204)
	resp = h.do(request{method: "GET", path: "/emulator/v1/friends", token: alice})
	requireStatus(t, resp, 200)
	decode(t, resp, &list)
	if f := list.Friends[0]; f.State != "PLAYING" || f.AppID != "0100c2500fc20000" {
		t.Errorf("ONLINE with a game: friend = %+v, want PLAYING it", f)
	}

	resp = h.do(request{method: "POST", path: "/emulator/v1/presence", token: bob,
		json: `{"state":"SLEEPING"}`})
	requireStatus(t, resp, 400)
}
