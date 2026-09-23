package npln

import (
	"context"
	"testing"
	"time"

	"github.com/n-popescu/switchnet/internal/db"
	friendspb "github.com/n-popescu/switchnet/proto/friends/v1"
)

// TestSubscribePresencesOnlyServesSelfAndFriends is the privacy boundary this
// service has to enforce on its own: unlike ListFriendUsers, the caller here
// names arbitrary presence resources, and a stranger's presence must not
// leak just because the console asked for it.
func TestSubscribePresencesOnlyServesSelfAndFriends(t *testing.T) {
	h := newHarness(t)
	h.createUser("watcher0000000002", "device-watcher-2")
	h.createUser("friend0000000002", "device-friend-2")
	h.createUser("stranger000000001", "device-stranger")
	ctx := context.Background()
	if err := h.store.AddFriendship(ctx, "watcher0000000002", "friend0000000002", h.now); err != nil {
		t.Fatalf("add friendship: %v", err)
	}

	rpcCtx := h.ctx(h.sessionFor(t, "watcher0000000002", "device-watcher-2"), "")
	stream, err := friendspb.NewPresenceServiceClient(h.conn).SubscribePresences(rpcCtx,
		&friendspb.SubscribePresencesRequest{
			User: "users/watcher0000000002",
			Presences: []string{
				"tenants/" + testTenant + "/users/watcher0000000002/presence",
				"tenants/" + testTenant + "/users/friend0000000002/presence",
				"tenants/" + testTenant + "/users/stranger000000001/presence",
			},
		})
	if err != nil {
		t.Fatalf("SubscribePresences: %v", err)
	}

	first, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	got := map[string]bool{}
	for _, p := range first.GetPresences().GetPresences() {
		got[p.GetName()] = true
	}
	if len(got) != 2 {
		t.Fatalf("got %d presences, want exactly self and the one friend: %v", len(got), got)
	}
	if got["tenants/"+testTenant+"/users/stranger000000001/presence"] {
		t.Error("a stranger's presence was served despite no friendship")
	}
	if !got["tenants/"+testTenant+"/users/watcher0000000002/presence"] {
		t.Error("the caller's own presence was not served")
	}
	if !got["tenants/"+testTenant+"/users/friend0000000002/presence"] {
		t.Error("a friend's presence was not served")
	}

	// EnumerationDone follows the initial page, per presence.proto.
	second, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv enumeration_done: %v", err)
	}
	if second.GetEnumerationDone() == nil {
		t.Errorf("second message is not enumeration_done: %v", second)
	}
}

// TestSubscribePresencesRespectsAFriendsPresencePrivacySetting covers the gap
// fixed alongside this test: watchablePresenceIDs used to allow any friend
// through regardless of the friend's own presence.Permissions setting, unlike
// internal/npln/friends.go's FriendUser.Relationship.PresenceReceivable (and
// BAAS's own friends.go presenceVisibleTo), which both correctly hide a
// friend's presence when it is set to SELF. A console subscribing directly to
// the friend's presence resource -- rather than reading it off the friends
// list -- bypassed that entirely.
func TestSubscribePresencesRespectsAFriendsPresencePrivacySetting(t *testing.T) {
	h := newHarness(t)
	h.createUser("watcher0000000003", "device-watcher-3")
	h.createUser("friend0000000003", "device-friend-3")
	ctx := context.Background()
	if err := h.store.AddFriendship(ctx, "watcher0000000003", "friend0000000003", h.now); err != nil {
		t.Fatalf("add friendship: %v", err)
	}
	perms := db.DefaultPermissions(h.now)
	perms.Presence = "SELF"
	if _, err := h.store.UpdateUser(ctx, "friend0000000003", db.UserPatch{Permissions: &perms}); err != nil {
		t.Fatalf("set presence permission: %v", err)
	}

	rpcCtx := h.ctx(h.sessionFor(t, "watcher0000000003", "device-watcher-3"), "")
	stream, err := friendspb.NewPresenceServiceClient(h.conn).SubscribePresences(rpcCtx,
		&friendspb.SubscribePresencesRequest{
			User: "users/watcher0000000003",
			Presences: []string{
				"tenants/" + testTenant + "/users/watcher0000000003/presence",
				"tenants/" + testTenant + "/users/friend0000000003/presence",
			},
		})
	if err != nil {
		t.Fatalf("SubscribePresences: %v", err)
	}

	first, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	got := map[string]bool{}
	for _, p := range first.GetPresences().GetPresences() {
		got[p.GetName()] = true
	}
	if got["tenants/"+testTenant+"/users/friend0000000003/presence"] {
		t.Error("a friend's presence was served despite that friend's Presence permission being SELF")
	}
	if !got["tenants/"+testTenant+"/users/watcher0000000003/presence"] {
		t.Error("the caller's own presence was not served")
	}
}

