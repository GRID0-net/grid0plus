package baas

import (
	"encoding/json"
	"io"
	"net/url"
	"strings"
	"testing"

	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/presencebus"
)

// TestTwoUsersBecomeFriendsAndSeePresence is the phase 1.2 definition of done.
//
// User A sends a friend request, B accepts, A appears in B's friend list, A's
// presence goes to PLAYING with a title id, and B's friend-list response
// reflects it. This is the whole feature end to end, in the order a real console
// performs it.
func TestTwoUsersBecomeFriendsAndSeePresence(t *testing.T) {
	h := newHarness(t)

	alice := h.newAccount()
	bob := h.newAccount()
	h.setNickname(alice, "Alice")
	h.setNickname(bob, "Bob")

	// ---- B generates a friend code and tells A ----
	bobCode := h.friendCode(bob)
	if len(bobCode) != 14 { // dddd-dddd-dddd
		t.Fatalf("friend code %q is not in the dddd-dddd-dddd form", bobCode)
	}

	// ---- A sends the request, by code ----
	resp := h.do(request{
		method: "POST", path: "/2.0.0/friend_requests",
		token: alice.AccessToken,
		json:  `{"friendCode":"` + bobCode + `"}`,
	})
	requireStatus(t, resp, 201)

	// Read the body once and decode it twice: once into the struct, once into a
	// raw map so the wire key names can be asserted directly (see below).
	rawSent, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var sent FriendRequestResponse
	if err := json.Unmarshal(rawSent, &sent); err != nil {
		t.Fatalf("response is not JSON: %v (body %s)", err, rawSent)
	}
	var sentWire map[string]any
	if err := json.Unmarshal(rawSent, &sentWire); err != nil {
		t.Fatalf("response is not a JSON object: %v (body %s)", err, rawSent)
	}
	if sent.State != db.RequestPending {
		t.Errorf("new request state = %q, want PENDING", sent.State)
	}
	// The response tells A who the request went to, so the console can show
	// "request sent to Bob" without a second lookup.
	if sent.Receiver == nil || sent.Receiver.ID != bob.UserID {
		t.Errorf("the request does not name Bob as the recipient: %+v", sent.Receiver)
	}
	if sent.Receiver != nil && sent.Receiver.Nickname != "Bob" {
		t.Errorf("recipient nickname = %q, want Bob", sent.Receiver.Nickname)
	}
	if sent.SenderID != alice.UserID || sent.ReceiverID != bob.UserID {
		t.Errorf("senderId/receiverId = %q/%q, want Alice/Bob", sent.SenderID, sent.ReceiverID)
	}

	// The client reads senderId/receiverId and sender.*/receiver.*; it reads no
	// from/to at all. Decoding into the struct above would not catch a wrong
	// json tag, so check the key names on the wire directly.
	for _, key := range []string{"id", "senderId", "receiverId", "state", "createdAt"} {
		if _, ok := sentWire[key]; !ok {
			t.Errorf("the friend-request response has no %q key: %v", key, sentWire)
		}
	}
	// friend_requests/%016llx: a UUID here is 2121-0104 on the inbox screen.
	if id, _ := sentWire["id"].(string); len(id) != 16 || strings.Trim(id, "0123456789abcdef") != "" {
		t.Errorf("request id %q is not the 16 hex digits the console parses", id)
	}
	// The parser aborts an item with no channels[0] string: 2121-0104.
	if ch, _ := sentWire["channels"].([]any); len(ch) == 0 {
		t.Errorf("the friend-request response has no channels[0]: %v", sentWire)
	} else if _, ok := ch[0].(string); !ok {
		t.Errorf("channels[0] = %v, want a channel name string", ch[0])
	}
	if _, ok := sentWire["receiver"]; !ok {
		t.Errorf("the friend-request response has no \"receiver\" key: %v", sentWire)
	}
	for _, dead := range []string{"from", "to"} {
		if _, ok := sentWire[dead]; ok {
			t.Errorf("the friend-request response still carries %q, which no client reads: %v", dead, sentWire)
		}
	}

	// ---- it is in A's outbox and B's inbox ----
	var outbox CollectionEnvelope[FriendRequestResponse]
	resp = h.do(request{
		method: "GET", path: "/2.0.0/users/" + alice.UserID + "/friend_requests/outbox",
		token: alice.AccessToken,
	})
	requireStatus(t, resp, 200)
	decode(t, resp, &outbox)
	if len(outbox.Items) != 1 || outbox.Items[0].ID != sent.ID {
		t.Fatalf("A's outbox = %+v, want the one request", outbox.Items)
	}

	var inbox CollectionEnvelope[FriendRequestResponse]
	resp = h.do(request{
		method: "GET", path: "/2.0.0/users/" + bob.UserID + "/friend_requests/inbox",
		token: bob.AccessToken,
	})
	requireStatus(t, resp, 200)
	decode(t, resp, &inbox)
	if len(inbox.Items) != 1 || inbox.Items[0].ID != sent.ID {
		t.Fatalf("B's inbox = %+v, want the one request", inbox.Items)
	}
	// B needs to know who is asking, or the accept screen is blank.
	if inbox.Items[0].Sender == nil || inbox.Items[0].Sender.ID != alice.UserID {
		t.Errorf("B's inbox entry does not name Alice: %+v", inbox.Items[0].Sender)
	}
	// The ids always identify both sides.
	if inbox.Items[0].SenderID != alice.UserID || inbox.Items[0].ReceiverID != bob.UserID {
		t.Errorf("B's inbox entry ids = %q/%q, want Alice/Bob",
			inbox.Items[0].SenderID, inbox.Items[0].ReceiverID)
	}
	// The recipient's own side is filled too, not omitted: the decompiled
	// parser reads receiver.nickname unconditionally (see
	// FriendRequestResponse's own doc comment), so leaving it out was the
	// leading suspect for 2121-0104/2124-0104 on the friend-requests screen.
	if inbox.Items[0].Receiver == nil || inbox.Items[0].Receiver.ID != bob.UserID {
		t.Errorf("B's inbox entry does not carry B's own receiver profile: %+v", inbox.Items[0].Receiver)
	}
	// channels/extras must be present, not omitted -- both are read
	// unconditionally by the same decompiled parser, and an entirely
	// missing key (as opposed to an empty array/object) is what a JSON
	// consumer that skips a length check before indexing channels[0] would
	// crash on.
	if inbox.Items[0].Channels == nil {
		t.Errorf("B's inbox entry has no channels array at all, want an empty one")
	}
	if inbox.Items[0].Extras == nil || inbox.Items[0].Extras["receiver"] == nil {
		t.Errorf("B's inbox entry has no extras.receiver, want a present (even if empty) one: %+v", inbox.Items[0].Extras)
	}

	// The relationship reads correctly from both sides while pending.
	if got := h.relationship(t, alice, bob.UserID).Type; got != RelationshipSent {
		t.Errorf("A's relationship to B = %q, want FRIEND_REQUEST_SENT", got)
	}
	if got := h.relationship(t, bob, alice.UserID).Type; got != RelationshipReceived {
		t.Errorf("B's relationship to A = %q, want FRIEND_REQUEST_RECEIVED", got)
	}

	// ---- B accepts ----
	resp = h.do(request{
		method: "PATCH", path: "/2.0.0/friend_requests/" + sent.ID,
		token: bob.AccessToken,
		patch: `[{"op":"replace","path":"/state","value":"ACCEPTED"}]`,
	})
	requireStatus(t, resp, 200)

	// ---- each appears in the other's friend list ----
	aliceFriends := h.friends(alice)
	if len(aliceFriends) != 1 {
		t.Fatalf("A has %d friends, want 1", len(aliceFriends))
	}
	if aliceFriends[0].FriendID != bob.UserID {
		t.Errorf("A's friend = %q, want B", aliceFriends[0].FriendID)
	}
	if aliceFriends[0].Friend.Nickname != "Bob" {
		t.Errorf("A's friend nickname = %q, want Bob", aliceFriends[0].Friend.Nickname)
	}
	// The capture shows links is absent from the friend sub-object; friend
	// code is not included in the friend list entry.
	if aliceFriends[0].CreatedAt == 0 {
		t.Error("createdAt (becameFriendAt) is zero")
	}

	bobFriends := h.friends(bob)
	if len(bobFriends) != 1 || bobFriends[0].FriendID != alice.UserID {
		t.Fatalf("B's friends = %+v, want just A", bobFriends)
	}
	if bobFriends[0].Friend.Nickname != "Alice" {
		t.Errorf("B's friend nickname = %q, want Alice", bobFriends[0].Friend.Nickname)
	}

	// Both sides now report FRIEND.
	for _, c := range []struct {
		self  account
		other string
	}{{alice, bob.UserID}, {bob, alice.UserID}} {
		if got := h.relationship(t, c.self, c.other).Type; got != RelationshipFriend {
			t.Errorf("relationship = %q, want FRIEND", got)
		}
	}

	// The pending request is gone from both boxes.
	resp = h.do(request{
		method: "GET", path: "/2.0.0/users/" + bob.UserID + "/friend_requests/inbox",
		token: bob.AccessToken,
	})
	decode(t, resp, &inbox)
	if len(inbox.Items) != 0 {
		t.Errorf("B's inbox still has %d pending requests after accepting", len(inbox.Items))
	}

	// ---- A launches Splatoon 3 ----
	// This is the request that makes "Playing Splatoon 3" appear in the stock
	// friends applet. If appInfo:appId is dropped, the friend shows as merely
	// online and nothing in the response says why.
	const appField = `{"mode":"regular"}`
	resp = h.reportPresence(alice, db.StatePlaying, splatoon3TitleID, appField)
	requireStatus(t, resp, 200)

	// ---- B's friend list reflects it ----
	bobFriends = h.friends(bob)
	if len(bobFriends) != 1 {
		t.Fatalf("B has %d friends", len(bobFriends))
	}
	got := bobFriends[0]

	if got.Friend.Presence.State != db.StatePlaying {
		t.Errorf("A's presence as seen by B = %q, want PLAYING", got.Friend.Presence.State)
	}
	// The 'friends' scope is the one a friend may read, and it is where the
	// console writes the app fields.
	friendScope := got.Friend.Presence.Extras[db.ScopeFriends]
	if friendScope == nil {
		t.Fatal("B cannot see the 'friends' scope of A's presence extras")
	}
	if friendScope[keyAppID] != splatoon3TitleID {
		t.Errorf("appInfo:appId = %v, want the Splatoon 3 title id. This is the "+
			"field that makes \"Playing Splatoon 3\" show up in the friends applet.",
			friendScope[keyAppID])
	}
	if friendScope[keyPresenceGroupID] != splatoon3TitleID {
		t.Errorf("appInfo:presenceGroupId = %v", friendScope[keyPresenceGroupID])
	}
	// appField is the game's own format and must come back byte for byte.
	if friendScope[keyAppField] != appField {
		t.Errorf("appField = %v, want %q returned verbatim", friendScope[keyAppField], appField)
	}

	// ---- and the change was published to the bus ----
	// The bus is what the phase 2 dashboard and NPLN presence service consume,
	// so a presence change that is stored but not published is only half done.
	var found *presencebus.Event
	for _, e := range h.bus.all() {
		if e.UserID == alice.UserID && e.State == db.StatePlaying {
			ev := e
			found = &ev
		}
	}
	if found == nil {
		t.Fatalf("no PLAYING event was published for A; events = %+v", h.bus.all())
	}
	if found.AppID != splatoon3TitleID {
		t.Errorf("published event appId = %q, want the Splatoon 3 title id", found.AppID)
	}
	if found.AppField != appField {
		t.Errorf("published event appField = %q, want it passed through", found.AppField)
	}
	if found.Source != "baas" {
		t.Errorf("published event source = %q, want baas", found.Source)
	}
}

