package presence

import (
	"context"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
)

func newStoreWithFriendship(t *testing.T, favorite bool) (*db.Memory, db.User) {
	t.Helper()
	store := db.NewMemory()
	ctx := context.Background()
	now := time.Now()

	owner := db.User{ID: "0000000000000001", Permissions: db.DefaultPermissions(now)}
	if err := store.CreateUser(ctx, owner,
		db.DeviceAccount{ID: "da-owner", UserID: owner.ID, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := store.CreateUser(ctx, db.User{ID: "0000000000000002"},
		db.DeviceAccount{ID: "da-viewer", UserID: "0000000000000002", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	if err := store.AddFriendship(ctx, owner.ID, "0000000000000002", now); err != nil {
		t.Fatalf("add friendship: %v", err)
	}
	if favorite {
		if err := store.SetFavorite(ctx, owner.ID, "0000000000000002", true); err != nil {
			t.Fatalf("set favorite: %v", err)
		}
	}
	return store, owner
}

func TestVisibleDefaultsToAnyFriend(t *testing.T) {
	store, owner := newStoreWithFriendship(t, false)
	owner.Permissions.Presence = "FRIENDS"
	if !Visible(context.Background(), store, owner, "0000000000000002") {
		t.Error("FRIENDS should show presence to any friend")
	}
	if Visible(context.Background(), store, owner, "not-a-friend") {
		t.Error("FRIENDS should not show presence to a stranger")
	}
}

func TestVisibleSelfHidesFromEveryone(t *testing.T) {
	store, owner := newStoreWithFriendship(t, true)
	owner.Permissions.Presence = "SELF"
	if Visible(context.Background(), store, owner, "0000000000000002") {
		t.Error("SELF must hide presence even from a favourite friend")
	}
}

func TestVisibleFavoriteFriendsRequiresTheFavoriteFlagOnTheOwnersSide(t *testing.T) {
	store, owner := newStoreWithFriendship(t, false)
	owner.Permissions.Presence = "FAVORITE_FRIENDS"
	if Visible(context.Background(), store, owner, "0000000000000002") {
		t.Error("FAVORITE_FRIENDS must not show presence to a friend that is not favourited")
	}

	favStore, favOwner := newStoreWithFriendship(t, true)
	favOwner.Permissions.Presence = "FAVORITE_FRIENDS"
	if !Visible(context.Background(), favStore, favOwner, "0000000000000002") {
		t.Error("FAVORITE_FRIENDS should show presence once the owner favourited that friend")
	}
}
