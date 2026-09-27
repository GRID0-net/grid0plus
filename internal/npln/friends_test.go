package npln

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/presencebus"
	friendspb "github.com/n-popescu/grid0plus/proto/friends/v1"
)

// TestListFriendUsersSetsPresenceDeliverableAndReceivable is ROADMAP §2.5's
// own warning: get these two flags wrong and the client neither shows a
// friend's presence nor publishes its own, while the friend list itself
// still looks perfectly healthy.
func TestListFriendUsersSetsPresenceDeliverableAndReceivable(t *testing.T) {
	h := newHarness(t)
	h.createUser("owner00000000001", "device-owner")
	h.createUser("friend0000000001", "device-friend")
	if err := h.store.AddFriendship(context.Background(), "owner00000000001", "friend0000000001", h.now); err != nil {
		t.Fatalf("add friendship: %v", err)
	}

	ctx := h.ctx(h.sessionFor(t, "owner00000000001", "device-owner"), "")
	client := friendspb.NewFriendsClient(h.conn)

	resp, err := client.ListFriendUsers(ctx, &friendspb.ListFriendUsersRequest{
		Parent: "tenants/" + testTenant + "/users/owner00000000001",
	})
	if err != nil {
		t.Fatalf("ListFriendUsers: %v", err)
	}
	if len(resp.GetFriendUsers()) != 1 {
		t.Fatalf("got %d friends, want 1", len(resp.GetFriendUsers()))
	}
	fu := resp.GetFriendUsers()[0]
	if fu.GetNsaId() != "friend0000000001" {
		t.Errorf("nsa id = %q, want the friend's user id", fu.GetNsaId())
	}
	// Both default to FRIENDS visibility, and they ARE friends, so presence
	// must flow both ways by default.
	if !fu.GetRelationship().GetPresenceReceivable() {
		t.Error("presence_receivable is false for a default-visibility friend")
	}
	if !fu.GetRelationship().GetPresenceDeliverable() {
		t.Error("presence_deliverable is false for a default-visibility friend")
	}
}

// TestListFriendUsersRespectsTheFriendsOwnPrivacySetting proves the flags
// are not just always true: a friend who set their presence to SELF must not
// be presence_receivable, even though the friendship itself still exists.
func TestListFriendUsersRespectsTheFriendsOwnPrivacySetting(t *testing.T) {
	h := newHarness(t)
	h.createUser("owner00000000002", "device-owner-2")
	h.createUser("private0000000001", "device-private")
	ctx := context.Background()
	if err := h.store.AddFriendship(ctx, "owner00000000002", "private0000000001", h.now); err != nil {
		t.Fatalf("add friendship: %v", err)
	}
	u, err := h.store.GetUser(ctx, "private0000000001")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	u.Permissions.Presence = "SELF"
	if _, err := h.store.UpdateUser(ctx, "private0000000001", db.UserPatch{Permissions: &u.Permissions}); err != nil {
		t.Fatalf("update permissions: %v", err)
	}

	rpcCtx := h.ctx(h.sessionFor(t, "owner00000000002", "device-owner-2"), "")
	resp, err := friendspb.NewFriendsClient(h.conn).ListFriendUsers(rpcCtx, &friendspb.ListFriendUsersRequest{})
	if err != nil {
		t.Fatalf("ListFriendUsers: %v", err)
	}
	if len(resp.GetFriendUsers()) != 1 {
		t.Fatalf("got %d friends, want 1", len(resp.GetFriendUsers()))
	}
	if resp.GetFriendUsers()[0].GetRelationship().GetPresenceReceivable() {
		t.Error("presence_receivable is true for a friend whose presence is set to SELF")
	}
}

