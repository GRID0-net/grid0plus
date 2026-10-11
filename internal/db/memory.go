package db

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Memory is an in-memory Store.
//
// It exists so the BAAS handler tests can exercise the real handlers with no
// database at all: they are the tests that matter most (they are what a console
// actually talks to) and they should run in milliseconds on any machine, in CI,
// with no container.
//
// It is not a second implementation of the business logic. All of that lives in
// internal/baas; this only stores and retrieves rows, and postgres_test.go runs
// the same store contract against PostgreSQL to keep the two honest.
type Memory struct {
	mu sync.RWMutex

	users          map[string]User
	devices        map[string]Device
	deviceAccounts map[string]DeviceAccount
	friendCodes    map[string]FriendCode
	// friendships is keyed by user id then friend id: one entry per direction.
	friendships    map[string]map[string]Friendship
	friendRequests map[string]FriendRequest
	blocks         map[string]map[string]Block
	presence       map[string]Presence
	images         map[string]Image
	fests          map[string]Fest
	// festEntries is keyed by fest id then user id: one entry per account per
	// fest, the same shape friendships uses for its own per-pair map.
	festEntries map[string]map[string]FestEntry
	festBattles map[string]map[string]FestBattle
	// festBattlePlayers is keyed by fest, then battle/user.
	festBattlePlayers map[string]map[string]FestBattlePlayer

	penneTokens  map[string]PenneToken
	pushChannels map[string]PushChannel // user/device account
	penneTickets map[string]penneTicket
	coopShifts   map[string]CoopShift
	// matches is append-only, ordered by insertion (which is creation order,
	// since CreateMatch is the only writer and never rewrites an existing id).
	matches []Match

	naRefreshTokens map[string]NARefreshToken
	ugcDocuments    map[string]UGCDocument
	ugcBlobs        map[string]UGCBlob
	nexRecords      map[nexRecordKey]NEXRecord

	// botLinks is keyed by user id, so an account belongs to at most one
	// Discord account; a Discord account can own many.
	botLinks map[string]botLink

	saveDataArchives map[string]SaveDataArchive
	// componentFiles is keyed by archive id then component file id, the same
	// shape friendships/festEntries use for their own per-parent maps.
	componentFiles map[string]map[string]ComponentFile
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{
		users:             map[string]User{},
		devices:           map[string]Device{},
		deviceAccounts:    map[string]DeviceAccount{},
		friendCodes:       map[string]FriendCode{},
		fests:             map[string]Fest{},
		festEntries:       map[string]map[string]FestEntry{},
		festBattles:       map[string]map[string]FestBattle{},
		festBattlePlayers: map[string]map[string]FestBattlePlayer{},
		penneTokens:       map[string]PenneToken{},
		pushChannels:      map[string]PushChannel{},
		penneTickets:      map[string]penneTicket{},
		coopShifts:        map[string]CoopShift{},
		friendships:       map[string]map[string]Friendship{},
		friendRequests:    map[string]FriendRequest{},
		blocks:            map[string]map[string]Block{},
		presence:          map[string]Presence{},
		images:            map[string]Image{},

		naRefreshTokens: map[string]NARefreshToken{},
		ugcDocuments:    map[string]UGCDocument{},
		ugcBlobs:        map[string]UGCBlob{},
		nexRecords:      map[nexRecordKey]NEXRecord{},

		botLinks: map[string]botLink{},

		saveDataArchives: map[string]SaveDataArchive{},
		componentFiles:   map[string]map[string]ComponentFile{},
	}
}

// Close is a no-op.
func (m *Memory) Close() error { return nil }

// Ping always succeeds: the in-memory store is reachable exactly as long as the
// process using it is.
func (m *Memory) Ping(context.Context) error { return nil }

// ---- users ---------------------------------------------------------------

// CreateUser inserts a user and its first device account.
func (m *Memory) CreateUser(_ context.Context, u User, da DeviceAccount) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.users[u.ID]; ok {
		return fmt.Errorf("%w: user %s exists", ErrConflict, u.ID)
	}
	if _, ok := m.deviceAccounts[da.ID]; ok {
		return fmt.Errorf("%w: device account %s exists", ErrConflict, da.ID)
	}
	if u.Extras == nil {
		u.Extras = EmptyExtras()
	}
	da.UserID = u.ID
	m.users[u.ID] = u
	m.deviceAccounts[da.ID] = da
	// ReportedAt is left zero: the console has not reported anything yet. That
	// is distinct from a reported OFFLINE, and the read path relies on the
	// difference.
	m.presence[u.ID] = Presence{
		UserID: u.ID, State: StateOffline, Extras: EmptyExtras(),
		LastSeenAt: u.CreatedAt,
	}
	return nil
}

// CreateUserWithLogin inserts a user with login credentials and no device
// account, and links it to the Discord account in u.DiscordUserID.
func (m *Memory) CreateUserWithLogin(_ context.Context, u User, primary bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.users[u.ID]; ok {
		return fmt.Errorf("%w: user %s exists", ErrConflict, u.ID)
	}
	for _, existing := range m.users {
		if existing.LoginUsername != "" && strings.EqualFold(existing.LoginUsername, u.LoginUsername) {
			return fmt.Errorf("%w: login username %s is taken", ErrConflict, u.LoginUsername)
		}
	}
	if u.DiscordUserID != "" {
		if err := m.checkLinkLocked("", u.DiscordUserID, primary); err != nil {
			return err
		}
	}
	if u.Extras == nil {
		u.Extras = EmptyExtras()
	}
	m.users[u.ID] = u
	if u.DiscordUserID != "" {
		m.botLinks[u.ID] = botLink{discordUserID: u.DiscordUserID, primary: primary}
	}
	m.presence[u.ID] = Presence{
		UserID: u.ID, State: StateOffline, Extras: EmptyExtras(),
		LastSeenAt: u.CreatedAt,
	}
	return nil
}

// botLink is one row of bot_account_links: which Discord account owns this
// user, and whether it is that account's main one.
type botLink struct {
	discordUserID string
	primary       bool
}

// checkLinkLocked applies the two uniqueness rules bot_account_links holds:
// an account belongs to at most one Discord account, and a Discord account has
// at most one main account. userID is the account being linked, "" when it
// does not exist yet, which is how a brand new account passes the first check.
func (m *Memory) checkLinkLocked(userID, discordUserID string, primary bool) error {
	for existingUser, link := range m.botLinks {
		if existingUser == userID {
			continue
		}
		if link.discordUserID == discordUserID && primary && link.primary {
			return fmt.Errorf("%w: discord user %s already has a main account", ErrConflict, discordUserID)
		}
	}
	return nil
}

