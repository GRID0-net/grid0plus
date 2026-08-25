package baas

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/n-popescu/switchnet/internal/db"
)

// The BAAS user object.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/BAAS-Server#user-information
//
// Field names and shapes match a real capture. Where SwitchNet has nothing to
// put in a field it emits the empty value rather than omitting the key, because
// the console's JSON reader has been seen to want the key present.

// UserResponse is the user object returned by /1.0.0/users, /1.0.0/login and
// every user read.
type UserResponse struct {
	ID       string `json:"id"`
	ETag     string `json:"etag"`
	Nickname string `json:"nickname"`
	Country  string `json:"country"`
	Birthday string `json:"birthday"`

	ThumbnailURL  string `json:"thumbnailUrl"`
	Thumbnail2URL string `json:"thumbnail2Url,omitempty"`

	// DeviceAccounts carries the password exactly once, in the response to
	// /1.0.0/users that created it. The console stores it and it can never be
	// read back, so losing that one response means the account is unreachable
	// from that console.
	DeviceAccounts []DeviceAccountResponse `json:"deviceAccounts"`

	Links       LinksResponse    `json:"links"`
	Permissions db.Permissions   `json:"permissions"`
	Extras      db.ExtrasByScope `json:"extras"`
	Presence    PresenceResponse `json:"presence"`

	Deleted bool `json:"deleted"`

	BlocksUpdatedAt     int64 `json:"blocksUpdatedAt"`
	FriendsUpdatedAt    int64 `json:"friendsUpdatedAt"`
	ThumbnailUploadedAt int64 `json:"thumbnailUploadedAt,omitempty"`
	CreatedAt           int64 `json:"createdAt"`
	UpdatedAt           int64 `json:"updatedAt"`
}

// DeviceAccountResponse is one device account.
type DeviceAccountResponse struct {
	ID       string `json:"id"`
	Password string `json:"password,omitempty"`
}

// LinksResponse is the linked-accounts block. Only friendCode is ever populated
// by SwitchNet: there is no Nintendo Account, Twitter or Facebook to link to.
type LinksResponse struct {
	FriendCode *FriendCodeLink `json:"friendCode,omitempty"`
	// NintendoAccount is present only if a /1.0.0/federation or /link request
	// recorded one.
	NintendoAccount *AccountLink `json:"nintendoAccount,omitempty"`
}

// FriendCodeLink is the friend code and its regeneration state.
type FriendCodeLink struct {
	ID            string `json:"id"`
	CreatedAt     int64  `json:"createdAt"`
	UpdatedAt     int64  `json:"updatedAt"`
	RegenerableAt int64  `json:"regenerableAt"`
	Regenerable   bool   `json:"regenerable"`
}

