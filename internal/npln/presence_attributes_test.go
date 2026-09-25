package npln

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	commonpb "github.com/n-popescu/switchnet/proto/common"
	friendspb "github.com/n-popescu/switchnet/proto/friends/v1"
)

// watchFriend opens watcherID's SubscribePresences on friendID and drains the
// opening heartbeat, initial page and enumeration_done, leaving the stream
// positioned at the first pushed change.
func watchFriend(t *testing.T, h *harness, watcherID, watcherDevice, friendID string) grpc.ServerStreamingClient[friendspb.SubscribePresencesResponse] {
	t.Helper()
	rpcCtx := h.ctx(h.sessionFor(t, watcherID, watcherDevice), "")
	stream, err := friendspb.NewPresenceServiceClient(h.conn).SubscribePresences(rpcCtx,
		&friendspb.SubscribePresencesRequest{
			User:      "users/" + watcherID,
			Presences: []string{"tenants/" + testTenant + "/users/" + friendID + "/presence"},
		})
	if err != nil {
		t.Fatalf("SubscribePresences: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := stream.Recv(); err != nil {
			t.Fatalf("recv opening message %d: %v", i, err)
		}
	}
	return stream
}

// publish sends one UpdatePresence over a KeepAlive stream.
func publish(t *testing.T, stream grpc.BidiStreamingClient[friendspb.KeepAliveRequest, friendspb.KeepAliveResponse],
	attrs map[string]*commonpb.Value, mask ...string) {
	t.Helper()
	up := &friendspb.KeepAliveRequest_UpdatePresence{
		Presence: &friendspb.Presence{Attributes: attrs},
	}
	if len(mask) > 0 {
		up.UpdateMask = &fieldmaskpb.FieldMask{Paths: mask}
	}
	if err := stream.Send(&friendspb.KeepAliveRequest{
		Request: &friendspb.KeepAliveRequest_UpdatePresence_{UpdatePresence: up},
	}); err != nil {
		t.Fatalf("send UpdatePresence: %v", err)
	}
}

// nextAttributes waits for the next pushed presence and returns its
// attributes.
func nextAttributes(t *testing.T, stream grpc.ServerStreamingClient[friendspb.SubscribePresencesResponse]) map[string]*commonpb.Value {
	t.Helper()
	for {
		msg, err := recvWithTimeout(t, stream)
		if err != nil {
			t.Fatalf("recv pushed presence: %v", err)
		}
		if ps := msg.GetPresences().GetPresences(); len(ps) == 1 {
			return ps[0].GetAttributes()
		}
	}
}

// TestAFriendOpeningARoomShowsAsJoinable is the in-game friend list bug: a
// friend's game publishes GameStatus/SessionId over KeepAlive and the
// watcher's list must receive them. The updates are partial, as a real
// console sends them -- replacing instead of merging would drop GameStatus and
// SessionId the moment the participant count moved, and the watcher would
// never see the room.
func TestAFriendOpeningARoomShowsAsJoinable(t *testing.T) {
	h := newHarness(t)
	h.createUser("watcher0000000010", "device-watcher-10")
	h.createUser("friend0000000010", "device-friend-10")
	if err := h.store.AddFriendship(context.Background(), "watcher0000000010", "friend0000000010", h.now); err != nil {
		t.Fatalf("add friendship: %v", err)
	}
	watch := watchFriend(t, h, "watcher0000000010", "device-watcher-10", "friend0000000010")

	friendCtx := h.ctx(h.sessionFor(t, "friend0000000010", "device-friend-10"), "")
	ka, err := friendspb.NewPresenceServiceClient(h.conn).KeepAlive(friendCtx)
	if err != nil {
		t.Fatalf("KeepAlive: %v", err)
	}

	publish(t, ka, map[string]*commonpb.Value{
		"GameStatus": intV(1), "SessionId": strV(""), "PlayerName": strV("friend"),
	})
	if got := nextAttributes(t, watch); got["GameStatus"].GetIntegerValue() != 1 {
		t.Fatalf("first push GameStatus = %v, want 1", got["GameStatus"])
	}

	publish(t, ka, map[string]*commonpb.Value{"GameStatus": intV(2), "SessionId": strV("room-uuid")})
	nextAttributes(t, watch)
	publish(t, ka, map[string]*commonpb.Value{"CurrentParticipants": intV(1)})
	got := nextAttributes(t, watch)

	if got["GameStatus"].GetIntegerValue() != 2 || got["SessionId"].GetStringValue() != "room-uuid" {
		t.Errorf("after a partial update, GameStatus=%v SessionId=%v, want 2 and room-uuid kept",
			got["GameStatus"], got["SessionId"])
	}
	if got["PlayerName"].GetStringValue() != "friend" {
		t.Errorf("PlayerName = %v, want the value from the first update kept", got["PlayerName"])
	}
	if got["CurrentParticipants"].GetIntegerValue() != 1 {
		t.Errorf("CurrentParticipants = %v, want 1", got["CurrentParticipants"])
	}
}

