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
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{
		users:          map[string]User{},
		devices:        map[string]Device{},
		deviceAccounts: map[string]DeviceAccount{},
		friendCodes:    map[string]FriendCode{},
		fests:          map[string]Fest{},
		friendships:    map[string]map[string]Friendship{},
		friendRequests: map[string]FriendRequest{},
		blocks:         map[string]map[string]Block{},
		presence:       map[string]Presence{},
		images:         map[string]Image{},
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
// account.
func (m *Memory) CreateUserWithLogin(_ context.Context, u User) error {
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
	if u.Extras == nil {
		u.Extras = EmptyExtras()
	}
	m.users[u.ID] = u
	m.presence[u.ID] = Presence{
		UserID: u.ID, State: StateOffline, Extras: EmptyExtras(),
		LastSeenAt: u.CreatedAt,
	}
	return nil
}

// GetUserByLoginUsername looks up a user by their login username.
func (m *Memory) GetUserByLoginUsername(_ context.Context, username string) (User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, u := range m.users {
		if u.LoginUsername != "" && strings.EqualFold(u.LoginUsername, username) {
			return cloneUser(u), nil
		}
	}
	return User{}, ErrNotFound
}

// GetUser returns a user by id.
func (m *Memory) GetUser(_ context.Context, id string) (User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	u, ok := m.users[id]
	if !ok {
		return User{}, ErrNotFound
	}
	return cloneUser(u), nil
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
	u.UpdatedAt = time.Now().UTC()
	m.users[id] = u
	return cloneUser(u), nil
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
		out = append(out, cloneUser(u))
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
			out = append(out, cloneUser(u))
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
	da.UserID = newUserID
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
	return f, nil
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

// ---- helpers -------------------------------------------------------------

// cloneUser deep-copies the maps in a user, so a caller mutating what it got
// back cannot reach into the store. Without this, a handler that edits an
// extras map in place would corrupt stored state in a way that only shows up
// under the memory store and never under PostgreSQL.
func cloneUser(u User) User {
	u.Extras = cloneExtras(u.Extras)
	if u.ThumbnailUploadedAt != nil {
		t := *u.ThumbnailUploadedAt
		u.ThumbnailUploadedAt = &t
	}
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
	return nil
}