// ---- friend requests ----------------------------------------------------

func TestFriendRequestBySelfIsRefused(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	code := h.friendCode(a)

	// When identity resolution upstream is broken, "add a friend" becomes a
	// self-request surprisingly often, and a friend graph full of self-edges is
	// very hard to read back. Refusing it also keeps a self-friendship out of
	// the friend list, which would otherwise look like a much stranger bug.
	for name, body := range map[string]string{
		"by friend code": `{"friendCode":"` + code + `"}`,
		"by user id":     `{"userId":"` + a.UserID + `"}`,
	} {
		resp := h.do(request{
			method: "POST", path: "/2.0.0/friend_requests",
			token: a.AccessToken, json: body,
		})
		if resp.StatusCode == 201 {
			t.Errorf("%s: a self friend request was accepted", name)
		}
	}
}

func TestFriendRequestAcceptsAFriendCodeInAnyFormPlayersType(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	b := h.newAccount()

	code := h.friendCode(b)
	digits := ""
	for _, r := range code {
		if r != '-' {
			digits += string(r)
		}
	}

	// Players copy codes with the SW- prefix, without dashes, or with stray
	// whitespace from a chat message. Accepting all of those is the difference
	// between "add friend" working and being told a valid code is invalid.
	for name, input := range map[string]string{
		"canonical":  code,
		"no dashes":  digits,
		"SW prefix":  "SW-" + code,
		"whitespace": "  " + code + "  ",
	} {
		resp := h.do(request{
			method: "POST", path: "/2.0.0/friend_requests",
			token: a.AccessToken, json: `{"friendCode":` + jsonString(input) + `}`,
		})
		if resp.StatusCode != 201 {
			t.Errorf("%s (%q): status = %d, want 201", name, input, resp.StatusCode)
		}
	}
}