// TestSubscribePresencesPushesOnAFreshReport proves the live-update path: a
// friend's presence change reaches an open stream without waiting for the
// heartbeat.
func TestSubscribePresencesPushesOnAFreshReport(t *testing.T) {
	h := newHarness(t)
	h.createUser("watcher0000000003", "device-watcher-3")
	h.createUser("friend0000000003", "device-friend-3")
	ctx := context.Background()
	if err := h.store.AddFriendship(ctx, "watcher0000000003", "friend0000000003", h.now); err != nil {
		t.Fatalf("add friendship: %v", err)
	}

	rpcCtx := h.ctx(h.sessionFor(t, "watcher0000000003", "device-watcher-3"), "")
	stream, err := friendspb.NewPresenceServiceClient(h.conn).SubscribePresences(rpcCtx,
		&friendspb.SubscribePresencesRequest{
			User:      "users/watcher0000000003",
			Presences: []string{"tenants/" + testTenant + "/users/friend0000000003/presence"},
		})
	if err != nil {
		t.Fatalf("SubscribePresences: %v", err)
	}
	if _, err := stream.Recv(); err != nil { // initial Presences
		t.Fatalf("recv initial: %v", err)
	}
	if _, err := stream.Recv(); err != nil { // enumeration_done
		t.Fatalf("recv enumeration_done: %v", err)
	}

	if _, err := h.store.UpdatePresence(ctx, "friend0000000003", presencePatch("ONLINE")); err != nil {
		t.Fatalf("update presence: %v", err)
	}
	if err := h.bus.Publish(ctx, presenceEvent("friend0000000003", "ONLINE")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	pushed, err := recvWithTimeout(t, stream)
	if err != nil {
		t.Fatalf("recv pushed presence: %v", err)
	}
	presences := pushed.GetPresences().GetPresences()
	if len(presences) != 1 || presences[0].GetState() != friendspb.State_ONLINE {
		t.Fatalf("got %+v, want one ONLINE presence for the friend", presences)
	}
}

// TestKeepAliveTouchesPresenceLiveness ties the bidi stream to the same
// liveness rule an authenticated BAAS request applies: talking to NPLN at
// all is proof of life, and PresenceService's own stream is where that
// matters most.
func TestKeepAliveTouchesPresenceLiveness(t *testing.T) {
	old := presenceKeepAlive
	presenceKeepAlive = 20 * time.Millisecond
	t.Cleanup(func() { presenceKeepAlive = old })

	h := newHarness(t)
	h.createUser("keepalive0000001", "device-keepalive")

	rpcCtx := h.ctx(h.sessionFor(t, "keepalive0000001", "device-keepalive"), "")
	stream, err := friendspb.NewPresenceServiceClient(h.conn).KeepAlive(rpcCtx)
	if err != nil {
		t.Fatalf("KeepAlive: %v", err)
	}
	if err := stream.Send(&friendspb.KeepAliveRequest{
		Request: &friendspb.KeepAliveRequest_Ack{Ack: &friendspb.Ack{User: "users/keepalive0000001"}},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if resp.GetHeartbeat() == nil {
		t.Errorf("response carries no heartbeat: %v", resp)
	}

	// The interceptor already touches presence once when the stream opens
	// (see enter() in server.go), which is enough to prove the mechanism --
	// resolving the caller's own presence should read ONLINE from liveness
	// alone, with nothing ever reported.
	p, err := h.srv.presence.Effective(context.Background(), "keepalive0000001", h.now)
	if err != nil {
		t.Fatalf("effective presence: %v", err)
	}
	if p.State != "ONLINE" {
		t.Errorf("state = %q, want ONLINE from KeepAlive traffic alone", p.State)
	}
}

// TestKeepAliveAnswersOnItsOwnClockNotOnceProPing is the regression test for
// the runaway-loop bug NextendoNetwork/splatoon-3 measured on real hardware
// (facts only, not its code): acking every client ping immediately makes the
// client re-send its next ping the instant it gets a reply, so the server
// ends up setting an ever-faster pace instead of the client's own. A real
// console's stream carried 1,362 messages each way in fifteen seconds under
// that bug, during which the server received no other application call --
// and the game eventually reported "A communication error has occurred" even
// though the stream itself was never actually dropped.
//
// Several rapid pings in a row must therefore get, at most, one reply within
// one keep-alive interval -- not one reply per ping.
func TestKeepAliveAnswersOnItsOwnClockNotOncePerPing(t *testing.T) {
	old := presenceKeepAlive
	presenceKeepAlive = 100 * time.Millisecond
	t.Cleanup(func() { presenceKeepAlive = old })

	h := newHarness(t)
	h.createUser("keepalive0000002", "device-keepalive-2")

	rpcCtx := h.ctx(h.sessionFor(t, "keepalive0000002", "device-keepalive-2"), "")
	stream, err := friendspb.NewPresenceServiceClient(h.conn).KeepAlive(rpcCtx)
	if err != nil {
		t.Fatalf("KeepAlive: %v", err)
	}

	const pings = 20
	for i := 0; i < pings; i++ {
		if err := stream.Send(&friendspb.KeepAliveRequest{
			Request: &friendspb.KeepAliveRequest_Ack{Ack: &friendspb.Ack{User: "users/keepalive0000002"}},
		}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	replies := make(chan *friendspb.KeepAliveResponse, pings)
	go func() {
		for {
			resp, err := stream.Recv()
			if err != nil {
				close(replies)
				return
			}
			replies <- resp
		}
	}()

	// Well under two intervals: long enough for a correct implementation's
	// one scheduled heartbeat, nowhere near enough for a broken one that
	// would already have exchanged many times that under the old
	// answer-every-ping behaviour.
	deadline := time.After(150 * time.Millisecond)
	count := 0
loop:
	for {
		select {
		case resp, ok := <-replies:
			if !ok {
				break loop
			}
			if resp.GetHeartbeat() != nil {
				count++
			}
			if count > 1 {
				t.Fatalf("replies = %d for %d pings within ~1.5 intervals, want at most 1 -- answering once per ping instead of once per interval", count, pings)
			}
		case <-deadline:
			break loop
		}
	}
}
