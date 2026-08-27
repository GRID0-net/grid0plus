package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/n-popescu/switchnet/config"
	"github.com/n-popescu/switchnet/internal/baas"
	"github.com/n-popescu/switchnet/internal/db"
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

const accountCreateUsage = `switchnetctl account create -device-account-id <id> -password <password> [-nickname <name>]

Creates a user whose device account id and password are exactly what you
give it, rather than server-generated ones.

Use this for a console whose local user already has a Nintendo Account
linked from before you pointed it at SwitchNet. That local user has a device
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
