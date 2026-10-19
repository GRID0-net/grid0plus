package nplnrest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/presence"
	"github.com/n-popescu/grid0plus/internal/presencebus"
)

// DAPresence is how a console on FW 22.5 reports its own presence: over the
// frontline, not as a BAAS PATCH. Splatoon 3 consoles never send the friends
// sysmodule's PATCH at all.
//
//	DAPresence { 0: string device account id, 1: string user id, 2: Presence }
//	Presence   { 0: u16 status, 1: string JSON, 2: u16 (1 in every frame seen) }
//
// status is nn::friends' own: 0 offline, 1 online, 2 playing. The JSON holds one
// object per visibility scope; "friends" carries the appInfo fields BAAS already
// stores.
type daPresence struct {
	UserID, DeviceAccountID string
	Status                  uint16
	JSON                    string
}

func parseDAPresence(frame []byte) (daPresence, error) {
	body, ok := frontlineBody(frame)
	if !ok {
		return daPresence{}, fmt.Errorf("%w: DAPresence has no body", errFrontlineFrame)
	}
	var d daPresence
	if slot, ok := fbFieldPos(frame, body, 1); ok {
		d.UserID = fbString(frame, slot)
	}
	if slot, ok := fbFieldPos(frame, body, 0); ok {
		d.DeviceAccountID = fbString(frame, slot)
	}
	// Both ids are 16 hex digits on the wire; anything else is a misread,
	// and acking a misread id makes npns abort.
	if !isHex16(d.UserID) || !isHex16(d.DeviceAccountID) {
		return daPresence{}, fmt.Errorf("%w: DAPresence ids %q %q", errFrontlineFrame, d.UserID, d.DeviceAccountID)
	}
	slot, ok := fbFieldPos(frame, body, 2)
	if !ok {
		return daPresence{}, fmt.Errorf("%w: DAPresence has no presence", errFrontlineFrame)
	}
	p, ok := fbTable(frame, slot)
	if !ok {
		return daPresence{}, fmt.Errorf("%w: DAPresence presence out of range", errFrontlineFrame)
	}
	d.Status, _ = fbU16(frame, p, 0)
	if js, ok := fbFieldPos(frame, p, 1); ok {
		d.JSON = fbString(frame, js)
	}
	if d.UserID == "" || d.DeviceAccountID == "" {
		return daPresence{}, fmt.Errorf("%w: DAPresence names no account", errFrontlineFrame)
	}
	return d, nil
}

type daPresenceScopes struct {
	Friends struct {
		AppID           string `json:"appInfo:appId"`
		PresenceGroupID string `json:"appInfo:presenceGroupId"`
		ACDIndex        *int   `json:"appInfo:acdIndex"`
		AppField        string `json:"appField"`
	} `json:"friends"`
}

// recordDAPresence stores a console's DAPresence the way BAAS stores a
// presence PATCH, and publishes it so friends' consoles and the dashboard
// see it.
//
// Fail-closed like BAAS's authenticate: the device account must exist, be
// live, and belong to the user the frame names. The frontline ticket cannot
// vouch for the user (it names only a penne id), so this pairing is the check.
func (s *Server) recordDAPresence(ctx context.Context, frame []byte) (string, error) {
	d, err := parseDAPresence(frame)
	if err != nil {
		return "", err
	}
	da, err := s.store.GetDeviceAccount(ctx, d.DeviceAccountID)
	if err != nil {
		return "", fmt.Errorf("device account %s: %w", d.DeviceAccountID, err)
	}
	if da.DeletedAt != nil || da.UserID != d.UserID {
		return "", fmt.Errorf("device account %s does not belong to %s", d.DeviceAccountID, d.UserID)
	}
	var state string
	switch d.Status {
	case 0:
		state = db.StateOffline
	case 1:
		state = db.StateOnline
	case 2:
		state = db.StatePlaying
	default:
		return "", fmt.Errorf("DAPresence status %d", d.Status)
	}
	var scopes daPresenceScopes
	var reported db.ExtrasByScope
	if d.JSON != "" {
		if err := json.Unmarshal([]byte(d.JSON), &scopes); err != nil {
			return "", fmt.Errorf("DAPresence JSON: %w", err)
		}
		if err := json.Unmarshal([]byte(d.JSON), &reported); err != nil {
			return "", fmt.Errorf("DAPresence JSON: %w", err)
		}
	}
	f := scopes.Friends
	// In a game npns mostly reports status 1 with the game's id, keeping 2 for
	// moments of online play. BAAS clears the app of anything not PLAYING, so stored
	// as ONLINE a friend would read "Online (Nintendo Switch)" for someone in a game.
	// npns sends an all-zero id in the Home Menu, so a real id is current, unlike the
	// stale app a console's BAAS PATCH can leave behind.
	if state == db.StateOnline && strings.Trim(f.AppID, "0") != "" {
		state = db.StatePlaying
	}
	if len(f.AppField) > 1024 || len(f.AppID) > 32 || len(f.PresenceGroupID) > 32 {
		return "", fmt.Errorf("DAPresence fields too long")
	}
	current, err := s.store.GetPresence(ctx, d.UserID)
	if err != nil {
		return "", err
	}
	extras := presenceExtras(current.Extras, reported)
	now := s.clock()
	patch := db.PresencePatch{
		Extras: &extras,
		State:  &state, AppID: &f.AppID, PresenceGroupID: &f.PresenceGroupID,
		AppField: &f.AppField, ACDIndex: f.ACDIndex, ReportedAt: &now,
	}
	if state == db.StateOffline {
		patch.LogoutAt = &now
	}
	updated, err := s.store.UpdatePresence(ctx, d.UserID, patch)
	if err != nil {
		return "", err
	}
	if s.bus != nil {
		if err := s.bus.Publish(ctx, presencebus.Event{
			UserID: d.UserID, State: updated.State, AppID: updated.AppID,
			AppField: updated.AppField, At: now, Source: "npns",
		}); err != nil {
			s.log.Warn("nplnrest: publish presence", "user", d.UserID, "error", err)
		}
	}
	s.log.Info("nplnrest: console presence", "user", d.UserID, "state", state, "app", f.AppID)
	return d.UserID, nil
}