// CreateDeviceAccountForUser adds a device account to an existing user.
func (m *Memory) CreateDeviceAccountForUser(_ context.Context, da DeviceAccount) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.deviceAccounts[da.ID]; ok {
		return fmt.Errorf("%w: device account %s exists", ErrConflict, da.ID)
	}
	m.deviceAccounts[da.ID] = da
	return nil
}

// GetUserByLoginUsername looks up a user by their login username.
func (m *Memory) GetUserByLoginUsername(_ context.Context, username string) (User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, u := range m.users {
		if u.LoginUsername != "" && strings.EqualFold(u.LoginUsername, username) {
			return m.cloneUser(u), nil
		}
	}
	return User{}, ErrNotFound
}

// GetUserByDiscordID returns the main account of the accounts a Discord
// account owns.
func (m *Memory) GetUserByDiscordID(_ context.Context, discordUserID string) (User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if discordUserID == "" {
		return User{}, ErrNotFound
	}
	for userID, link := range m.botLinks {
		if link.discordUserID == discordUserID && link.primary {
			return m.cloneUser(m.users[userID]), nil
		}
	}
	return User{}, ErrNotFound
}

// ListUsersByDiscordID returns every account a Discord account owns, its main
// account first, then oldest.
func (m *Memory) ListUsersByDiscordID(_ context.Context, discordUserID string) ([]User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []User
	for userID, link := range m.botLinks {
		if link.discordUserID != discordUserID {
			continue
		}
		if u, ok := m.users[userID]; ok {
			out = append(out, m.cloneUser(u))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		iPrimary := m.botLinks[out[i].ID].primary
		jPrimary := m.botLinks[out[j].ID].primary
		if iPrimary != jPrimary {
			return iPrimary
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// GetUserByDiscordLogin returns one of the accounts a Discord account owns,
// named by its login. An account belonging to somebody else is not one this
// Discord account owns, so the link does the refusing, and "no such login" and
// "not yours" come out as the same ErrNotFound.
func (m *Memory) GetUserByDiscordLogin(_ context.Context, discordUserID, username string) (User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if discordUserID == "" || username == "" {
		return User{}, ErrNotFound
	}
	for userID, link := range m.botLinks {
		if link.discordUserID != discordUserID {
			continue
		}
		if u, ok := m.users[userID]; ok && u.LoginUsername != "" && strings.EqualFold(u.LoginUsername, username) {
			return m.cloneUser(u), nil
		}
	}
	return User{}, ErrNotFound
}

// LinkBotAccount attaches an account to the Discord account that owns it.
func (m *Memory) LinkBotAccount(_ context.Context, discordUserID, userID string, primary bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if discordUserID == "" || userID == "" {
		return fmt.Errorf("%w: a link needs a Discord account and a user", ErrNotFound)
	}
	if _, ok := m.users[userID]; !ok {
		return fmt.Errorf("%w: user %s", ErrNotFound, userID)
	}
	if existing, ok := m.botLinks[userID]; ok {
		if existing.discordUserID != discordUserID {
			return fmt.Errorf("%w: user %s belongs to another Discord account", ErrConflict, userID)
		}
		// Already this Discord account's. Re-linking the same pair is what
		// /link does when somebody runs it twice, and it is not an error.
		if !primary || existing.primary {
			return nil
		}
	}
	if err := m.checkLinkLocked(userID, discordUserID, primary); err != nil {
		return err
	}
	m.botLinks[userID] = botLink{discordUserID: discordUserID, primary: primary}
	return nil
}

// GetUser returns a user by id.
func (m *Memory) GetUser(_ context.Context, id string) (User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	u, ok := m.users[id]
	if !ok {
		return User{}, ErrNotFound
	}
	return m.cloneUser(u), nil
}

// UpdateUser applies a patch.
func (m *Memory) UpdateUser(_ context.Context, id string, p UserPatch) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	u, ok := m.users[id]
	if !ok {
		return User{}, ErrNotFound
	}
	if p.Nickname != nil {
		u.Nickname = *p.Nickname
	}
	if p.Country != nil {
		u.Country = *p.Country
	}
	if p.Birthday != nil {
		u.Birthday = *p.Birthday
	}
	if p.ThumbnailURL != nil {
		u.ThumbnailURL = *p.ThumbnailURL
	}
	if p.Thumbnail2URL != nil {
		u.Thumbnail2URL = *p.Thumbnail2URL
	}
	if p.ThumbnailUploadedAt != nil {
		t := *p.ThumbnailUploadedAt
		u.ThumbnailUploadedAt = &t
	}
	if p.Permissions != nil {
		u.Permissions = *p.Permissions
	}
	if p.Extras != nil {
		u.Extras = cloneExtras(*p.Extras)
	}
	if p.Deleted != nil {
		u.Deleted = *p.Deleted
	}
	if p.LoginPasswordHash != nil {
		u.LoginPasswordHash = *p.LoginPasswordHash
	}
	u.UpdatedAt = time.Now().UTC()
	m.users[id] = u
	return m.cloneUser(u), nil
}

// FindUsers returns the users matching a query.
func (m *Memory) FindUsers(_ context.Context, q UserQuery) ([]User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// A nil IDs slice means "do not filter on id"; a non-nil empty one means
	// "match nothing". PostgreSQL's `id = ANY($1)` already behaves this way for
	// an empty array, and the two implementations have to agree.
	var wantIDs map[string]bool
	if q.IDs != nil {
		wantIDs = make(map[string]bool, len(q.IDs))
		for _, id := range q.IDs {
			wantIDs[id] = true
		}
	}

	var out []User
	for id, u := range m.users {
		if u.Deleted {
			continue
		}
		if q.FriendCode != "" {
			fc, ok := m.friendCodes[id]
			if !ok || fc.Code != q.FriendCode {
				continue
			}
		}
		if q.Nickname != "" && !strings.EqualFold(u.Nickname, q.Nickname) {
			continue
		}
		if wantIDs != nil && !wantIDs[id] {
			continue
		}
		out = append(out, m.cloneUser(u))
	}
	sortUsersNewestFirst(out)
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// ListUsers returns every user, newest first.
func (m *Memory) ListUsers(_ context.Context, limit int) ([]User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []User
	for _, u := range m.users {
		if !u.Deleted {
			out = append(out, m.cloneUser(u))
		}
	}
	sortUsersNewestFirst(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func sortUsersNewestFirst(us []User) {
	sort.Slice(us, func(i, j int) bool {
		if us[i].CreatedAt.Equal(us[j].CreatedAt) {
			return us[i].ID < us[j].ID
		}
		return us[i].CreatedAt.After(us[j].CreatedAt)
	})
}

// ---- devices and device accounts ----------------------------------------

// UpsertDevice records that a console was seen.
func (m *Memory) UpsertDevice(_ context.Context, d Device) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.devices[d.ID]; ok {
		d.FirstSeenAt = existing.FirstSeenAt
	}
	m.devices[d.ID] = d
	return nil
}

// GetDeviceAccount returns a device account by id.
func (m *Memory) GetDeviceAccount(_ context.Context, id string) (DeviceAccount, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	da, ok := m.deviceAccounts[id]
	if !ok {
		return DeviceAccount{}, ErrNotFound
	}
	return da, nil
}

// ListDeviceAccounts returns a user's live device accounts.
func (m *Memory) ListDeviceAccounts(_ context.Context, userID string) ([]DeviceAccount, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []DeviceAccount
	for _, da := range m.deviceAccounts {
		if da.UserID == userID && da.DeletedAt == nil {
			out = append(out, da)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// ListAllDeviceAccounts returns a user's device accounts, deleted ones
// included -- see the interface doc for why ListDeviceAccounts's own
// exclusion does not serve the Debug tab.
func (m *Memory) ListAllDeviceAccounts(_ context.Context, userID string) ([]DeviceAccount, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []DeviceAccount
	for _, da := range m.deviceAccounts {
		if da.UserID == userID {
			out = append(out, da)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// DeleteDeviceAccount marks a device account deleted.
func (m *Memory) DeleteDeviceAccount(_ context.Context, userID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	da, ok := m.deviceAccounts[id]
	if !ok || da.UserID != userID || da.DeletedAt != nil {
		return ErrNotFound
	}
	now := time.Now().UTC()
	da.DeletedAt = &now
	da.UpdatedAt = now
	m.deviceAccounts[id] = da
	return nil
}

// ReassignDeviceAccount moves a device account to a different user.
func (m *Memory) ReassignDeviceAccount(_ context.Context, deviceAccountID, newUserID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	da, ok := m.deviceAccounts[deviceAccountID]
	if !ok {
		return ErrNotFound
	}
	if _, ok := m.users[newUserID]; !ok {
		return ErrNotFound
	}
	oldUserID := da.UserID
	da.UserID = newUserID
	da.UpdatedAt = time.Now().UTC()
	m.deviceAccounts[deviceAccountID] = da
	if old, ok := m.users[oldUserID]; ok && old.Provisional && oldUserID != newUserID {
		for _, other := range m.deviceAccounts {
			if other.UserID == oldUserID {
				return nil
			}
		}
		m.deleteUserLocked(oldUserID)
	}
	return nil
}

// deleteUserLocked removes a user and everything Postgres's ON DELETE
// CASCADE foreign keys would remove with it.
func (m *Memory) deleteUserLocked(id string) {
	delete(m.users, id)
	delete(m.botLinks, id)
	delete(m.friendCodes, id)
	delete(m.presence, id)
	delete(m.friendships, id)
	for _, byFriend := range m.friendships {
		delete(byFriend, id)
	}
	delete(m.blocks, id)
	for _, byBlocked := range m.blocks {
		delete(byBlocked, id)
	}
	for rid, r := range m.friendRequests {
		if r.FromUserID == id || r.ToUserID == id {
			delete(m.friendRequests, rid)
		}
	}
	for daID, da := range m.deviceAccounts {
		if da.UserID == id {
			delete(m.deviceAccounts, daID)
		}
	}
	for imgID, img := range m.images {
		if img.OwnerID == id {
			delete(m.images, imgID)
		}
	}
	for tok, t := range m.naRefreshTokens {
		if t.UserID == id {
			delete(m.naRefreshTokens, tok)
		}
	}
	for _, entries := range m.festEntries {
		delete(entries, id)
	}
}

// UndeleteDeviceAccount clears DeletedAt.
func (m *Memory) UndeleteDeviceAccount(_ context.Context, deviceAccountID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	da, ok := m.deviceAccounts[deviceAccountID]
	if !ok || da.DeletedAt == nil {
		return ErrNotFound
	}
	da.DeletedAt = nil
	da.UpdatedAt = time.Now().UTC()
	m.deviceAccounts[deviceAccountID] = da
	return nil
}

// RevokeDeviceAccountTokens invalidates every token minted before `before`.
func (m *Memory) RevokeDeviceAccountTokens(_ context.Context, deviceAccountID string, before time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	da, ok := m.deviceAccounts[deviceAccountID]
	if !ok {
		return ErrNotFound
	}
	t := before
	da.TokensRevokedBefore = &t
	da.UpdatedAt = time.Now().UTC()
	m.deviceAccounts[deviceAccountID] = da
	return nil
}

// ---- friend codes --------------------------------------------------------

// GetFriendCode returns a user's friend code.
func (m *Memory) GetFriendCode(_ context.Context, userID string) (FriendCode, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	fc, ok := m.friendCodes[userID]
	if !ok {
		return FriendCode{}, ErrNotFound
	}
	return fc, nil
}

// SetFriendCode writes a user's friend code.
func (m *Memory) SetFriendCode(_ context.Context, fc FriendCode) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for uid, existing := range m.friendCodes {
		if existing.Code == fc.Code && uid != fc.UserID {
			return fmt.Errorf("%w: friend code %s is taken", ErrConflict, fc.Code)
		}
	}
	if prev, ok := m.friendCodes[fc.UserID]; ok {
		fc.CreatedAt = prev.CreatedAt
	}
	m.friendCodes[fc.UserID] = fc
	return nil
}

// ---- friendships ---------------------------------------------------------

// AddFriendship writes both directions.
func (m *Memory) AddFriendship(_ context.Context, a, b string, at time.Time) error {
	if a == b {
		return fmt.Errorf("%w: a user cannot befriend themselves", ErrConflict)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, id := range []string{a, b} {
		if _, ok := m.users[id]; !ok {
			return fmt.Errorf("%w: user %s", ErrNotFound, id)
		}
	}
	m.addDirectionLocked(a, b, at)
	m.addDirectionLocked(b, a, at)
	for _, id := range []string{a, b} {
		u := m.users[id]
		u.FriendsUpdatedAt = at
		u.UpdatedAt = at
		m.users[id] = u
	}
	return nil
}

func (m *Memory) addDirectionLocked(from, to string, at time.Time) {
	if m.friendships[from] == nil {
		m.friendships[from] = map[string]Friendship{}
	}
	if _, ok := m.friendships[from][to]; ok {
		return
	}
	m.friendships[from][to] = Friendship{UserID: from, FriendID: to, BecameFriendAt: at}
}

// RemoveFriendship removes both directions.
func (m *Memory) RemoveFriendship(_ context.Context, a, b string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, ok1 := m.friendships[a][b]
	_, ok2 := m.friendships[b][a]
	if !ok1 && !ok2 {
		return ErrNotFound
	}
	delete(m.friendships[a], b)
	delete(m.friendships[b], a)

	now := time.Now().UTC()
	for _, id := range []string{a, b} {
		if u, ok := m.users[id]; ok {
			u.FriendsUpdatedAt = now
			u.UpdatedAt = now
			m.users[id] = u
		}
	}
	return nil
}

// ListFriends returns one user's friendships.
func (m *Memory) ListFriends(_ context.Context, userID string, limit int) ([]Friendship, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []Friendship
	for _, f := range m.friendships[userID] {
		f.Extras = cloneFriendshipExtras(f.Extras)
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Favorite != out[j].Favorite {
			return out[i].Favorite
		}
		if out[i].BecameFriendAt.Equal(out[j].BecameFriendAt) {
			return out[i].FriendID < out[j].FriendID
		}
		return out[i].BecameFriendAt.Before(out[j].BecameFriendAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// GetFriendship returns one direction of a friendship.
func (m *Memory) GetFriendship(_ context.Context, userID, friendID string) (Friendship, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	f, ok := m.friendships[userID][friendID]
	if !ok {
		return Friendship{}, ErrNotFound
	}
	f.Extras = cloneFriendshipExtras(f.Extras)
	return f, nil
}

// SetFriendshipExtras replaces the extras on one direction only.
func (m *Memory) SetFriendshipExtras(_ context.Context, userID, friendID string, extras ExtrasByScope) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	f, ok := m.friendships[userID][friendID]
	if !ok {
		return ErrNotFound
	}
	f.Extras = cloneFriendshipExtras(extras)
	m.friendships[userID][friendID] = f
	return nil
}

// SetFavorite sets the favourite flag on one direction only.
func (m *Memory) SetFavorite(_ context.Context, userID, friendID string, favorite bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	f, ok := m.friendships[userID][friendID]
	if !ok {
		return ErrNotFound
	}
	f.Favorite = favorite
	m.friendships[userID][friendID] = f
	return nil
}

// ---- NPLN Ugcstore documents ----------------------------------------------

// GetUGCDocument returns a Ugcstore document by name, or ErrNotFound.
func (m *Memory) GetUGCDocument(_ context.Context, name string) (UGCDocument, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.ugcDocuments[name]
	if !ok {
		return UGCDocument{}, ErrNotFound
	}
	d.Fields = append([]byte(nil), d.Fields...)
	return d, nil
}

// PutUGCDocument creates or replaces a Ugcstore document.
func (m *Memory) PutUGCDocument(_ context.Context, d UGCDocument) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d.Fields = append([]byte{}, d.Fields...)
	m.ugcDocuments[d.Name] = d
	return nil
}

// CompareAndPutUGCDocuments commits the entire batch only if every revision matches.
func (m *Memory) CompareAndPutUGCDocuments(_ context.Context, changes []UGCDocumentCAS) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]bool{}
	for _, change := range changes {
		name := change.Document.Name
		old, exists := m.ugcDocuments[name]
		if seen[name] || (change.PreviousUpdateTime == nil && exists) ||
			(change.PreviousUpdateTime != nil && (!exists || !old.UpdateTime.Equal(*change.PreviousUpdateTime))) {
			return ErrConflict
		}
		seen[name] = true
	}
	for _, change := range changes {
		d := change.Document
		d.Fields = append([]byte(nil), d.Fields...)
		m.ugcDocuments[d.Name] = d
	}
	return nil
}

// ListUGCDocuments returns every document whose name starts with prefix,
// newest name first.
func (m *Memory) ListUGCDocuments(_ context.Context, prefix string) ([]UGCDocument, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []UGCDocument
	for name, d := range m.ugcDocuments {
		if strings.HasPrefix(name, prefix) {
			d.Fields = append([]byte(nil), d.Fields...)
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}

// DeleteUGCDocument removes a Ugcstore document, or returns ErrNotFound.
func (m *Memory) DeleteUGCDocument(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.ugcDocuments[name]; !ok {
		return ErrNotFound
	}
	delete(m.ugcDocuments, name)
	return nil
}

// ---- NEX game-server records ----------------------------------------------

type nexRecordKey struct{ title, kind, key string }

func cloneNEXRecord(r NEXRecord) NEXRecord {
	r.Value = append([]byte{}, r.Value...)
	return r
}

// PutNEXRecord creates or replaces a record.
func (m *Memory) PutNEXRecord(_ context.Context, r NEXRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nexRecords[nexRecordKey{r.Title, r.Kind, r.Key}] = cloneNEXRecord(r)
	return nil
}

// GetNEXRecord returns a record, or ErrNotFound.
func (m *Memory) GetNEXRecord(_ context.Context, title, kind, key string) (NEXRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.nexRecords[nexRecordKey{title, kind, key}]
	if !ok {
		return NEXRecord{}, ErrNotFound
	}
	return cloneNEXRecord(r), nil
}

// ListNEXRecords returns a kind's records, most recently updated first.
func (m *Memory) ListNEXRecords(_ context.Context, q NEXRecordQuery) ([]NEXRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []NEXRecord
	for k, r := range m.nexRecords {
		if k.title != q.Title || k.kind != q.Kind || (q.Owner != "" && r.Owner != q.Owner) {
			continue
		}
		out = append(out, cloneNEXRecord(r))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].Key < out[j].Key
	})
	limit := q.Limit
	if limit <= 0 || limit > DefaultNEXRecordLimit {
		limit = DefaultNEXRecordLimit
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// DeleteNEXRecord removes a record, or returns ErrNotFound.
func (m *Memory) DeleteNEXRecord(_ context.Context, title, kind, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := nexRecordKey{title, kind, key}
	if _, ok := m.nexRecords[k]; !ok {
		return ErrNotFound
	}
	delete(m.nexRecords, k)
	return nil
}

// ---- Nintendo Account refresh tokens -------------------------------------

// CreateNARefreshToken stores a refresh token, and drops any that expired
// before t.CreatedAt.
func (m *Memory) CreateNARefreshToken(_ context.Context, t NARefreshToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.users[t.UserID]; !ok {
		return ErrNotFound
	}
	for h, old := range m.naRefreshTokens {
		if !old.ExpiresAt.After(t.CreatedAt) {
			delete(m.naRefreshTokens, h)
		}
	}
	if _, ok := m.naRefreshTokens[t.TokenSHA256]; ok {
		return ErrConflict
	}
	t.Scope = append([]string(nil), t.Scope...)
	m.naRefreshTokens[t.TokenSHA256] = t
	return nil
}

// GetNARefreshToken returns the token with this hash, or ErrNotFound if there
// is none or it expired at or before now.
func (m *Memory) GetNARefreshToken(_ context.Context, tokenSHA256 string, now time.Time) (NARefreshToken, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.naRefreshTokens[tokenSHA256]
	if !ok || !t.ExpiresAt.After(now) {
		return NARefreshToken{}, ErrNotFound
	}
	t.Scope = append([]string(nil), t.Scope...)
	return t, nil
}

// ---- friend requests -----------------------------------------------------

// CreateFriendRequest inserts a request, or returns the existing pending one.
func (m *Memory) CreateFriendRequest(_ context.Context, r FriendRequest) (FriendRequest, error) {
	if r.FromUserID == r.ToUserID {
		return FriendRequest{}, fmt.Errorf("%w: a user cannot befriend themselves", ErrConflict)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, existing := range m.friendRequests {
		if existing.State == RequestPending &&
			existing.FromUserID == r.FromUserID && existing.ToUserID == r.ToUserID {
			return existing, nil
		}
	}
	m.friendRequests[r.ID] = r
	return r, nil
}

// GetFriendRequest returns a request by id.
func (m *Memory) GetFriendRequest(_ context.Context, id string) (FriendRequest, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.friendRequests[id]
	if !ok {
		return FriendRequest{}, ErrNotFound
	}
	return r, nil
}

// SetFriendRequestState resolves a request.
func (m *Memory) SetFriendRequestState(_ context.Context, id, state string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	r, ok := m.friendRequests[id]
	if !ok || r.State != RequestPending {
		return ErrNotFound
	}
	r.State = state
	r.UpdatedAt = at
	m.friendRequests[id] = r
	return nil
}

// ListFriendRequests returns a user's inbox or outbox.
func (m *Memory) ListFriendRequests(_ context.Context, userID string, incoming, pendingOnly bool) ([]FriendRequest, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []FriendRequest
	for _, r := range m.friendRequests {
		if incoming && r.ToUserID != userID {
			continue
		}
		if !incoming && r.FromUserID != userID {
			continue
		}
		if pendingOnly && r.State != RequestPending {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

// FindPendingRequest returns a pending request for an ordered pair.
func (m *Memory) FindPendingRequest(_ context.Context, from, to string) (FriendRequest, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, r := range m.friendRequests {
		if r.State == RequestPending && r.FromUserID == from && r.ToUserID == to {
			return r, nil
		}
	}
	return FriendRequest{}, ErrNotFound
}

// ---- blocks --------------------------------------------------------------

// AddBlock blocks a user.
func (m *Memory) AddBlock(_ context.Context, userID, blockedID string, at time.Time) error {
	if userID == blockedID {
		return fmt.Errorf("%w: a user cannot block themselves", ErrConflict)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.users[blockedID]; !ok {
		return fmt.Errorf("%w: user %s", ErrNotFound, blockedID)
	}
	if m.blocks[userID] == nil {
		m.blocks[userID] = map[string]Block{}
	}
	if _, ok := m.blocks[userID][blockedID]; !ok {
		m.blocks[userID][blockedID] = Block{UserID: userID, BlockedUserID: blockedID, CreatedAt: at}
	}
	if u, ok := m.users[userID]; ok {
		u.BlocksUpdatedAt = at
		u.UpdatedAt = at
		m.users[userID] = u
	}
	return nil
}

// RemoveBlock unblocks a user.
func (m *Memory) RemoveBlock(_ context.Context, userID, blockedID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.blocks[userID][blockedID]; !ok {
		return ErrNotFound
	}
	delete(m.blocks[userID], blockedID)
	now := time.Now().UTC()
	if u, ok := m.users[userID]; ok {
		u.BlocksUpdatedAt = now
		u.UpdatedAt = now
		m.users[userID] = u
	}
	return nil
}

// ListBlocks returns who a user has blocked.
func (m *Memory) ListBlocks(_ context.Context, userID string) ([]Block, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []Block
	for _, b := range m.blocks[userID] {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].BlockedUserID < out[j].BlockedUserID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// IsBlockedEitherWay reports whether either user has blocked the other.
func (m *Memory) IsBlockedEitherWay(_ context.Context, a, b string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ab := m.blocks[a][b]
	_, ba := m.blocks[b][a]
	return ab || ba, nil
}

// IsBlocking reports whether blocker has blocked blocked, in that direction
// only.
func (m *Memory) IsBlocking(_ context.Context, blocker, blocked string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.blocks[blocker][blocked]
	return ok, nil
}

// ---- presence ------------------------------------------------------------

// GetPresence returns a user's presence.
func (m *Memory) GetPresence(_ context.Context, userID string) (Presence, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pr, ok := m.presence[userID]
	if !ok {
		return offlinePresence(userID), nil
	}
	return clonePresence(pr), nil
}

// GetPresences returns presence for several users at once.
func (m *Memory) GetPresences(_ context.Context, userIDs []string) (map[string]Presence, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make(map[string]Presence, len(userIDs))
	for _, id := range userIDs {
		if pr, ok := m.presence[id]; ok {
			out[id] = clonePresence(pr)
		} else {
			out[id] = offlinePresence(id)
		}
	}
	return out, nil
}

// UpdatePresence applies a patch.
func (m *Memory) UpdatePresence(_ context.Context, userID string, p PresencePatch) (Presence, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	pr, ok := m.presence[userID]
	if !ok {
		pr = offlinePresence(userID)
	}
	if p.State != nil {
		pr.State = *p.State
	}
	if p.AppID != nil {
		pr.AppID = *p.AppID
	}
	if p.PresenceGroupID != nil {
		pr.PresenceGroupID = *p.PresenceGroupID
	}
	if p.AppField != nil {
		pr.AppField = *p.AppField
	}
	if p.ACDIndex != nil {
		pr.ACDIndex = *p.ACDIndex
	}
	if p.Extras != nil {
		pr.Extras = cloneExtras(*p.Extras)
	}
	if p.ReportedAt != nil {
		pr.ReportedAt = *p.ReportedAt
	}
	if p.LogoutAt != nil {
		t := *p.LogoutAt
		pr.LogoutAt = &t
	}
	if p.ReportedAt != nil {
		pr.LastSeenAt = *p.ReportedAt
	} else {
		pr.LastSeenAt = time.Now().UTC()
	}
	m.presence[userID] = pr
	return clonePresence(pr), nil
}

// TouchPresence records that a user made an authenticated request.
func (m *Memory) TouchPresence(_ context.Context, userID string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	pr, ok := m.presence[userID]
	if !ok {
		// Touch records that the console is alive, not that it said anything, so
		// ReportedAt stays zero.
		pr = offlinePresence(userID)
	}
	if at.After(pr.LastSeenAt) {
		pr.LastSeenAt = at
	}
	m.presence[userID] = pr
	return nil
}

// ---- images --------------------------------------------------------------

// CreateImage records an uploaded image.
func (m *Memory) CreateImage(_ context.Context, img Image) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.images[img.ID]; ok {
		return fmt.Errorf("%w: image %s exists", ErrConflict, img.ID)
	}
	m.images[img.ID] = img
	return nil
}

// GetImage returns an image by id.
func (m *Memory) GetImage(_ context.Context, id string) (Image, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	img, ok := m.images[id]
	if !ok {
		return Image{}, ErrNotFound
	}
	return img, nil
}

// ---- OLSC save data archives (phase 4) -------------------------------------

func (m *Memory) CreateSaveDataArchive(_ context.Context, a SaveDataArchive) (SaveDataArchive, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.saveDataArchives[a.ID]; ok {
		return SaveDataArchive{}, fmt.Errorf("%w: save data archive %s exists", ErrConflict, a.ID)
	}
	if a.Status == "" {
		a.Status = "uploading"
	}
	now := time.Now().UTC()
	a.CreatedAt, a.UpdatedAt = now, now
	m.saveDataArchives[a.ID] = a
	return a, nil
}

func (m *Memory) GetSaveDataArchive(_ context.Context, id string) (SaveDataArchive, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.saveDataArchives[id]
	if !ok {
		return SaveDataArchive{}, ErrNotFound
	}
	return a, nil
}

func (m *Memory) ListSaveDataArchives(_ context.Context, q SaveDataArchiveQuery) ([]SaveDataArchive, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []SaveDataArchive
	for _, a := range m.saveDataArchives {
		if a.NSAID != q.NSAID {
			continue
		}
		if q.ApplicationID != "" && a.ApplicationID != q.ApplicationID {
			continue
		}
		if q.DeviceID != "" && a.DeviceID != q.DeviceID {
			continue
		}
		out = append(out, a)
	}
	// Most recently saved first, id as the tiebreak so the order is total —
	// the same reasoning ListFests documents for its own sort.
	sort.Slice(out, func(i, j int) bool {
		if out[i].SavedAt.Equal(out[j].SavedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].SavedAt.After(out[j].SavedAt)
	})
	return out, nil
}

func (m *Memory) UpdateSaveDataArchive(_ context.Context, id string, p SaveDataArchivePatch) (SaveDataArchive, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.saveDataArchives[id]
	if !ok {
		return SaveDataArchive{}, ErrNotFound
	}
	if p.Status != nil {
		a.Status = *p.Status
	}
	if p.EncodedDigest != nil {
		a.EncodedDigest = *p.EncodedDigest
	}
	if p.FinishedAt != nil {
		a.FinishedAt = p.FinishedAt
	}
	if p.DataSize != nil {
		a.DataSize = *p.DataSize
	}
	if p.NumOfPartitions != nil {
		a.NumOfPartitions = *p.NumOfPartitions
	}
	a.UpdatedAt = time.Now().UTC()
	m.saveDataArchives[id] = a
	return a, nil
}

func (m *Memory) DeleteSaveDataArchive(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.saveDataArchives[id]; !ok {
		return ErrNotFound
	}
	delete(m.saveDataArchives, id)
	delete(m.componentFiles, id)
	return nil
}

func (m *Memory) CreateComponentFile(_ context.Context, f ComponentFile) (ComponentFile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.componentFiles[f.ArchiveID][f.ID]; ok {
		return ComponentFile{}, fmt.Errorf("%w: component file %s exists", ErrConflict, f.ID)
	}
	if f.Status == "" {
		f.Status = "uploading"
	}
	now := time.Now().UTC()
	f.CreatedAt, f.UpdatedAt = now, now
	byArchive, ok := m.componentFiles[f.ArchiveID]
	if !ok {
		byArchive = map[string]ComponentFile{}
		m.componentFiles[f.ArchiveID] = byArchive
	}
	byArchive[f.ID] = f
	return f, nil
}

func (m *Memory) GetComponentFile(_ context.Context, id string) (ComponentFile, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, byArchive := range m.componentFiles {
		if f, ok := byArchive[id]; ok {
			return f, nil
		}
	}
	return ComponentFile{}, ErrNotFound
}

func (m *Memory) ListComponentFiles(_ context.Context, archiveID string) ([]ComponentFile, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ComponentFile, 0, len(m.componentFiles[archiveID]))
	for _, f := range m.componentFiles[archiveID] {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out, nil
}

func (m *Memory) UpdateComponentFile(_ context.Context, id string, p ComponentFilePatch) (ComponentFile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for archiveID, byArchive := range m.componentFiles {
		f, ok := byArchive[id]
		if !ok {
			continue
		}
		if p.Status != nil {
			f.Status = *p.Status
		}
		if p.ArchiveSize != nil {
			f.ArchiveSize = *p.ArchiveSize
		}
		if p.EncodedArchiveDigest != nil {
			f.EncodedArchiveDigest = *p.EncodedArchiveDigest
		}
		f.UpdatedAt = time.Now().UTC()
		m.componentFiles[archiveID][id] = f
		return f, nil
	}
	return ComponentFile{}, ErrNotFound
}

// ---- operator debugging ----------------------------------------------------

// Stats counts rows directly, holding the read lock for the whole pass — a
// snapshot at one instant rather than a sum of independent reads, the same
// consistency PostgreSQL's own single-query Stats gets from running in one
// statement.
func (m *Memory) Stats(context.Context) (Stats, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s := Stats{
		Users:      len(m.users),
		Devices:    len(m.devices),
		Fests:      len(m.fests),
		CoopShifts: len(m.coopShifts),
		Matches:    len(m.matches),
		Images:     len(m.images),

		SaveDataArchives: len(m.saveDataArchives),
	}
	for _, u := range m.users {
		if u.Provisional {
			s.Users--
			s.ProvisionalUsers++
		}
		if u.Deleted {
			s.DeletedUsers++
		}
	}
	for _, da := range m.deviceAccounts {
		s.DeviceAccounts++
		if da.DeletedAt != nil {
			s.DeletedDeviceAccounts++
		}
	}
	for _, byFriend := range m.friendships {
		s.Friendships += len(byFriend)
	}
	return s, nil
}

// ---- helpers -------------------------------------------------------------

// cloneUser deep-copies the maps in a user, so a caller mutating what it got
// back cannot reach into the store. Without this, a handler that edits an
// extras map in place would corrupt stored state in a way that only shows up
// under the memory store and never under PostgreSQL. The owning Discord
// account is filled in from botLinks, the way the PostgreSQL store joins it in.
func (m *Memory) cloneUser(u User) User {
	u.Extras = cloneExtras(u.Extras)
	if u.ThumbnailUploadedAt != nil {
		t := *u.ThumbnailUploadedAt
		u.ThumbnailUploadedAt = &t
	}
	u.DiscordUserID = m.botLinks[u.ID].discordUserID
	return u
}

func clonePresence(p Presence) Presence {
	p.Extras = cloneExtras(p.Extras)
	if p.LogoutAt != nil {
		t := *p.LogoutAt
		p.LogoutAt = &t
	}
	return p
}

// cloneFriendshipExtras copies a friendship's extras. Unlike cloneExtras, nil
// stays nil: a friendship nobody has patched has no extras, not a user's
// default ones.
func cloneFriendshipExtras(e ExtrasByScope) ExtrasByScope {
	if e == nil {
		return nil
	}
	return cloneExtras(e)
}

func cloneExtras(e ExtrasByScope) ExtrasByScope {
	if e == nil {
		return EmptyExtras()
	}
	out := make(ExtrasByScope, len(e))
	for scope, kv := range e {
		copied := make(map[string]any, len(kv))
		for k, v := range kv {
			copied[k] = v
		}
		out[scope] = copied
	}
	return out
}

// Compile-time check that Memory satisfies the interface.
var _ Store = (*Memory)(nil)

// ---- splatfests ----------------------------------------------------------

func (m *Memory) UpsertFest(_ context.Context, f Fest) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.fests[f.ID]; ok {
		f.CreatedAt = existing.CreatedAt
		if f.EndAt.IsZero() {
			f.EndAt = existing.EndAt
		}
	} else if f.CreatedAt.IsZero() {
		f.CreatedAt = time.Now().UTC()
	}
	f.UpdatedAt = time.Now().UTC()
	// Copy the slice: the caller keeps a reference to the one it passed, and a
	// store that hands out its own backing array lets a caller mutate stored
	// state by accident. The PostgreSQL implementation cannot do that, so
	// without this the two would disagree under the contract test.
	f.Regions = append([]string(nil), f.Regions...)
	f.PackKeys = append([]string(nil), f.PackKeys...)
	m.fests[f.ID] = f
	return nil
}

func (m *Memory) GetFest(_ context.Context, id string) (Fest, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	f, ok := m.fests[id]
	if !ok {
		return Fest{}, ErrNotFound
	}
	f.Regions = append([]string(nil), f.Regions...)
	f.PackKeys = append([]string(nil), f.PackKeys...)
	return f, nil
}

func (m *Memory) ListFests(_ context.Context) ([]Fest, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]Fest, 0, len(m.fests))
	for _, f := range m.fests {
		f.Regions = append([]string(nil), f.Regions...)
		f.PackKeys = append([]string(nil), f.PackKeys...)
		out = append(out, f)
	}
	// Soonest first, id as the tiebreak so the order is total: a map range is
	// random, and an unstable order is exactly the kind of difference the
	// contract test exists to catch between the two implementations.
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartAt.Equal(out[j].StartAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].StartAt.Before(out[j].StartAt)
	})
	return out, nil
}

func (m *Memory) DeleteFest(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.fests, id)
	// The entries go with it, matching the ON DELETE CASCADE on the table.
	// Without this a recreated fest of the same id would inherit the old votes.
	delete(m.festEntries, id)
	delete(m.festBattles, id)
	delete(m.festBattlePlayers, id)
	return nil
}

// ---- fest entries -----------------------------------------------------------

// UpsertFestEntry records or changes a player's team choice. An upsert, not
// an insert: internal/toyohr allows changing team up until the fest opens,
// which is a second call with the same fest/user pair.
func (m *Memory) UpsertFestEntry(_ context.Context, e FestEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	byUser, ok := m.festEntries[e.FestID]
	if !ok {
		byUser = map[string]FestEntry{}
		m.festEntries[e.FestID] = byUser
	}
	if existing, ok := byUser[e.UserID]; ok {
		e.CreatedAt = existing.CreatedAt
	} else if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	e.UpdatedAt = time.Now().UTC()
	byUser[e.UserID] = e
	return nil
}

func (m *Memory) GetFestEntry(_ context.Context, festID, userID string) (FestEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	e, ok := m.festEntries[festID][userID]
	if !ok {
		return FestEntry{}, ErrNotFound
	}
	return e, nil
}

func (m *Memory) ListFestEntries(_ context.Context, festID string) ([]FestEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]FestEntry, 0, len(m.festEntries[festID]))
	for _, e := range m.festEntries[festID] {
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *Memory) RecordFestBattle(_ context.Context, b FestBattle) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.fests[b.FestID]; !ok {
		return ErrNotFound
	}
	if m.festBattles[b.FestID] == nil {
		m.festBattles[b.FestID] = map[string]FestBattle{}
	}
	if _, exists := m.festBattles[b.FestID][b.BattleID]; exists {
		return nil
	}
	b.Ink = cloneInk(b.Ink)
	m.festBattles[b.FestID][b.BattleID] = b
	return nil
}

func (m *Memory) RecordFestBattlePlayer(_ context.Context, p FestBattlePlayer) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.fests[p.FestID]; !ok {
		return ErrNotFound
	}
	if m.festBattlePlayers[p.FestID] == nil {
		m.festBattlePlayers[p.FestID] = map[string]FestBattlePlayer{}
	}
	key := p.BattleID + "/" + p.UserID
	if _, exists := m.festBattlePlayers[p.FestID][key]; !exists {
		m.festBattlePlayers[p.FestID][key] = p
	}
	return nil
}

func (m *Memory) ListFestBattles(_ context.Context, festID string) ([]FestBattle, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	summed := map[string]map[string]int64{}
	for _, p := range m.festBattlePlayers[festID] {
		if summed[p.BattleID] == nil {
			summed[p.BattleID] = map[string]int64{}
		}
		summed[p.BattleID][p.Team] += p.Paint
	}
	out := make([]FestBattle, 0, len(m.festBattles[festID]))
	for _, b := range m.festBattles[festID] {
		if ink, ok := summed[b.BattleID]; ok {
			b.Ink = cloneInk(ink)
		} else {
			b.Ink = cloneInk(b.Ink)
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FinishedAt.Equal(out[j].FinishedAt) {
			return out[i].BattleID < out[j].BattleID
		}
		return out[i].FinishedAt.Before(out[j].FinishedAt)
	})
	return out, nil
}

func cloneInk(in map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(in))
	for team, points := range in {
		out[team] = points
	}
	return out
}

// ---- Salmon Run / Big Run schedule ---------------------------------------

func (m *Memory) UpsertCoopShift(_ context.Context, c CoopShift) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.coopShifts[c.ID]; ok {
		c.CreatedAt = existing.CreatedAt
	} else if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	c.UpdatedAt = time.Now().UTC()
	// Copy the slices, the same reason UpsertFest copies Regions/PackKeys: the
	// caller keeps its own reference, and handing out the backing array lets
	// it mutate stored state by accident in a way PostgreSQL cannot.
	c.Weapons = append([]int64(nil), c.Weapons...)
	c.Rewards = append([]int64(nil), c.Rewards...)
	c.RewardBorders = append([]int64(nil), c.RewardBorders...)
	m.coopShifts[c.ID] = c
	return nil
}

func (m *Memory) GetCoopShift(_ context.Context, id string) (CoopShift, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	c, ok := m.coopShifts[id]
	if !ok {
		return CoopShift{}, ErrNotFound
	}
	c.Weapons = append([]int64(nil), c.Weapons...)
	c.Rewards = append([]int64(nil), c.Rewards...)
	c.RewardBorders = append([]int64(nil), c.RewardBorders...)
	return c, nil
}

func (m *Memory) ListCoopShifts(_ context.Context) ([]CoopShift, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]CoopShift, 0, len(m.coopShifts))
	for _, c := range m.coopShifts {
		c.Weapons = append([]int64(nil), c.Weapons...)
		c.Rewards = append([]int64(nil), c.Rewards...)
		c.RewardBorders = append([]int64(nil), c.RewardBorders...)
		out = append(out, c)
	}
	// Soonest first, id as the tiebreak, the same total-order reasoning as
	// ListFests: a map range is random and the contract test compares order.
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartAt.Equal(out[j].StartAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].StartAt.Before(out[j].StartAt)
	})
	return out, nil
}

func (m *Memory) DeleteCoopShift(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.coopShifts, id)
	return nil
}

// ---- match history ---------------------------------------------------------

func (m *Memory) CreateMatch(_ context.Context, match Match) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// A match is written once, at formation. Matches the PostgreSQL
	// implementation's ON CONFLICT (id) DO NOTHING: a retried write after a
	// network blip must not fail the caller or duplicate the row.
	for _, existing := range m.matches {
		if existing.ID == match.ID {
			return nil
		}
	}
	match.PlayerIDs = append([]string(nil), match.PlayerIDs...)
	m.matches = append(m.matches, match)
	return nil
}