func TestListBlockingUsersReturnsBlocks(t *testing.T) {
	h := newHarness(t)
	h.createUser("blocker00000001", "device-blocker")
	h.createUser("blocked00000001", "device-blocked")
	if err := h.store.AddBlock(context.Background(), "blocker00000001", "blocked00000001", h.now); err != nil {
		t.Fatalf("add block: %v", err)
	}

	ctx := h.ctx(h.sessionFor(t, "blocker00000001", "device-blocker"), "")
	resp, err := friendspb.NewFriendsClient(h.conn).ListBlockingUsers(ctx, &friendspb.ListBlockingUsersRequest{})
	if err != nil {
		t.Fatalf("ListBlockingUsers: %v", err)
	}
	if len(resp.GetBlockingUsers()) != 1 || resp.GetBlockingUsers()[0].GetNsaId() != "blocked00000001" {
		t.Fatalf("got %+v, want one block naming blocked00000001", resp.GetBlockingUsers())
	}
}

// TestActivateUserRefusesAnotherUsersName is the same rule matchmaking's
// ticket ownership checks and AllocateIceServerSet's doc comment both apply:
// the token decides who a call is about, not a field the client filled in.
func TestActivateUserRefusesAnotherUsersName(t *testing.T) {
	h := newHarness(t)
	h.createUser("caller00000000001", "device-caller")
	h.createUser("other0000000000001", "device-other")

	ctx := h.ctx(h.sessionFor(t, "caller00000000001", "device-caller"), "")
	_, err := friendspb.NewFriendsClient(h.conn).ActivateUser(ctx, &friendspb.ActivateUserRequest{
		Name: "users/other0000000000001",
	})
	if err == nil {
		t.Fatal("ActivateUser for another user succeeded")
	}

	if _, err := friendspb.NewFriendsClient(h.conn).ActivateUser(ctx, &friendspb.ActivateUserRequest{
		Name: "users/caller00000000001",
	}); err != nil {
		t.Errorf("ActivateUser for self: %v", err)
	}
}

// TestSubscribeFriendUsersPushesAFreshSnapshotWhenAFriendsPresenceChanges is
// the streams-must-stay-warm-AND-useful property: a console watching this
// stream should see a friend go online without waiting for the keep-alive
// timer.
func TestSubscribeFriendUsersPushesAFreshSnapshotWhenAFriendsPresenceChanges(t *testing.T) {
	h := newHarness(t)
	h.createUser("watcher0000000001", "device-watcher")
	h.createUser("watched0000000001", "device-watched")
	ctx := context.Background()
	if err := h.store.AddFriendship(ctx, "watcher0000000001", "watched0000000001", h.now); err != nil {
		t.Fatalf("add friendship: %v", err)
	}

	rpcCtx := h.ctx(h.sessionFor(t, "watcher0000000001", "device-watcher"), "")
	stream, err := friendspb.NewFriendsClient(h.conn).SubscribeFriendUsers(rpcCtx,
		&friendspb.SubscribeFriendUsersRequest{User: "users/watcher0000000001"})
	if err != nil {
		t.Fatalf("SubscribeFriendUsers: %v", err)
	}

	first, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv initial snapshot: %v", err)
	}
	if len(first.GetFriendAccounts()) != 1 {
		t.Fatalf("initial snapshot has %d friend accounts, want 1", len(first.GetFriendAccounts()))
	}

	// A fresh presence report for the friend, published on the bus exactly as
	// BAAS's PATCH device_accounts handler would.
	if _, err := h.store.UpdatePresence(ctx, "watched0000000001", presencePatch("ONLINE")); err != nil {
		t.Fatalf("update presence: %v", err)
	}
	if err := h.bus.Publish(ctx, presenceEvent("watched0000000001", "ONLINE")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	pushed, err := recvWithTimeout(t, stream)
	if err != nil {
		t.Fatalf("recv pushed snapshot: %v", err)
	}
	users := pushed.GetFriendAccounts()[0].GetUsers()
	if len(users) != 1 {
		t.Fatalf("pushed snapshot has %d users, want 1", len(users))
	}
}