// TestAMaskedKeyTheUpdateOmitsIsRemoved is FieldMask semantics for a map
// entry: named in the mask, absent from the update, means clear it.
func TestAMaskedKeyTheUpdateOmitsIsRemoved(t *testing.T) {
	a := newPresenceAttributes()
	a.open("u")
	a.merge("u", map[string]*commonpb.Value{"SessionId": strV("room"), "GameStatus": intV(2)}, nil)
	a.merge("u", map[string]*commonpb.Value{"GameStatus": intV(1)}, []string{"attributes.GameStatus", "attributes.SessionId"})

	got := a.get("u")
	if _, ok := got["SessionId"]; ok {
		t.Error("SessionId survived an update whose mask named it without a value")
	}
	if got["GameStatus"].GetIntegerValue() != 1 {
		t.Errorf("GameStatus = %v, want 1", got["GameStatus"])
	}
}

// TestAPlayerWhoLeavesTheGameStopsShowingTheirRoom: the attributes describe a
// live session, so the KeepAlive stream closing takes them with it. Otherwise
// a friend would keep offering to join a room that no longer exists.
func TestAPlayerWhoLeavesTheGameStopsShowingTheirRoom(t *testing.T) {
	h := newHarness(t)
	h.createUser("watcher0000000011", "device-watcher-11")
	h.createUser("friend0000000011", "device-friend-11")
	if err := h.store.AddFriendship(context.Background(), "watcher0000000011", "friend0000000011", h.now); err != nil {
		t.Fatalf("add friendship: %v", err)
	}
	watch := watchFriend(t, h, "watcher0000000011", "device-watcher-11", "friend0000000011")

	friendCtx, cancel := context.WithCancel(h.ctx(h.sessionFor(t, "friend0000000011", "device-friend-11"), ""))
	ka, err := friendspb.NewPresenceServiceClient(h.conn).KeepAlive(friendCtx)
	if err != nil {
		t.Fatalf("KeepAlive: %v", err)
	}
	publish(t, ka, map[string]*commonpb.Value{"GameStatus": intV(2), "SessionId": strV("room-uuid")})
	if got := nextAttributes(t, watch); got["SessionId"].GetStringValue() != "room-uuid" {
		t.Fatalf("SessionId = %v, want room-uuid", got["SessionId"])
	}

	cancel()
	if got := nextAttributes(t, watch); len(got) != 0 {
		t.Errorf("attributes after the friend's stream closed = %v, want none", got)
	}
}

// TestAReconnectDoesNotWipeTheNewStreamsAttributes: a console opens its new
// KeepAlive before the old one is torn down, and the old one's close must not
// clear what the new one already published.
func TestAReconnectDoesNotWipeTheNewStreamsAttributes(t *testing.T) {
	a := newPresenceAttributes()
	a.open("u")
	a.open("u")
	a.merge("u", map[string]*commonpb.Value{"GameStatus": intV(1)}, nil)
	a.close("u")
	if a.get("u") == nil {
		t.Fatal("closing one of two streams cleared the attributes")
	}
	a.close("u")
	if a.get("u") != nil {
		t.Error("closing the last stream left the attributes in place")
	}
}
