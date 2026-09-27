package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/n-popescu/grid0plus/config"
	"github.com/n-popescu/grid0plus/internal/baas"
	"github.com/n-popescu/grid0plus/internal/db"
)

// accountCreate implements `switchnetctl account create`.
//
// It talks to the database directly rather than through the baas HTTP API,
// because the whole point is to insert a device account id and password this
// server did not generate — the HTTP registration endpoint always generates
// its own.
func accountCreate(cfg config.Config, args []string) {
	fs := flag.NewFlagSet("account create", flag.ExitOnError)
	deviceAccountID := fs.String("device-account-id", "",
		"16 hex character device account id, read from the console's own save data")
	password := fs.String("password", "",
		"40 character device account password, read from the console's own save data")
	nickname := fs.String("nickname", "",
		"nickname to show in the friends applet (optional, can be changed later)")
	fs.Usage = func() { fmt.Fprint(os.Stderr, accountCreateUsage) }
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *deviceAccountID == "" || *password == "" {
		fs.Usage()
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store, err := db.OpenPostgres(ctx, cfg.Database.URL, cfg.Database.MaxConns, cfg.Database.AutoMigrate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: open database: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	user, err := baas.ProvisionDeviceAccount(ctx, store, time.Now().UTC(),
		*deviceAccountID, *password, *nickname)
	switch {
	case errors.Is(err, db.ErrConflict):
		fmt.Fprintf(os.Stderr,
			"switchnetctl: a device account with this id already exists.\n"+
				"If you are re-running this after an earlier success, nothing more is\n"+
				"needed — the console can already log in.\n")
		os.Exit(1)
	case err != nil:
		fmt.Fprintf(os.Stderr, "switchnetctl: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("created user %s\n", user.ID)
	if user.Nickname != "" {
		fmt.Printf("  nickname:       %s\n", user.Nickname)
	}
	fmt.Printf("  device account: %s\n", *deviceAccountID)
	fmt.Println()
	fmt.Println("Nothing needs to change on the console. It already has this id and")
	fmt.Println("password stored for that local user, and will present them the next")
	fmt.Println("time it logs in.")
}

// accountRevokeTokens implements `switchnetctl account revoke-tokens`.
func accountRevokeTokens(cfg config.Config, args []string) {
	fs := flag.NewFlagSet("account revoke-tokens", flag.ExitOnError)
	deviceAccountID := fs.String("device-account-id", "",
		"16 hex character device account id whose live sessions should be revoked")
	fs.Usage = func() { fmt.Fprint(os.Stderr, accountRevokeTokensUsage) }
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *deviceAccountID == "" {
		fs.Usage()
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store, err := db.OpenPostgres(ctx, cfg.Database.URL, cfg.Database.MaxConns, cfg.Database.AutoMigrate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: open database: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	err = store.RevokeDeviceAccountTokens(ctx, *deviceAccountID, now)
	switch {
	case errors.Is(err, db.ErrNotFound):
		fmt.Fprintf(os.Stderr, "switchnetctl: no device account with id %s\n", *deviceAccountID)
		os.Exit(1)
	case err != nil:
		fmt.Fprintf(os.Stderr, "switchnetctl: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("revoked tokens for device account %s, minted before %s\n",
		*deviceAccountID, now.Format(time.RFC3339))
	fmt.Println()
	fmt.Println("Every access, user and id token already minted for this device account is")
	fmt.Println("refused from now on, even though it has not expired. The console's next")
	fmt.Println("login (POST /1.0.0/login) mints a fresh one and works normally -- this does")
	fmt.Println("not disable the account. To stop it logging in at all, delete the device")
	fmt.Println("account or user instead.")
}

const accountRevokeTokensUsage = `switchnetctl account revoke-tokens -device-account-id <id>

Invalidates every access, user and id token already minted for a device
account, without rotating the server's signing key (which would invalidate
every OTHER console's sessions too) and without deleting anything.

Use this if a token for this console leaked -- from a debug log, a shared
screen, a compromised machine that had a copy of one. The device account's
password is untouched, so its normal next login still works and mints a
new, valid session.
`

// accountDisable implements `switchnetctl account disable`.
func accountDisable(cfg config.Config, args []string) {
	fs := flag.NewFlagSet("account disable", flag.ExitOnError)
	deviceAccountID := fs.String("device-account-id", "",
		"16 hex character device account id to disable")
	fs.Usage = func() { fmt.Fprint(os.Stderr, accountDisableUsage) }
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *deviceAccountID == "" {
		fs.Usage()
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store, err := db.OpenPostgres(ctx, cfg.Database.URL, cfg.Database.MaxConns, cfg.Database.AutoMigrate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: open database: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	da, err := store.GetDeviceAccount(ctx, *deviceAccountID)
	switch {
	case errors.Is(err, db.ErrNotFound):
		fmt.Fprintf(os.Stderr, "switchnetctl: no device account with id %s\n", *deviceAccountID)
		os.Exit(1)
	case err != nil:
		fmt.Fprintf(os.Stderr, "switchnetctl: %v\n", err)
		os.Exit(1)
	}
	if da.DeletedAt != nil {
		fmt.Printf("device account %s is already disabled (since %s)\n",
			*deviceAccountID, da.DeletedAt.Format(time.RFC3339))
		return
	}

	if err := store.DeleteDeviceAccount(ctx, da.UserID, *deviceAccountID); err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("disabled device account %s\n", *deviceAccountID)
	fmt.Println()
	fmt.Println("The console can no longer log in with this credential -- its next")
	fmt.Println("POST /1.0.0/login is refused the same way an unknown device account")
	fmt.Println("would be, and any token it already holds stops working immediately.")
	fmt.Println("'switchnetctl account enable' reverses this.")
}

const accountDisableUsage = `switchnetctl account disable -device-account-id <id>

Blocks a single console from logging in at all, without deleting its user,
its friends, or anything else -- reversible with 'switchnetctl account
enable'.

Use this for a console you want to lock out entirely, as opposed to
'account revoke-tokens', which only ends its current sessions and lets it
log back in immediately after.
`

// accountEnable implements `switchnetctl account enable`.
func accountEnable(cfg config.Config, args []string) {
	fs := flag.NewFlagSet("account enable", flag.ExitOnError)
	deviceAccountID := fs.String("device-account-id", "",
		"16 hex character device account id to re-enable")
	fs.Usage = func() { fmt.Fprint(os.Stderr, accountEnableUsage) }
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *deviceAccountID == "" {
		fs.Usage()
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store, err := db.OpenPostgres(ctx, cfg.Database.URL, cfg.Database.MaxConns, cfg.Database.AutoMigrate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: open database: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	err = store.UndeleteDeviceAccount(ctx, *deviceAccountID)
	switch {
	case errors.Is(err, db.ErrNotFound):
		fmt.Fprintf(os.Stderr,
			"switchnetctl: no disabled device account with id %s "+
				"(it may not exist, or may already be enabled)\n", *deviceAccountID)
		os.Exit(1)
	case err != nil:
		fmt.Fprintf(os.Stderr, "switchnetctl: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("re-enabled device account %s\n", *deviceAccountID)
	fmt.Println()
	fmt.Println("The console can log in with its existing password again. Nothing was")
	fmt.Println("regenerated -- if you also want to be sure any old, leaked token")
	fmt.Println("cannot be reused, run 'switchnetctl account revoke-tokens' as well.")
}

const accountEnableUsage = `switchnetctl account enable -device-account-id <id>

Reverses 'switchnetctl account disable': the console can log in again with
its existing credential.
`

// accountDeleteUser implements `switchnetctl account delete-user`.
func accountDeleteUser(cfg config.Config, args []string) {
	fs := flag.NewFlagSet("account delete-user", flag.ExitOnError)
	userID := fs.String("user-id", "", "16 hex character user id to delete")
	fs.Usage = func() { fmt.Fprint(os.Stderr, accountDeleteUserUsage) }
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *userID == "" {
		fs.Usage()
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store, err := db.OpenPostgres(ctx, cfg.Database.URL, cfg.Database.MaxConns, cfg.Database.AutoMigrate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: open database: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	user, err := store.GetUser(ctx, *userID)
	switch {
	case errors.Is(err, db.ErrNotFound):
		fmt.Fprintf(os.Stderr, "switchnetctl: no user with id %s\n", *userID)
		os.Exit(1)
	case err != nil:
		fmt.Fprintf(os.Stderr, "switchnetctl: %v\n", err)
		os.Exit(1)
	}
	if user.Deleted {
		fmt.Printf("user %s is already deleted\n", *userID)
		return
	}

	deleted := true
	if _, err := store.UpdateUser(ctx, *userID, db.UserPatch{Deleted: &deleted}); err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("deleted user %s\n", *userID)
	fmt.Println()
	fmt.Println("Every device account under this person is refused at login, any of them")
	fmt.Println("attempting /1.0.0/federation is refused the same way, and every token")
	fmt.Println("already minted for any of them stops working immediately -- the same")
	fmt.Println("token check that runs on every authenticated request now also refuses")
	fmt.Println("once it re-reads this user as deleted, not just once a token expires.")
	fmt.Println("Nothing about the user, its device accounts, friends or history is")
	fmt.Println("erased. 'switchnetctl account restore-user' reverses this.")
}

const accountDeleteUserUsage = `switchnetctl account delete-user -user-id <id>

Blocks every device account belonging to one person at once -- reversible
with 'switchnetctl account restore-user'.

This is a different, more severe tool than 'account disable', which blocks
a single console's credential. Use this to evict a person entirely: every
console they have ever linked to this user, and any future Nintendo
Account federation naming this user as the link target, stops working in
one action.
`

// accountRestoreUser implements `switchnetctl account restore-user`.
func accountRestoreUser(cfg config.Config, args []string) {
	fs := flag.NewFlagSet("account restore-user", flag.ExitOnError)
	userID := fs.String("user-id", "", "16 hex character user id to restore")
	fs.Usage = func() { fmt.Fprint(os.Stderr, accountRestoreUserUsage) }
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *userID == "" {
		fs.Usage()
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store, err := db.OpenPostgres(ctx, cfg.Database.URL, cfg.Database.MaxConns, cfg.Database.AutoMigrate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: open database: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	user, err := store.GetUser(ctx, *userID)
	switch {
	case errors.Is(err, db.ErrNotFound):
		fmt.Fprintf(os.Stderr, "switchnetctl: no user with id %s\n", *userID)
		os.Exit(1)
	case err != nil:
		fmt.Fprintf(os.Stderr, "switchnetctl: %v\n", err)
		os.Exit(1)
	}
	if !user.Deleted {
		fmt.Printf("user %s is not deleted\n", *userID)
		return
	}

	notDeleted := false
	if _, err := store.UpdateUser(ctx, *userID, db.UserPatch{Deleted: &notDeleted}); err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("restored user %s\n", *userID)
	fmt.Println()
	fmt.Println("Its device accounts can log in and federate again with their existing")
	fmt.Println("credentials -- nothing was regenerated. Any device account individually")
	fmt.Println("disabled with 'account disable' before the user was deleted is still")
	fmt.Println("disabled; this only reverses the user-level delete.")
}

const accountRestoreUserUsage = `switchnetctl account restore-user -user-id <id>

Reverses 'switchnetctl account delete-user': every device account under
this person can log in and federate again.
`

const accountCreateUsage = `switchnetctl account create -device-account-id <id> -password <password> [-nickname <name>]

Creates a user whose device account id and password are exactly what you
give it, rather than server-generated ones.

Use this for a console whose local user already has a Nintendo Account
linked from before you pointed it at GRID0+. That local user has a device
account id and password cached in its own save data —
su/baas/<guid>.dat, inside save folder 8000000000000010 — and it has no way
to be told to ask for a new one short of unlinking the account on the
console. Read the id and password out of that file with:

  python3 scripts/capture-baas.py --parse-save /path/to/su/baas/<guid>.dat

then pass them here. This server will then recognise the credential the
console already has, and the next login from that local user succeeds with
no console-side change at all.

A local user that has never linked any account does not need this: opening
the friends applet drives the console through the normal POST /1.0.0/users
registration by itself, the same as scripts/seed-dev-data.sh does.

The device account password is a permanent, unrotatable credential for that
console. Treat the value you pass here like a private key, and never put it
in a file that could be committed.
`
