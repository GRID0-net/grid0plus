package baas

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
)

// The BAAS user object.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/BAAS-Server#user-information
//
// Field names and shapes match a real capture. Where GRID0+ has nothing to
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
	// Presence is a pointer, omitted entirely for visSelf: a 2026-08-27 capture
	// of a working login/federation/GET-self response never has a 'presence'
	// key at all — only a friend-visible read of someone else's user object
	// does. Sending it for self is an extra key the console's reader has not
	// been confirmed to tolerate, unlike the missing-key case the doc comment
	// above warns about.
	Presence *PresenceResponse `json:"presence,omitempty"`

	Deleted bool `json:"deleted"`

	BlocksUpdatedAt  int64 `json:"blocksUpdatedAt"`
	FriendsUpdatedAt int64 `json:"friendsUpdatedAt"`
	// NicknameUpdatedAt has no dedicated column: db.User has no nickname-only
	// timestamp, so this reuses UpdatedAt, which already moves whenever the
	// nickname does (just not only then). Omitted for a user whose nickname
	// was never set -- registerUser's response never carries it, the same
	// "not set yet" treatment Birthday gives its own sentinel -- and present
	// otherwise, matching every login/federation/GET-self/friend-view capture
	// example seen so far. A real capture that shows the value itself
	// diverging from UpdatedAt would be the signal to give it its own column.
	NicknameUpdatedAt   int64 `json:"nicknameUpdatedAt,omitempty"`
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
// by GRID0+: there is no Nintendo Account, Twitter or Facebook to link to.
//
// A 2026-09-20 decompile of the account sysmodule confirms a "google" entry
// exists in this same block, shaped identically to AccountLink
// ($.links.google.id, $.links.google.createdAt). Not added: this server has
// no real Google OAuth integration to back it with, and a private server has
// no legitimate way to acquire one -- the same boundary Phase 3's eShop
// draws, not a gap to fill.
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
	Pf        int              `json:"pf"`
	Extras    db.ExtrasByScope `json:"extras"`
	UpdatedAt int64            `json:"updatedAt"`
	LogoutAt  int64            `json:"logoutAt"`
}

// CollectionEnvelope wraps a pageable BAAS list response.
//
// Confirmed by captures at docs/CAPTURES/raw/03-friends.json and
// docs/CAPTURES/raw/04-friend-requests-inbox.json. count is the requested page
// size; itemsPerPage is the number of items actually returned.
type CollectionEnvelope[T any] struct {
	Count        int    `json:"count"`
	ETag         string `json:"etag"`
	Items        []T    `json:"items"`
	ItemsPerPage int    `json:"itemsPerPage"`
}

func newCollection[T any](requested int, items []T) CollectionEnvelope[T] {
	if items == nil {
		items = []T{}
	}
	return CollectionEnvelope[T]{
		Count:        requested,
		ETag:         "",
		Items:        items,
		ItemsPerPage: len(items),
	}
}

// FriendUserResponse is the friend's profile embedded in a friend list entry.
//
// It differs from UserResponse in that birthday, country and links are absent,
// and nicknameUpdatedAt is added. Shape confirmed by the capture at
// docs/CAPTURES/raw/03-friends.json.
type FriendUserResponse struct {
	ID       string `json:"id"`
	ETag     string `json:"etag"`
	Nickname string `json:"nickname"`

	ThumbnailURL  string `json:"thumbnailUrl"`
	Thumbnail2URL string `json:"thumbnail2Url,omitempty"`

	DeviceAccounts []DeviceAccountResponse `json:"deviceAccounts"`

	Permissions db.Permissions   `json:"permissions"`
	Extras      db.ExtrasByScope `json:"extras"`
	Presence    PresenceResponse `json:"presence"`

	Deleted bool `json:"deleted"`

	BlocksUpdatedAt     int64 `json:"blocksUpdatedAt"`
	FriendsUpdatedAt    int64 `json:"friendsUpdatedAt"`
	NicknameUpdatedAt   int64 `json:"nicknameUpdatedAt"`
	ThumbnailUploadedAt int64 `json:"thumbnailUploadedAt,omitempty"`
	CreatedAt           int64 `json:"createdAt"`
	UpdatedAt           int64 `json:"updatedAt"`
}

