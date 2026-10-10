package db

import (
	"context"
	"errors"
	"os"
	"strings"
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
		`TRUNCATE users, devices, images, npln_fests, npln_coop_shifts, npln_matches,
		          scsi_save_data_archives RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// runStoreContract exercises one Store implementation.
func runStoreContract(t *testing.T, open func(*testing.T) Store) {
	tests := map[string]func(*testing.T, Store){
		"users":                     testUsers,
		"deviceAccounts":            testDeviceAccounts,
		"reassignDeviceAccount":     testReassignDeviceAccount,
		"revokeDeviceAccountTokens": testRevokeDeviceAccountTokens,
		"undeleteDeviceAccount":     testUndeleteDeviceAccount,
		"friendCodes":               testFriendCodes,
		"friendships":               testFriendships,
		"naRefreshTokens":           testNARefreshTokens,
		"ugcDocuments":              testUGCDocuments,
		"nexRecords":                testNEXRecords,
		"friendRequests":            testFriendRequests,
		"blocks":                    testBlocks,
		"presence":                  testPresence,
		"images":                    testImages,
		"findUsers":                 testFindUsers,
		"userLogin":                 testUserLogin,
		"fests":                     testFests,
		"festEntries":               testFestEntries,
		"festBattles":               testFestBattles,
		"penne":                     testPenne,
		"coopShifts":                testCoopShifts,
		"matches":                   testMatches,
		"ugcCompareAndPut":          testUGCCompareAndPut,
		"saveDataArchives":          testSaveDataArchives,
		"componentFiles":            testComponentFiles,
		"ping":                      testPing,
		"stats":                     testStats,
		"provisionalUsers":          testProvisionalUsers,
		"discordUsers":              testDiscordUsers,
	}
	for name, fn := range tests {
		t.Run(name, func(t *testing.T) {
			fn(t, open(t))
		})
	}
}

func testUGCCompareAndPut(t *testing.T, store Store) {
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	prefix := uuid.NewString()
	a := UGCDocument{Name: prefix + "a", Owner: "alice", Fields: []byte{1}, CreateTime: now, UpdateTime: now}
	marker := UGCDocument{Name: prefix + "marker", Owner: "alice", Fields: []byte{2}, CreateTime: now, UpdateTime: now}
	if err := store.CompareAndPutUGCDocuments(ctx, []UGCDocumentCAS{{Document: a}, {Document: marker}}); err != nil {
		t.Fatal(err)
	}
	updated := a
	updated.Fields = []byte{3}
	updated.UpdateTime = now.Add(time.Second)
	// A marker conflict must roll back an earlier attribute update in the batch.
	if err := store.CompareAndPutUGCDocuments(ctx, []UGCDocumentCAS{{Document: updated, PreviousUpdateTime: &now}, {Document: marker}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict=%v", err)
	}
	got, err := store.GetUGCDocument(ctx, a.Name)
	if err != nil || string(got.Fields) != string(a.Fields) {
		t.Fatalf("partial credit committed: %+v %v", got, err)
	}
	if err := store.CompareAndPutUGCDocuments(ctx, []UGCDocumentCAS{{Document: updated, PreviousUpdateTime: &now}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompareAndPutUGCDocuments(ctx, []UGCDocumentCAS{{Document: a, PreviousUpdateTime: &now}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision accepted: %v", err)
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
		ID:                  id,
		Nickname:            nickname,
		Birthday:            "0000-00-00",
		Permissions:         DefaultPermissions(now),
		Extras:              EmptyExtras(),
		ThumbnailUploadedAt: &now,
		BlocksUpdatedAt:     now,
		FriendsUpdatedAt:    now,
		CreatedAt:           now,
		UpdatedAt:           now,
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
	// Caught a real bug once: Postgres's CreateUser/CreateUserWithLogin
	// INSERTs never listed this column, so the Go-side value was silently
	// dropped -- the in-memory store never had this bug (it stores the
	// struct directly, no column list to miss), so this only shows up
	// against a real database.
	if got.ThumbnailUploadedAt == nil {
		t.Error("thumbnailUploadedAt did not round-trip through CreateUser")
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

	// Deleted is a patchable field like any other, and FindUsers/ListUsers
	// must stop returning the user the moment it is set -- both
	// implementations filter it themselves rather than the caller doing so,
	// per Postgres's `NOT u.deleted` and Memory's equivalent skip.
	deleted := true
	updated, err = s.UpdateUser(ctx, u.ID, UserPatch{Deleted: &deleted})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Deleted {
		t.Error("Deleted did not round-trip")
	}
	if found, err := s.FindUsers(ctx, UserQuery{IDs: []string{u.ID}}); err != nil {
		t.Fatal(err)
	} else if len(found) != 0 {
		t.Errorf("FindUsers returned a deleted user: %+v", found)
	}

	notDeleted := false
	updated, err = s.UpdateUser(ctx, u.ID, UserPatch{Deleted: &notDeleted})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Deleted {
		t.Error("Deleted did not clear")
	}
	if found, err := s.FindUsers(ctx, UserQuery{IDs: []string{u.ID}}); err != nil {
		t.Fatal(err)
	} else if len(found) != 1 {
		t.Errorf("FindUsers did not return the restored user: %+v", found)
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
		ID:                  hexID(seq),
		Nickname:            "Bob",
		Birthday:            "0000-00-00",
		Permissions:         DefaultPermissions(now),
		Extras:              EmptyExtras(),
		ThumbnailUploadedAt: &now,
		BlocksUpdatedAt:     now,
		FriendsUpdatedAt:    now,
		CreatedAt:           now,
		UpdatedAt:           now,
		LoginUsername:       "bob",
		LoginPasswordHash:   "$2a$10$notarealhash",
	}
	if err := s.CreateUserWithLogin(ctx, u, true); err != nil {
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
	if got.ThumbnailUploadedAt == nil {
		t.Error("thumbnailUploadedAt did not round-trip through CreateUserWithLogin")
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

	// CreateDeviceAccountForUser is what gives a website-registered person
	// one, on demand -- baas/accounts.go's deviceAccountForLogin, for a
	// username/password login with no console-issued credential to present.
	seq++
	da := DeviceAccount{
		ID:             hexID(seq),
		UserID:         u.ID,
		PasswordSHA256: "0000000000000000000000000000000000000000000000000000000000000000",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.CreateDeviceAccountForUser(ctx, da); err != nil {
		t.Fatalf("create device account for user: %v", err)
	}

	gotDA, err := s.GetDeviceAccount(ctx, da.ID)
	if err != nil {
		t.Fatalf("get device account: %v", err)
	}
	if gotDA.UserID != u.ID {
		t.Errorf("device account user = %s, want %s", gotDA.UserID, u.ID)
	}

	das, err = s.ListDeviceAccounts(ctx, u.ID)
	if err != nil {
		t.Fatalf("list device accounts after create: %v", err)
	}
	if len(das) != 1 || das[0].ID != da.ID {
		t.Errorf("got device accounts %+v, want exactly [%s]", das, da.ID)
	}

	if err := s.CreateDeviceAccountForUser(ctx, da); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate device account id: err = %v, want ErrConflict", err)
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
	if err := s.CreateUserWithLogin(ctx, dup, true); !errors.Is(err, ErrConflict) {
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

	// ListAllDeviceAccounts still shows it -- the Debug tab's undelete button
	// has nothing to act on otherwise.
	all, err := s.ListAllDeviceAccounts(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].DeletedAt == nil {
		t.Errorf("ListAllDeviceAccounts = %+v, want the one deleted device account", all)
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

func testRevokeDeviceAccountTokens(t *testing.T, s Store) {
	ctx := t.Context()
	u := makeUser(t, s, "Revocable")
	das, err := s.ListDeviceAccounts(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	da := das[0]

	if da.TokensRevokedBefore != nil {
		t.Fatalf("a fresh device account already has TokensRevokedBefore set: %v", da.TokensRevokedBefore)
	}

	cutoff := time.Now().UTC().Truncate(time.Microsecond)
	if err := s.RevokeDeviceAccountTokens(ctx, da.ID, cutoff); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	got, err := s.GetDeviceAccount(ctx, da.ID)
	if err != nil {
		t.Fatalf("get device account: %v", err)
	}
	if got.TokensRevokedBefore == nil || !got.TokensRevokedBefore.Equal(cutoff) {
		t.Errorf("tokens_revoked_before = %v, want it to equal %v exactly", got.TokensRevokedBefore, cutoff)
	}
	// Everything else about the account is untouched.
	if got.PasswordSHA256 != da.PasswordSHA256 || got.UserID != da.UserID {
		t.Error("revoking tokens changed the device account's password or owner")
	}

	// Revoking again with a later cutoff moves it forward -- an operator
	// noticing a second leak should not have to remember the first cutoff.
	later := cutoff.Add(time.Hour)
	if err := s.RevokeDeviceAccountTokens(ctx, da.ID, later); err != nil {
		t.Fatalf("revoke again: %v", err)
	}
	got, err = s.GetDeviceAccount(ctx, da.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TokensRevokedBefore == nil || !got.TokensRevokedBefore.Equal(later) {
		t.Errorf("tokens_revoked_before after a second revoke = %v, want %v", got.TokensRevokedBefore, later)
	}

	if err := s.RevokeDeviceAccountTokens(ctx, "ffffffffffffffff", cutoff); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoke a missing device account: err = %v, want ErrNotFound", err)
	}
}

func testUndeleteDeviceAccount(t *testing.T, s Store) {
	ctx := t.Context()
	u := makeUser(t, s, "Reinstatable")
	das, err := s.ListDeviceAccounts(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	da := das[0]

	// Undeleting one that was never deleted is refused -- there is nothing to
	// reinstate, and silently succeeding would hide a caller's mistake about
	// which id it meant.
	if err := s.UndeleteDeviceAccount(ctx, da.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("undelete a never-deleted device account: err = %v, want ErrNotFound", err)
	}

	if err := s.DeleteDeviceAccount(ctx, u.ID, da.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	deleted, err := s.GetDeviceAccount(ctx, da.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.DeletedAt == nil {
		t.Fatal("device account was not marked deleted")
	}

	if err := s.UndeleteDeviceAccount(ctx, da.ID); err != nil {
		t.Fatalf("undelete: %v", err)
	}
	restored, err := s.GetDeviceAccount(ctx, da.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.DeletedAt != nil {
		t.Errorf("device account still marked deleted after undelete: %v", restored.DeletedAt)
	}
	// Nothing else about the account moved.
	if restored.PasswordSHA256 != da.PasswordSHA256 || restored.UserID != da.UserID {
		t.Error("undelete changed the device account's password or owner")
	}

	if err := s.UndeleteDeviceAccount(ctx, "ffffffffffffffff"); !errors.Is(err, ErrNotFound) {
		t.Errorf("undelete a missing device account: err = %v, want ErrNotFound", err)
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

	// Friendship extras: per direction, round-trip, and no row, no write.
	if err := s.AddFriendship(ctx, a.ID, b.ID, now); err != nil {
		t.Fatalf("re-add friendship: %v", err)
	}
	if err := s.SetFriendshipExtras(ctx, a.ID, b.ID, ExtrasByScope{"self": {"isConfirmed": true}}); err != nil {
		t.Fatalf("set friendship extras: %v", err)
	}
	f, err = s.GetFriendship(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if f.Extras["self"]["isConfirmed"] != true {
		t.Errorf("friendship extras = %v, want self.isConfirmed true", f.Extras)
	}
	listed, err := s.ListFriends(ctx, a.ID, 0)
	if err != nil || len(listed) != 1 || listed[0].Extras["self"]["isConfirmed"] != true {
		t.Errorf("ListFriends did not carry the extras: %+v, %v", listed, err)
	}
	reverse, err = s.GetFriendship(ctx, b.ID, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reverse.Extras["self"]) != 0 {
		t.Errorf("friendship extras leaked into the other direction: %v", reverse.Extras)
	}
	if err := s.SetFriendshipExtras(ctx, a.ID, "0000000000000000", ExtrasByScope{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("extras on a non-friend: err = %v, want ErrNotFound", err)
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

	// IsBlocking is the one-directional half: only the blocker it is asked
	// about is blocking. A response describing the caller's own action cannot
	// use IsBlockedEitherWay without claiming the caller did something they
	// did not.
	if err := s.AddBlock(ctx, b.ID, a.ID, now); err != nil {
		t.Fatalf("add reverse block: %v", err)
	}
	for _, c := range []struct {
		blocker, blocked string
		want             bool
	}{
		{a.ID, b.ID, true},
		{b.ID, a.ID, true},
		{a.ID, a.ID, false},
	} {
		got, err := s.IsBlocking(ctx, c.blocker, c.blocked)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("IsBlocking(%s, %s) = %v, want %v", c.blocker, c.blocked, got, c.want)
		}
	}
	if err := s.RemoveBlock(ctx, b.ID, a.ID); err != nil {
		t.Fatalf("remove reverse block: %v", err)
	}
	// And with only B's block gone, A's is still the only one that shows.
	if got, _ := s.IsBlocking(ctx, b.ID, a.ID); got {
		t.Error("B still blocks A after the reverse block was removed")
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

	// LastSeenAt tracks the report's own timestamp, not the moment the write
	// happened -- a patch carrying a ReportedAt far from wall-clock time must
	// come back exactly, not merely "close to now". A test that only ever
	// passes a ReportedAt captured moments before the call cannot tell "value
	// from the patch" from "value from the database's own clock", which is
	// exactly the gap that let the two Store implementations disagree here
	// (Postgres used to always write now()) without either failing.
	reportedInThePast := now.Add(-30 * 24 * time.Hour)
	backdated, err := s.UpdatePresence(ctx, u.ID, PresencePatch{
		State: &state, ReportedAt: &reportedInThePast,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !backdated.LastSeenAt.Equal(reportedInThePast) {
		t.Errorf("last_seen_at = %v, want it to equal the reported timestamp %v exactly",
			backdated.LastSeenAt, reportedInThePast)
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

func testSaveDataArchives(t *testing.T, s Store) {
	ctx := t.Context()
	u := makeUser(t, s, "Alice")
	now := time.Now().UTC().Truncate(time.Microsecond)

	a := SaveDataArchive{
		ID: "aa00000000000001", NSAID: u.ID, ApplicationID: "0100abcd00010000",
		DeviceID: "dev0000000000001", DataSize: 0, Status: "uploading",
		// A real value observed on real hardware 2026-09-24
		// (12237957226414712540, which by itself exceeds int64's own
		// maximum -- SeriesID is stored as its exact decimal digit string
		// for exactly this reason, not any fixed-width integer type;
		// migrations 0019/0020 record the two overflows found on the way
		// to that).
		SeriesID: "12237957226414712540",
		SavedAt:  now,
	}
	created, err := s.CreateSaveDataArchive(ctx, a)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Status != "uploading" {
		t.Errorf("status = %q, want uploading (default)", created.Status)
	}
	if created.SeriesID != a.SeriesID {
		t.Errorf("SeriesID = %q, want %q", created.SeriesID, a.SeriesID)
	}

	if _, err := s.CreateSaveDataArchive(ctx, a); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate id: err = %v, want ErrConflict", err)
	}

	got, err := s.GetSaveDataArchive(ctx, a.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ApplicationID != a.ApplicationID || got.DeviceID != a.DeviceID {
		t.Errorf("archive = %+v", got)
	}

	if _, err := s.GetSaveDataArchive(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get missing: err = %v, want ErrNotFound", err)
	}

	// A second archive for a different title must not show up in a
	// title-filtered listing.
	other := a
	other.ID = "aa00000000000002"
	other.ApplicationID = "0100abcd00020000"
	other.SavedAt = now.Add(time.Minute)
	if _, err := s.CreateSaveDataArchive(ctx, other); err != nil {
		t.Fatalf("create other: %v", err)
	}

	list, err := s.ListSaveDataArchives(ctx, SaveDataArchiveQuery{NSAID: u.ID, ApplicationID: a.ApplicationID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != a.ID {
		t.Errorf("filtered list = %+v, want just %s", list, a.ID)
	}

	all, err := s.ListSaveDataArchives(ctx, SaveDataArchiveQuery{NSAID: u.ID})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	// Most recently saved first.
	if len(all) != 2 || all[0].ID != other.ID || all[1].ID != a.ID {
		t.Errorf("unfiltered list order = %+v", all)
	}

	digest := "ZGlnZXN0"
	finishedAt := now.Add(time.Second)
	updated, err := s.UpdateSaveDataArchive(ctx, a.ID, SaveDataArchivePatch{
		Status: strPtr("fixed"), EncodedDigest: &digest, FinishedAt: &finishedAt,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Status != "fixed" || updated.EncodedDigest != digest || updated.FinishedAt == nil {
		t.Errorf("updated = %+v", updated)
	}

	if err := s.DeleteSaveDataArchive(ctx, a.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetSaveDataArchive(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete: err = %v, want ErrNotFound", err)
	}
	if err := s.DeleteSaveDataArchive(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete missing: err = %v, want ErrNotFound", err)
	}
}

func testComponentFiles(t *testing.T, s Store) {
	ctx := t.Context()
	u := makeUser(t, s, "Alice")
	now := time.Now().UTC().Truncate(time.Microsecond)

	archive, err := s.CreateSaveDataArchive(ctx, SaveDataArchive{
		ID: "cc00000000000001", NSAID: u.ID, ApplicationID: "0100abcd00010000",
		DeviceID: "dev0000000000001", SavedAt: now,
	})
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}

	meta := ComponentFile{ID: "cf00000000000001", ArchiveID: archive.ID, Index: 0, Datatype: "meta", BlobKey: "blob-meta"}
	save := ComponentFile{ID: "cf00000000000002", ArchiveID: archive.ID, Index: 1, Datatype: "save", BlobKey: "blob-save"}
	if _, err := s.CreateComponentFile(ctx, meta); err != nil {
		t.Fatalf("create meta: %v", err)
	}
	if _, err := s.CreateComponentFile(ctx, save); err != nil {
		t.Fatalf("create save: %v", err)
	}
	if _, err := s.CreateComponentFile(ctx, meta); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate id: err = %v, want ErrConflict", err)
	}

	got, err := s.GetComponentFile(ctx, save.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Datatype != "save" || got.BlobKey != "blob-save" {
		t.Errorf("component file = %+v", got)
	}
	if _, err := s.GetComponentFile(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get missing: err = %v, want ErrNotFound", err)
	}

	list, err := s.ListComponentFiles(ctx, archive.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 || list[0].Index != 0 || list[1].Index != 1 {
		t.Errorf("list order = %+v", list)
	}

	size := int64(4096)
	digest := "ZmlsZWRpZ2VzdA"
	updated, err := s.UpdateComponentFile(ctx, save.ID, ComponentFilePatch{
		Status: strPtr("fixed"), ArchiveSize: &size, EncodedArchiveDigest: &digest,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Status != "fixed" || updated.ArchiveSize != size || updated.EncodedArchiveDigest != digest {
		t.Errorf("updated = %+v", updated)
	}
	if _, err := s.UpdateComponentFile(ctx, "missing", ComponentFilePatch{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update missing: err = %v, want ErrNotFound", err)
	}

	// Deleting the parent archive takes its component files with it.
	if err := s.DeleteSaveDataArchive(ctx, archive.ID); err != nil {
		t.Fatalf("delete archive: %v", err)
	}
	if _, err := s.GetComponentFile(ctx, save.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get component after archive delete: err = %v, want ErrNotFound", err)
	}
}

func strPtr(s string) *string { return &s }

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

func testCoopShifts(t *testing.T, s Store) {
	ctx := t.Context()

	sept := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	oct := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)

	first := CoopShift{
		ID: "normal-1", Kind: "normal", StageID: 1, Boss: "SakeJaw",
		Weapons:      []int64{20000, 20010, 20020, 20030},
		KumaWeaponID: 20900, RewardGearID: 0,
		StartAt: sept, EndAt: sept.Add(8 * time.Hour),
	}
	second := CoopShift{
		ID: "big_run-1", Kind: "big_run", StageID: 100, Boss: "SakeRope",
		Weapons:      []int64{21000, 21010, 21020, 21030},
		KumaWeaponID: 21900, RewardGearID: 7,
		Rewards:       []int64{100, 200, 300},
		RewardBorders: []int64{160, 140, 110}, IsSpecial: true,
		StartAt: oct, EndAt: oct.Add(48 * time.Hour),
	}

	if err := s.UpsertCoopShift(ctx, first); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.UpsertCoopShift(ctx, second); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := s.GetCoopShift(ctx, "normal-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.StageID != 1 || got.Boss != "SakeJaw" {
		t.Errorf("round-tripped %+v, want stage/boss to match", got)
	}
	if !got.StartAt.UTC().Equal(sept) {
		t.Errorf("start_at = %s, want %s", got.StartAt.UTC(), sept)
	}
	if len(got.Weapons) != 4 || got.Weapons[0] != 20000 {
		// Positional order matters: main_weapons is sent to the console in
		// this order, so a reordering here would be silently wrong on the
		// wire even though nothing about the store contract would catch it
		// as a missing value.
		t.Errorf("weapons = %v, want [20000 20010 20020 20030] in order", got.Weapons)
	}

	gotBigRun, err := s.GetCoopShift(ctx, "big_run-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(gotBigRun.Rewards) != 3 || gotBigRun.Rewards[0] != 100 {
		t.Errorf("rewards = %v, want [100 200 300] in order", gotBigRun.Rewards)
	}
	if len(gotBigRun.RewardBorders) != 3 || gotBigRun.RewardBorders[0] != 160 || !gotBigRun.IsSpecial {
		t.Errorf("borders = %v special = %v, want [160 140 110] true", gotBigRun.RewardBorders, gotBigRun.IsSpecial)
	}

	// Soonest first.
	list, err := s.ListCoopShifts(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("listed %d coop shifts, want 2", len(list))
	}
	if list[0].ID != "normal-1" || list[1].ID != "big_run-1" {
		t.Errorf("list order = [%s %s], want the sooner shift first", list[0].ID, list[1].ID)
	}

	// Upsert replaces rather than duplicating, and preserves created_at.
	updated := first
	updated.Boss = "SakelienGiant"
	if err := s.UpsertCoopShift(ctx, updated); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	list, err = s.ListCoopShifts(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("re-upserting created a duplicate: %d coop shifts", len(list))
	}
	again, err := s.GetCoopShift(ctx, "normal-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if again.Boss != "SakelienGiant" {
		t.Error("boss was not updated")
	}
	if !again.CreatedAt.Equal(got.CreatedAt) {
		t.Errorf("created_at changed on update: %s -> %s", got.CreatedAt, again.CreatedAt)
	}

	// A stored shift's weapons must not alias the caller's slice.
	mutable := []int64{22000, 22010, 22030, 22050}
	third := CoopShift{
		ID: "normal-2", Kind: "normal", StageID: 2, Weapons: mutable,
		KumaWeaponID: 22900,
		StartAt:      oct.Add(24 * time.Hour), EndAt: oct.Add(32 * time.Hour),
	}
	if err := s.UpsertCoopShift(ctx, third); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	mutable[0] = 99999
	stored, err := s.GetCoopShift(ctx, "normal-2")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.Weapons[0] != 22000 {
		t.Error("mutating the caller's slice changed stored state")
	}

	if _, err := s.GetCoopShift(ctx, "normal-99999"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing coop shift returned %v, want ErrNotFound", err)
	}

	if err := s.DeleteCoopShift(ctx, "normal-2"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetCoopShift(ctx, "normal-2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted coop shift returned %v, want ErrNotFound", err)
	}
	// Idempotent, the same as DeleteFest.
	if err := s.DeleteCoopShift(ctx, "normal-2"); err != nil {
		t.Errorf("deleting a missing coop shift errored: %v", err)
	}
}

// testFestEntries exercises the Splatfest vote store against both
// implementations.
//
// The two properties that matter are the ones a divergence would corrupt a
// result with: one row per player per fest, and votes not outliving the fest
// they belong to.
func testFestBattles(t *testing.T, s Store) {
	ctx := t.Context()
	fest := Fest{ID: "NXTD-00051", Resource: "nxtd-00051-fes",
		Revision: "2222222222222222222222222222222222222222",
		StartAt:  time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC),
		Regions:  []string{"EU"}, Enabled: true}
	if err := s.UpsertFest(ctx, fest); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 10, 3, 5, 0, 0, 0, time.UTC)
	b := FestBattle{FestID: fest.ID, BattleID: "match-1", Mode: "fest_regular",
		WinningTeam: "Alpha", Ink: map[string]int64{"Alpha": 1410}, FinishedAt: when}
	for range 2 {
		if err := s.RecordFestBattle(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListFestBattles(ctx, fest.ID)
	if err != nil || len(got) != 1 || got[0].Ink["Alpha"] != 1410 {
		t.Fatalf("idempotent battle record: %v, %v", got, err)
	}
	got[0].Ink["Alpha"] = 0
	again, err := s.ListFestBattles(ctx, fest.ID)
	if err != nil || again[0].Ink["Alpha"] != 1410 {
		t.Fatalf("battle read must not mutate stored ink: %v, %v", again, err)
	}
	// Each console reports only its own result: the battle's ink becomes the
	// per-team sum of its players, and a repeated report counts once.
	players := []FestBattlePlayer{
		{FestID: fest.ID, BattleID: "match-1", UserID: "u1", Team: "Alpha", Paint: 1410},
		{FestID: fest.ID, BattleID: "match-1", UserID: "u2", Team: "Bravo", Paint: 900},
		{FestID: fest.ID, BattleID: "match-1", UserID: "u3", Team: "Bravo", Paint: 100},
		{FestID: fest.ID, BattleID: "match-1", UserID: "u2", Team: "Bravo", Paint: 900},
	}
	for _, p := range players {
		if err := s.RecordFestBattlePlayer(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	summed, err := s.ListFestBattles(ctx, fest.ID)
	if err != nil || len(summed) != 1 || summed[0].Ink["Alpha"] != 1410 || summed[0].Ink["Bravo"] != 1000 {
		t.Fatalf("battle ink from players: %v, %v", summed, err)
	}
	if err := s.DeleteFest(ctx, fest.ID); err != nil {
		t.Fatal(err)
	}
	after, err := s.ListFestBattles(ctx, fest.ID)
	if err != nil || len(after) != 0 {
		t.Fatalf("deleted fest retained battles: %v, %v", after, err)
	}
}

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

func testMatches(t *testing.T, s Store) {
	ctx := t.Context()

	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	older := Match{
		ID: "session-older", Tenant: "dce9377b", Mode: "turf_war",
		HostUserID: "aaaaaaaaaaaaaaaa",
		PlayerIDs:  []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"},
		CreatedAt:  now.Add(-time.Hour),
	}
	newer := Match{
		ID: "session-newer", Tenant: "dce9377b", Mode: "salmon_run",
		HostUserID: "cccccccccccccccc",
		PlayerIDs:  []string{"cccccccccccccccc", "dddddddddddddddd", "eeeeeeeeeeeeeeee"},
		Forced:     true,
		CreatedAt:  now,
	}

	if err := s.CreateMatch(ctx, older); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.CreateMatch(ctx, newer); err != nil {
		t.Fatalf("create: %v", err)
	}

	list, err := s.ListMatches(ctx, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d matches, want 2", len(list))
	}
	// Newest first.
	if list[0].ID != "session-newer" || list[1].ID != "session-older" {
		t.Errorf("order = [%s %s], want the newer match first", list[0].ID, list[1].ID)
	}
	if !list[0].Forced {
		t.Error("forced did not round-trip")
	}
	if len(list[0].PlayerIDs) != 3 || list[0].PlayerIDs[0] != "cccccccccccccccc" {
		t.Errorf("player_ids = %v, want them back in order", list[0].PlayerIDs)
	}
	if list[1].HostUserID != "aaaaaaaaaaaaaaaa" {
		t.Errorf("host_user_id = %q, want aaaaaaaaaaaaaaaa", list[1].HostUserID)
	}

	// limit is honoured.
	limited, err := s.ListMatches(ctx, 1)
	if err != nil {
		t.Fatalf("list limited: %v", err)
	}
	if len(limited) != 1 || limited[0].ID != "session-newer" {
		t.Errorf("limited list = %+v, want just the newer match", limited)
	}

	// Re-recording the same id is not an error and does not duplicate the
	// row: a match is written once, at formation, and a retried write after a
	// network blip must not fail the caller or grow the history.
	if err := s.CreateMatch(ctx, older); err != nil {
		t.Fatalf("re-create: %v", err)
	}
	list, err = s.ListMatches(ctx, 10)
	if err != nil {
		t.Fatalf("list after re-create: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("re-recording an existing match id created a duplicate: %d matches", len(list))
	}

	// Per tenant: another game's match is in the full list and in its own,
	// and in neither of the other game's.
	s2 := Match{ID: "s2-1", Tenant: "splatoon2", Mode: "mode 12", HostUserID: "aaaaaaaaaaaaaaaa",
		PlayerIDs: []string{"aaaaaaaaaaaaaaaa"}, CreatedAt: now.Add(time.Minute)}
	if err := s.CreateMatch(ctx, s2); err != nil {
		t.Fatalf("create s2: %v", err)
	}
	if all, _ := s.ListMatches(ctx, 10); len(all) != 3 {
		t.Errorf("full list has %d matches, want 3", len(all))
	}
	s3only, err := s.ListMatchesForTenant(ctx, "dce9377b", 10)
	if err != nil || len(s3only) != 2 || s3only[0].ID != "session-newer" {
		t.Errorf("dce9377b's matches = %+v, %v; want its two, newest first", s3only, err)
	}
	s2only, err := s.ListMatchesForTenant(ctx, "splatoon2", 1)
	if err != nil || len(s2only) != 1 || s2only[0].ID != "s2-1" {
		t.Errorf("splatoon2's matches = %+v, %v; want just s2-1", s2only, err)
	}
	if none, _ := s.ListMatchesForTenant(ctx, "nobody", 10); len(none) != 0 {
		t.Errorf("an unknown tenant matched %d matches", len(none))
	}
}

// testPing is in the contract because health checks read it, and a Ping that
// answers on one implementation and not the other would make a service look
// degraded only in the deployment that matters.
func testPing(t *testing.T, s Store) {
	if err := s.Ping(context.Background()); err != nil {
		t.Errorf("Ping on a live store: %v", err)
	}
}

// testStats guards against Stats drifting from what the other contract tests
// already establish -- a friendship written as two rows (testFriendships),
// deleted counted alongside total rather than instead of it (testUsers).
func testStats(t *testing.T, s Store) {
	ctx := t.Context()

	before, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}

	a := makeUser(t, s, "Alice")
	b := makeUser(t, s, "Bob")
	if err := s.AddFriendship(ctx, a.ID, b.ID, time.Now()); err != nil {
		t.Fatalf("add friendship: %v", err)
	}
	deleted := true
	if _, err := s.UpdateUser(ctx, b.ID, UserPatch{Deleted: &deleted}); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	got, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if got.Users != before.Users+2 {
		t.Errorf("users = %d, want %d", got.Users, before.Users+2)
	}
	if got.DeletedUsers != before.DeletedUsers+1 {
		t.Errorf("deletedUsers = %d, want %d", got.DeletedUsers, before.DeletedUsers+1)
	}
	// makeUser creates one device account per user.
	if got.DeviceAccounts != before.DeviceAccounts+2 {
		t.Errorf("deviceAccounts = %d, want %d", got.DeviceAccounts, before.DeviceAccounts+2)
	}
	// AddFriendship writes both directions.
	if got.Friendships != before.Friendships+2 {
		t.Errorf("friendships = %d, want %d", got.Friendships, before.Friendships+2)
	}
}

func testNARefreshTokens(t *testing.T, s Store) {
	ctx := t.Context()
	a := makeUser(t, s, "Alice")
	now := time.Now().UTC().Truncate(time.Microsecond)

	tok := NARefreshToken{
		TokenSHA256: "aa" + strings.Repeat("0", 62),
		UserID:      a.ID,
		Scope:       []string{"openid", "user"},
		ExpiresAt:   now.Add(time.Hour),
		CreatedAt:   now,
	}
	if err := s.CreateNARefreshToken(ctx, tok); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetNARefreshToken(ctx, tok.TokenSHA256, now)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.UserID != a.ID || len(got.Scope) != 2 || got.Scope[0] != "openid" || got.Scope[1] != "user" {
		t.Errorf("got %+v, want Alice with [openid user]", got)
	}
	if !got.ExpiresAt.Equal(tok.ExpiresAt) {
		t.Errorf("expires_at = %v, want %v", got.ExpiresAt, tok.ExpiresAt)
	}

	// Expired at exactly its expiry, and unknown hashes, are both not found.
	if _, err := s.GetNARefreshToken(ctx, tok.TokenSHA256, tok.ExpiresAt); !errors.Is(err, ErrNotFound) {
		t.Errorf("get at expiry: err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetNARefreshToken(ctx, "bb"+strings.Repeat("0", 62), now); !errors.Is(err, ErrNotFound) {
		t.Errorf("get unknown: err = %v, want ErrNotFound", err)
	}

	// A token for a user that does not exist is refused.
	orphan := tok
	orphan.TokenSHA256 = "cc" + strings.Repeat("0", 62)
	orphan.UserID = "0000000000000000"
	if err := s.CreateNARefreshToken(ctx, orphan); !errors.Is(err, ErrNotFound) {
		t.Errorf("create for unknown user: err = %v, want ErrNotFound", err)
	}

	// No scope reads back as empty, not as an error.
	bare := tok
	bare.TokenSHA256 = "dd" + strings.Repeat("0", 62)
	bare.Scope = nil
	if err := s.CreateNARefreshToken(ctx, bare); err != nil {
		t.Fatalf("create without scope: %v", err)
	}
	if got, err := s.GetNARefreshToken(ctx, bare.TokenSHA256, now); err != nil || len(got.Scope) != 0 {
		t.Errorf("scope-less token: %+v, %v", got, err)
	}
}

func testUGCDocuments(t *testing.T, s Store) {
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	name := "tenants/t-x-lp1/documents/services/GameRecord/users/u1/VsUserAttribute/Data"

	if _, err := s.GetUGCDocument(ctx, name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get before put: err = %v, want ErrNotFound", err)
	}
	d := UGCDocument{Name: name, Owner: "u1", Fields: []byte{0x0a, 0x01}, CreateTime: now, UpdateTime: now}
	if err := s.PutUGCDocument(ctx, d); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := s.GetUGCDocument(ctx, name)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Owner != "u1" || string(got.Fields) != string(d.Fields) ||
		!got.CreateTime.Equal(now) || !got.UpdateTime.Equal(now) {
		t.Errorf("got %+v, want %+v", got, d)
	}

	// Replacing keeps one row and the new content.
	later := now.Add(time.Minute)
	d.Fields, d.UpdateTime = []byte{0x0a, 0x02, 0x03}, later
	if err := s.PutUGCDocument(ctx, d); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got, _ := s.GetUGCDocument(ctx, name); string(got.Fields) != string(d.Fields) || !got.UpdateTime.Equal(later) {
		t.Errorf("after replace: %+v", got)
	}

	// An empty field set is a document, not an absence.
	empty := UGCDocument{Name: name + "2", Owner: "u1", CreateTime: now, UpdateTime: now}
	if err := s.PutUGCDocument(ctx, empty); err != nil {
		t.Fatalf("put empty: %v", err)
	}
	if got, err := s.GetUGCDocument(ctx, empty.Name); err != nil || len(got.Fields) != 0 {
		t.Errorf("empty document: %+v, %v", got, err)
	}

	// Listing is by name prefix, newest name first, and a prefix character
	// that is a LIKE wildcard must not match anything else.
	for _, n := range []string{"t/a_x/1", "t/a_x/2", "t/abx/3"} {
		if err := s.PutUGCDocument(ctx, UGCDocument{Name: n, Owner: "u1", CreateTime: now, UpdateTime: now}); err != nil {
			t.Fatalf("put %s: %v", n, err)
		}
	}
	listed, err := s.ListUGCDocuments(ctx, "t/a_x/")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 2 || listed[0].Name != "t/a_x/2" || listed[1].Name != "t/a_x/1" {
		t.Errorf("list t/a_x/ = %+v, want t/a_x/2 then t/a_x/1 only", listed)
	}

	if err := s.DeleteUGCDocument(ctx, name); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetUGCDocument(ctx, name); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete: err = %v, want ErrNotFound", err)
	}
	if err := s.DeleteUGCDocument(ctx, name); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: err = %v, want ErrNotFound", err)
	}
}

func testNEXRecords(t *testing.T, s Store) {
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)

	if _, err := s.GetNEXRecord(ctx, "splatoon2", "fest_vote", "1:a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get before put: err = %v, want ErrNotFound", err)
	}
	put := func(kind, key, owner, value string, at time.Time) {
		t.Helper()
		if err := s.PutNEXRecord(ctx, NEXRecord{Title: "splatoon2", Kind: kind, Key: key, Owner: owner,
			Value: []byte(value), UpdatedAt: at}); err != nil {
			t.Fatalf("put %s/%s: %v", kind, key, err)
		}
	}
	put("fest_vote", "1:a", "a", `{"team":0}`, now)
	put("fest_vote", "1:b", "b", `{"team":1}`, now.Add(time.Second))
	put("fest_vote", "1:c", "a", `{"team":1}`, now.Add(2*time.Second))
	put("unique_id", "1:a", "a", `{}`, now) // same key, another kind: a separate record
	if err := s.PutNEXRecord(ctx, NEXRecord{Title: "other", Kind: "fest_vote", Key: "1:a", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetNEXRecord(ctx, "splatoon2", "fest_vote", "1:a")
	if err != nil || got.Owner != "a" || string(got.Value) != `{"team":0}` || !got.UpdatedAt.Equal(now) {
		t.Fatalf("get: %+v %v", got, err)
	}

	// Replacing keeps one record with the new content.
	put("fest_vote", "1:a", "a", `{"team":1}`, now.Add(3*time.Second))
	all, err := s.ListNEXRecords(ctx, NEXRecordQuery{Title: "splatoon2", Kind: "fest_vote"})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, r := range all {
		keys = append(keys, r.Key)
	}
	if strings.Join(keys, ",") != "1:a,1:c,1:b" {
		t.Fatalf("list order %v, want most recently updated first", keys)
	}

	mine, err := s.ListNEXRecords(ctx, NEXRecordQuery{Title: "splatoon2", Kind: "fest_vote", Owner: "a"})
	if err != nil || len(mine) != 2 {
		t.Fatalf("owner filter: %d records, err %v", len(mine), err)
	}
	if one, _ := s.ListNEXRecords(ctx, NEXRecordQuery{Title: "splatoon2", Kind: "fest_vote", Limit: 1}); len(one) != 1 {
		t.Fatalf("limit: %d records", len(one))
	}
	if none, _ := s.ListNEXRecords(ctx, NEXRecordQuery{Title: "splatoon2", Kind: "nothing"}); len(none) != 0 {
		t.Fatalf("an unused kind listed %d records", len(none))
	}

	// An empty value is a record, not an absence.
	put("empty", "k", "", "", now)
	if r, err := s.GetNEXRecord(ctx, "splatoon2", "empty", "k"); err != nil || len(r.Value) != 0 {
		t.Fatalf("empty value: %+v %v", r, err)
	}

	if err := s.DeleteNEXRecord(ctx, "splatoon2", "fest_vote", "1:a"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNEXRecord(ctx, "splatoon2", "fest_vote", "1:a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if _, err := s.GetNEXRecord(ctx, "other", "fest_vote", "1:a"); err != nil {
		t.Fatal("deleting one title's record touched another's")
	}
}

// makeProvisionalUser is makeUser for a console's pre-link placeholder.
func makeProvisionalUser(t *testing.T, s Store) (User, DeviceAccount) {
	t.Helper()
	seq++
	now := time.Now().UTC().Truncate(time.Microsecond)
	u := User{
		ID: hexID(seq), Birthday: "0000-00-00",
		Permissions: DefaultPermissions(now), Extras: EmptyExtras(),
		Provisional:     true,
		BlocksUpdatedAt: now, FriendsUpdatedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	da := DeviceAccount{
		ID: hexID(seq + 100000), UserID: u.ID,
		PasswordSHA256: "0000000000000000000000000000000000000000000000000000000000000000",
		CreatedAt:      now, UpdatedAt: now,
	}
	if err := s.CreateUser(t.Context(), u, da); err != nil {
		t.Fatalf("create provisional user: %v", err)
	}
	return u, da
}

// Linking a console moves its device account to the real person and must
// delete the placeholder it leaves behind, with everything hanging off it.
// A placeholder that still has another device account stays.
func testProvisionalUsers(t *testing.T, s Store) {
	ctx := t.Context()
	person := makeUser(t, s, "Person")

	throwaway, da := makeProvisionalUser(t, s)
	if got, err := s.GetUser(ctx, throwaway.ID); err != nil || !got.Provisional {
		t.Fatalf("provisional flag did not round-trip: %+v, %v", got, err)
	}
	if err := s.SetFriendCode(ctx, FriendCode{UserID: throwaway.ID, Code: "111122223333", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), RegenerableAt: time.Now().UTC()}); err != nil {
		t.Fatalf("set friend code: %v", err)
	}
	if err := s.AddFriendship(ctx, throwaway.ID, person.ID, time.Now().UTC()); err != nil {
		t.Fatalf("add friendship: %v", err)
	}

	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ProvisionalUsers != 1 {
		t.Errorf("provisional users = %d, want 1", stats.ProvisionalUsers)
	}

	if err := s.ReassignDeviceAccount(ctx, da.ID, person.ID); err != nil {
		t.Fatalf("reassign: %v", err)
	}
	if _, err := s.GetUser(ctx, throwaway.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the placeholder survived linking: err = %v", err)
	}
	if _, err := s.GetFriendCode(ctx, throwaway.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the placeholder's friend code survived: err = %v", err)
	}
	if friends, err := s.ListFriends(ctx, person.ID, 10); err != nil || len(friends) != 0 {
		t.Errorf("a friendship with the deleted placeholder survived: %+v, %v", friends, err)
	}
	if got, err := s.GetDeviceAccount(ctx, da.ID); err != nil || got.UserID != person.ID {
		t.Errorf("device account after linking = %+v, %v", got, err)
	}

	// Two consoles' worth of device accounts: moving one keeps the user.
	shared, first := makeProvisionalUser(t, s)
	seq++
	second := DeviceAccount{ID: hexID(seq + 100000), UserID: shared.ID,
		PasswordSHA256: first.PasswordSHA256, CreatedAt: first.CreatedAt, UpdatedAt: first.UpdatedAt}
	if err := s.CreateDeviceAccountForUser(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := s.ReassignDeviceAccount(ctx, first.ID, person.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetUser(ctx, shared.ID); err != nil {
		t.Errorf("a placeholder with a device account left was deleted: %v", err)
	}
}

// testDiscordUsers is the contract for one Discord account owning several
// GRID0+ accounts: one of them is the main account, the rest are extras, and
// reaching an account by its login only ever reaches one of the caller's own.
func testDiscordUsers(t *testing.T, s Store) {
	ctx := t.Context()
	const aliceDiscord = "123456789012345678"
	const bobDiscord = "223456789012345678"

	newLoginUser := func(login, discordID string) User {
		t.Helper()
		seq++
		now := time.Now().UTC().Truncate(time.Microsecond)
		u := User{
			ID: hexID(seq), Nickname: login, Birthday: "0000-00-00",
			Permissions: DefaultPermissions(now), Extras: EmptyExtras(),
			BlocksUpdatedAt: now, FriendsUpdatedAt: now,
			CreatedAt: now, UpdatedAt: now,
			LoginUsername: login, LoginPasswordHash: "$2a$10$old",
			DiscordUserID: discordID,
		}
		return u
	}

	// The first account a Discord account makes is its main account.
	alice := newLoginUser("alice", aliceDiscord)
	if err := s.CreateUserWithLogin(ctx, alice, true); err != nil {
		t.Fatalf("create alice: %v", err)
	}
	got, err := s.GetUserByDiscordID(ctx, aliceDiscord)
	if err != nil || got.ID != alice.ID || got.DiscordUserID != aliceDiscord || got.Provisional {
		t.Fatalf("GetUserByDiscordID = %+v, %v", got, err)
	}
	for _, bad := range []string{"", "999"} {
		if _, err := s.GetUserByDiscordID(ctx, bad); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetUserByDiscordID(%q): err = %v, want ErrNotFound", bad, err)
		}
	}

	// A second account for the same Discord account is allowed, and does not
	// take over as the main one.
	aliceAlt := newLoginUser("alice_alt", aliceDiscord)
	if err := s.CreateUserWithLogin(ctx, aliceAlt, false); err != nil {
		t.Fatalf("create a second account for one Discord account: %v", err)
	}
	if got, err := s.GetUserByDiscordID(ctx, aliceDiscord); err != nil || got.ID != alice.ID {
		t.Errorf("the main account changed: got %+v, %v", got, err)
	}
	owned, err := s.ListUsersByDiscordID(ctx, aliceDiscord)
	if err != nil {
		t.Fatalf("ListUsersByDiscordID: %v", err)
	}
	if len(owned) != 2 || owned[0].ID != alice.ID || owned[1].ID != aliceAlt.ID {
		t.Fatalf("ListUsersByDiscordID = %+v, want the main account first", owned)
	}
	// A third main account would mean two accounts a bare command could mean.
	aliceThird := newLoginUser("alice_third", aliceDiscord)
	if err := s.CreateUserWithLogin(ctx, aliceThird, true); !errors.Is(err, ErrConflict) {
		t.Errorf("a second main account: err = %v, want ErrConflict", err)
	}
	if found, err := s.GetUserByLoginUsername(ctx, "alice_third"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the refused account was created anyway: %+v, %v", found, err)
	}

	// Naming an account by its login reaches it, and only when the caller owns
	// it. A login that does not exist and a login that is somebody else's are
	// the same answer.
	for _, tc := range []struct{ discordID, login string }{
		{aliceDiscord, "alice_alt"},
		{aliceDiscord, "ALICE_ALT"}, // login matching is case-insensitive
	} {
		if got, err := s.GetUserByDiscordLogin(ctx, tc.discordID, tc.login); err != nil {
			t.Errorf("GetUserByDiscordLogin(%q, %q): %v", tc.discordID, tc.login, err)
		} else if got.ID != aliceAlt.ID {
			t.Errorf("GetUserByDiscordLogin(%q, %q) = %s, want %s", tc.discordID, tc.login, got.ID, aliceAlt.ID)
		}
	}
	bob := newLoginUser("bob", bobDiscord)
	if err := s.CreateUserWithLogin(ctx, bob, true); err != nil {
		t.Fatalf("create bob: %v", err)
	}
	for _, tc := range []struct{ discordID, login string }{
		{bobDiscord, "alice_alt"}, // somebody else's account
		{aliceDiscord, "bob"},     // a login that does not exist at all
		{aliceDiscord, ""},        // no login named
		{"", "alice_alt"},         // no caller
		{bobDiscord, "nobody"},    // a login that does not exist
	} {
		if got, err := s.GetUserByDiscordLogin(ctx, tc.discordID, tc.login); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetUserByDiscordLogin(%q, %q) = %+v, %v, want ErrNotFound", tc.discordID, tc.login, got, err)
		}
	}
	if owned, err := s.ListUsersByDiscordID(ctx, ""); err != nil || len(owned) != 0 {
		t.Errorf("ListUsersByDiscordID(\"\") = %+v, %v, want nothing", owned, err)
	}

	// Linking an account made before the bot existed. The first account a
	// Discord account links is its main account; beside an existing one, only
	// an extra can be linked, and re-linking the same pair is a no-op rather
	// than an error.
	const carolDiscord = "323456789012345678"
	prebot := newLoginUser("prebot", "")
	if err := s.CreateUserWithLogin(ctx, prebot, true); err != nil {
		t.Fatalf("create an account with no Discord owner: %v", err)
	}
	if got, err := s.GetUser(ctx, prebot.ID); err != nil || got.DiscordUserID != "" {
		t.Fatalf("an unlinked account is owned by nobody: %+v, %v", got, err)
	}
	if err := s.LinkBotAccount(ctx, carolDiscord, prebot.ID, true); err != nil {
		t.Fatalf("link an account made before the bot: %v", err)
	}
	if got, err := s.GetUserByDiscordID(ctx, carolDiscord); err != nil || got.ID != prebot.ID {
		t.Errorf("the first linked account is not the main one: %+v, %v", got, err)
	}
	if err := s.LinkBotAccount(ctx, carolDiscord, prebot.ID, true); err != nil {
		t.Errorf("re-linking the same account: %v", err)
	}
	if err := s.LinkBotAccount(ctx, carolDiscord, prebot.ID, false); err != nil {
		t.Errorf("re-linking as an extra: %v", err)
	}
	if got, err := s.GetUserByDiscordLogin(ctx, carolDiscord, "prebot"); err != nil || got.ID != prebot.ID {
		t.Errorf("a linked account is not reachable by its own login: %+v, %v", got, err)
	}
	if err := s.LinkBotAccount(ctx, bobDiscord, prebot.ID, true); !errors.Is(err, ErrConflict) {
		t.Errorf("an account another Discord account owns: err = %v, want ErrConflict", err)
	}
	if err := s.LinkBotAccount(ctx, bobDiscord, "000000000000dead", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("linking a user that does not exist: err = %v, want ErrNotFound", err)
	}
	if err := s.LinkBotAccount(ctx, "", prebot.ID, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("linking with no Discord account: err = %v, want ErrNotFound", err)
	}

	// The owner is joined in on every read, not just the Discord lookups.
	if got, err := s.GetUser(ctx, aliceAlt.ID); err != nil || got.DiscordUserID != aliceDiscord {
		t.Errorf("GetUser: DiscordUserID = %q, %v", got.DiscordUserID, err)
	}
	if got, err := s.GetUserByLoginUsername(ctx, "alice_alt"); err != nil || got.DiscordUserID != aliceDiscord {
		t.Errorf("GetUserByLoginUsername: DiscordUserID = %q, %v", got.DiscordUserID, err)
	}
	if found, err := s.FindUsers(ctx, UserQuery{IDs: []string{alice.ID, aliceAlt.ID, bob.ID}}); err != nil {
		t.Errorf("FindUsers: %v", err)
	} else {
		owners := map[string]string{}
		for _, u := range found {
			owners[u.ID] = u.DiscordUserID
		}
		if owners[alice.ID] != aliceDiscord || owners[aliceAlt.ID] != aliceDiscord || owners[bob.ID] != bobDiscord {
			t.Errorf("FindUsers owners = %+v", owners)
		}
	}
	if all, err := s.ListUsers(ctx, 1000); err != nil {
		t.Errorf("ListUsers: %v", err)
	} else {
		for _, u := range all {
			if u.ID == aliceAlt.ID && u.DiscordUserID != aliceDiscord {
				t.Errorf("ListUsers: alice_alt's owner = %q", u.DiscordUserID)
			}
		}
	}

	hash := "$2a$10$new"
	if _, err := s.UpdateUser(ctx, alice.ID, UserPatch{LoginPasswordHash: &hash}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetUser(ctx, alice.ID); got.LoginPasswordHash != hash {
		t.Errorf("password hash after update = %q", got.LoginPasswordHash)
	}
}

func testPenne(t *testing.T, s Store) {
	ctx := t.Context()
	u := makeUser(t, s, "Alice")
	now := time.Now().UTC().Truncate(time.Microsecond)

	tok := PenneToken{Token: "tok-1", PenneID: "penne-a", Account: "0123456789abcdef0123456789abcdef", ApplicationID: "0100000000000010"}
	if err := s.SetPenneToken(ctx, tok); err != nil {
		t.Fatal(err)
	}
	// A console that registers again replaces the owner, not adds a second.
	tok.PenneID = "penne-b"
	if err := s.SetPenneToken(ctx, tok); err != nil {
		t.Fatal(err)
	}
	got, err := s.PenneTokens(ctx, []string{"tok-1", "unknown"})
	if err != nil || len(got) != 1 || got[0] != tok {
		t.Fatalf("penne tokens = %+v, %v", got, err)
	}

	if err := s.SetPushChannel(ctx, PushChannel{UserID: u.ID, DeviceAccountID: "da1", DeviceToken: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPushChannel(ctx, PushChannel{UserID: u.ID, DeviceAccountID: "da1", DeviceToken: "tok-1"}); err != nil {
		t.Fatal(err)
	}
	chans, err := s.PushChannels(ctx, []string{u.ID, "nobody"})
	if err != nil || len(chans) != 1 || chans[0].DeviceToken != "tok-1" {
		t.Fatalf("push channels = %+v, %v", chans, err)
	}
	if chans, err := s.PushChannels(ctx, []string{}); err != nil || len(chans) != 0 {
		t.Fatalf("an empty id list matched %+v, %v", chans, err)
	}

	if err := s.SetPenneTicket(ctx, "ticket", "penne-b", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if id, err := s.PenneTicket(ctx, "ticket", now); err != nil || id != "penne-b" {
		t.Fatalf("ticket = %q, %v", id, err)
	}
	if _, err := s.PenneTicket(ctx, "ticket", now.Add(2*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an expired ticket = %v, want ErrNotFound", err)
	}
	if _, err := s.PenneTicket(ctx, "nope", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown ticket = %v, want ErrNotFound", err)
	}
}