func (m *Memory) ListMatches(_ context.Context, limit int) ([]Match, error) {
	return m.listMatches("", limit), nil
}

func (m *Memory) ListMatchesForTenant(_ context.Context, tenant string, limit int) ([]Match, error) {
	return m.listMatches(tenant, limit), nil
}

// listMatches filters by tenant unless it is empty.
func (m *Memory) listMatches(tenant string, limit int) []Match {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]Match, 0, len(m.matches))
	for _, match := range m.matches {
		if tenant != "" && match.Tenant != tenant {
			continue
		}
		match.PlayerIDs = append([]string(nil), match.PlayerIDs...)
		out = append(out, match)
	}
	// Newest first, matching the PostgreSQL implementation's ORDER BY
	// created_at DESC.
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit <= 0 || limit > DefaultMatchListLimit {
		limit = DefaultMatchListLimit
	}
	if limit < len(out) {
		out = out[:limit]
	}
	return out
}

// ---- push notifications (Penne) ----

type penneTicket struct {
	penneID string
	expires time.Time
}

func (m *Memory) SetPenneToken(_ context.Context, t PenneToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.penneTokens[t.Token] = t
	return nil
}

func (m *Memory) PenneTokens(_ context.Context, tokens []string) ([]PenneToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []PenneToken
	for _, tok := range tokens {
		if t, ok := m.penneTokens[tok]; ok {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Token < out[j].Token })
	return out, nil
}

