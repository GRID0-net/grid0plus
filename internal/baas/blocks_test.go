package baas

import (
	"net/url"
	"testing"
)

// TestBlockingTearsDownTheRelationship covers the rule that makes a block
// actually a block.
//
// A block that leaves the friendship in place is not a block: the two would
// still see each other in their friend lists.
func TestBlockingTearsDownTheRelationship(t *testing.T) {
	h := newHarness(t)
	alice := h.newAccount()
	bob := h.newAccount()
	h.befriend(alice, bob)

	resp := h.do(request{
		method: "POST", path: "/1.0.0/users/" + alice.UserID + "/blocks",
		token: alice.AccessToken,
		json:  `{"userId":"` + bob.UserID + `"}`,
	})
	requireStatus(t, resp, 204)

	if len(h.friends(alice)) != 0 {
		t.Error("Alice still has Bob as a friend after blocking him")
	}
	if len(h.friends(bob)) != 0 {
		t.Error("Bob still has Alice as a friend after being blocked")
	}
	if got := h.relationship(t, alice, bob.UserID).Type; got != RelationshipBlocked {
		t.Errorf("Alice's relationship to Bob = %q, want BLOCKED", got)
	}
}

func TestBlockedUserCannotSendAFriendRequest(t *testing.T) {
	h := newHarness(t)
	alice := h.newAccount()
	bob := h.newAccount()
	aliceCode := h.generateFriendCode(alice)

	resp := h.do(request{
		method: "POST", path: "/1.0.0/users/" + alice.UserID + "/blocks",
		token: alice.AccessToken, json: `{"userId":"` + bob.UserID + `"}`,
	})
	requireStatus(t, resp, 204)

	// Bob does not know he is blocked, and must not be able to find out. The
	// error is the same one a user who refuses friend requests produces, so a
	// block is indistinguishable from a preference.
	resp = h.do(request{
		method: "POST", path: "/2.0.0/friend_requests",
		token: bob.AccessToken, json: `{"friendCode":"` + aliceCode + `"}`,
	})
	requireError(t, resp, 403, "forbidden")
}

func TestBlockSuppressesRequestsInBothDirections(t *testing.T) {
	h := newHarness(t)
	alice := h.newAccount()
	bob := h.newAccount()
	bobCode := h.generateFriendCode(bob)

	resp := h.do(request{
		method: "POST", path: "/1.0.0/users/" + alice.UserID + "/blocks",
		token: alice.AccessToken, json: `{"userId":"` + bob.UserID + `"}`,
	})
	requireStatus(t, resp, 204)

	// Alice blocked Bob, so Alice also cannot add Bob. A one-directional
	// suppression would let the blocker keep reaching the blocked user.
	resp = h.do(request{
		method: "POST", path: "/2.0.0/friend_requests",
		token: alice.AccessToken, json: `{"friendCode":"` + bobCode + `"}`,
	})
	requireError(t, resp, 403, "forbidden")
}

func TestBlockingCancelsAPendingRequest(t *testing.T) {
	h := newHarness(t)
	alice := h.newAccount()
	bob := h.newAccount()
	aliceCode := h.generateFriendCode(alice)

	resp := h.do(request{
		method: "POST", path: "/2.0.0/friend_requests",
		token: bob.AccessToken, json: `{"friendCode":"` + aliceCode + `"}`,
	})
	requireStatus(t, resp, 201)

	resp = h.do(request{
		method: "POST", path: "/1.0.0/users/" + alice.UserID + "/blocks",
		token: alice.AccessToken, json: `{"userId":"` + bob.UserID + `"}`,
	})
	requireStatus(t, resp, 204)

	// Leaving the request in the inbox means a blocked person can still put a
	// row on somebody's screen.
	var inbox CollectionEnvelope[FriendRequestResponse]
	resp = h.do(request{
		method: "GET", path: "/2.0.0/users/" + alice.UserID + "/friend_requests/inbox",
		token: alice.AccessToken,
	})
	requireStatus(t, resp, 200)
	decode(t, resp, &inbox)
	if len(inbox.Items) != 0 {
		t.Errorf("Alice's inbox still has %d entries from a blocked user", len(inbox.Items))
	}
}