func TestFriendRequestByFormEncodedBody(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	b := h.newAccount()
	code := h.friendCode(b)

	// Which encoding the friends sysmodule uses is not documented, so both are
	// accepted. Refusing the wrong guess would be a dead end during bring-up.
	resp := h.do(request{
		method: "POST", path: "/2.0.0/friend_requests",
		token: a.AccessToken,
		form:  url.Values{"friendCode": {code}},
	})
	requireStatus(t, resp, 201)
}

// TestFriendRequestByReceiverID is the regression test for a real,
// reproducible bug on real hardware: sending a friend request by entering a
// friend code returned 400/2121-2001 every time. The console does not repeat
// the friend code on this call -- it already resolved the target through the
// preceding GET /1.0.0/users and GET .../relationships/<id> calls (see the
// GET side of that flow elsewhere in this file) -- and posts, byte for byte,
// measured from a real console on 2026-09-24:
//
//	{"channels":["FRIEND_CODE"],"senderId":"<caller>","receiverId":"<target>",
//	 "extras":{"sender":{"route:sender":"me"}}}
//
// This server only recognised friendCode/userId/nsaId, none of which this
// body carries, so target resolution silently failed with invalid_params
// every time -- the friend-request screen could show a resolved profile and
// still refuse to ever send a request to it.
func TestFriendRequestByReceiverID(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	b := h.newAccount()

	resp := h.do(request{
		method: "POST", path: "/2.0.0/friend_requests",
		token: a.AccessToken,
		json: `{"channels":["FRIEND_CODE"],"senderId":"` + a.UserID + `","receiverId":"` +
			b.UserID + `","extras":{"sender":{"route:sender":"me"}}}`,
	})
	requireStatus(t, resp, 201)

	var got FriendRequestResponse
	decode(t, resp, &got)
	if got.SenderID != a.UserID || got.ReceiverID != b.UserID {
		t.Errorf("request ids = %q/%q, want %q/%q", got.SenderID, got.ReceiverID, a.UserID, b.UserID)
	}
}