// FriendResponse is one entry in GET /2.0.0/users/<id>/friends.
//
// It represents the relationship from the caller's perspective. Shape confirmed
// by the capture at docs/CAPTURES/raw/03-friends.json.
type FriendResponse struct {
	UserID     string             `json:"userId"`
	FriendID   string             `json:"friendId"`
	Friend     FriendUserResponse `json:"friend"`
	IsFavorite bool               `json:"isFavorite"`
	// Channels tracks per-channel authorization. In captures this contains
	// {"FRIEND_CODE": {"authorizedAt": <unix>}}. GRID0+ does not track
	// channel authorization separately, so this is always an empty map.
	//
	// A 2026-09-20 decompile of the operator's own console's friends
	// sysmodule found the parser for these channel-name strings, and
	// FRIEND_CODE is one of ten it recognises: NX_FACED, FRIEND_CODE, IN_APP,
	// NINTENDO_ACCOUNT, 3DS, NNID, FACEBOOK, TWITTER, WECHAT, CAMPUS. That
	// confirms the shape (a channel names HOW a friendship was authorized,
	// and FRIEND_CODE means "by exchanging codes") without settling the
	// trigger condition or what authorizedAt represents beyond its name.
	//
	// TODO(capture-needed): settle what triggers a FRIEND_CODE entry and what
	// the authorizedAt timestamp represents. Change: listFriends in friends.go.
	Channels   map[string]any   `json:"channels"`
	Extras     db.ExtrasByScope `json:"extras"`
	FriendNote string           `json:"friendNote"`
	ETag       string           `json:"etag"`
	CreatedAt  int64            `json:"createdAt"`
	UpdatedAt  int64            `json:"updatedAt"`
}

// TODO(capture-needed): extras.self on this type (not FriendUserResponse's own
// extras.self, which is the friend's -- this one, the relationship's) can
// apparently also carry a boolean "isOnlineNotification" key. Found
// 2026-09-20 in a decompile of the operator's own console's friends
// sysmodule: the string "$.extras.self.isOnlineNotification" is a
// JSONPath-style field the client reads out of a response, in the same
// "$.extras.self.*" family as the already-confirmed route:sender/isConfirmed
// keys above -- and it lines up exactly with the friends IPC command
// ChangeFriendOnlineNotificationFlag (per-friend "notify me when this friend
// comes online", distinct from the console's own online-status broadcast).
// Not implemented: docs/CAPTURES/raw/03-friends.json's real, working capture
// has no such key for a normal (default-state) friendship, which is
// consistent with it only appearing once actually toggled away from
// whatever the default is -- but there is no confirmed write path (which
// endpoint persists the toggle, or what the default even is) to build
// against yet. Change: friendUserResponse/listFriends in friends.go, once a
// capture shows a relationship with this flag set.

