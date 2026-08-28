package db

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The store contract runs against every implementation, so the in-memory store
// used by the handler tests cannot drift away from the PostgreSQL one that runs
// in production. A behaviour that only holds for one of them is a bug waiting
// for the first real console.
//
// PostgreSQL is only exercised when SWITCHNET_TEST_DATABASE_URL is set, so
// `go test ./...` works on a machine with no database. CI sets it.

func TestMemoryStore(t *testing.T) {
	runStoreContract(t, func(t *testing.T) Store { return NewMemory() })
}

func TestPostgresStore(t *testing.T) {
	url := os.Getenv("SWITCHNET_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SWITCHNET_TEST_DATABASE_URL is not set; skipping the PostgreSQL contract")
	}

	runStoreContract(t, func(t *testing.T) Store {
		t.Helper()
		store, err := OpenPostgres(t.Context(), url, 4, true)
		if err != nil {
			t.Fatalf("open postgres: %v", err)
		}
		truncateAll(t, store.Pool())
		t.Cleanup(func() { _ = store.Close() })
		return store
	})
}

// truncateAll empties every table between subtests, so one does not see another's
// rows. The migrations themselves are only applied once.
func truncateAll(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	// users is the root of every foreign key, so CASCADE clears the rest.
	_, err := pool.Exec(t.Context(),
		`TRUNCATE users, devices, images, npln_fests, npln_matches RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// runStoreContract exercises one Store implementation.
func runStoreContract(t *testing.T, open func(*testing.T) Store) {
	tests := map[string]func(*testing.T, Store){
		"users":                 testUsers,
		"deviceAccounts":        testDeviceAccounts,
		"reassignDeviceAccount": testReassignDeviceAccount,
		"friendCodes":           testFriendCodes,
		"friendships":           testFriendships,
		"friendRequests":        testFriendRequests,
		"blocks":                testBlocks,
		"presence":              testPresence,
		"images":                testImages,
		"findUsers":             testFindUsers,
		"userLogin":             testUserLogin,
		"fests":                 testFests,
		"festEntries":           testFestEntries,
		"matches":               testMatches,
	}
	for name, fn := range tests {
		t.Run(name, func(t *testing.T) {
			fn(t, open(t))
		})
	}
}

// ---- fixtures ------------------------------------------------------------

var seq int

// makeUser inserts a user with a distinct id and returns it.
func makeUser(t *testing.T, s Store, nickname string) User {
	t.Helper()
	seq++
	now := time.Now().UTC().Truncate(time.Microsecond)
	id := hexID(seq)
	u := User{
		ID:               id,
		Nickname:         nickname,
		Birthday:         "0000-00-00",
		Permissions:      DefaultPermissions(now),
		Extras:           EmptyExtras(),
		BlocksUpdatedAt:  now,
		FriendsUpdatedAt: now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	da := DeviceAccount{
		ID:             hexID(seq + 100000),
		UserID:         id,
		PasswordSHA256: "0000000000000000000000000000000000000000000000000000000000000000",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.CreateUser(t.Context(), u, da); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

// hexID returns a deterministic 16-hex-digit id.
func hexID(n int) string {
	const digits = "0123456789abcdef"
	buf := make([]byte, 16)
	for i := 15; i >= 0; i-- {
		buf[i] = digits[n&0xf]
		n >>= 4
	}
	return string(buf)
}

// ---- users ---------------------------------------------------------------

func testUsers(t *testing.T, s Store) {
	ctx := t.Context()
	u := makeUser(t, s, "Alice")

	got, err := s.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if got.Nickname != "Alice" {
		t.Errorf("nickname = %q", got.Nickname)
	}
	// Every scope must round-trip, even when empty: the handlers index into
	// these without checking the key exists.
	for _, scope := range Scopes {
		if _, ok := got.Extras[scope]; !ok {
			t.Errorf("extras is missing the %q scope after a round trip", scope)
		}
	}
	if got.Permissions.Presence != "FRIENDS" {
		t.Errorf("permissions did not round-trip: %+v", got.Permissions)
	}

	if _, err := s.GetUser(ctx, "ffffffffffffffff"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get missing user: err = %v, want ErrNotFound", err)
	}

	// A patch touches only the fields it names.
	nickname := "Renamed"
	updated, err := s.UpdateUser(ctx, u.ID, UserPatch{Nickname: &nickname})
	if err != nil {
		t.Fatalf("update user: %v", err)
	}
	if updated.Nickname != "Renamed" {
		t.Errorf("nickname = %q", updated.Nickname)
	}
	if updated.Birthday != "0000-00-00" {
		t.Errorf("an unnamed field changed: birthday = %q", updated.Birthday)
	}

	// A JSON round trip through extras must not lose keys.
	extras := updated.Extras
	extras[ScopeSelf]["nxAccount"] = "abc"
	extras[ScopeFriends]["playLog"] = `[{"appInfo:appId":"0100c2500fc20000"}]`
	updated, err = s.UpdateUser(ctx, u.ID, UserPatch{Extras: &extras})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Extras[ScopeSelf]["nxAccount"] != "abc" {
		t.Errorf("self extras did not round-trip: %+v", updated.Extras[ScopeSelf])
	}
	if updated.Extras[ScopeFriends]["playLog"] == nil {
		t.Error("friends extras did not round-trip")
	}

	// A duplicate user id must conflict rather than overwrite.
	dup := u
	err = s.CreateUser(ctx, dup, DeviceAccount{
		ID: hexID(999999), UserID: u.ID,
		PasswordSHA256: "0000000000000000000000000000000000000000000000000000000000000000",
		CreatedAt:      time.Now(), UpdatedAt: time.Now(),
	})
	if !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate user: err = %v, want ErrConflict", err)
	}
}

// testUserLogin covers the login-credential path: a user that exists with no
// device account at all, found by username rather than by any console-issued
// credential.
func testUserLogin(t *testing.T, s Store) {
	ctx := t.Context()
	seq++
	now := time.Now().UTC().Truncate(time.Microsecond)
	u := User{
		ID:                hexID(seq),
		Nickname:          "Bob",
		Birthday:          "0000-00-00",
		Permissions:       DefaultPermissions(now),
		Extras:            EmptyExtras(),
		BlocksUpdatedAt:   now,
		FriendsUpdatedAt:  now,
		CreatedAt:         now,
		UpdatedAt:         now,
		LoginUsername:     "bob",
		LoginPasswordHash: "$2a$10$notarealhash",
	}
	if err := s.CreateUserWithLogin(ctx, u); err != nil {
		t.Fatalf("create user with login: %v", err)
	}

	// Found by id, same as a console-created user.
	got, err := s.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if got.LoginUsername != "bob" || got.LoginPasswordHash != "$2a$10$notarealhash" {
		t.Errorf("login credentials did not round-trip: %+v", got)
	}

	// No device account at all -- exists, just deviceless, until this person
	// links a console.
	das, err := s.ListDeviceAccounts(ctx, u.ID)
	if err != nil {
		t.Fatalf("list device accounts: %v", err)
	}
	if len(das) != 0 {
		t.Errorf("got %d device accounts for a website-registered user, want 0", len(das))
	}

	// Found by username, case-insensitively -- matches how the login page
	// and the NA-link stub both look a person up.
	byUsername, err := s.GetUserByLoginUsername(ctx, "BOB")
	if err != nil {
		t.Fatalf("get user by login username: %v", err)
	}
	if byUsername.ID != u.ID {
		t.Errorf("got user %s, want %s", byUsername.ID, u.ID)
	}

	if _, err := s.GetUserByLoginUsername(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get missing login username: err = %v, want ErrNotFound", err)
	}

	// A duplicate username conflicts, case-insensitively, rather than
	// silently creating a second person who can log in as "bob".
	seq++
	dup := User{
		ID:                hexID(seq),
		Birthday:          "0000-00-00",
		Permissions:       DefaultPermissions(now),
		Extras:            EmptyExtras(),
		BlocksUpdatedAt:   now,
		FriendsUpdatedAt:  now,
		CreatedAt:         now,
		UpdatedAt:         now,
		LoginUsername:     "BOB",
		LoginPasswordHash: "$2a$10$anotherhash",
	}
	if err := s.CreateUserWithLogin(ctx, dup); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate login username: err = %v, want ErrConflict", err)
	}

	// A console-created user (the existing path, via CreateUser) has no login
	// credentials at all -- registerUser never sets them.
	consoleUser := makeUser(t, s, "ConsoleOnly")
	got, err = s.GetUser(ctx, consoleUser.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if got.LoginUsername != "" || got.LoginPasswordHash != "" {
		t.Errorf("console-created user has login credentials set: %+v", got)
	}
}

// TestMemoryStoreDoesNotAliasStoredMaps is memory-only: the PostgreSQL store
// cannot alias, because it goes through JSON.
//
// Without cloning, a handler that edits an extras map in place would corrupt
// stored state in a way that only shows up under the memory store — which is
// exactly where the handler tests run, so the bug would be invisible until
// production.
func TestMemoryStoreDoesNotAliasStoredMaps(t *testing.T) {
	s := NewMemory()
	u := makeUser(t, s, "Alice")

	got, err := s.GetUser(t.Context(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	got.Extras[ScopeSelf]["injected"] = "value"

	again, err := s.GetUser(t.Context(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := again.Extras[ScopeSelf]["injected"]; present {
		t.Error("mutating a returned user reached into the store")
	}
}

// ---- device accounts -----------------------------------------------------

func testDeviceAccounts(t *testing.T, s Store) {
	ctx := t.Context()
	u := makeUser(t, s, "Alice")

	das, err := s.ListDeviceAccounts(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(das) != 1 {
		t.Fatalf("got %d device accounts, want 1 created with the user", len(das))
	}
	da := das[0]

	got, err := s.GetDeviceAccount(ctx, da.ID)
	if err != nil {
		t.Fatalf("get device account: %v", err)
	}
	if got.UserID != u.ID {
		t.Errorf("device account owner = %q, want %q", got.UserID, u.ID)
	}

	if _, err := s.GetDeviceAccount(ctx, "ffffffffffffffff"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get missing device account: err = %v, want ErrNotFound", err)
	}

	if err := s.DeleteDeviceAccount(ctx, u.ID, da.ID); err != nil {
		t.Fatalf("delete device account: %v", err)
	}

	// A deleted device account is still readable, with DeletedAt set. Returning
	// ErrNotFound instead would make "deleted" indistinguishable from "never
	// existed", and the login handler answers those two differently.
	got, err = s.GetDeviceAccount(ctx, da.ID)
	if err != nil {
		t.Fatalf("get deleted device account: %v", err)
	}
	if got.DeletedAt == nil {
		t.Error("DeletedAt is nil after deletion")
	}

	// It no longer appears in the user's list.
	das, err = s.ListDeviceAccounts(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(das) != 0 {
		t.Errorf("a deleted device account is still listed: %+v", das)
	}

	// Deleting twice, or deleting somebody else's, is not found.
	if err := s.DeleteDeviceAccount(ctx, u.ID, da.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: err = %v, want ErrNotFound", err)
	}
	other := makeUser(t, s, "Bob")
	if err := s.DeleteDeviceAccount(ctx, other.ID, da.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-user delete: err = %v, want ErrNotFound", err)
	}
}

func testReassignDeviceAccount(t *testing.T, s Store) {
	ctx := t.Context()
	from := makeUser(t, s, "Throwaway")
	to := makeUser(t, s, "Doughston")

	das, err := s.ListDeviceAccounts(ctx, from.ID)
	if err != nil {
		t.Fatal(err)
	}
	da := das[0]

	if err := s.ReassignDeviceAccount(ctx, da.ID, to.ID); err != nil {
		t.Fatalf("reassign: %v", err)
	}

	got, err := s.GetDeviceAccount(ctx, da.ID)
	if err != nil {
		t.Fatalf("get device account: %v", err)
	}
	if got.UserID != to.ID {
		t.Errorf("device account owner = %q, want %q", got.UserID, to.ID)
	}
	// Same id, same password — a console that already cached them does not
	// need to re-register.
	if got.ID != da.ID || got.PasswordSHA256 != da.PasswordSHA256 {
		t.Error("reassignment changed the device account's id or password")
	}

	// It moved: gone from the old owner's list, present in the new owner's.
	fromList, err := s.ListDeviceAccounts(ctx, from.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fromList) != 0 {
		t.Errorf("reassigned device account is still listed under the old owner: %+v", fromList)
	}
	// makeUser gives "to" its own device account already, so a correct
	// reassignment leaves it with two: its own plus the one just moved over.
	toList, err := s.ListDeviceAccounts(ctx, to.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range toList {
		if d.ID == da.ID {
			found = true
		}
	}
	if len(toList) != 2 || !found {
		t.Errorf("new owner's device accounts = %+v, want its own plus %q", toList, da.ID)
	}

	if err := s.ReassignDeviceAccount(ctx, "ffffffffffffffff", to.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("reassign a missing device account: err = %v, want ErrNotFound", err)
	}
	if err := s.ReassignDeviceAccount(ctx, da.ID, "ffffffffffffffff"); !errors.Is(err, ErrNotFound) {
		t.Errorf("reassign to a missing user: err = %v, want ErrNotFound", err)
	}
}

func testFriendCodes(t *testing.T, s Store) {
	ctx := t.Context()
	a := makeUser(t, s, "Alice")
	b := makeUser(t, s, "Bob")
	now := time.Now().UTC().Truncate(time.Microsecond)

	if _, err := s.GetFriendCode(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a user with no code: err = %v, want ErrNotFound", err)
	}

	fc := FriendCode{
		UserID: a.ID, Code: "1111-2222-3333",
		CreatedAt: now, UpdatedAt: now, RegenerableAt: now.Add(24 * time.Hour),
	}
	if err := s.SetFriendCode(ctx, fc); err != nil {
		t.Fatalf("set friend code: %v", err)
	}

	got, err := s.GetFriendCode(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Code != fc.Code {
		t.Errorf("code = %q", got.Code)
	}
	if got.Regenerable(now) {
		t.Error("regenerable during the cooldown")
	}
	if !got.Regenerable(now.Add(25 * time.Hour)) {
		t.Error("not regenerable after the cooldown")
	}

	// A code belongs to one user. Without the constraint, two people could hold
	// the same code and "add by code" would be ambiguous.
	err = s.SetFriendCode(ctx, FriendCode{
		UserID: b.ID, Code: fc.Code,
		CreatedAt: now, UpdatedAt: now, RegenerableAt: now,
	})
	if !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate code: err = %v, want ErrConflict", err)
	}

	// Regenerating replaces the same user's code.
	fc.Code = "4444-5555-6666"
	fc.UpdatedAt = now.Add(time.Hour)
	if err := s.SetFriendCode(ctx, fc); err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	got, err = s.GetFriendCode(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Code != "4444-5555-6666" {
		t.Errorf("code after regeneration = %q", got.Code)
	}
}

// ---- friendships ---------------------------------------------------------

func testFriendships(t *testing.T, s Store) {
	ctx := t.Context()
	a := makeUser(t, s, "Alice")
	b := makeUser(t, s, "Bob")
	now := time.Now().UTC().Truncate(time.Microsecond)

	if err := s.AddFriendship(ctx, a.ID, b.ID, now); err != nil {
		t.Fatalf("add friendship: %v", err)
	}

	// Both directions must exist. One row would make the flag shared, and
	// favouriting somebody would silently favourite you back.
	for _, pair := range [][2]string{{a.ID, b.ID}, {b.ID, a.ID}} {
		f, err := s.GetFriendship(ctx, pair[0], pair[1])
		if err != nil {
			t.Fatalf("friendship %s -> %s: %v", pair[0], pair[1], err)
		}
		if f.Favorite {
			t.Error("a new friendship is already a favourite")
		}
	}

	// Adding twice is idempotent.
	if err := s.AddFriendship(ctx, a.ID, b.ID, now); err != nil {
		t.Fatalf("re-add friendship: %v", err)
	}
	friends, err := s.ListFriends(ctx, a.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(friends) != 1 {
		t.Errorf("re-adding created %d rows, want 1", len(friends))
	}

	// friends_updated_at has to move, or the console keeps serving a stale
	// cached friend list.
	updated, err := s.GetUser(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.FriendsUpdatedAt.After(a.FriendsUpdatedAt) &&
		!updated.FriendsUpdatedAt.Equal(now) {
		t.Errorf("friends_updated_at did not move: %v (was %v)",
			updated.FriendsUpdatedAt, a.FriendsUpdatedAt)
	}

	// A favourite is one-directional.
	if err := s.SetFavorite(ctx, a.ID, b.ID, true); err != nil {
		t.Fatalf("set favourite: %v", err)
	}
	f, err := s.GetFriendship(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Favorite {
		t.Error("the favourite flag was not set")
	}
	reverse, err := s.GetFriendship(ctx, b.ID, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reverse.Favorite {
		t.Error("the favourite flag leaked into the other direction")
	}

	// Nobody is their own friend.
	if err := s.AddFriendship(ctx, a.ID, a.ID, now); err == nil {
		t.Error("a self-friendship was accepted")
	}

	// Removing clears both directions.
	if err := s.RemoveFriendship(ctx, a.ID, b.ID); err != nil {
		t.Fatalf("remove friendship: %v", err)
	}
	for _, pair := range [][2]string{{a.ID, b.ID}, {b.ID, a.ID}} {
		if _, err := s.GetFriendship(ctx, pair[0], pair[1]); !errors.Is(err, ErrNotFound) {
			t.Errorf("friendship %s -> %s survived removal", pair[0], pair[1])
		}
	}
	if err := s.RemoveFriendship(ctx, a.ID, b.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second remove: err = %v, want ErrNotFound", err)
	}

	if err := s.SetFavorite(ctx, a.ID, b.ID, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("favourite a non-friend: err = %v, want ErrNotFound", err)
	}
}

func testFriendRequests(t *testing.T, s Store) {
	ctx := t.Context()
	a := makeUser(t, s, "Alice")
	b := makeUser(t, s, "Bob")
	now := time.Now().UTC().Truncate(time.Microsecond)

	req := FriendRequest{
		ID: uuid.NewString(), FromUserID: a.ID, ToUserID: b.ID,
		State: RequestPending, CreatedAt: now, UpdatedAt: now,
	}
	created, err := s.CreateFriendRequest(ctx, req)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if created.ID != req.ID {
		t.Errorf("id = %q", created.ID)
	}

	// A retry returns the existing row rather than a second one, so a console
	// that thinks its request was lost does not fill the inbox.
	retry := req
	retry.ID = uuid.NewString()
	again, err := s.CreateFriendRequest(ctx, retry)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if again.ID != req.ID {
		t.Errorf("a retry created a new row: %q", again.ID)
	}

	inbox, err := s.ListFriendRequests(ctx, b.ID, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 {
		t.Errorf("B's inbox has %d entries, want 1", len(inbox))
	}
	outbox, err := s.ListFriendRequests(ctx, a.ID, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(outbox) != 1 {
		t.Errorf("A's outbox has %d entries, want 1", len(outbox))
	}
	// The inbox and outbox must not be mixed up: A has nothing incoming.
	if in, _ := s.ListFriendRequests(ctx, a.ID, true, true); len(in) != 0 {
		t.Errorf("A's inbox has %d entries, want 0", len(in))
	}

	found, err := s.FindPendingRequest(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.ID != req.ID {
		t.Errorf("find pending: id = %q", found.ID)
	}
	// The ordered pair matters: B has not asked A.
	if _, err := s.FindPendingRequest(ctx, b.ID, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the reverse direction was found: err = %v", err)
	}

	if err := s.SetFriendRequestState(ctx, req.ID, RequestAccepted, now); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// A resolved request cannot be resolved again.
	if err := s.SetFriendRequestState(ctx, req.ID, RequestDenied, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("second resolve: err = %v, want ErrNotFound", err)
	}
	// The row is kept, so the outbox can still show what happened.
	got, err := s.GetFriendRequest(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != RequestAccepted {
		t.Errorf("state = %q, want ACCEPTED", got.State)
	}
	// But it is out of the pending lists.
	if in, _ := s.ListFriendRequests(ctx, b.ID, true, true); len(in) != 0 {
		t.Errorf("B's pending inbox still has %d entries", len(in))
	}
	// And still visible when pending-only is off.
	if all, _ := s.ListFriendRequests(ctx, b.ID, true, false); len(all) != 1 {
		t.Errorf("B's full inbox has %d entries, want 1", len(all))
	}

	// A self-request is refused.
	_, err = s.CreateFriendRequest(ctx, FriendRequest{
		ID: uuid.NewString(), FromUserID: a.ID, ToUserID: a.ID,
		State: RequestPending, CreatedAt: now, UpdatedAt: now,
	})
	if err == nil {
		t.Error("a self friend request was accepted")
	}
}

func testBlocks(t *testing.T, s Store) {
	ctx := t.Context()
	a := makeUser(t, s, "Alice")
	b := makeUser(t, s, "Bob")
	now := time.Now().UTC().Truncate(time.Microsecond)

	if err := s.AddBlock(ctx, a.ID, b.ID, now); err != nil {
		t.Fatalf("add block: %v", err)
	}
	// Adding twice is idempotent.
	if err := s.AddBlock(ctx, a.ID, b.ID, now); err != nil {
		t.Fatalf("re-add block: %v", err)
	}

	blocks, err := s.ListBlocks(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 {
		t.Fatalf("A has %d blocks, want 1", len(blocks))
	}
	// A block is one-directional: only the blocker sees it.
	if bl, _ := s.ListBlocks(ctx, b.ID); len(bl) != 0 {
		t.Errorf("B sees %d blocks, want 0", len(bl))
	}

	// Requests and presence are suppressed in both directions, so both have to
	// be detectable from either side.
	for _, pair := range [][2]string{{a.ID, b.ID}, {b.ID, a.ID}} {
		blocked, err := s.IsBlockedEitherWay(ctx, pair[0], pair[1])
		if err != nil {
			t.Fatal(err)
		}
		if !blocked {
			t.Errorf("IsBlockedEitherWay(%s, %s) = false", pair[0], pair[1])
		}
	}

	if err := s.RemoveBlock(ctx, a.ID, b.ID); err != nil {
		t.Fatalf("remove block: %v", err)
	}
	if err := s.RemoveBlock(ctx, a.ID, b.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second remove: err = %v, want ErrNotFound", err)
	}
	if blocked, _ := s.IsBlockedEitherWay(ctx, a.ID, b.ID); blocked {
		t.Error("still blocked after removal")
	}

	if err := s.AddBlock(ctx, a.ID, a.ID, now); err == nil {
		t.Error("a self-block was accepted")
	}
	if err := s.AddBlock(ctx, a.ID, "ffffffffffffffff", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("blocking a missing user: err = %v, want ErrNotFound", err)
	}
}

// ---- presence ------------------------------------------------------------

func testPresence(t *testing.T, s Store) {
	ctx := t.Context()
	u := makeUser(t, s, "Alice")
	now := time.Now().UTC().Truncate(time.Microsecond)

	// A user who has never reported presence reads as OFFLINE, not ErrNotFound:
	// "never seen" and "offline" are the same thing to a friend list.
	p, err := s.GetPresence(ctx, u.ID)
	if err != nil {
		t.Fatalf("get presence: %v", err)
	}
	if p.State != StateOffline {
		t.Errorf("initial state = %q, want OFFLINE", p.State)
	}

	// So does a user that does not exist at all: a dangling friendship must not
	// fail the whole friend-list request.
	p, err = s.GetPresence(ctx, "ffffffffffffffff")
	if err != nil {
		t.Fatalf("get presence for a missing user: %v", err)
	}
	if p.State != StateOffline {
		t.Errorf("missing user state = %q, want OFFLINE", p.State)
	}

	state := StatePlaying
	appID := "0100c2500fc20000"
	appField := `{"mode":"regular"}`
	acd := 3
	extras := EmptyExtras()
	extras[ScopeFriends]["appInfo:appId"] = appID
	extras[ScopeFriends]["appField"] = appField

	updated, err := s.UpdatePresence(ctx, u.ID, PresencePatch{
		State: &state, AppID: &appID, PresenceGroupID: &appID,
		AppField: &appField, ACDIndex: &acd, Extras: &extras, ReportedAt: &now,
	})
	if err != nil {
		t.Fatalf("update presence: %v", err)
	}
	if updated.State != StatePlaying || updated.AppID != appID {
		t.Errorf("presence = %+v", updated)
	}
	if updated.AppField != appField {
		t.Errorf("appField = %q, want it verbatim", updated.AppField)
	}
	if updated.ACDIndex != acd {
		t.Errorf("acdIndex = %d, want %d", updated.ACDIndex, acd)
	}
	if updated.Extras[ScopeFriends]["appInfo:appId"] != appID {
		t.Errorf("extras did not round-trip: %+v", updated.Extras[ScopeFriends])
	}

	// A patch touches only the fields it names.
	onlyState := StateOnline
	updated, err = s.UpdatePresence(ctx, u.ID, PresencePatch{State: &onlyState})
	if err != nil {
		t.Fatal(err)
	}
	if updated.AppID != appID {
		t.Errorf("a state-only patch cleared the app id")
	}

	// Touch moves last_seen_at without touching the reported state. Conflating
	// the two is what makes a friend list either permanently offline or
	// permanently playing.
	before := updated
	later := now.Add(time.Minute)
	if err := s.TouchPresence(ctx, u.ID, later); err != nil {
		t.Fatalf("touch presence: %v", err)
	}
	after, err := s.GetPresence(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.LastSeenAt.After(before.ReportedAt) {
		t.Errorf("last_seen_at = %v, want it after the report at %v",
			after.LastSeenAt, before.ReportedAt)
	}
	if after.State != before.State {
		t.Errorf("touch changed the state from %q to %q", before.State, after.State)
	}

	// Touching with an older timestamp must not move it backwards: two requests
	// can arrive out of order.
	if err := s.TouchPresence(ctx, u.ID, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	rewound, err := s.GetPresence(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rewound.LastSeenAt.Before(after.LastSeenAt) {
		t.Errorf("last_seen_at went backwards: %v then %v", after.LastSeenAt, rewound.LastSeenAt)
	}

	// A batch read returns every requested user, OFFLINE for those with no row.
	other := makeUser(t, s, "Bob")
	batch, err := s.GetPresences(ctx, []string{u.ID, other.ID, "ffffffffffffffff"})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 3 {
		t.Fatalf("batch has %d entries, want 3", len(batch))
	}
	if batch[u.ID].State != StateOnline {
		t.Errorf("batch state for the reporting user = %q", batch[u.ID].State)
	}
	if batch["ffffffffffffffff"].State != StateOffline {
		t.Errorf("batch state for a missing user = %q, want OFFLINE",
			batch["ffffffffffffffff"].State)
	}

	logout := now.Add(2 * time.Minute)
	offline := StateOffline
	updated, err = s.UpdatePresence(ctx, u.ID, PresencePatch{State: &offline, LogoutAt: &logout})
	if err != nil {
		t.Fatal(err)
	}
	if updated.LogoutAt == nil {
		t.Fatal("logoutAt was not stored")
	}
	if !updated.LogoutAt.Equal(logout) {
		t.Errorf("logoutAt = %v, want %v", updated.LogoutAt, logout)
	}
}

// ---- images --------------------------------------------------------------

func testImages(t *testing.T, s Store) {
	ctx := t.Context()
	u := makeUser(t, s, "Alice")
	now := time.Now().UTC().Truncate(time.Microsecond)

	img := Image{
		ID: "0123456789abcdef0123456789abcdef", OwnerID: u.ID,
		Format: "jpg", Width: 256, Height: 256, SizeBytes: 1234,
		RelPath:   "0123456789abcdef0123456789abcdef.jpg",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateImage(ctx, img); err != nil {
		t.Fatalf("create image: %v", err)
	}

	got, err := s.GetImage(ctx, img.ID)
	if err != nil {
		t.Fatalf("get image: %v", err)
	}
	if got.OwnerID != u.ID || got.Width != 256 || got.RelPath != img.RelPath {
		t.Errorf("image = %+v", got)
	}

	if err := s.CreateImage(ctx, img); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate image: err = %v, want ErrConflict", err)
	}
	if _, err := s.GetImage(ctx, "ffffffffffffffffffffffffffffffff"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get missing image: err = %v, want ErrNotFound", err)
	}
}

// ---- lookups -------------------------------------------------------------

func testFindUsers(t *testing.T, s Store) {
	ctx := t.Context()
	a := makeUser(t, s, "Alice")
	b := makeUser(t, s, "Bob")
	now := time.Now().UTC().Truncate(time.Microsecond)

	if err := s.SetFriendCode(ctx, FriendCode{
		UserID: b.ID, Code: "9999-8888-7777",
		CreatedAt: now, UpdatedAt: now, RegenerableAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	found, err := s.FindUsers(ctx, UserQuery{FriendCode: "9999-8888-7777"})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].ID != b.ID {
		t.Errorf("by friend code: %+v", found)
	}

	// A code nobody owns finds nobody, rather than everybody.
	found, err = s.FindUsers(ctx, UserQuery{FriendCode: "0000-0000-0000"})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Errorf("an unknown friend code matched %d users", len(found))
	}

	// Nickname matching is case-insensitive: players do not retype exactly.
	found, err = s.FindUsers(ctx, UserQuery{Nickname: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].ID != a.ID {
		t.Errorf("by nickname: %+v", found)
	}

	found, err = s.FindUsers(ctx, UserQuery{IDs: []string{a.ID, b.ID, "ffffffffffffffff"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Errorf("by ids: got %d, want 2 (the missing one skipped)", len(found))
	}

	// The friend-list path passes an empty id set when a user has no friends. It
	// must return nothing, not everybody.
	found, err = s.FindUsers(ctx, UserQuery{IDs: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Errorf("an empty id list returned %d users", len(found))
	}

	all, err := s.ListUsers(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 2 {
		t.Errorf("ListUsers returned %d, want at least 2", len(all))
	}
	limited, err := s.ListUsers(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 {
		t.Errorf("ListUsers(1) returned %d", len(limited))
	}
}

// ---- migrations ----------------------------------------------------------

func TestMigrationsAreContiguousAndOrdered(t *testing.T) {
	migrations, err := Migrations()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("no migrations were embedded")
	}
	for i, m := range migrations {
		if m.Version != i+1 {
			t.Errorf("migration %d has version %d; versions must run 1..n with no gaps",
				i, m.Version)
		}
		if m.Up == "" {
			t.Errorf("migration %04d_%s is empty", m.Version, m.Name)
		}
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	url := os.Getenv("SWITCHNET_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SWITCHNET_TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()
	// The first call may have nothing to do if another test already migrated;
	// the second must always report nothing pending.
	store, err := OpenPostgres(ctx, url, 2, true)
	if err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	defer func() { _ = store.Close() }()

	conn, err := store.Pool().Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()

	applied, err := Migrate(ctx, conn.Conn())
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if applied != 0 {
		t.Errorf("the second migration run applied %d migrations, want 0", applied)
	}
}

// testFests exercises the Splatfest store against both implementations.
//
// The ordering assertions matter more than they look. ListFests backs the
// dashboard's fest list and the "which fest is live" lookup, and a map range in
// the in-memory store is random — so without a total order the two
// implementations disagree intermittently, which is exactly the class of bug
// this contract test already caught once for an empty id filter.
func testFests(t *testing.T, s Store) {
	ctx := t.Context()

	sept := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	oct := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)

	first := Fest{
		ID: "NXTD-00042", Resource: "nxtd-00042_data",
		Revision: "0123456789abcdef0123456789abcdef01234567",
		StartAt:  sept, Regions: []string{"JP", "EU"}, Enabled: true,
		PackKeys: []string{"000102030405060708090a0b0c0d0e0f", "0f0e0d0c0b0a09080706050403020100"},
	}
	second := Fest{
		ID: "NXTD-00043", Resource: "nxtd-00043_data",
		Revision: "89abcdef0123456789abcdef0123456789abcdef",
		StartAt:  oct, Regions: []string{"US"},
	}

	if err := s.UpsertFest(ctx, first); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.UpsertFest(ctx, second); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := s.GetFest(ctx, "NXTD-00042")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Resource != "nxtd-00042_data" || got.Revision != first.Revision {
		t.Errorf("round-tripped %+v, want resource/revision to match", got)
	}
	if !got.StartAt.UTC().Equal(sept) {
		t.Errorf("start_at = %s, want %s", got.StartAt.UTC(), sept)
	}
	if len(got.Regions) != 2 || got.Regions[0] != "JP" || got.Regions[1] != "EU" {
		t.Errorf("regions = %v, want [JP EU] in order", got.Regions)
	}
	if !got.Enabled {
		t.Error("enabled did not round-trip")
	}
	// The keys must round-trip in order: GetFestDecryptionKey returns them
	// positionally (notice, start, result, ...), so a reordering hands the
	// console the wrong key for a phase and the pack silently fails to open.
	if len(got.PackKeys) != 2 || got.PackKeys[0] != "000102030405060708090a0b0c0d0e0f" {
		t.Errorf("pack keys = %v, want them back in order", got.PackKeys)
	}

	// Soonest first.
	list, err := s.ListFests(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("listed %d fests, want 2", len(list))
	}
	if list[0].ID != "NXTD-00042" || list[1].ID != "NXTD-00043" {
		t.Errorf("list order = [%s %s], want the sooner fest first", list[0].ID, list[1].ID)
	}

	// Upsert replaces rather than duplicating, and preserves created_at.
	updated := first
	updated.Enabled = false
	updated.Revision = "ffffffffffffffffffffffffffffffffffffffff"
	if err := s.UpsertFest(ctx, updated); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	list, err = s.ListFests(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("re-upserting created a duplicate: %d fests", len(list))
	}
	again, err := s.GetFest(ctx, "NXTD-00042")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if again.Enabled {
		t.Error("enabled was not updated")
	}
	if !again.CreatedAt.Equal(got.CreatedAt) {
		t.Errorf("created_at changed on update: %s -> %s", got.CreatedAt, again.CreatedAt)
	}

	// A stored fest's regions must not alias the caller's slice.
	mutable := []string{"JP"}
	third := Fest{
		ID: "NXTD-00044", Resource: "nxtd-00044_data",
		Revision: "1111111111111111111111111111111111111111",
		StartAt:  oct.Add(24 * time.Hour), Regions: mutable,
	}
	if err := s.UpsertFest(ctx, third); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	mutable[0] = "MUTATED"
	stored, err := s.GetFest(ctx, "NXTD-00044")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.Regions[0] != "JP" {
		t.Error("mutating the caller's slice changed stored state")
	}

	if _, err := s.GetFest(ctx, "NXTD-99999"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing fest returned %v, want ErrNotFound", err)
	}

	if err := s.DeleteFest(ctx, "NXTD-00044"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetFest(ctx, "NXTD-00044"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted fest returned %v, want ErrNotFound", err)
	}
	// Idempotent: the dashboard's delete button should not error on a second click.
	if err := s.DeleteFest(ctx, "NXTD-00044"); err != nil {
		t.Errorf("deleting a missing fest errored: %v", err)
	}
}

// testFestEntries exercises the Splatfest vote store against both
// implementations.
//
// The two properties that matter are the ones a divergence would corrupt a
// result with: one row per player per fest, and votes not outliving the fest
// they belong to.
func testFestEntries(t *testing.T, s Store) {
	ctx := t.Context()

	// A real fest and real users: PostgreSQL has foreign keys to both, and the
	// in-memory store does not -- which is exactly the kind of difference this
	// contract exists to keep from mattering.
	fest := Fest{
		ID: "NXTD-00050", Resource: "nxtd-00050-fes",
		Revision: "2222222222222222222222222222222222222222",
		StartAt:  time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC),
		Regions:  []string{"EU"}, Enabled: true,
	}
	if err := s.UpsertFest(ctx, fest); err != nil {
		t.Fatalf("upsert fest: %v", err)
	}
	alice := makeUser(t, s, "alice")
	bob := makeUser(t, s, "bob")

	if err := s.UpsertFestEntry(ctx, FestEntry{
		FestID: fest.ID, UserID: alice.ID, Team: "alpha", Region: "EU",
	}); err != nil {
		t.Fatalf("upsert entry: %v", err)
	}
	if err := s.UpsertFestEntry(ctx, FestEntry{
		FestID: fest.ID, UserID: bob.ID, Team: "bravo", Region: "EU",
	}); err != nil {
		t.Fatalf("upsert entry: %v", err)
	}

	got, err := s.GetFestEntry(ctx, fest.ID, alice.ID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if got.Team != "alpha" || got.Region != "EU" {
		t.Errorf("entry = %+v, want alpha/EU", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Error("timestamps were not set")
	}

	// Changing team replaces the row. Two rows for one player would count them
	// twice in the vote, and the tally is the whole point of this table.
	if err := s.UpsertFestEntry(ctx, FestEntry{
		FestID: fest.ID, UserID: alice.ID, Team: "charlie", Region: "EU",
	}); err != nil {
		t.Fatalf("change team: %v", err)
	}
	entries, err := s.ListFestEntries(ctx, fest.ID)
	if err != nil {
		t.Fatalf("list entries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 -- changing team added a row", len(entries))
	}
	after, err := s.GetFestEntry(ctx, fest.ID, alice.ID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if after.Team != "charlie" {
		t.Errorf("team = %q, want charlie", after.Team)
	}
	if !after.CreatedAt.Equal(got.CreatedAt) {
		t.Errorf("created_at moved on update: %s -> %s", got.CreatedAt, after.CreatedAt)
	}

	if _, err := s.GetFestEntry(ctx, fest.ID, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a player who never voted returned %v, want ErrNotFound", err)
	}
	if list, err := s.ListFestEntries(ctx, "NXTD-99999"); err != nil {
		t.Errorf("listing a fest with no entries errored: %v", err)
	} else if len(list) != 0 {
		t.Errorf("a fest that does not exist has %d entries", len(list))
	}

	// Deleting the fest takes its votes with it. Without that, recreating the
	// same fest id inherits the old vote -- and PostgreSQL does it through ON
	// DELETE CASCADE while the in-memory store has to do it by hand, which is
	// precisely the divergence this contract catches.
	if err := s.DeleteFest(ctx, fest.ID); err != nil {
		t.Fatalf("delete fest: %v", err)
	}
	if list, err := s.ListFestEntries(ctx, fest.ID); err != nil {
		t.Fatalf("list entries: %v", err)
	} else if len(list) != 0 {
		t.Errorf("%d votes outlived the fest they belonged to", len(list))
	}
}

// testMatches exercises the match log against both implementations.
//
// Two properties matter. Recording the same id twice must not error, because a
// match log is not worth failing a match over. And the log must NOT be tied to
// the users in it: history that disappears when somebody leaves is not history,
// which is why players is an array rather than a join table.
func testMatches(t *testing.T, s Store) {
	ctx := t.Context()

	base := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	for i, m := range []Match{
		{ID: "m1", Mode: "turf_war", Players: []string{"alice", "bob"}, Host: "alice", CreatedAt: base},
		{ID: "m2", Mode: "turf_war", Players: []string{"bob", "carol"}, Host: "bob", CreatedAt: base.Add(time.Minute)},
		{ID: "m3", Mode: "salmon_run", Players: []string{"alice"}, Host: "alice", CreatedAt: base.Add(2 * time.Minute)},
	} {
		if err := s.RecordMatch(ctx, m); err != nil {
			t.Fatalf("record match %d: %v", i, err)
		}
	}

	// Recording the same id again is a no-op, not an error.
	if err := s.RecordMatch(ctx, Match{
		ID: "m1", Mode: "changed", Players: []string{"nobody"}, Host: "nobody",
	}); err != nil {
		t.Fatalf("re-recording a match errored: %v", err)
	}

	list, err := s.ListMatches(ctx, 10)
	if err != nil {
		t.Fatalf("list matches: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d matches, want 3", len(list))
	}
	// Newest first: the only read this table has is "what happened recently".
	if list[0].ID != "m3" || list[2].ID != "m1" {
		t.Errorf("order = %s %s %s, want m3 m2 m1", list[0].ID, list[1].ID, list[2].ID)
	}
	if list[2].Mode != "turf_war" {
		t.Errorf("re-recording overwrote the original: mode = %q", list[2].Mode)
	}
	if len(list[0].Players) != 1 || list[0].Players[0] != "alice" {
		t.Errorf("players = %v", list[0].Players)
	}

	if limited, err := s.ListMatches(ctx, 2); err != nil {
		t.Fatalf("list matches: %v", err)
	} else if len(limited) != 2 {
		t.Errorf("the limit was ignored: got %d", len(limited))
	}

	// A match with no players at all: nil and empty must mean the same thing in
	// both implementations, which is the divergence this contract keeps finding.
	if err := s.RecordMatch(ctx, Match{ID: "empty", Mode: "turf_war", Host: "alice"}); err != nil {
		t.Fatalf("a match with a nil player list was refused: %v", err)
	}

	// The stored slice must not alias the caller's.
	mutable := []string{"alice"}
	if err := s.RecordMatch(ctx, Match{
		ID: "alias", Mode: "turf_war", Players: mutable, Host: "alice",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	mutable[0] = "MUTATED"
	list, err = s.ListMatches(ctx, 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, m := range list {
		if m.ID == "alias" && m.Players[0] != "alice" {
			t.Error("mutating the caller's slice changed stored state")
		}
	}
}