func TestUnknownFriendCodeIsNotFoundAndAMalformedOneIsRejected(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()

	// A well-formed code nobody owns is "not found".
	resp := h.do(request{
		method: "POST", path: "/2.0.0/friend_requests",
		token: a.AccessToken, json: `{"friendCode":"0000-0000-0001"}`,
	})
	requireError(t, resp, 404, "resource_is_not_found")

	// Something that is not a code at all gets the dedicated format error, so a
	// player who mistyped is told which of the two problems they have.
	resp = h.do(request{
		method: "POST", path: "/2.0.0/friend_requests",
		token: a.AccessToken, json: `{"friendCode":"not-a-code"}`,
	})
	requireError(t, resp, 400, "invalid_friend_code_format")
}

func TestARetriedFriendRequestDoesNotDuplicate(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	b := h.newAccount()
	code := h.friendCode(b)

	// A console that thinks its request was lost retries. Two rows would show B
	// the same request twice.
	var first, second FriendRequestResponse
	for _, dst := range []*FriendRequestResponse{&first, &second} {
		resp := h.do(request{
			method: "POST", path: "/2.0.0/friend_requests",
			token: a.AccessToken, json: `{"friendCode":"` + code + `"}`,
		})
		requireStatus(t, resp, 201)
		decode(t, resp, dst)
	}
	if first.ID != second.ID {
		t.Errorf("a retried request created a second row: %q then %q", first.ID, second.ID)
	}

	var inbox CollectionEnvelope[FriendRequestResponse]
	resp := h.do(request{
		method: "GET", path: "/2.0.0/users/" + b.UserID + "/friend_requests/inbox",
		token: b.AccessToken,
	})
	decode(t, resp, &inbox)
	if len(inbox.Items) != 1 {
		t.Errorf("B's inbox has %d entries, want 1", len(inbox.Items))
	}
}

// TestCrossingFriendRequestsBecomeAFriendship covers two people adding each
// other at the same moment. They should end up friends, not with an unanswered
// request each.
func TestCrossingFriendRequestsBecomeAFriendship(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	b := h.newAccount()

	aCode := h.friendCode(a)
	bCode := h.friendCode(b)

	resp := h.do(request{
		method: "POST", path: "/2.0.0/friend_requests",
		token: a.AccessToken, json: `{"friendCode":"` + bCode + `"}`,
	})
	requireStatus(t, resp, 201)

	resp = h.do(request{
		method: "POST", path: "/2.0.0/friend_requests",
		token: b.AccessToken, json: `{"friendCode":"` + aCode + `"}`,
	})
	requireStatus(t, resp, 201)

	var crossed FriendRequestResponse
	decode(t, resp, &crossed)
	if crossed.State != requestStateToWire(db.RequestAccepted) {
		t.Errorf("the crossing request state = %q, want %q (the console's own spelling)",
			crossed.State, requestStateToWire(db.RequestAccepted))
	}

	if len(h.friends(a)) != 1 || len(h.friends(b)) != 1 {
		t.Errorf("A has %d friends and B has %d, want 1 each",
			len(h.friends(a)), len(h.friends(b)))
	}
}