// FriendRequestResponse is one entry in the inbox or outbox.
//
// The per-item shape was settled 2026-09-22 by decompiling the operator's own
// console's friends sysmodule — the two captures that would have shown it (04,
// 05) both returned empty item lists, which is why it stayed a guess for so
// long. The inbox parser (`0x1cf640`) and the outbox parser (`0x1cfe20`) read
// identical field sets, and it is `senderId`/`receiverId` with `sender.*` and
// `receiver.*` profiles, not `from`/`to`:
//
//	$.items[*].id                $.items[*].state
//	$.items[*].senderId          $.items[*].receiverId
//	$.items[*].sender.nickname   $.items[*].receiver.nickname
//	$.items[*].sender.thumbnailUrl
//	$.items[*].receiver.thumbnailUrl
//	$.items[*].createdAt
//	$.items[*].channels[0]
//	$.items[*].extras.receiver.read
//	$.items[*].extras.sender.route:*
//	$.items[*].extras.senderAndReceiver.route:*
//
// GRID0+ sent `from` and `to`, so every field the client actually reads was
// missing: the friend-requests screen had rows with no name, no picture and no
// ids to act on.
//
// TODO(capture-needed): `extras.receiver.read` is the recipient's unread flag --
// the parser reads it, and this server keeps no read state for a request, so it
// is not sent. Whether the client treats the absent key as unread (the useful
// default) or as read needs a capture with a real pending request in it. Change:
// friendRequestResponse in friends.go.
type FriendRequestResponse struct {
	ID string `json:"id"`
	// SenderID and ReceiverID are always both present: they are ids, not
	// profiles, and the client uses them to know which side is which.
	SenderID   string `json:"senderId"`
	ReceiverID string `json:"receiverId"`
	// Sender and Receiver hold the two profiles, for both parties -- including
	// the caller's own side. See friendRequestResponse's own doc comment in
	// friends.go for why this is unconditional now, not omitted for whichever
	// side is the caller as it used to be.
	Sender   *UserResponse `json:"sender,omitempty"`
	Receiver *UserResponse `json:"receiver,omitempty"`
	// Channels and Extras are read by the decompiled inbox/outbox parsers
	// (see this type's own field-list comment below) but were previously
	// absent from this server's response entirely. See friendRequestResponse
	// for what each is populated with and why.
	Channels []any                     `json:"channels"`
	Extras   map[string]map[string]any `json:"extras"`
	// State's real values are confirmed: a
	// 2026-09-20 decompile of the operator's own console's friends sysmodule
	// found the actual enum -- PENDING, AUTHORIZED, REJECTED, CANCELED,
	// EXPIRED -- and three of these are spelled differently from the
	// pre-capture guess this comment used to carry (ACCEPTED, DENIED,
	// CANCELLED). See requestStateFromWire in friends.go, which now
	// translates the real spelling onto this server's own internal
	// db.RequestAccepted/RequestDenied/RequestCancelled constants; this
	// field always carries this server's spelling, not the console's.
	// EXPIRED is not currently produced anywhere -- nothing here assigns a
	// request that state -- so it never appears in a real response, only in
	// the console's own enum.
	State     string `json:"state"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

// RelationshipResponse is the answer to GET /2.0.0/users/<id>/relationships/<id>.
//
// # The wire shape, settled 2026-09-22 by decompiling the friends sysmodule
//
// The request builder (`FUN_001d5b90`) formats this endpoint's URL and then
// constructs its response object at `FUN_001d5d60`, whose vtable is the table
// at `0x258558`. That table registers exactly two field callbacks:
//
//   - `0x1d5d90` reads `$.isFriend` and `$.isBlocking`, storing each as a
//     boolean flag;
//   - `0x1d5e30` reads `$.sentFriendRequestIds[0]` and reduces it to a third
//     boolean -- it never reads the id itself, only whether the array has a
//     first element.
//
// So the client's entire model of a relationship is three booleans, and these
// three keys are the ones that have to be right. GRID0+ used to send none of
// them, which read as the all-false default: **every user looked like a
// stranger**, including an existing friend, and an outgoing request looked like
// no request at all.
//
// Type, User, IsFavorite and BecameFriendAt are this server's own additions.
// The client ignores keys it does not ask for, and Type in particular is worth
// keeping for GRID0+'s own tooling and tests -- but none of the four is read
// by the console, and "$.becameFriendAt" appears nowhere in the friends
// sysmodule. Do not treat their presence as evidence about the real wire shape.
type RelationshipResponse struct {
	// IsFriend is true when a friendship exists in either direction.
	IsFriend bool `json:"isFriend"`
	// IsBlocking is true when the CALLER blocks the other user -- not when the
	// caller is blocked. The distinction is deliberate: telling a blocked user
	// they are blocked is exactly what a block is meant to prevent, and the
	// all-false pair of IsFriend/IsBlocking is the same state an ordinary
	// stranger produces, so a block stays invisible from the blocked side.
	IsBlocking bool `json:"isBlocking"`
	// SentFriendRequestIDs carries the caller's pending outgoing request ids.
	// The client reads only element zero and only its presence, so one entry is
	// enough; an empty array means "no request outstanding".
	SentFriendRequestIDs []string `json:"sentFriendRequestIds"`

	// Type is NONE, FRIEND, FRIEND_REQUEST_SENT, FRIEND_REQUEST_RECEIVED or
	// BLOCKED. GRID0+'s own, not the console's.
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

// BlockResponse is one entry in GET /1.0.0/users/<id>/blocks.
//
// Shape settled 2026-09-22 by decompiling the operator's own console's friends
// sysmodule (see docs/CAPTURES/friends-sysmodule-decompile.md). The blocks
// response parser (an incremental JSON key callback at import address 0x1b50e0,
// reached from the request builder at 0x1b4c60 that formats this very
// endpoint) reads:
//
//	$.items[*]                                  the array marker
//	$.items[*].targetUserId                     hex-digit string
//	$.items[*].targetUser.nickname
//	$.items[*].targetUser.thumbnailUrl
//	$.items[*].extras.self.reason
//	$.items[*].extras.self.route:*
//	$.items[*].createdAt
//
// Two consequences, both of which this type and listBlocks used to get wrong.
// It reads targetUser, not user -- and it reads $.items[*], which means the
// response has to be an envelope: given a bare top-level array, the array
// marker never matches and the screen is simply empty, with nothing in the
// console's log to say why.
//
// TODO(capture-needed): extras.self.reason is parsed as a string (the parser
// routes it through the same string-to-enum helper its other enum fields use)
// and this server always sends none, which the key callback tolerates. What
// the values mean -- and what the extras.self.route:* block on a *blocked*
// user holds, since a block shows no presence -- needs a capture. Change: the
// entry construction in listBlocks (friendcodes.go).
type BlockResponse struct {
	// TargetUserID is the blocked user's id, a hex-digit string ("%016llx"
	// in the URL path, dec:hex-decoded by the client).
	TargetUserID string `json:"targetUserId"`

	TargetUser *UserResponse `json:"targetUser,omitempty"`

	CreatedAt int64 `json:"createdAt"`
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
// defaultThumb1/defaultThumb2 fill in for a user with no uploaded picture --
// see ensureDefaultAvatar's comment for why this must never be blank. Every
// call site should pass s.defaultAvatarURLOrEmpty(), not a literal "".
func userResponse(now time.Time, u db.User, fc *db.FriendCode, pres db.Presence, das []db.DeviceAccount, vis visibility, defaultThumb1, defaultThumb2 string) UserResponse {
	thumbnailURL := u.ThumbnailURL
	thumbnail2URL := u.Thumbnail2URL
	// A real capture (2026-08-27) of a working account always carries both
	// sizes, even for the "no picture uploaded" case (".../1/default" and
	// ".../2/default") -- GRID0+ has one default image, not two sizes of
	// it, so both fields point at the same relative path under their own
	// '/1/' and '/2/' prefixes rather than leaving the large one blank.
	if thumbnailURL == "" {
		thumbnailURL = defaultThumb1
	}
	if thumbnail2URL == "" {
		thumbnail2URL = defaultThumb2
	}
	out := UserResponse{
		ID:               u.ID,
		ETag:             etagFor(u),
		Nickname:         u.Nickname,
		Country:          u.Country,
		Birthday:         u.Birthday,
		ThumbnailURL:     thumbnailURL,
		Thumbnail2URL:    thumbnail2URL,
		DeviceAccounts:   []DeviceAccountResponse{},
		Links:            LinksResponse{},
		Permissions:      u.Permissions,
		Extras:           scopedExtras(u.Extras, vis),
		Deleted:          u.Deleted,
		BlocksUpdatedAt:  u.BlocksUpdatedAt.Unix(),
		FriendsUpdatedAt: u.FriendsUpdatedAt.Unix(),
		CreatedAt:        u.CreatedAt.Unix(),
		UpdatedAt:        u.UpdatedAt.Unix(),
	}
	if u.Nickname != "" {
		out.NicknameUpdatedAt = u.UpdatedAt.Unix()
	}
	if vis != visSelf {
		p := presenceResponse(pres, vis)
		out.Presence = &p
	}
	// nintendoAccountId is recordNintendoAccountLink's own bookkeeping key,
	// read back by this function to populate Links.NintendoAccount above --
	// it was never meant to reach the client itself, and a 2026-08-27 capture
	// of a working account's extras.self confirmed real Nintendo doesn't send
	// it (only nxAccount, which is a different, deliberately-client-visible
	// key). scopedExtras aliases the stored map rather than copying it, so
	// this copies the 'self' scope before deleting from it -- deleting
	// straight from out.Extras would delete from the stored user too.
	if self, ok := out.Extras[db.ScopeSelf]; ok {
		if _, present := self["nintendoAccountId"]; present {
			cleaned := make(map[string]any, len(self))
			for k, v := range self {
				if k != "nintendoAccountId" {
					cleaned[k] = v
				}
			}
			out.Extras[db.ScopeSelf] = cleaned
		}
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
	// recordNintendoAccountLink stores this in extras; it belongs in the
	// response too, or a console that just did /1.0.0/federation gets a
	// 200 whose links object never reflects the link it just made. Found
	// 2026-08-26: real hardware got exactly that empty links object right
	// after a successful federation call, then gave up (error 2124-3121)
	// without ever calling /1.0.0/users/{id}/link.
	if self, ok := u.Extras[db.ScopeSelf]; ok {
		if naID, ok := self["nintendoAccountId"].(string); ok && naID != "" {
			out.Links.NintendoAccount = &AccountLink{
				ID:        naID,
				CreatedAt: u.UpdatedAt.Unix(),
				UpdatedAt: u.UpdatedAt.Unix(),
			}
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

// friendUserResponse builds the friend sub-object embedded in FriendResponse.
//
// birthday, country and links are absent in the capture; nicknameUpdatedAt is
// added. The db.User type has no separate NicknameUpdatedAt field, so UpdatedAt
// is used as a conservative approximation.
func friendUserResponse(u db.User, pres db.Presence, vis visibility) FriendUserResponse {
	out := FriendUserResponse{
		ID:                u.ID,
		ETag:              etagFor(u),
		Nickname:          u.Nickname,
		ThumbnailURL:      u.ThumbnailURL,
		Thumbnail2URL:     u.Thumbnail2URL,
		DeviceAccounts:    []DeviceAccountResponse{},
		Permissions:       u.Permissions,
		Extras:            scopedExtras(u.Extras, vis),
		Presence:          presenceResponse(pres, vis),
		Deleted:           u.Deleted,
		BlocksUpdatedAt:   u.BlocksUpdatedAt.Unix(),
		FriendsUpdatedAt:  u.FriendsUpdatedAt.Unix(),
		NicknameUpdatedAt: u.UpdatedAt.Unix(),
		CreatedAt:         u.CreatedAt.Unix(),
		UpdatedAt:         u.UpdatedAt.Unix(),
	}
	if u.ThumbnailUploadedAt != nil {
		out.ThumbnailUploadedAt = u.ThumbnailUploadedAt.Unix()
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
		State:  p.State,
		Pf:     1, // platform identifier; always 1 in every captured presence
		Extras: scopedExtras(p.Extras, vis),
	}
	// A zero ReportedAt means the console has never reported. Unix() on a zero
	// time is a large negative number, which the console would read as a
	// timestamp in the year 1, so send 0 instead.
	if !p.ReportedAt.IsZero() {
		out.UpdatedAt = p.ReportedAt.Unix()
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