func (m *Memory) PenneTokensOf(_ context.Context, penneID string) ([]PenneToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []PenneToken
	for _, t := range m.penneTokens {
		if t.PenneID == penneID {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Token < out[j].Token })
	return out, nil
}

func (m *Memory) SetPushChannel(_ context.Context, c PushChannel) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.users[c.UserID]; !ok {
		return ErrNotFound
	}
	m.pushChannels[c.UserID+"/"+c.DeviceAccountID] = c
	return nil
}

func (m *Memory) PushChannels(_ context.Context, userIDs []string) ([]PushChannel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := map[string]bool{}
	for _, id := range userIDs {
		want[id] = true
	}
	var out []PushChannel
	for _, c := range m.pushChannels {
		if want[c.UserID] {
			if _, ok := m.users[c.UserID]; ok {
				out = append(out, c)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UserID != out[j].UserID {
			return out[i].UserID < out[j].UserID
		}
		return out[i].DeviceAccountID < out[j].DeviceAccountID
	})
	return out, nil
}

func (m *Memory) SetPenneTicket(_ context.Context, ticket, penneID string, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.penneTickets[ticket] = penneTicket{penneID: penneID, expires: expires}
	return nil
}

func (m *Memory) PenneTicket(_ context.Context, ticket string, now time.Time) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.penneTickets[ticket]
	if !ok || !now.Before(t.expires) {
		return "", ErrNotFound
	}
	return t.penneID, nil
}