// AccountLink is a generic linked account.
type AccountLink struct {
	ID        string `json:"id"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

// PresenceResponse is the online status block.
type PresenceResponse struct {
	State     string           `json:"state"`
	Extras    db.ExtrasByScope `json:"extras"`
	UpdatedAt int64            `json:"updatedAt"`
	LogoutAt  int64            `json:"logoutAt"`
}

// FriendResponse is one entry in a friend list.
//
// TODO(capture-needed): the shape of GET /2.0.0/users/<id>/friends is not
// documented anywhere, and no public implementation exists to compare against.
// What is known:
//
//   - the request takes a 'count' query parameter, default 300, so it is a
//     collection endpoint;
//   - the friends sysmodule needs each friend's nickname, thumbnail, friend
//     code and presence to draw the friend list, so a friend entry has to be at
//     least as rich as a user object.
//
// SwitchNet returns a JSON array of objects that extend the user object with
// isFavorite and becameFriendAt, because every other BAAS endpoint returns its
// resource bare rather than wrapped in an envelope. If a capture shows an
// envelope such as {"friends":[...]}, the only place to change is
// friends.go's listFriends — this struct stays as it is.
//
// Capture with: add a friend on a real console, then read
// GET /2.0.0/users/<id>/friends off the wire. See docs/CAPTURES/README.md.
type FriendResponse struct {
	UserResponse
	IsFavorite     bool  `json:"isFavorite"`
	BecameFriendAt int64 `json:"becameFriendAt"`
}

// FriendRequestResponse is one entry in the inbox or outbox.
//
// TODO(capture-needed): as with FriendResponse, the exact shape is
// undocumented. The fields here are the minimum the console needs to draw the
// "friend requests" screen: who sent it, when, and enough profile to show them.
type FriendRequestResponse struct {
	ID   string        `json:"id"`
	From *UserResponse `json:"from,omitempty"`
	To   *UserResponse `json:"to,omitempty"`
	// State is PENDING, ACCEPTED, DENIED or CANCELLED.
	State     string `json:"state"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

// RelationshipResponse is the answer to GET /2.0.0/users/<id>/relationships/<id>.
//
// TODO(capture-needed): undocumented. 'type' is modelled on the values the
// console's own nn::friends relationship enum uses.
type RelationshipResponse struct {
	// Type is NONE, FRIEND, FRIEND_REQUEST_SENT, FRIEND_REQUEST_RECEIVED or BLOCKED.
	Type string `json:"type"`
	// User is the other party.
	User *UserResponse `json:"user,omitempty"`
	// IsFavorite is set when the relationship is FRIEND.
	IsFavorite bool `json:"isFavorite"`
	// BecameFriendAt is set when the relationship is FRIEND.
	BecameFriendAt int64 `json:"becameFriendAt,omitempty"`
}

// Relationship types.
const (
	RelationshipNone     = "NONE"
	RelationshipFriend   = "FRIEND"
	RelationshipSent     = "FRIEND_REQUEST_SENT"
	RelationshipReceived = "FRIEND_REQUEST_RECEIVED"
	RelationshipBlocked  = "BLOCKED"
)

// BlockResponse is one blocked user.
type BlockResponse struct {
	User      *UserResponse `json:"user,omitempty"`
	CreatedAt int64         `json:"createdAt"`
}

// ---- conversion ----------------------------------------------------------

// visibility says how much of a user another party is allowed to see.
type visibility int

const (
	// visSelf is the user reading their own record: everything, including the
	// device account list and the 'self' extras.
	visSelf visibility = iota
	// visFavoriteFriend is a friend the user marked as a favourite.
	visFavoriteFriend
	// visFriend is an ordinary friend.
	visFriend
	// visStranger is anybody else: enough to be found and added, nothing more.
	visStranger
)

// userResponse converts a stored user into the wire object.
//
// pres is the user's effective presence, already resolved through the TTL rules
// in presence.go. fc may be nil if the user has no friend code yet. now is the
// server's clock, passed in rather than read here so that the friend code's
// 'regenerable' flag and the presence TTLs are decided by one consistent time.
func userResponse(now time.Time, u db.User, fc *db.FriendCode, pres db.Presence, das []db.DeviceAccount, vis visibility) UserResponse {
	out := UserResponse{
		ID:               u.ID,
		ETag:             etagFor(u),
		Nickname:         u.Nickname,
		Country:          u.Country,
		Birthday:         u.Birthday,
		ThumbnailURL:     u.ThumbnailURL,
		Thumbnail2URL:    u.Thumbnail2URL,
		DeviceAccounts:   []DeviceAccountResponse{},
		Links:            LinksResponse{},
		Permissions:      u.Permissions,
		Extras:           scopedExtras(u.Extras, vis),
		Presence:         presenceResponse(pres, vis),
		Deleted:          u.Deleted,
		BlocksUpdatedAt:  u.BlocksUpdatedAt.Unix(),
		FriendsUpdatedAt: u.FriendsUpdatedAt.Unix(),
		CreatedAt:        u.CreatedAt.Unix(),
		UpdatedAt:        u.UpdatedAt.Unix(),
	}
	if u.ThumbnailUploadedAt != nil {
		out.ThumbnailUploadedAt = u.ThumbnailUploadedAt.Unix()
	}
	if fc != nil {
		out.Links.FriendCode = &FriendCodeLink{
			ID:            fc.Code,
			CreatedAt:     fc.CreatedAt.Unix(),
			UpdatedAt:     fc.UpdatedAt.Unix(),
			RegenerableAt: fc.RegenerableAt.Unix(),
			Regenerable:   fc.Regenerable(now),
		}
	}

	// The device account list is private. Handing a friend the ids of the
	// consoles somebody logs in from serves no purpose and is the sort of leak
	// that turns into an identity problem later.
	if vis == visSelf {
		for _, da := range das {
			out.DeviceAccounts = append(out.DeviceAccounts, DeviceAccountResponse{ID: da.ID})
		}
	}
	return out
}

// scopedExtras returns only the extras scopes a viewer may read.
//
// The five scopes exist precisely so a user can put something in 'self' that no
// friend sees. Returning all five to everybody makes the whole mechanism
// decorative.
func scopedExtras(extras db.ExtrasByScope, vis visibility) db.ExtrasByScope {
	allowed := map[string]bool{db.ScopeEveryone: true}
	switch vis {
	case visSelf:
		for _, s := range db.Scopes {
			allowed[s] = true
		}
	case visFavoriteFriend:
		allowed[db.ScopeFavoriteFriends] = true
		allowed[db.ScopeFriends] = true
		allowed[db.ScopeFOAF] = true
	case visFriend:
		allowed[db.ScopeFriends] = true
		allowed[db.ScopeFOAF] = true
	case visStranger:
	}

	// Every scope key is always present, empty when not visible: the console
	// indexes into these without checking whether the key exists.
	out := make(db.ExtrasByScope, len(db.Scopes))
	for _, s := range db.Scopes {
		if allowed[s] && extras[s] != nil {
			out[s] = extras[s]
		} else {
			out[s] = map[string]any{}
		}
	}
	return out
}

// presenceResponse converts a resolved presence into the wire object.
func presenceResponse(p db.Presence, vis visibility) PresenceResponse {
	out := PresenceResponse{
		State:     p.State,
		Extras:    scopedExtras(p.Extras, vis),
		UpdatedAt: p.ReportedAt.Unix(),
	}
	if p.LogoutAt != nil {
		out.LogoutAt = p.LogoutAt.Unix()
	}
	return out
}

// etagFor derives the ETag the console uses for conditional requests.
//
// Nintendo's value is opaque and 16 hex digits. Deriving it from the fields that
// can change means it moves exactly when the user does, which is the entire
// contract an ETag has to satisfy.
func etagFor(u db.User) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%s|%s|%d|%d|%d",
		u.ID, u.Nickname, u.Country, u.Birthday, u.ThumbnailURL,
		u.UpdatedAt.UnixNano(), u.FriendsUpdatedAt.UnixNano(), u.BlocksUpdatedAt.UnixNano())
	return `"` + hex.EncodeToString(h.Sum(nil)[:8]) + `"`
}

// ---- identifier generation ----------------------------------------------

// newHexID returns a random lowercase hex id of n bytes: the shape BAAS uses for
// user ids and device account ids (8 bytes, 16 digits) and image ids (16 bytes).
func newHexID(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// devicePasswordAlphabet is the character set of a device account password.
const devicePasswordAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// devicePasswordLength matches Nintendo's: 40 characters.
const devicePasswordLength = 40

// newDevicePassword returns a device account password.
//
// 40 characters from a 62-symbol alphabet is about 238 bits, which is why it is
// stored as a plain SHA-256 rather than through a slow key derivation function:
// there is nothing to brute-force.
func newDevicePassword() (string, error) {
	var sb strings.Builder
	sb.Grow(devicePasswordLength)
	max := big.NewInt(int64(len(devicePasswordAlphabet)))
	for range devicePasswordLength {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("generate device password: %w", err)
		}
		sb.WriteByte(devicePasswordAlphabet[n.Int64()])
	}
	return sb.String(), nil
}

// hashPassword returns the hex-encoded SHA-256 of a device account password.
func hashPassword(password string) string {
	sum := sha256.Sum256([]byte(password))
	return hex.EncodeToString(sum[:])
}