func TestOnlyTheRecipientMayAcceptAndOnlyTheSenderMayCancel(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	b := h.newAccount()
	stranger := h.newAccount()
	code := h.friendCode(b)

	send := func() string {
		t.Helper()
		resp := h.do(request{
			method: "POST", path: "/2.0.0/friend_requests",
			token: a.AccessToken, json: `{"friendCode":"` + code + `"}`,
		})
		requireStatus(t, resp, 201)
		var sent FriendRequestResponse
		decode(t, resp, &sent)
		return sent.ID
	}

	// The sender must not be able to accept their own request.
	resp := h.do(request{
		method: "PATCH", path: "/2.0.0/friend_requests/" + send(),
		token: a.AccessToken,
		patch: `[{"op":"replace","path":"/state","value":"ACCEPTED"}]`,
	})
	requireError(t, resp, 403, "insufficient_scope")

	// A third party must not be able to touch it at all. The console's
	// 16-hex id is looked up among the caller's own requests, so to a
	// stranger it does not exist.
	resp = h.do(request{
		method: "PATCH", path: "/2.0.0/friend_requests/" + send(),
		token: stranger.AccessToken,
		patch: `[{"op":"replace","path":"/state","value":"ACCEPTED"}]`,
	})
	requireError(t, resp, 404, "resource_is_not_found")

	// The recipient must not be able to "cancel" — that is the sender's action.
	resp = h.do(request{
		method: "PATCH", path: "/2.0.0/friend_requests/" + send(),
		token: b.AccessToken,
		patch: `[{"op":"replace","path":"/state","value":"CANCELLED"}]`,
	})
	requireError(t, resp, 403, "insufficient_scope")

	if len(h.friends(a)) != 0 {
		t.Error("a friendship was created without the recipient accepting")
	}
}

func TestFriendRequestCanBeDeniedAndCancelled(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	b := h.newAccount()
	code := h.friendCode(b)

	send := func() string {
		t.Helper()
		resp := h.do(request{
			method: "POST", path: "/2.0.0/friend_requests",
			token: a.AccessToken, json: `{"friendCode":"` + code + `"}`,
		})
		requireStatus(t, resp, 201)
		var sent FriendRequestResponse
		decode(t, resp, &sent)
		return sent.ID
	}

	// Denied by the recipient.
	id := send()
	resp := h.do(request{
		method: "PATCH", path: "/2.0.0/friend_requests/" + id,
		token: b.AccessToken,
		patch: `[{"op":"replace","path":"/state","value":"DENIED"}]`,
	})
	requireStatus(t, resp, 200)
	if len(h.friends(a)) != 0 {
		t.Error("a denied request created a friendship")
	}
	// A resolved request cannot be resolved again.
	resp = h.do(request{
		method: "PATCH", path: "/2.0.0/friend_requests/" + id,
		token: b.AccessToken,
		patch: `[{"op":"replace","path":"/state","value":"ACCEPTED"}]`,
	})
	if resp.StatusCode == 200 {
		t.Error("an already-denied request was accepted afterwards")
	}

	// Cancelled by the sender.
	id = send()
	resp = h.do(request{
		method: "PATCH", path: "/2.0.0/friend_requests/" + id,
		token: a.AccessToken,
		patch: `[{"op":"replace","path":"/state","value":"CANCELLED"}]`,
	})
	requireStatus(t, resp, 200)
	if len(h.friends(a)) != 0 {
		t.Error("a cancelled request created a friendship")
	}
}

func TestFriendRequestAcceptsThePlainStateObjectBodyToo(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	b := h.newAccount()
	code := h.friendCode(b)

	resp := h.do(request{
		method: "POST", path: "/2.0.0/friend_requests",
		token: a.AccessToken, json: `{"friendCode":"` + code + `"}`,
	})
	requireStatus(t, resp, 201)
	var sent FriendRequestResponse
	decode(t, resp, &sent)

	// The PATCH body shape is undocumented, so both a JSON patch array and a
	// plain object are accepted. Guessing wrong would stop friend requests
	// working with nothing in the log to explain it.
	resp = h.do(request{
		method: "PATCH", path: "/2.0.0/friend_requests/" + sent.ID,
		token: b.AccessToken, json: `{"state":"ACCEPTED"}`,
	})
	requireStatus(t, resp, 200)

	if len(h.friends(a)) != 1 {
		t.Error("the plain-object accept did not create the friendship")
	}
}

