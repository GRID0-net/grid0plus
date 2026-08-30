package npln

import (
	"context"
	"testing"

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
