package dashboard

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
)

func TestDebugStatsReportsRowCounts(t *testing.T) {
	now := time.Now().UTC()
	srv, store := newTestServer(t, "", now)
	ctx := context.Background()

	addUser(t, store, "alice", "Alice")
	addUser(t, store, "bob", "Bob")
	if err := store.AddFriendship(ctx, "alice", "bob", now); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/debug/stats", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	var got db.Stats
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Users != 2 {
		t.Errorf("users = %d, want 2", got.Users)
	}
	if got.DeviceAccounts != 2 {
		t.Errorf("deviceAccounts = %d, want 2", got.DeviceAccounts)
	}
	if got.Friendships != 2 {
		t.Errorf("friendships = %d, want 2 (both directions)", got.Friendships)
	}
}

// TestDebugAccountShowsRawAndEffectivePresenceSideBySide covers the whole
// point of the account lookup panel: a console that is idle in the Home
// Menu reports nothing, so its raw presence row stays whatever it was --
// while the effective column, resolved through internal/presence the same
// way the friends applet is, still says ONLINE. The two columns disagreeing
// here is expected, not a bug, and is exactly the case an operator debugging
// "friends were never online" needs to see.
func TestDebugAccountShowsRawAndEffectivePresenceSideBySide(t *testing.T) {
	now := time.Now().UTC()
	srv, store := newTestServer(t, "", now)
	ctx := context.Background()
	addUser(t, store, "alice", "Alice")

	if err := store.TouchPresence(ctx, "alice", now); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/debug/accounts/alice", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	var got debugAccount
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.RawPresence.State != "OFFLINE" {
		t.Errorf("raw presence state = %q, want OFFLINE (nothing was ever reported)", got.RawPresence.State)
	}
	if got.EffectivePresence.State != "ONLINE" {
		t.Errorf("effective presence state = %q, want ONLINE (a fresh heartbeat)", got.EffectivePresence.State)
	}
	if len(got.DeviceAccounts) != 1 || got.DeviceAccounts[0].ID != "da-alice" {
		t.Fatalf("device accounts = %+v, want [da-alice]", got.DeviceAccounts)
	}
}

func TestDebugAccountIncludesADeletedDeviceAccount(t *testing.T) {
	now := time.Now().UTC()
	srv, store := newTestServer(t, "", now)
	ctx := context.Background()
	addUser(t, store, "alice", "Alice")
	if err := store.DeleteDeviceAccount(ctx, "alice", "da-alice"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/debug/accounts/alice", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	var got debugAccount
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The Players tab's own detail panel would hide this -- the Debug tab
	// must not, or there is nothing to undelete.
	if len(got.DeviceAccounts) != 1 || got.DeviceAccounts[0].DeletedAt == "" {
		t.Fatalf("device accounts = %+v, want one with deletedAt set", got.DeviceAccounts)
	}
}

func TestDebugAccountOfAnUnknownAccountIs404(t *testing.T) {
	srv, _ := newTestServer(t, "", time.Now().UTC())
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/debug/accounts/nope", nil))
	if rec.Code != 404 {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestDebugSetAccountDeletedTogglesTheFailClosedFlag(t *testing.T) {
	srv, store := newTestServer(t, "", time.Now().UTC())
	ctx := context.Background()
	addUser(t, store, "alice", "Alice")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/debug/accounts/alice/deleted", strings.NewReader(`{"deleted":true}`)))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	u, err := store.GetUser(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if !u.Deleted {
		t.Error("Deleted was not set")
	}

	// Reversible: setting it back is a normal write, not a special path.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/debug/accounts/alice/deleted", strings.NewReader(`{"deleted":false}`)))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	u, err = store.GetUser(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if u.Deleted {
		t.Error("Deleted was not cleared")
	}
}

func TestDebugSetAccountDeletedOfAnUnknownAccountIs404(t *testing.T) {
	srv, _ := newTestServer(t, "", time.Now().UTC())
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/debug/accounts/nope/deleted", strings.NewReader(`{"deleted":true}`)))
	if rec.Code != 404 {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestDebugRevokeDeviceAccountTokensInvalidatesEarlierTokens(t *testing.T) {
	now := time.Now().UTC()
	srv, store := newTestServer(t, "", now)
	ctx := context.Background()
	addUser(t, store, "alice", "Alice")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/debug/accounts/alice/device-accounts/da-alice/revoke-tokens", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	da, err := store.GetDeviceAccount(ctx, "da-alice")
	if err != nil {
		t.Fatal(err)
	}
	if da.TokensRevokedBefore == nil {
		t.Error("TokensRevokedBefore was not set")
	}
}

func TestDebugSetDeviceAccountDeletedAndUndeleted(t *testing.T) {
	now := time.Now().UTC()
	srv, store := newTestServer(t, "", now)
	ctx := context.Background()
	addUser(t, store, "alice", "Alice")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST",
		"/api/debug/accounts/alice/device-accounts/da-alice/deleted", strings.NewReader(`{"deleted":true}`)))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	da, err := store.GetDeviceAccount(ctx, "da-alice")
	if err != nil {
		t.Fatal(err)
	}
	if da.DeletedAt == nil {
		t.Fatal("DeletedAt was not set")
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST",
		"/api/debug/accounts/alice/device-accounts/da-alice/deleted", strings.NewReader(`{"deleted":false}`)))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	da, err = store.GetDeviceAccount(ctx, "da-alice")
	if err != nil {
		t.Fatal(err)
	}
	if da.DeletedAt != nil {
		t.Error("DeletedAt was not cleared by undelete")
	}
}
