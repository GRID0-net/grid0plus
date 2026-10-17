// Package db is GRID0+'s persistence layer.
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
	// ErrConflict means uniqueness or an expected document revision conflicts.
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

	// Provisional marks the user a console's own POST /1.0.0/users had to
	// invent before anyone said who the person is. It works like any user
	// until its console links to a real person's account; then it is
	// deleted (ReassignDeviceAccount). Not counted as a person.
	Provisional bool
	// DiscordUserID is the Discord account that owns this user through the
	// bot. Empty for any user nobody owns, a console's placeholder most of
	// all. Read-only: the ownership itself lives in bot_account_links, and a
	// user belongs to at most one Discord account however many accounts that
	// Discord account owns.
	DiscordUserID string

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

// EmptyExtras returns an extras block with all five scopes present, seeded
// with the default keys a real working account's extras carry on every
// scope, per a 2026-08-27 capture and reconfirmed 2026-09-21 via a live
// mitmdump capture of GRID0+'s own federation response (which showed all
// five scopes as literal empty '{}', diverging from the reference).
//
// This exact seeding was implemented once before (commit d61d206), deployed,
// tested on real hardware, and reverted (b5b32ba) after it did not fix
// 2124-0292 -- that investigation later found 2124-0292's real cause
// elsewhere (nxAccountBlob's header, see accounts.go). Reapplying now since
// it is a real, capture-confirmed content gap independent of that fix, not
// a guess, and the earlier test only ruled it out for 2124-0292 specifically
// -- it was never implicated in, or tested against, the new post-federation
// symptom this capture was taken for.
func EmptyExtras() ExtrasByScope {
	e := make(ExtrasByScope, len(Scopes))
	for _, s := range Scopes {
		e[s] = map[string]any{"playLog": ""}
	}
	e[ScopeSelf]["isMediaUploaded"] = true
	e[ScopeFriends]["isChatActivated"] = true
	e[ScopeFriends]["isChatInvitable"] = true
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
// UGCDocument is one NPLN Ugcstore document. Fields is the document's
// nn.npln.common.MapValue, protobuf-encoded; the store does not look inside.
type UGCDocument struct {
	Name       string
	Owner      string
	Fields     []byte
	CreateTime time.Time
	UpdateTime time.Time
}

// UGCBlob is one Ugcstore attachment: the bytes a console uploaded, and the
// document it is attached to once a commit links it ("" until then). Total
// is the size the uploader declared, -1 while unknown.
type UGCBlob struct {
	ID         string
	Owner      string
	Document   string
	MimeType   string
	Total      int64
	Complete   bool
	Data       []byte
	CreateTime time.Time
}

// UGCDocumentCAS pairs a replacement with its expected prior revision. Nil
// requires absence. A batch commits together, so a grade credit cannot be
// saved without its retry marker (or vice versa).
type UGCDocumentCAS struct {
	Document           UGCDocument
	PreviousUpdateTime *time.Time
}

// NEXRecord is one piece of state a NEX game server keeps for a title: a
// unique id, a Splatfest vote, an uploaded match result. See migration 0021
// for why these share one table.
type NEXRecord struct {
	// Title is the game server ("splatoon2").
	Title string
	// Kind is what the record is ("unique_id", "fest_vote", ...).
	Kind string
	// Key is unique within (Title, Kind).
	Key string
	// Owner is the user id the record belongs to, "" for none.
	Owner string
	// Value is the title's own encoding; the store does not look inside.
	Value     []byte
	UpdatedAt time.Time
}

// NEXRecordQuery selects records of one kind.
type NEXRecordQuery struct {
	Title string
	Kind  string
	// Owner narrows to one user's records when set.
	Owner string
	// Limit bounds the result; zero or anything above DefaultNEXRecordLimit
	// means DefaultNEXRecordLimit.
	Limit int
}

// DefaultNEXRecordLimit bounds ListNEXRecords.
const DefaultNEXRecordLimit = 1000

// NARefreshToken is one Nintendo Account link refresh token. TokenSHA256 is
// the lowercase hex SHA-256 of the token; the token itself is never stored.
type NARefreshToken struct {
	TokenSHA256 string
	UserID      string
	Scope       []string
	ExpiresAt   time.Time
	CreatedAt   time.Time
}

type Friendship struct {
	UserID         string
	FriendID       string
	Favorite       bool
	BecameFriendAt time.Time
	// Extras is what the console has PATCHed onto this direction of the
	// friendship (e.g. extras.self.isConfirmed). Nil or empty when it has
	// set nothing.
	Extras ExtrasByScope
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
	Deleted             *bool
	LoginPasswordHash   *string
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

// Store is everything GRID0+ persists.
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
	// EndAt, when set, is an operator-chosen end of play replacing the
	// standard 48 h one. Zero keeps the standard. An upsert with a zero EndAt
	// keeps the stored one, so rebuilding a fest's packs does not undo it.
	EndAt time.Time
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

// FestEntry is one player's team choice for one Splatfest.
//
// internal/toyohr decides what a tally of these means; this type just carries one.
type FestEntry struct {
	// FestID is uppercase, matching Fest.ID.
	FestID string
	// UserID is the player, from their authenticated session.
	UserID string
	// Team is the team they picked, as the game names it. Not validated against
	// a list: the team names live inside the BCAT pack, so what is valid depends
	// on the pack rather than on anything this layer can know.
	Team string
	// Region they entered from, uppercase.
	Region string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// FestBattle is one completed Splatfest match, recorded once under its referee
// report ID. Only the winning team's identity and mode are needed for the team
// ranking; Ink holds the reported paint points by team for midpoint ranking.
type FestBattle struct {
	FestID, BattleID, Mode, WinningTeam string
	Ink                                 map[string]int64
	FinishedAt                          time.Time
}

// FestBattlePlayer is one player's part in a Splatfest battle, from that
// player's own referee report: each console reports only its own result, so a
// battle's ink is the sum of these rows across its players.
type FestBattlePlayer struct {
	FestID, BattleID, UserID, Team string
	Paint                          int64
}

// PenneToken is a notification token npns obtained from Penne for one local
// account on one console; PushChannel is that token as BAAS learnt it for a
// signed-in user. Together they say where a push to a user goes.
type PenneToken struct {
	Token, PenneID, Account, ApplicationID string
}

type PushChannel struct {
	UserID, DeviceAccountID, DeviceToken string
}

// CoopShift is one Salmon Run rotation slot, as the operator scheduled it.
//
// Only the mechanics, the same split Fest makes: when it runs and what real
// Nintendo stage/weapon/boss ids it carries. Unlike a fest there is no
// separate content pack -- internal/toyohr's CoopShift (the domain type this
// row round-trips to) is what decides what these values mean and validates
// them; this type just carries them. See internal/toyohr/coop_ids.go for
// where the real ids come from.
type CoopShift struct {
	// ID is "<kind>-<start unix seconds>", derived rather than typed --
	// internal/toyohr.CoopShift.ID.
	ID string
	// Kind is "normal" or "big_run".
	Kind string
	// StageID is a real Salmon Run stage id.
	StageID int32
	// Boss is the king salmonid's codename for this shift's Xtrawave, or ""
	// if none is scheduled.
	Boss string
	// Weapons are exactly four real main-weapon ids.
	Weapons []int64
	// KumaWeaponID is the real Grizzco weapon id this shift's boss wave uses.
	KumaWeaponID int64
	// RewardGearID is a gear reward id, or 0 for none.
	RewardGearID int32
	// Rewards are a Big Run's scale-tier reward values; empty for "normal".
	Rewards []int64
	// RewardBorders are a Big Run's gold, silver and bronze high-score
	// borders, in that order; empty for every other kind.
	RewardBorders []int64
	// IsSpecial is a Big Run's BigRunParams.IsSpecial flag.
	IsSpecial bool
	// StartAt is when the shift opens; EndAt is when it closes. Both must
	// land on Splatoon 3's two-hour slot boundary -- enforced in
	// internal/toyohr, where the reason can be explained.
	StartAt time.Time
	EndAt   time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Match is one game session NPLN's matchmaking queue has formed, recorded
// for the dashboard's match history.
//
// The queue itself lives in memory (internal/matchmaking) and does not
// survive a restart; this is the record that does. GRID0+ only ever knows
// a match was FORMED — gameplay is peer-to-peer, so there is no signal for
// when, or whether, one actually starts or ends, and no EndedAt to carry.
type Match struct {
	// ID is the session id internal/matchmaking.Queue assigned.
	ID     string
	Tenant string
	Mode   string
	// HostUserID is the player whose console the others connect to.
	HostUserID string
	PlayerIDs  []string
	// Forced is whether an operator started this match with fewer players
	// than the mode wanted, via the dashboard's matchmaking controls.
	Forced    bool
	CreatedAt time.Time
}

// DefaultMatchListLimit bounds ListMatches when the caller passes zero or a
// number larger than this, the same way internal/baas's page-size caps do —
// the dashboard is the only caller today, and it always asks for a bounded
// page, but the store's contract should not depend on that being true
// forever.
const DefaultMatchListLimit = 200

// Stats is a set of row counts across the main tables, for the dashboard's
// Debug tab. Deleted rows (a soft-deleted user, a soft-deleted device
// account) are counted separately rather than excluded, so the two numbers
// together tell an operator whether "nobody's here" means "empty database"
// or "everyone got deleted".
type Stats struct {
	// Users counts people: ProvisionalUsers, the placeholders consoles
	// create before linking, are counted apart.
	Users                 int
	ProvisionalUsers      int
	DeletedUsers          int
	DeviceAccounts        int
	DeletedDeviceAccounts int
	Devices               int
	Friendships           int
	Fests                 int
	CoopShifts            int
	Matches               int
	Images                int
	SaveDataArchives      int
}

// SaveDataArchive is one OLSC cloud-save backup for a single title on a
// single device (phase 4). Mirrors storage.hac.lp1.scsi.srv.nintendo.net's "save
// data archive" object field-for-field where the shape is confirmed.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/OLSC-Servers#save-data-archive
// See internal/scsi/README.md for which parts of this are confirmed from
// that page and which are this project's own defensible reading.
type SaveDataArchive struct {
	ID string

	// NSAID is the BAAS user id ("nsa_id" in Nintendo's own field name).
	NSAID         string
	ApplicationID string
	DeviceID      string
	DeviceSerial  string

	DataSize    int64
	Description string
	// SeriesID is stored as its exact decimal digit string, not a fixed-
	// width integer: a real value observed on real hardware 2026-09-24
	// (12237957226414712540) exceeds int64's own maximum
	// (9223372036854775807), so it is not signed 64-bit either -- BIGINT
	// (migration 0019, which fixed a smaller INTEGER overflow first) still
	// cannot hold it. Nothing here does arithmetic on it; it is an opaque
	// identifier the console sends and reads back, the same treatment
	// EncodedDigest already gets.
	SeriesID string
	Datatype int

	// Status is "uploading" or "fixed" — see internal/scsi/models.go.
	Status                    string
	AutoBackup                bool
	NumOfPartitions           int
	LaunchRequiredVersion     int
	PreviousSaveDataArchiveID string
	// EncodedDigest is whatever the console sent, stored opaquely: GRID0+
	// never has the console's own encryption key, so it can neither compute
	// nor verify this value.
	EncodedDigest string

	SavedAt time.Time
	// FinishedAt is nil until finish_upload — restore must never see an
	// archive that never finished uploading.
	FinishedAt *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// SaveDataArchiveQuery filters ListSaveDataArchives. NSAID is required —
// every real request is scoped to the caller's own account; ApplicationID
// and DeviceID are optional narrowing filters, matching the "datatype",
// "device_id" and "application_id" query params the wiki documents for GET
// /api/nx/v2/save_data_archives.
type SaveDataArchiveQuery struct {
	NSAID         string
	ApplicationID string
	DeviceID      string
}

// SaveDataArchivePatch applies a partial update to a SaveDataArchive. A nil
// field leaves the column unchanged.
type SaveDataArchivePatch struct {
	Status          *string
	EncodedDigest   *string
	FinishedAt      *time.Time
	DataSize        *int64
	NumOfPartitions *int
}

// ComponentFile is one uploaded/downloadable part of a SaveDataArchive.
//
// Reference: same wiki page, "component file object".
type ComponentFile struct {
	ID        string
	ArchiveID string
	Index     int
	// Datatype is "meta" or "save".
	Datatype string
	// Status is "uploading" or "fixed".
	Status               string
	ArchiveSize          int64
	EncodedArchiveDigest string
	// BlobKey is the key this component's raw bytes are stored under in the
	// configured blob store (internal/scsi.BlobStore) — never exposed to the
	// console directly.
	BlobKey string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ComponentFilePatch applies a partial update to a ComponentFile. A nil field
// leaves the column unchanged.
type ComponentFilePatch struct {
	Status               *string
	ArchiveSize          *int64
	EncodedArchiveDigest *string
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
	// account, and links it to the Discord account in u.DiscordUserID.
	// primary says whether it is that Discord account's main account, the one
	// commands that name no account act on; an empty u.DiscordUserID ignores
	// it. ErrConflict if the username is taken, or if that Discord account
	// already has a main account.
	CreateUserWithLogin(ctx context.Context, u User, primary bool) error
	// GetUserByLoginUsername looks up a user by their login username.
	// ErrNotFound if none matches.
	GetUserByLoginUsername(ctx context.Context, username string) (User, error)
	// GetUserByDiscordID returns the main account of the accounts a Discord
	// account owns. ErrNotFound if it owns none.
	GetUserByDiscordID(ctx context.Context, discordUserID string) (User, error)
	// ListUsersByDiscordID returns every account a Discord account owns, its
	// main account first, then oldest first.
	ListUsersByDiscordID(ctx context.Context, discordUserID string) ([]User, error)
	// GetUserByDiscordLogin returns one of the accounts a Discord account
	// owns, named by its login. ErrNotFound when no account has that login,
	// and ErrNotFound too when it belongs to somebody else: one answer for
	// both, so whether a login exists is not something a stranger can ask.
	GetUserByDiscordLogin(ctx context.Context, discordUserID, username string) (User, error)
	// LinkBotAccount attaches an account to the Discord account that owns it.
	// ErrConflict if it already belongs to a different Discord account, or if
	// primary and that Discord account already has a main account.
	LinkBotAccount(ctx context.Context, discordUserID, userID string, primary bool) error
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
	// CreateDeviceAccountForUser adds a device account to a user that
	// already exists, unlike CreateUser's bundled "new person and their
	// first device account together". The third way a device account comes
	// into being: registerUser's console-driven path makes one directly,
	// CreateUserWithLogin makes a person with none at all, and this is what
	// gives that person one afterward -- baas/accounts.go's
	// deviceAccountForLogin, for a caller with login credentials but no
	// device account of its own to present. da.UserID must already name a
	// real user; this does not check that itself, matching CreateUser's own
	// division of labour with its caller.
	CreateDeviceAccountForUser(ctx context.Context, da DeviceAccount) error
	// GetDeviceAccount returns a device account by id, or ErrNotFound. A
	// deleted device account is returned with DeletedAt set rather than hidden,
	// so the caller can tell "deleted" apart from "never existed".
	GetDeviceAccount(ctx context.Context, id string) (DeviceAccount, error)
	// ListDeviceAccounts returns a user's device accounts, excluding deleted ones.
	ListDeviceAccounts(ctx context.Context, userID string) ([]DeviceAccount, error)
	// ListAllDeviceAccounts returns a user's device accounts, deleted ones
	// included. For the dashboard's Debug tab: undeleting one requires being
	// able to find it in the first place, which ListDeviceAccounts's
	// existing exclusion (correct for the console-facing paths it serves)
	// makes impossible.
	ListAllDeviceAccounts(ctx context.Context, userID string) ([]DeviceAccount, error)
	// DeleteDeviceAccount marks a device account deleted.
	DeleteDeviceAccount(ctx context.Context, userID, id string) error
	// ---- splatfests (phase 2) ----

	// UpsertFest creates or replaces a fest by id.
	UpsertFest(ctx context.Context, f Fest) error
	// GetFest returns a fest by id, or ErrNotFound.
	GetFest(ctx context.Context, id string) (Fest, error)
	// ListFests returns every fest, soonest first.
	ListFests(ctx context.Context) ([]Fest, error)
	// UpsertFestEntry records or changes a player's team choice.
	//
	// An upsert rather than an insert: two rows for one player would count them
	// twice in the vote. Whether changing team is ALLOWED is a rule about fest
	// phases and lives in internal/toyohr, not here.
	UpsertFestEntry(ctx context.Context, e FestEntry) error
	// GetFestEntry returns a player's entry for a fest, or ErrNotFound.
	GetFestEntry(ctx context.Context, festID, userID string) (FestEntry, error)
	// ListFestEntries returns every entry for a fest, oldest first.
	ListFestEntries(ctx context.Context, festID string) ([]FestEntry, error)
	// RecordFestBattle is idempotent: eight consoles may report the same match.
	RecordFestBattle(ctx context.Context, b FestBattle) error
	// ListFestBattles returns each battle with Ink summed from its recorded
	// players when it has any, and the battle row's own Ink otherwise (battles
	// recorded before per-player rows existed).
	ListFestBattles(ctx context.Context, festID string) ([]FestBattle, error)
	// RecordFestBattlePlayer is idempotent per player: a console may report
	// the same match more than once, and each report must count once.
	RecordFestBattlePlayer(ctx context.Context, p FestBattlePlayer) error
	// CreateFestEntry records a player's team only if they have none yet, and
	// returns the entry that stands either way: the first pick is final, and
	// a console and the Discord bot voting at the same moment must not both
	// win. created reports whether e was the one stored.
	CreateFestEntry(ctx context.Context, e FestEntry) (stored FestEntry, created bool, err error)

	// ---- push notifications (Penne) ----

	// SetPenneToken records or replaces a notification token's owner.
	SetPenneToken(ctx context.Context, t PenneToken) error
	// PenneTokens returns the known tokens among tokens; unknown ones are
	// skipped, not an error.
	PenneTokens(ctx context.Context, tokens []string) ([]PenneToken, error)
	// PenneTokensOf returns every token registered by one console (penne id).
	PenneTokensOf(ctx context.Context, penneID string) ([]PenneToken, error)
	// SetPushChannel records or replaces a user's push channel on a device.
	SetPushChannel(ctx context.Context, c PushChannel) error
	// PushChannels returns every push channel of the given users.
	PushChannels(ctx context.Context, userIDs []string) ([]PushChannel, error)
	// SetPenneTicket records a login ticket issued to a penne id.
	SetPenneTicket(ctx context.Context, ticket, penneID string, expires time.Time) error
	// PenneTicket returns the penne id a ticket was issued to, or ErrNotFound
	// for an unknown or expired one.
	PenneTicket(ctx context.Context, ticket string, now time.Time) (string, error)

	// DeleteFest removes a fest. Not an error if it does not exist: the
	// dashboard's delete button should be idempotent.
	DeleteFest(ctx context.Context, id string) error

	// ---- Salmon Run / Big Run schedule (phase 2) ----

	// UpsertCoopShift creates or replaces a coop shift by id.
	UpsertCoopShift(ctx context.Context, c CoopShift) error
	// GetCoopShift returns a coop shift by id, or ErrNotFound.
	GetCoopShift(ctx context.Context, id string) (CoopShift, error)
	// ListCoopShifts returns every coop shift, soonest first.
	ListCoopShifts(ctx context.Context) ([]CoopShift, error)
	// DeleteCoopShift removes a coop shift. Not an error if it does not
	// exist: the same idempotent-delete reasoning as DeleteFest.
	DeleteCoopShift(ctx context.Context, id string) error

	// ---- match history (phase 2) ----

	// CreateMatch records a formed game session. Called once, at formation —
	// there is nothing to update afterward, since nothing tells this server
	// when a match ends.
	CreateMatch(ctx context.Context, m Match) error
	// ListMatches returns the most recent matches, newest first, capped at
	// limit.
	ListMatches(ctx context.Context, limit int) ([]Match, error)
	// ListMatchesForTenant is ListMatches for one game's tenant. Filtered
	// here rather than by the caller, so that one busy game cannot push the
	// other's matches past the limit.
	ListMatchesForTenant(ctx context.Context, tenant string, limit int) ([]Match, error)

	// ---- devices and device accounts (continued) ----

	// ReassignDeviceAccount moves a device account to a different user,
	// keeping its id and password unchanged. Used only after the caller has
	// already proven it holds that password (see completeLogin in
	// internal/baas/accounts.go) — this changes who a credential belongs to,
	// not who is allowed to present it. ErrNotFound if the device account or
	// the target user does not exist.
	//
	// If the user it moves away from is provisional and has no device
	// account left, that user is deleted in the same transaction: it was
	// only ever this console's placeholder.
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
	// SetFriendshipExtras replaces the extras on one direction only.
	SetFriendshipExtras(ctx context.Context, userID, friendID string, extras ExtrasByScope) error

	// CreateNARefreshToken stores a refresh token, and drops any that expired
	// before t.CreatedAt.
	CreateNARefreshToken(ctx context.Context, t NARefreshToken) error
	// GetNARefreshToken returns the token with this hash, or ErrNotFound if
	// there is none or it expired at or before now.
	GetNARefreshToken(ctx context.Context, tokenSHA256 string, now time.Time) (NARefreshToken, error)

	// GetUGCDocument returns a Ugcstore document by name, or ErrNotFound.
	GetUGCDocument(ctx context.Context, name string) (UGCDocument, error)
	// PutUGCDocument creates or replaces a Ugcstore document.
	PutUGCDocument(ctx context.Context, d UGCDocument) error
	CompareAndPutUGCDocuments(ctx context.Context, changes []UGCDocumentCAS) error
	// ListUGCDocuments returns every Ugcstore document whose name starts with
	// prefix, newest name first.
	ListUGCDocuments(ctx context.Context, prefix string) ([]UGCDocument, error)
	// PutUGCBlob creates or replaces an attachment's bytes and state. It does
	// not change which document the blob is attached to.
	PutUGCBlob(ctx context.Context, b UGCBlob) error
	// GetUGCBlob returns an attachment by id, or ErrNotFound.
	GetUGCBlob(ctx context.Context, id string) (UGCBlob, error)
	// GetUGCDocumentBlob returns the attachment of a document, or ErrNotFound.
	GetUGCDocumentBlob(ctx context.Context, document string) (UGCBlob, error)
	// AttachUGCBlob makes blob id the document's only attachment, deleting
	// any it had before. ErrNotFound if the blob does not exist.
	AttachUGCBlob(ctx context.Context, id, document string) error
	// DetachUGCDocumentBlob deletes a document's attachment, if it has one.
	DetachUGCDocumentBlob(ctx context.Context, document string) error
	// PurgeUnattachedUGCBlobs deletes blobs never attached that were created
	// before the given time.
	PurgeUnattachedUGCBlobs(ctx context.Context, before time.Time) error
	// DeleteUGCDocument removes a Ugcstore document, or returns ErrNotFound.
	DeleteUGCDocument(ctx context.Context, name string) error

	// PutNEXRecord creates or replaces a record.
	PutNEXRecord(ctx context.Context, r NEXRecord) error
	// GetNEXRecord returns a record, or ErrNotFound.
	GetNEXRecord(ctx context.Context, title, kind, key string) (NEXRecord, error)
	// ListNEXRecords returns a kind's records, most recently updated first
	// (then by key), optionally only one owner's.
	ListNEXRecords(ctx context.Context, q NEXRecordQuery) ([]NEXRecord, error)
	// DeleteNEXRecord removes a record, or returns ErrNotFound.
	DeleteNEXRecord(ctx context.Context, title, kind, key string) error

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
	// IsBlocking reports whether blocker has blocked blocked -- one direction
	// only. It is not a substitute for IsBlockedEitherWay, which is what
	// suppression uses; this is for the one place a response has to describe
	// the caller's own action rather than the pair's combined state. See
	// RelationshipResponse.IsBlocking.
	IsBlocking(ctx context.Context, blocker, blocked string) (bool, error)

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

	// ---- OLSC save data archives (phase 4) ----

	// CreateSaveDataArchive inserts a new archive, in "uploading" status if
	// a.Status is left empty.
	CreateSaveDataArchive(ctx context.Context, a SaveDataArchive) (SaveDataArchive, error)
	// GetSaveDataArchive returns an archive by id, or ErrNotFound.
	GetSaveDataArchive(ctx context.Context, id string) (SaveDataArchive, error)
	// ListSaveDataArchives returns archives matching q, most recently saved
	// first.
	ListSaveDataArchives(ctx context.Context, q SaveDataArchiveQuery) ([]SaveDataArchive, error)
	// UpdateSaveDataArchive applies a patch and returns the updated archive,
	// or ErrNotFound.
	UpdateSaveDataArchive(ctx context.Context, id string, p SaveDataArchivePatch) (SaveDataArchive, error)
	// DeleteSaveDataArchive removes an archive and its component files.
	DeleteSaveDataArchive(ctx context.Context, id string) error

	// CreateComponentFile inserts a new component file, in "uploading"
	// status if f.Status is left empty.
	CreateComponentFile(ctx context.Context, f ComponentFile) (ComponentFile, error)
	// GetComponentFile returns a component file by id, or ErrNotFound.
	GetComponentFile(ctx context.Context, id string) (ComponentFile, error)
	// ListComponentFiles returns an archive's component files, by index.
	ListComponentFiles(ctx context.Context, archiveID string) ([]ComponentFile, error)
	// UpdateComponentFile applies a patch and returns the updated component
	// file, or ErrNotFound.
	UpdateComponentFile(ctx context.Context, id string, p ComponentFilePatch) (ComponentFile, error)

	// ---- operator debugging ----

	// Stats returns row counts across the main tables, for the dashboard's
	// Debug tab. A cheap, approximate sanity check ("did `make seed` actually
	// write anything") rather than a metrics endpoint — nothing here is
	// expected to be called often enough to need to be fast.
	Stats(ctx context.Context) (Stats, error)

	// Close releases the store's resources.
	Close() error
}
