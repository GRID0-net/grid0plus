package npln

import (
	"bytes"
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

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

// TestKeepAliveAcksEveryPingImmediately is the regression test for getting
// this backwards once already, in this same session: an earlier version of
// this function answered on its own independent timer instead of the
// client's pings, on the theory that acking immediately would cause a
// runaway loop. NextendoNetwork/splatoon-3's own hardware measurement (facts
// only, not its code) says the opposite is true: a real Nintendo server
// acks every single ping (128, 34 and 77 of them, across three real
// sessions, one reply each), and answering on a slower independent clock is
// what killed real streams at 59.998s / 1m0.001s with "a communication
// error has occurred" -- because the FIRST reply is what announces the
// {interval, deadline} contract the client's own patience is calibrated
// against, and the client got no reply to its very first ping.
func TestKeepAliveAcksEveryPingImmediately(t *testing.T) {
	old := presenceKeepAlive
	presenceKeepAlive = time.Hour // any tick firing would also pass a ping; keep it out of reach
	t.Cleanup(func() { presenceKeepAlive = old })

	h := newHarness(t)
	h.createUser("keepalive0000003", "device-keepalive-3")

	rpcCtx := h.ctx(h.sessionFor(t, "keepalive0000003", "device-keepalive-3"), "")
	stream, err := friendspb.NewPresenceServiceClient(h.conn).KeepAlive(rpcCtx)
	if err != nil {
		t.Fatalf("KeepAlive: %v", err)
	}

	// The opening reply, before any ping is sent, announces the contract.
	opening, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv opening heartbeat: %v", err)
	}
	if opening.GetHeartbeat() == nil {
		t.Fatalf("opening message carries no heartbeat: %v", opening)
	}

	for i := 0; i < 3; i++ {
		if err := stream.Send(&friendspb.KeepAliveRequest{
			Request: &friendspb.KeepAliveRequest_Ack{Ack: &friendspb.Ack{User: "users/keepalive0000003"}},
		}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		resp, err := stream.Recv()
		if err != nil {
			t.Fatalf("recv %d: %v", i, err)
		}
		if resp.GetHeartbeat() == nil {
			t.Errorf("ping %d: response carries no heartbeat: %v", i, resp)
		}
	}
}

// TestPresenceHeartbeatCarriesTheUndocumentedDeadlineField covers the field
// this repository's own .proto has no name for: a real Heartbeat carries a
// second, unknown field alongside Interval -- {2: {1: 50}}, the 50-second
// deadline the client's own patience is calibrated against (see
// presenceHeartbeatDeadline's doc comment for the capture this reproduces).
// A Heartbeat missing it is a likely reason a client would fall back to a
// shorter, internal default instead of the stated one.
func TestPresenceHeartbeatCarriesTheUndocumentedDeadlineField(t *testing.T) {
	hb := presenceHeartbeat()
	raw, err := proto.Marshal(hb)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := []byte{0x12, 0x02, 0x08, 0x32}
	if !bytes.Contains(raw, want) {
		t.Errorf("encoded Heartbeat = % x, want it to contain the field-2 deadline % x", raw, want)
	}
}