// ---- shared streaming test helpers -----------------------------------------

// presencePatch builds a minimal db.PresencePatch reporting the given state,
// timestamped now -- enough to make internal/presence's TTL rules read it as
// fresh.
func presencePatch(state string) db.PresencePatch {
	now := time.Now().UTC()
	return db.PresencePatch{State: &state, ReportedAt: &now}
}

// presenceEvent builds the presencebus.Event a fresh BAAS presence report
// would publish, for tests that push one directly rather than going through
// BAAS's own HTTP handler.
func presenceEvent(userID, state string) presencebus.Event {
	return presencebus.Event{UserID: userID, State: state, At: time.Now().UTC(), Source: "test"}
}

// recvStream is the minimal shape both SubscribeFriendUsers's and
// SubscribePresences's generated client streams satisfy.
type recvStream[T any] interface {
	Recv() (T, error)
}

// recvWithTimeout waits for one message rather than hanging the test forever
// if a push does not arrive.
func recvWithTimeout[T any](t *testing.T, stream recvStream[T]) (T, error) {
	t.Helper()
	type result struct {
		msg T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		msg, err := stream.Recv()
		ch <- result{msg, err}
	}()
	select {
	case r := <-ch:
		return r.msg, r.err
	case <-time.After(5 * time.Second):
		var zero T
		return zero, context.DeadlineExceeded
	}
}

func TestActivateCurrentUserStillRequiresTheCallersToken(t *testing.T) {
	h := newHarness(t)
	h.createUser("alias-owner", "alias-device")
	client := friendspb.NewFriendsClient(h.conn)
	req := &friendspb.ActivateUserRequest{Name: "tenants/current/users/current"}
	if _, err := client.ActivateUser(h.ctx("", ""), req); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("anonymous activation = %v, want Unauthenticated", err)
	}
	ctx := h.ctx(h.sessionFor(t, "alias-owner", "alias-device"), "")
	if _, err := client.ActivateUser(ctx, req); err != nil {
		t.Fatalf("activate authenticated current user: %v", err)
	}
	for _, name := range []string{"tenants/current/users/someone-else", "tenants/foreign/users/current"} {
		if _, err := client.ActivateUser(ctx, &friendspb.ActivateUserRequest{Name: name}); status.Code(err) != codes.PermissionDenied {
			t.Errorf("ActivateUser(%q) = %v, want PermissionDenied", name, err)
		}
	}
}

// A captured healthy stream advertises 50s but sends a tiny, unchanged-list
// keepalive every 20s. A full snapshot on that tick is a different message.
func TestSubscribeFriendUsersSendsALightweightKeepalive(t *testing.T) {
	old := friendsStreamTick
	friendsStreamTick = 20 * time.Millisecond
	t.Cleanup(func() { friendsStreamTick = old })

	h := newHarness(t)
	h.createUser("heartbeat0000001", "device-heartbeat")
	rpcCtx := h.ctx(h.sessionFor(t, "heartbeat0000001", "device-heartbeat"), "")
	stream, err := friendspb.NewFriendsClient(h.conn).SubscribeFriendUsers(rpcCtx,
		&friendspb.SubscribeFriendUsersRequest{User: "users/heartbeat0000001"})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := recvWithTimeout(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	if got := initial.GetKeepAliveInterval().AsDuration(); got != 50*time.Second {
		t.Fatalf("initial advertised interval = %s, want 50s", got)
	}
	pulse, err := recvWithTimeout(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	if got := pulse.GetKeepAliveInterval().AsDuration(); got != 50*time.Second {
		t.Fatalf("keepalive advertised interval = %s, want 50s", got)
	}
	if len(pulse.GetFriendAccounts()) != 0 || len(pulse.GetBlockingAccounts()) != 0 {
		t.Fatalf("unchanged friend list was resent on the keepalive tick: %v", pulse)
	}
}