func TestBlockedUserIsNotFindable(t *testing.T) {
	h := newHarness(t)
	alice := h.newAccount()
	bob := h.newAccount()
	bobCode := h.generateFriendCode(bob)

	resp := h.do(request{
		method: "POST", path: "/1.0.0/users/" + alice.UserID + "/blocks",
		token: alice.AccessToken, json: `{"userId":"` + bob.UserID + `"}`,
	})
	requireStatus(t, resp, 204)

	resp = h.do(request{
		method: "GET", path: "/1.0.0/users?friendCode=" + url.QueryEscape(bobCode),
		token: alice.AccessToken,
	})
	requireStatus(t, resp, 200)

	var found []UserResponse
	decode(t, resp, &found)
	if len(found) != 0 {
		t.Errorf("a blocked user is still findable: %+v", found)
	}
}

func TestBlockedRelationshipReturnsNoProfile(t *testing.T) {
	h := newHarness(t)
	alice := h.newAccount()
	bob := h.newAccount()
	h.setNickname(bob, "Bob")

	resp := h.do(request{
		method: "POST", path: "/1.0.0/users/" + alice.UserID + "/blocks",
		token: alice.AccessToken, json: `{"userId":"` + bob.UserID + `"}`,
	})
	requireStatus(t, resp, 204)

	rel := h.relationship(t, alice, bob.UserID)
	if rel.Type != RelationshipBlocked {
		t.Fatalf("relationship = %q, want BLOCKED", rel.Type)
	}
	// Confirming the account exists is exactly what a block is meant to prevent.
	if rel.User != nil {
		t.Errorf("a blocked relationship returned a profile: %+v", rel.User)
	}
}

func TestListAndRemoveBlocks(t *testing.T) {
	h := newHarness(t)
	alice := h.newAccount()
	bob := h.newAccount()
	h.setNickname(bob, "Bob")

	resp := h.do(request{
		method: "POST", path: "/1.0.0/users/" + alice.UserID + "/blocks",
		token: alice.AccessToken, json: `{"userId":"` + bob.UserID + `"}`,
	})
	requireStatus(t, resp, 204)

	var blocks []BlockResponse
	resp = h.do(request{
		method: "GET", path: "/1.0.0/users/" + alice.UserID + "/blocks",
		token: alice.AccessToken,
	})
	requireStatus(t, resp, 200)
	decode(t, resp, &blocks)
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(blocks))
	}
	// Alice needs to see who she blocked, or the unblock screen is a list of ids.
	if blocks[0].User == nil || blocks[0].User.Nickname != "Bob" {
		t.Errorf("block entry = %+v, want Bob's profile", blocks[0].User)
	}

	resp = h.do(request{
		method: "DELETE",
		path:   "/1.0.0/users/" + alice.UserID + "/blocks/" + bob.UserID,
		token:  alice.AccessToken,
	})
	requireStatus(t, resp, 204)

	resp = h.do(request{
		method: "GET", path: "/1.0.0/users/" + alice.UserID + "/blocks",
		token: alice.AccessToken,
	})
	decode(t, resp, &blocks)
	if len(blocks) != 0 {
		t.Errorf("still %d blocks after unblocking", len(blocks))
	}

	// And they can be friends again.
	h.befriend(alice, bob)
	if len(h.friends(alice)) != 1 {
		t.Error("they could not become friends after the block was removed")
	}
}

func TestBlockingYourselfIsRefused(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()

	resp := h.do(request{
		method: "POST", path: "/1.0.0/users/" + a.UserID + "/blocks",
		token: a.AccessToken, json: `{"userId":"` + a.UserID + `"}`,
	})
	requireError(t, resp, 400, "invalid_params")
}

func TestBlockingAcceptsFormEncoding(t *testing.T) {
	h := newHarness(t)
	alice := h.newAccount()
	bob := h.newAccount()

	// Which encoding the friends sysmodule sends is undocumented, so both work.
	resp := h.do(request{
		method: "POST", path: "/1.0.0/users/" + alice.UserID + "/blocks",
		token: alice.AccessToken,
		form:  url.Values{"nsaId": {bob.UserID}},
	})
	requireStatus(t, resp, 204)
}

func TestUnblockingSomebodyWhoIsNotBlockedIsNotFound(t *testing.T) {
	h := newHarness(t)
	alice := h.newAccount()
	bob := h.newAccount()

	resp := h.do(request{
		method: "DELETE",
		path:   "/1.0.0/users/" + alice.UserID + "/blocks/" + bob.UserID,
		token:  alice.AccessToken,
	})
	requireError(t, resp, 404, "resource_is_not_found")
}