// TestFriendRequestAcceptsTheRealConsoleStateSpelling is the actual bug this
// project shipped before a 2026-09-20 decompile of the operator's own
// console's friends sysmodule found the real enum: the console spells these
// AUTHORIZED/REJECTED/CANCELED, not the pre-capture guess of ACCEPTED/
// DENIED/CANCELLED this test file's other cases still use (kept working as
// an alias, but never what a real console would actually send). Before
// requestStateFromWire existed, a real console accepting a friend request
// would have gotten errInvalidParams back -- not a cosmetic mismatch, a
// completely broken core feature.
func TestFriendRequestAcceptsTheRealConsoleStateSpelling(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	b := h.newAccount()
	code := h.friendCode(b)

	resp := h.do(request{
		method: "POST", path: "/2.0.0/friend_requests",
		token: a.AccessToken, json: `{"friendCode":"` + code + `"}`,
	})
	requireStatus(t, resp, 201)
	var sent FriendRequestResponse
	decode(t, resp, &sent)

	resp = h.do(request{
		method: "PATCH", path: "/2.0.0/friend_requests/" + sent.ID,
		token: b.AccessToken,
		patch: `[{"op":"replace","path":"/state","value":"AUTHORIZED"}]`,
	})
	requireStatus(t, resp, 200)

	if len(h.friends(a)) != 1 {
		t.Error("PATCH with the console's real \"AUTHORIZED\" spelling did not create the friendship")
	}

	// The response the console reads back must use its own spelling too --
	// requestStateToWire is the write path's mirror, and both directions
	// have to agree or a console that successfully accepts still cannot
	// make sense of its own confirmation.
	var resolved FriendRequestResponse
	decode(t, resp, &resolved)
	if resolved.State != "AUTHORIZED" {
		t.Errorf("PATCH response state = %q, want the console's own \"AUTHORIZED\" spelling", resolved.State)
	}
}

func TestFriendRequestToAUserWhoRefusesThemIsRejected(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	b := h.newAccount()
	code := h.friendCode(b)

	resp := h.do(request{
		method: "PATCH", path: "/1.0.0/users/" + b.UserID,
		token: b.AccessToken,
		patch: `[{"op":"replace","path":"/permissions/friendRequestReception","value":false}]`,
	})
	requireStatus(t, resp, 200)

	resp = h.do(request{
		method: "POST", path: "/2.0.0/friend_requests",
		token: a.AccessToken, json: `{"friendCode":"` + code + `"}`,
	})
	requireError(t, resp, 403, "forbidden")
}

func TestFriendRequestToAnExistingFriendIsRejected(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	b := h.newAccount()
	h.befriend(a, b)

	resp := h.do(request{
		method: "POST", path: "/2.0.0/friend_requests",
		token: a.AccessToken, json: `{"userId":"` + b.UserID + `"}`,
	})
	requireError(t, resp, 409, "resource_already_exists")
}

// ---- favourites and unfriending -----------------------------------------

func TestFavouriteIsPerDirection(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	b := h.newAccount()
	h.befriend(a, b)

	resp := h.do(request{
		method: "PATCH", path: "/2.0.0/users/" + a.UserID + "/friends/" + b.UserID,
		token: a.AccessToken,
		patch: `[{"op":"replace","path":"/isFavorite","value":true}]`,
	})
	requireStatus(t, resp, 204)

	if !h.friends(a)[0].IsFavorite {
		t.Error("A does not see B as a favourite")
	}
	// A marking B as a favourite says nothing about what B sees. Storing one
	// shared flag would make favouriting somebody silently favourite you back.
	if h.friends(b)[0].IsFavorite {
		t.Error("marking a favourite leaked into the other direction")
	}
}

func TestUnfriendingRemovesBothDirections(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	b := h.newAccount()
	h.befriend(a, b)

	resp := h.do(request{
		method: "DELETE", path: "/2.0.0/users/" + a.UserID + "/friends/" + b.UserID,
		token: a.AccessToken,
	})
	requireStatus(t, resp, 204)

	if len(h.friends(a)) != 0 {
		t.Error("A still has B as a friend")
	}
	// A one-sided removal leaves B looking at a friend who cannot see them.
	if len(h.friends(b)) != 0 {
		t.Error("B still has A as a friend")
	}
	if got := h.relationship(t, b, a.UserID).Type; got != RelationshipNone {
		t.Errorf("B's relationship to A = %q, want NONE", got)
	}
}

func TestUnfriendingSomebodyWhoIsNotAFriendIsNotFound(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	b := h.newAccount()

	resp := h.do(request{
		method: "DELETE", path: "/2.0.0/users/" + a.UserID + "/friends/" + b.UserID,
		token: a.AccessToken,
	})
	requireError(t, resp, 404, "resource_is_not_found")
}

// ---- friend list shape --------------------------------------------------

func TestFriendListRespectsTheCountParameter(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	for range 3 {
		b := h.newAccount()
		h.befriend(a, b)
	}

	resp := h.do(request{
		method: "GET", path: "/2.0.0/users/" + a.UserID + "/friends?count=2",
		token: a.AccessToken,
	})
	requireStatus(t, resp, 200)

	var env CollectionEnvelope[FriendResponse]
	decode(t, resp, &env)
	if env.ItemsPerPage != 2 {
		t.Errorf("count=2 returned %d friends", env.ItemsPerPage)
	}

	resp = h.do(request{
		method: "GET", path: "/2.0.0/users/" + a.UserID + "/friends?count=0",
		token: a.AccessToken,
	})
	requireError(t, resp, 400, "invalid_params")
}