// ---- NPLN Ugcstore attachments ---------------------------------------------

func copyBlob(b UGCBlob) UGCBlob {
	b.Data = append([]byte(nil), b.Data...)
	return b
}

// PutUGCBlob creates or replaces an attachment, keeping its document link.
func (m *Memory) PutUGCBlob(_ context.Context, b UGCBlob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.ugcBlobs[b.ID]; ok {
		b.Document = old.Document
	} else {
		b.Document = ""
	}
	m.ugcBlobs[b.ID] = copyBlob(b)
	return nil
}

// GetUGCBlob returns an attachment by id, or ErrNotFound.
func (m *Memory) GetUGCBlob(_ context.Context, id string) (UGCBlob, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.ugcBlobs[id]
	if !ok {
		return UGCBlob{}, ErrNotFound
	}
	return copyBlob(b), nil
}

// GetUGCDocumentBlob returns a document's attachment, or ErrNotFound.
func (m *Memory) GetUGCDocumentBlob(_ context.Context, document string) (UGCBlob, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, b := range m.ugcBlobs {
		if b.Document == document {
			return copyBlob(b), nil
		}
	}
	return UGCBlob{}, ErrNotFound
}

// AttachUGCBlob makes id the document's only attachment.
func (m *Memory) AttachUGCBlob(_ context.Context, id, document string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.ugcBlobs[id]
	if !ok {
		return ErrNotFound
	}
	for other, o := range m.ugcBlobs {
		if other != id && o.Document == document {
			delete(m.ugcBlobs, other)
		}
	}
	b.Document = document
	m.ugcBlobs[id] = b
	return nil
}

// DetachUGCDocumentBlob deletes a document's attachment, if any.
func (m *Memory) DetachUGCDocumentBlob(_ context.Context, document string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, b := range m.ugcBlobs {
		if b.Document == document {
			delete(m.ugcBlobs, id)
		}
	}
	return nil
}

// PurgeUnattachedUGCBlobs deletes old blobs that were never attached.
func (m *Memory) PurgeUnattachedUGCBlobs(_ context.Context, before time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, b := range m.ugcBlobs {
		if b.Document == "" && b.CreateTime.Before(before) {
			delete(m.ugcBlobs, id)
		}
	}
	return nil
}
