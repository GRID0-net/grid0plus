package baas

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/n-popescu/switchnet/internal/db"
)

var deviceAccountIDPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

// ProvisionDeviceAccount creates a user whose device account id and password
// are exactly what the caller supplies, instead of ones this server
// generates itself.
//
// Every other path to an account — registerUser, and the console's own first
// login — has this server pick the id and password, because it is the only
// party that will ever need to recognise them again. This function exists for
// the one case where that is backwards: a console whose local user already
// has a device account from somewhere else (real Nintendo, or a previous
// SwitchNet instance). That credential is cached in the console's own save
// data and it has no way to be told to ask for a new one short of unlinking
// the account. The only way to make that local user work here is for this
// server to recognise the credential it already has.
func ProvisionDeviceAccount(ctx context.Context, store db.Store, now time.Time, deviceAccountID, password, nickname string) (db.User, error) {
	deviceAccountID = strings.ToLower(strings.TrimSpace(deviceAccountID))
	if !deviceAccountIDPattern.MatchString(deviceAccountID) {
		return db.User{}, fmt.Errorf("device account id must be 16 lowercase hex characters, got %q", deviceAccountID)
	}
	if password == "" {
		return db.User{}, errors.New("password must not be empty")
	}

	userID, err := newHexID(8)
	if err != nil {
		return db.User{}, fmt.Errorf("generate user id: %w", err)
	}

	user := db.User{
		ID:               userID,
		Nickname:         nickname,
		Birthday:         "0000-00-00",
		Permissions:      db.DefaultPermissions(now),
		Extras:           db.EmptyExtras(),
		BlocksUpdatedAt:  now,
		FriendsUpdatedAt: now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	deviceAccount := db.DeviceAccount{
		ID:             deviceAccountID,
		UserID:         userID,
		PasswordSHA256: hashPassword(password),
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := store.CreateUser(ctx, user, deviceAccount); err != nil {
		return db.User{}, err
	}
	return user, nil
}