func TestFriendListDoesNotLeakPrivateFields(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	b := h.newAccount()
	h.befriend(a, b)

	// Put something in B's private 'self' extras.
	resp := h.do(request{
		method: "PATCH", path: "/1.0.0/users/" + b.UserID,
		token: b.AccessToken,
		patch: `[{"op":"add","path":"/extras/self/secret","value":"private"}]`,
	})
	requireStatus(t, resp, 200)

	friend := h.friends(a)[0]

	// The five extras scopes exist precisely so a user can put something in
	// 'self' that no friend sees. Returning all five to everybody makes the
	// whole mechanism decorative.
	if v, present := friend.Friend.Extras[db.ScopeSelf]["secret"]; present {
		t.Errorf("A can read B's 'self' extras: %v", v)
	}
	// Handing a friend the ids of the consoles somebody logs in from serves no
	// purpose and is the sort of leak that turns into an identity problem later.
	if len(friend.Friend.DeviceAccounts) != 0 {
		t.Errorf("A can see B's device accounts: %+v", friend.Friend.DeviceAccounts)
	}
}

// TestRelationshipCarriesTheThreeFlagsTheClientReads asserts on the wire, not
// on the Go struct.
//
// The friends sysmodule's response object for GET .../relationships/<id> is
// built at FUN_001d5d60 with the vtable at 0x258558, which registers exactly two
// field callbacks: one reading $.isFriend and $.isBlocking, one reading
// $.sentFriendRequestIds[0]. GRID0+ used to send none of those three keys,
// which the client reads as the all-false default -- a friend and a total
// stranger were indistinguishable on screen. Decoding into RelationshipResponse
// would not catch a wrong json tag here, so this decodes into a map.
func TestRelationshipCarriesTheThreeFlagsTheClientReads(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	b := h.newAccount()
	c := h.newAccount()

	get := func(self account, other string) map[string]any {
		t.Helper()
		resp := h.do(request{
			method: "GET",
			path:   "/2.0.0/users/" + self.UserID + "/relationships/" + other,
			token:  self.AccessToken,
		})
		requireStatus(t, resp, 200)
		var out map[string]any
		decode(t, resp, &out)
		for _, key := range []string{"isFriend", "isBlocking", "sentFriendRequestIds"} {
			if _, ok := out[key]; !ok {
				t.Fatalf("response has no %q key: %v", key, out)
			}
		}
		return out
	}

	// A stranger: all three false/empty.
	stranger := get(a, b.UserID)
	if stranger["isFriend"] != false || stranger["isBlocking"] != false {
		t.Errorf("stranger relationship = %v, want isFriend/isBlocking false", stranger)
	}
	if ids, ok := stranger["sentFriendRequestIds"].([]any); !ok || len(ids) != 0 {
		t.Errorf("stranger sentFriendRequestIds = %v, want []", stranger["sentFriendRequestIds"])
	}

	// A friendship: isFriend true.
	h.befriend(a, b)
	if got := get(a, b.UserID); got["isFriend"] != true {
		t.Errorf("friend relationship = %v, want isFriend true", got)
	}

	// An outgoing request: non-empty sentFriendRequestIds, which is how the
	// client tells "I have already asked" from "I could ask".
	code := h.friendCode(c)
	resp := h.do(request{
		method: "POST", path: "/2.0.0/friend_requests",
		token: a.AccessToken, json: `{"friendCode":"` + code + `"}`,
	})
	requireStatus(t, resp, 201)
	sent := get(a, c.UserID)
	if ids, ok := sent["sentFriendRequestIds"].([]any); !ok || len(ids) != 1 {
		t.Errorf("sent request relationship = %v, want one sentFriendRequestIds entry", sent)
	}
	if sent["isFriend"] != false {
		t.Errorf("a pending request reported isFriend = %v, want false", sent["isFriend"])
	}

	// A block is visible to the blocker as their own action...
	resp = h.do(request{
		method: "POST", path: "/1.0.0/users/" + a.UserID + "/blocks",
		token: a.AccessToken, json: `{"userId":"` + c.UserID + `"}`,
	})
	requireStatus(t, resp, 204)
	if got := get(a, c.UserID); got["isBlocking"] != true {
		t.Errorf("blocker's own relationship = %v, want isBlocking true", got)
	}

	// ...and to the blocked user it looks exactly like a stranger, which is the
	// same all-false, no-profile state an ordinary stranger produces. Telling a
	// blocked user they were blocked is what a block is meant to prevent.
	blocked := get(c, a.UserID)
	if blocked["isBlocking"] != false || blocked["isFriend"] != false {
		t.Errorf("blocked user's view = %v, want both flags false", blocked)
	}
	if ids, ok := blocked["sentFriendRequestIds"].([]any); !ok || len(ids) != 0 {
		t.Errorf("blocked user's sentFriendRequestIds = %v, want []", blocked["sentFriendRequestIds"])
	}
}