// presenceRefreshEvery re-stamps a console's last report while its frontline
// stays open. npns sends DAPresence only when the status changes -- 15 and 80
// minutes apart on hardware -- so without this a player mid-game would fall
// out of BAAS's report TTL and read as merely online. The open connection is
// the proof of life; once it closes the ordinary TTL takes over.
const presenceRefreshEvery = time.Minute

// presenceKeeper holds the users a frontline connection has reported for.
type presenceKeeper struct {
	mu    sync.Mutex
	users map[string]bool
	once  sync.Once
}

func (k *presenceKeeper) add(s *Server, ctx context.Context, userID string) {
	k.mu.Lock()
	if k.users == nil {
		k.users = map[string]bool{}
	}
	k.users[userID] = true
	k.mu.Unlock()
	k.once.Do(func() { go k.run(s, ctx) })
}

func (k *presenceKeeper) run(s *Server, ctx context.Context) {
	t := time.NewTicker(presenceRefreshEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		k.mu.Lock()
		users := make([]string, 0, len(k.users))
		for u := range k.users {
			users = append(users, u)
		}
		k.mu.Unlock()
		now := s.clock()
		for _, u := range users {
			if _, err := s.store.UpdatePresence(ctx, u, db.PresencePatch{ReportedAt: &now}); err != nil && ctx.Err() == nil {
				s.log.Warn("nplnrest: refresh console presence", "user", u, "error", err)
			}
		}
	}
}

func isHex16(s string) bool {
	if len(s) != 16 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// presenceExtras merges a DAPresence's scopes into the stored presence extras.
//
// The JSON npns sends is already BAAS's extras-by-scope, and the extras are
// the only place a friend's console reads the game from: the friend-list
// presence carries appInfo:* under extras.friends and no flat app id at all.
// Storing only the flat fields made a playing friend read as "Online
// (Nintendo Switch)". Each report replaces the app keys of every scope it
// names, so a game that closed does not linger; other keys are kept.
func presenceExtras(current, reported db.ExtrasByScope) db.ExtrasByScope {
	out := db.EmptyExtras()
	for scope, kv := range current {
		if out[scope] == nil {
			continue
		}
		for k, v := range kv {
			out[scope][k] = v
		}
	}
	for _, scope := range db.Scopes {
		kv, ok := reported[scope]
		if !ok {
			continue
		}
		for _, k := range []string{presence.KeyAppID, presence.KeyPresenceGroupID, presence.KeyACDIndex, presence.KeyAppField} {
			delete(out[scope], k)
		}
		for k, v := range kv {
			out[scope][k] = v
		}
	}
	return out
}

// bindFrontline ties a verified DAPresence's user to its frontline, so pushes
// for that user reach this console. A notification names the console account
// (Uid) it is for, which only the console's notification token records; with
// more than one account on the console the user's own cannot be told apart,
// so nothing is bound rather than risk delivering to the wrong one.
func (s *Server) bindFrontline(ctx context.Context, conn *frontlineConn, userID string) {
	tokens, err := s.store.PenneTokensOf(ctx, conn.penneID)
	if err != nil {
		s.log.Warn("nplnrest: look up console accounts", "error", err)
		return
	}
	accounts := map[string]bool{}
	for _, t := range tokens {
		accounts[t.Account] = true
	}
	if len(accounts) != 1 {
		s.log.Info("nplnrest: frontline not bound", "user", userID, "accounts", len(accounts))
		return
	}
	for a := range accounts {
		conn.bind(userID, a)
	}
}
