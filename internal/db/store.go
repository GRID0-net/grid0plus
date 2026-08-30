// Package db is SwitchNet's persistence layer.
//
// The Store interface is deliberately narrow and free of business logic: it
// reads and writes rows, and the handlers in internal/baas decide what those
// rows mean. That split is what lets the handler tests run against an in-memory
// store with no database, while production runs the same handlers against
// PostgreSQL.
package db

import (
	"context"
	"errors"
	"time"
)

// Sentinel errors every Store implementation must return, so handlers can map
// them onto the right Nintendo error response without knowing which backend
// they are talking to.
var (
	// ErrNotFound means the row does not exist.
	ErrNotFound = errors.New("db: not found")
	// ErrConflict means the write would violate a uniqueness constraint.
	ErrConflict = errors.New("db: conflict")
)

// User is a BAAS account.
type User struct {
	ID       string
	Nickname string
	Country  string
	Birthday string

	ThumbnailURL        string
	Thumbnail2URL       string
	ThumbnailUploadedAt *time.Time

	Permissions Permissions
	// Extras holds the five visibility scopes of user extras.
	Extras ExtrasByScope

	// LoginUsername and LoginPasswordHash let a person authenticate
	// directly — through the self-registration website and the Nintendo
	// Account link page — independently of any device account. Empty for a
	// user that was created by a console (registerUser) rather than by a
	// person registering on the website. A user can exist with no device
	// account at all: CreateUser's "never created without a device
	// account" invariant only ever applied to the console-driven path.
	//
	// LoginPasswordHash is bcrypt, not the plain SHA-256 device accounts
	// use — that one is fine unhashed-strength because it is a
	// server-generated 238-bit random string; this one is human-chosen and
	// has to resist guessing.
	LoginUsername     string
	LoginPasswordHash string

	Deleted bool

	BlocksUpdatedAt  time.Time
	FriendsUpdatedAt time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Permissions is a user's privacy settings.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/BAAS-Server#privacy-settings
type Permissions struct {
	PersonalAnalytics    bool `json:"personalAnalytics"`
	PersonalNotification bool `json:"personalNotification"`
	// FriendRequestReception is whether the user accepts friend requests at all.
	FriendRequestReception bool `json:"friendRequestReception"`
	// Friends is who may see the user's friend list: EVERYONE, FRIENDS or SELF.
	Friends string `json:"friends"`
	// Presence is who may see the user's online status: FRIENDS,
	// FAVORITE_FRIENDS or SELF.
	//
	// The default has to be FRIENDS. SELF is a friends list where everybody is
	// permanently offline and nothing in the response says why.
	Presence string `json:"presence"`

	PresenceUpdatedAt             int64 `json:"presenceUpdatedAt"`
	PersonalAnalyticsUpdatedAt    int64 `json:"personalAnalyticsUpdatedAt"`
	PersonalNotificationUpdatedAt int64 `json:"personalNotificationUpdatedAt"`
}

// DefaultPermissions is what a freshly registered user gets.
func DefaultPermissions(now time.Time) Permissions {
	ts := now.Unix()
	return Permissions{
		PersonalAnalytics:             true,
		PersonalNotification:          true,
		FriendRequestReception:        true,
		Friends:                       "EVERYONE",
		Presence:                      "FRIENDS",
		PresenceUpdatedAt:             ts,
		PersonalAnalyticsUpdatedAt:    ts,
		PersonalNotificationUpdatedAt: ts,
	}
}

// Visibility scopes. Every extras block is keyed by these.
const (
	ScopeSelf            = "self"
	ScopeFavoriteFriends = "favoriteFriends"
	ScopeFriends         = "friends"
	// ScopeFOAF is "friend of a friend".
	ScopeFOAF     = "foaf"
	ScopeEveryone = "everyone"
)

// Scopes is every visibility scope, in the order Nintendo emits them.
var Scopes = []string{ScopeSelf, ScopeFavoriteFriends, ScopeFriends, ScopeFOAF, ScopeEveryone}

// ExtrasByScope maps a visibility scope to that scope's key/value pairs.
//
// The values are stored and returned verbatim. The console and the games put
// keys in here that are not documented anywhere, and dropping an unrecognised
// key would silently break whatever depends on it.
type ExtrasByScope map[string]map[string]any

// EmptyExtras returns an extras block with all five scopes present and empty,
// which is what the console gets for a new account.
func EmptyExtras() ExtrasByScope {
	e := make(ExtrasByScope, len(Scopes))
	for _, s := range Scopes {
		e[s] = map[string]any{}
	}
	return e
}

// DeviceAccount is the credential a console stores and presents on every login.
type DeviceAccount struct {
	ID     string
	UserID string
	// PasswordSHA256 is the hex-encoded SHA-256 of the password.
	PasswordSHA256 string
	DeviceID       string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      *time.Time
	// TokensRevokedBefore invalidates every token minted for this device
	// account before this instant, without touching the password or deleting
	// the account: a token whose 'iat' predates it is refused even though it
	// has not expired. Nil means nothing has ever been revoked.
	TokensRevokedBefore *time.Time
}

// Device is a console, identified by its TLS client certificate.
type Device struct {
	ID            string
	SerialNumber  string
	ProductCode   string
	DeviceType    string
	IsT           bool
	SystemVersion string
	FirstSeenAt   time.Time
	LastSeenAt    time.Time
}

// FriendCode is a user's friend code and its regeneration cooldown.
type FriendCode struct {
	UserID        string
	Code          string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	RegenerableAt time.Time
}

// Regenerable reports whether the code may be regenerated at t.
func (f FriendCode) Regenerable(t time.Time) bool { return !t.Before(f.RegenerableAt) }

// Friendship is one direction of a mutual friendship.
type Friendship struct {
	UserID         string
	FriendID       string
	Favorite       bool
	BecameFriendAt time.Time
}

// Friend request states.
const (
	RequestPending   = "PENDING"
	RequestAccepted  = "ACCEPTED"
	RequestDenied    = "DENIED"
	RequestCancelled = "CANCELLED"
)

// FriendRequest is a pending or resolved friend request.
type FriendRequest struct {
	ID         string
	FromUserID string
	ToUserID   string
	State      string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Block is one user blocking another.
type Block struct {
	UserID        string
	BlockedUserID string
	CreatedAt     time.Time
}

// Presence states, in increasing strength. A weaker signal must never overwrite
// a stronger, fresher one.
const (
	StateOffline  = "OFFLINE"
	StateInactive = "INACTIVE"
	StateOnline   = "ONLINE"
	StatePlaying  = "PLAYING"
)

// Presence is what a user is doing.
//
// State and ReportedAt are what the console said. LastSeenAt is when it last
// made any authenticated request. See internal/db/migrations/0007_presence.up.sql
// for why both are needed.
type Presence struct {
	UserID string
	State  string

	AppID           string
	PresenceGroupID string
	AppField        string
	ACDIndex        int

	Extras ExtrasByScope

	ReportedAt time.Time
	LastSeenAt time.Time
	LogoutAt   *time.Time
}

// Image is an uploaded profile picture.
type Image struct {
	ID        string
	OwnerID   string
	Format    string
	Width     int
	Height    int
	SizeBytes int
	RelPath   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// UserPatch is a set of optional changes to a user. A nil field is left alone,
// which is what a JSON patch that does not mention a path should do.
type UserPatch struct {
	Nickname            *string
	Country             *string
	Birthday            *string
	ThumbnailURL        *string
	Thumbnail2URL       *string
	ThumbnailUploadedAt *time.Time
	Permissions         *Permissions
	Extras              *ExtrasByScope
}

// PresencePatch is a set of optional changes to a presence row.
type PresencePatch struct {
	State           *string
	AppID           *string
	PresenceGroupID *string
	AppField        *string
	ACDIndex        *int
	Extras          *ExtrasByScope
	ReportedAt      *time.Time
	LogoutAt        *time.Time
}

// UserQuery filters a user lookup.
type UserQuery struct {
	// FriendCode matches exactly, in the dashed twelve-digit form.
	FriendCode string
	// Nickname matches case-insensitively and exactly.
	Nickname string
	// IDs matches any of the given user ids.
	//
	// A nil slice means "do not filter on id". A non-nil EMPTY slice means
	// "match nothing", which is not the same thing: the friend-list path builds
	// this slice from a user's friendships, and a user with no friends must get
	// an empty result rather than every account on the server.
	IDs []string
	// Limit caps the result. Zero means no cap.
	Limit int
}

// Store is everything SwitchNet persists.
//
// Implementations must be safe for concurrent use.
// Fest is a Splatfest, as the operator scheduled it.
//
// Only the mechanics: when it runs, where it is published, and which BCAT pack
// carries its content. The team names, banners and idol dialogue live inside
// that pack, not here. internal/toyohr decides what these values mean --
// notably that StartAt has to land on a two-hour boundary -- and this type just
// carries them.
type Fest struct {
	// ID is uppercase, "NXTD-00042"-shaped.
	ID string
	// Resource is the BCAT pack identifier, lowercase.
	Resource string
	// Revision is the pack's 40-hex-character revision.
	Revision string
	// StartAt is when play opens; every other phase derives from it.
	StartAt time.Time
	// Regions the fest is published in.
	Regions []string
	// PackKeys are the hex-encoded AES-128 keys that decrypt this fest's BCAT
	// packs, in the order GetFestDecryptionKey returns them.
	PackKeys []string
	// Enabled is whether the operator has turned this fest on. A scheduled but
	// disabled fest is how a fest is held ready without being announced.
	Enabled bool

	CreatedAt time.Time
	UpdatedAt time.Time
}

type Store interface {
	// Ping reports whether the store is reachable.
	//
	// For health checks. A service whose database has gone away is running and
	// useless, and that is the state worth being able to see from the outside --
	// "the process is up" is already answered by the connection succeeding.
	Ping(ctx context.Context) error

	// ---- users ----

	// CreateUser inserts a new user together with its first device account, in
	// one transaction. A user created this way — the console-driven path,
	// registerUser — cannot be logged into without a device account, so the
	// two are never created separately here. CreateUserWithLogin is the other
	// way a user comes into existence, with login credentials and no device
	// account at all.
	CreateUser(ctx context.Context, u User, da DeviceAccount) error
	// CreateUserWithLogin creates a user with login credentials
	// (LoginUsername/LoginPasswordHash already set on u) and no device
	// account. ErrConflict if the username is taken.
	CreateUserWithLogin(ctx context.Context, u User) error
	// GetUserByLoginUsername looks up a user by their login username.
	// ErrNotFound if none matches.
	GetUserByLoginUsername(ctx context.Context, username string) (User, error)
	// GetUser returns a user by id, or ErrNotFound.
	GetUser(ctx context.Context, id string) (User, error)
	// UpdateUser applies a patch and returns the updated user.
	UpdateUser(ctx context.Context, id string, p UserPatch) (User, error)
	// FindUsers returns the users matching a query.
	FindUsers(ctx context.Context, q UserQuery) ([]User, error)
	// ListUsers returns every user, newest first. Used by the dashboard.
	ListUsers(ctx context.Context, limit int) ([]User, error)

	// ---- devices and device accounts ----

	// UpsertDevice records that a console was seen.
	UpsertDevice(ctx context.Context, d Device) error
	// GetDeviceAccount returns a device account by id, or ErrNotFound. A
	// deleted device account is returned with DeletedAt set rather than hidden,
	// so the caller can tell "deleted" apart from "never existed".
	GetDeviceAccount(ctx context.Context, id string) (DeviceAccount, error)
	// ListDeviceAccounts returns a user's device accounts, excluding deleted ones.
	ListDeviceAccounts(ctx context.Context, userID string) ([]DeviceAccount, error)
	// DeleteDeviceAccount marks a device account deleted.
	DeleteDeviceAccount(ctx context.Context, userID, id string) error
	// ---- splatfests (phase 2) ----

	// UpsertFest creates or replaces a fest by id.
	UpsertFest(ctx context.Context, f Fest) error
	// GetFest returns a fest by id, or ErrNotFound.
	GetFest(ctx context.Context, id string) (Fest, error)
	// ListFests returns every fest, soonest first.
	ListFests(ctx context.Context) ([]Fest, error)
	// DeleteFest removes a fest. Not an error if it does not exist: the
	// dashboard's delete button should be idempotent.
	DeleteFest(ctx context.Context, id string) error

	// ---- devices and device accounts (continued) ----

	// ReassignDeviceAccount moves a device account to a different user,
	// keeping its id and password unchanged. Used only after the caller has
	// already proven it holds that password (see completeLogin in
	// internal/baas/accounts.go) — this changes who a credential belongs to,
	// not who is allowed to present it. ErrNotFound if the device account or
	// the target user does not exist.
	ReassignDeviceAccount(ctx context.Context, deviceAccountID, newUserID string) error

	// UndeleteDeviceAccount clears DeletedAt, letting a soft-deleted device
	// account log in again. ErrNotFound if the device account does not exist
	// or is not currently deleted.
	UndeleteDeviceAccount(ctx context.Context, deviceAccountID string) error

	// RevokeDeviceAccountTokens sets TokensRevokedBefore, invalidating every
	// token minted for this device account before `before`. ErrNotFound if the
	// device account does not exist.
	RevokeDeviceAccountTokens(ctx context.Context, deviceAccountID string, before time.Time) error

	// ---- friend codes ----

	// GetFriendCode returns a user's friend code, or ErrNotFound.
	GetFriendCode(ctx context.Context, userID string) (FriendCode, error)
	// SetFriendCode writes a user's friend code. It returns ErrConflict if the
	// code is already taken by somebody else.
	SetFriendCode(ctx context.Context, fc FriendCode) error

	// ---- friendships ----

	// AddFriendship writes both directions of a friendship and moves both
	// users' friends_updated_at, in one transaction.
	AddFriendship(ctx context.Context, a, b string, at time.Time) error
	// RemoveFriendship removes both directions.
	RemoveFriendship(ctx context.Context, a, b string) error
	// ListFriends returns one user's friendships.
	ListFriends(ctx context.Context, userID string, limit int) ([]Friendship, error)
	// GetFriendship returns one direction, or ErrNotFound.
	GetFriendship(ctx context.Context, userID, friendID string) (Friendship, error)
	// SetFavorite sets the favourite flag on one direction only.
	SetFavorite(ctx context.Context, userID, friendID string, favorite bool) error

	// ---- friend requests ----

	// CreateFriendRequest inserts a request. If a pending request already
	// exists for the same ordered pair it returns that one unchanged, so a
	// console retrying does not fill the inbox with duplicates.
	CreateFriendRequest(ctx context.Context, r FriendRequest) (FriendRequest, error)
	// GetFriendRequest returns a request by id, or ErrNotFound.
	GetFriendRequest(ctx context.Context, id string) (FriendRequest, error)
	// SetFriendRequestState resolves a request.
	SetFriendRequestState(ctx context.Context, id, state string, at time.Time) error
	// ListFriendRequests returns a user's inbox (incoming) or outbox
	// (outgoing). Only pending requests are returned when pendingOnly is set.
	ListFriendRequests(ctx context.Context, userID string, incoming, pendingOnly bool) ([]FriendRequest, error)
	// FindPendingRequest returns a pending request for an ordered pair, or
	// ErrNotFound.
	FindPendingRequest(ctx context.Context, from, to string) (FriendRequest, error)

	// ---- blocks ----

	// AddBlock blocks a user and moves the blocker's blocks_updated_at.
	AddBlock(ctx context.Context, userID, blockedID string, at time.Time) error
	// RemoveBlock unblocks a user.
	RemoveBlock(ctx context.Context, userID, blockedID string) error
	// ListBlocks returns who a user has blocked.
	ListBlocks(ctx context.Context, userID string) ([]Block, error)
	// IsBlockedEitherWay reports whether either user has blocked the other.
	// Friend requests and presence are suppressed in both directions, so both
	// have to be checked.
	IsBlockedEitherWay(ctx context.Context, a, b string) (bool, error)

	// ---- presence ----

	// GetPresence returns a user's presence. A user who has never reported
	// presence gets a zero-value OFFLINE row rather than ErrNotFound, because
	// "never seen" and "offline" are the same thing to a friend list.
	GetPresence(ctx context.Context, userID string) (Presence, error)
	// GetPresences returns presence for several users at once. Users with no
	// row are returned as OFFLINE.
	GetPresences(ctx context.Context, userIDs []string) (map[string]Presence, error)
	// UpdatePresence applies a patch, creating the row if needed.
	UpdatePresence(ctx context.Context, userID string, p PresencePatch) (Presence, error)
	// TouchPresence records that a user made an authenticated request, without
	// changing their reported state.
	TouchPresence(ctx context.Context, userID string, at time.Time) error

	// ---- images ----

	// CreateImage records an uploaded image.
	CreateImage(ctx context.Context, img Image) error
	// GetImage returns an image by id, or ErrNotFound.
	GetImage(ctx context.Context, id string) (Image, error)

	// Close releases the store's resources.
	Close() error
}