// ---- helpers ------------------------------------------------------------

func (h *harness) relationship(t *testing.T, self account, other string) RelationshipResponse {
	t.Helper()
	resp := h.do(request{
		method: "GET",
		path:   "/2.0.0/users/" + self.UserID + "/relationships/" + other,
		token:  self.AccessToken,
	})
	requireStatus(t, resp, 200)

	var out RelationshipResponse
	decode(t, resp, &out)
	return out
}

// TestPatchingAFriendsExtrasSticksAndIsOneDirectional pins what the console
// actually sends after showing a new friend: PATCH
// .../friends/<id> with /extras/self/isConfirmed. That was answered 400 until
// 2026-09-23. It has to be accepted, handed back on the patcher's friend
// list, and stay out of the other direction. Asserted on the wire.
func TestPatchingAFriendsExtrasSticksAndIsOneDirectional(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	b := h.newAccount()
	h.befriend(a, b)

	resp := h.do(request{
		method: "PATCH", path: "/2.0.0/users/" + a.UserID + "/friends/" + b.UserID,
		token: a.AccessToken,
		patch: `[{"op":"add","path":"/extras/self/isConfirmed","value":true}]`,
	})
	requireStatus(t, resp, 204)

	extrasOf := func(self account, friend string) map[string]any {
		t.Helper()
		resp := h.do(request{method: "GET", path: "/2.0.0/users/" + self.UserID + "/friends", token: self.AccessToken})
		requireStatus(t, resp, 200)
		var body struct {
			Items []struct {
				FriendID string                    `json:"friendId"`
				Extras   map[string]map[string]any `json:"extras"`
			} `json:"items"`
		}
		decode(t, resp, &body)
		for _, it := range body.Items {
			if it.FriendID == friend {
				return it.Extras["self"]
			}
		}
		t.Fatalf("%s is not on %s's friend list", friend, self.UserID)
		return nil
	}

	if got := extrasOf(a, b.UserID)["isConfirmed"]; got != true {
		t.Errorf("A's view of B: extras.self.isConfirmed = %v, want true", got)
	}
	if got := extrasOf(a, b.UserID)["playLog"]; got != "" {
		t.Errorf("the default playLog key was lost when stored extras were layered on: %v", got)
	}
	if _, leaked := extrasOf(b, a.UserID)["isConfirmed"]; leaked {
		t.Error("isConfirmed leaked into B's view of A")
	}

	// Removing it works, and a stranger cannot be patched.
	resp = h.do(request{
		method: "PATCH", path: "/2.0.0/users/" + a.UserID + "/friends/" + b.UserID,
		token: a.AccessToken,
		patch: `[{"op":"remove","path":"/extras/self/isConfirmed"}]`,
	})
	requireStatus(t, resp, 204)
	if _, still := extrasOf(a, b.UserID)["isConfirmed"]; still {
		t.Error("isConfirmed survived a remove")
	}
	c := h.newAccount()
	resp = h.do(request{
		method: "PATCH", path: "/2.0.0/users/" + a.UserID + "/friends/" + c.UserID,
		token: a.AccessToken,
		patch: `[{"op":"add","path":"/extras/self/isConfirmed","value":true}]`,
	})
	if resp.StatusCode == 204 {
		t.Error("patching a non-friend's extras succeeded")
	}
}

func TestOpeningARequestersProfileMayMarkTheRequestRead(t *testing.T) {
	h := newHarness(t)
	a := h.newAccount()
	b := h.newAccount()
	resp := h.do(request{
		method: "POST", path: "/2.0.0/friend_requests",
		token: a.AccessToken, json: `{"friendCode":"` + h.friendCode(b) + `"}`,
	})
	requireStatus(t, resp, 201)
	var sent FriendRequestResponse
	decode(t, resp, &sent)

	// 2121-2001 on the profile when this was refused as a bad state patch.
	read := `[{"op":"replace","path":"/extras/receiver/read","value":true}]`
	resp = h.do(request{method: "PATCH", path: "/2.0.0/friend_requests/" + sent.ID, token: b.AccessToken, patch: read})
	requireStatus(t, resp, 200)
	var got FriendRequestResponse
	decode(t, resp, &got)
	if got.State != "PENDING" {
		t.Errorf("marking a request read changed its state to %q", got.State)
	}
	// Only the recipient reads their inbox.
	resp = h.do(request{method: "PATCH", path: "/2.0.0/friend_requests/" + sent.ID, token: a.AccessToken, patch: read})
	requireError(t, resp, 404, "resource_is_not_found")
}
