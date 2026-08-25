package baas

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/n-popescu/switchnet/internal/db"
	"github.com/n-popescu/switchnet/internal/presencebus"
)

// Presence is the part of the friends system that is easiest to get wrong, and
// it is what the whole feature is judged on: a friend list where everybody is
// permanently offline looks broken even though every other endpoint works.
//
// Two rules, both learned from a deployment where they were missing:
//
//  1. A reported state expires. The console reports PLAYING when a game starts
//     and does not report again. Without a TTL, a console that was switched off
//     mid-game shows as playing forever.
//
//  2. Request traffic is proof of life. The console does not report anything
//     while it sits in the Home Menu, which is exactly where players look at
//     each other's status. But it keeps polling its friend list. Treating that
//     traffic as an ONLINE heartbeat is what makes an idle console visible.
//
// The read path takes the strongest fresh signal and a weak signal never
// downgrades a strong one.

// effectivePresence resolves a user's presence through the TTL rules.
func (s *Server) effectivePresence(ctx context.Context, userID string) (db.Presence, error) {
	p, err := s.store.GetPresence(ctx, userID)
	if err != nil {
		return db.Presence{}, err
	}
	return s.resolvePresence(p), nil
}

// effectivePresences resolves presence for several users at once.
func (s *Server) effectivePresences(ctx context.Context, userIDs []string) (map[string]db.Presence, error) {
	raw, err := s.store.GetPresences(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[string]db.Presence, len(raw))
	for id, p := range raw {
		out[id] = s.resolvePresence(p)
	}
	return out, nil
}

// resolvePresence applies the TTL rules to a stored presence row.
func (s *Server) resolvePresence(p db.Presence) db.Presence {
	now := s.now()

	reportedFresh := !p.ReportedAt.IsZero() && now.Sub(p.ReportedAt) <= s.cfg.PresenceTTL
	seenFresh := !p.LastSeenAt.IsZero() && now.Sub(p.LastSeenAt) <= s.cfg.ConsolePresenceTTL

	switch {
	case reportedFresh && (p.State == db.StatePlaying || p.State == db.StateOnline || p.State == db.StateInactive):
		// The console said what it is doing recently enough to believe.
		return p

	case seenFresh:
		// Nothing fresh was reported, but the console is talking to us. It is
		// online, and it is not in a game as far as we know — so the app fields
		// are cleared rather than kept, otherwise a friend list would show
		// "playing X" for a game that was closed an hour ago.
		p.State = db.StateOnline
		p.AppID = ""
		p.PresenceGroupID = ""
		p.AppField = ""
		p.Extras = clearPresenceAppFields(p.Extras)
		return p

	default:
		p.State = db.StateOffline
		p.AppID = ""
		p.PresenceGroupID = ""
		p.AppField = ""
		p.Extras = clearPresenceAppFields(p.Extras)
		return p
	}
}

// Presence extras keys, as the console writes them.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/BAAS-Server#presence-extras
const (
	keyAppID = "appInfo:appId"
	// keyPresenceGroupID groups related titles. It is usually the same value as
	// the app id.
	keyPresenceGroupID = "appInfo:presenceGroupId"
	// keyACDIndex was added in system version 19.0.0.
	keyACDIndex = "appInfo:acdIndex"
	// keyAppField is a JSON-encoded string the game chooses. It is what makes
	// the friends applet show in-game detail, and it is passed through
	// untouched: parsing it would mean guessing at each game's private format.
	keyAppField = "appField"
)

// clearPresenceAppFields removes the app fields from every extras scope, leaving
// any other key alone.
func clearPresenceAppFields(extras db.ExtrasByScope) db.ExtrasByScope {
	out := make(db.ExtrasByScope, len(db.Scopes))
	for _, scope := range db.Scopes {
		kv := map[string]any{}
		for k, v := range extras[scope] {
			switch k {
			case keyAppID, keyPresenceGroupID, keyACDIndex, keyAppField:
			default:
				kv[k] = v
			}
		}
		out[scope] = kv
	}
	return out
}

// ---- PATCH /1.0.0/users/<id>/device_accounts/<id> ------------------------

// patchDeviceAccount is how a console reports its presence.
//
// This is not obvious from the endpoint's name, and it is the single most
// important request in the friends system. The friends sysmodule sends a JSON
// patch here whenever the console's state changes:
//
//	PATCH /1.0.0/users/<uid>/device_accounts/<daid>
//	Content-Type: application/json-patch+json
//
//	[{"op":"replace","path":"/presence/state","value":"PLAYING"},
//	 {"op":"add","path":"/presence/extras/friends/appField","value":"{...}"},
//	 {"op":"add","path":"/presence/extras/friends/appInfo:appId","value":"0100c2500fc20000"},
//	 {"op":"add","path":"/presence/extras/friends/appInfo:acdIndex","value":0},
//	 {"op":"add","path":"/presence/extras/friends/appInfo:presenceGroupId","value":"0100c2500fc20000"}]
//
// Shape confirmed against kinnay/NintendoClients, nintendo/switch/baas.py,
// BAASClient.update_presence.
//
// appInfo:appId is the field that makes "Playing Splatoon 3" appear in the stock
// friends applet. If it is dropped, the friend shows as merely online and
// nothing in the response says why.
func (s *Server) patchDeviceAccount(w http.ResponseWriter, r *http.Request, c *caller) {
	if !s.requireSelf(w, r, c) {
		return
	}
	daid := strings.ToLower(r.PathValue("daid"))
	if daid != c.DeviceAccountID {
		// The token was minted for one device account; it may not report
		// presence as another. Two consoles sharing a user must not be able to
		// overwrite each other's state through a URL they made up.
		s.log.Warn("baas: refused presence report for another device account",
			"user", c.UserID, "token_device_account", c.DeviceAccountID, "target", daid)
		writeError(w, r, errInsufficientScope)
		return
	}

	ops, err := parseJSONPatch(w, r)
	if err != nil {
		writeError(w, r, errInvalidParams)
		return
	}

	current, err := s.store.GetPresence(r.Context(), c.UserID)
	if err != nil {
		writeInternalError(s.log, w, r, "load presence", err)
		return
	}

	patch, err := presencePatchFromOps(ops, current.Extras)
	if err != nil {
		s.log.Warn("baas: rejected presence patch", "user", c.UserID, "error", err)
		writeError(w, r, errInvalidParams)
		return
	}

	now := s.now()
	patch.ReportedAt = &now
	// A console reporting OFFLINE is logging out. Recording when lets the
	// friends applet show "last online" instead of nothing.
	if patch.State != nil && *patch.State == db.StateOffline {
		patch.LogoutAt = &now
	}

	updated, err := s.store.UpdatePresence(r.Context(), c.UserID, patch)
	if err != nil {
		writeInternalError(s.log, w, r, "update presence", err)
		return
	}

	s.log.Info("baas: presence reported",
		"user", c.UserID, "state", updated.State, "title", updated.AppID)

	s.publishPresence(r.Context(), presencebus.Event{
		UserID:          c.UserID,
		State:           updated.State,
		AppID:           updated.AppID,
		PresenceGroupID: updated.PresenceGroupID,
		AppField:        updated.AppField,
		At:              now,
		Source:          "baas",
	})

	// The console does not read this body, but returning the resulting state
	// makes the endpoint answerable with curl during bring-up.
	writeJSON(w, http.StatusOK, PresenceResponse{
		State:     updated.State,
		Extras:    updated.Extras,
		UpdatedAt: updated.ReportedAt.Unix(),
	})
}

// ---- PATCH /1.0.0/users/<id> -------------------------------------------

// patchUser applies a JSON patch to a user's profile.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/BAAS-Server#patch-100usersid
//
// The documented working paths are /nickname, /country, /birthday,
// /thumbnailUrl, /extras/self/nxAccount and the five /permissions/* entries.
// A path outside that set is rejected rather than silently ignored, so a
// firmware sending something new shows up in the log instead of quietly not
// taking effect.
func (s *Server) patchUser(w http.ResponseWriter, r *http.Request, c *caller) {
	if !s.requireSelf(w, r, c) {
		return
	}

	ops, err := parseJSONPatch(w, r)
	if err != nil {
		writeError(w, r, errInvalidParams)
		return
	}

	user, err := s.store.GetUser(r.Context(), c.UserID)
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(w, r, errResourceNotFound)
		return
	case err != nil:
		writeInternalError(s.log, w, r, "load user", err)
		return
	}

	patch := db.UserPatch{}
	permissions := user.Permissions
	permissionsChanged := false
	extras := user.Extras
	extrasChanged := false
	now := s.now()

	for _, op := range ops {
		switch op.Path {
		case "/nickname":
			v, ok := op.stringValue()
			if !ok {
				writeError(w, r, errInvalidParams)
				return
			}
			// The friends applet shows this to other players, so cap it rather
			// than storing an arbitrarily long string.
			if len(v) > 64 {
				writeError(w, r, errInvalidParams)
				return
			}
			patch.Nickname = &v

		case "/country":
			v, ok := op.stringValue()
			if !ok || len(v) > 8 {
				writeError(w, r, errInvalidParams)
				return
			}
			patch.Country = &v

		case "/birthday":
			v, ok := op.stringValue()
			if !ok || len(v) > 10 {
				writeError(w, r, errInvalidParams)
				return
			}
			patch.Birthday = &v

		case "/thumbnailUrl":
			v, ok := op.stringValue()
			if !ok {
				writeError(w, r, errInvalidParams)
				return
			}
			patch.ThumbnailURL = &v
			patch.ThumbnailUploadedAt = &now

		case "/permissions/personalAnalytics":
			v, ok := op.boolValue()
			if !ok {
				writeError(w, r, errInvalidParams)
				return
			}
			permissions.PersonalAnalytics = v
			permissions.PersonalAnalyticsUpdatedAt = now.Unix()
			permissionsChanged = true

		case "/permissions/personalNotification":
			v, ok := op.boolValue()
			if !ok {
				writeError(w, r, errInvalidParams)
				return
			}
			permissions.PersonalNotification = v
			permissions.PersonalNotificationUpdatedAt = now.Unix()
			permissionsChanged = true

		case "/permissions/friendRequestReception":
			v, ok := op.boolValue()
			if !ok {
				writeError(w, r, errInvalidParams)
				return
			}
			permissions.FriendRequestReception = v
			permissionsChanged = true

		case "/permissions/friends":
			v, ok := op.stringValue()
			if !ok || !validFriendsVisibility(v) {
				writeError(w, r, errInvalidParams)
				return
			}
			permissions.Friends = v
			permissionsChanged = true

		case "/permissions/presence":
			v, ok := op.stringValue()
			if !ok || !validPresenceVisibility(v) {
				writeError(w, r, errInvalidParams)
				return
			}
			permissions.Presence = v
			permissions.PresenceUpdatedAt = now.Unix()
			permissionsChanged = true

		default:
			// /extras/<scope>/<key> is open-ended: the console and the games put
			// their own keys in here, and a firmware update adds more.
			if scope, key, ok := parseExtrasPath(op.Path); ok {
				if extras[scope] == nil {
					extras[scope] = map[string]any{}
				}
				if op.Op == "remove" {
					delete(extras[scope], key)
				} else {
					extras[scope][key] = op.Value
				}
				extrasChanged = true
				continue
			}

			s.log.Warn("baas: unsupported user patch path",
				"user", c.UserID, "op", op.Op, "path", op.Path)
			writeError(w, r, errInvalidParams)
			return
		}
	}

	if permissionsChanged {
		patch.Permissions = &permissions
	}
	if extrasChanged {
		patch.Extras = &extras
	}

	user, err = s.store.UpdateUser(r.Context(), c.UserID, patch)
	if err != nil {
		writeInternalError(s.log, w, r, "update user", err)
		return
	}
	s.writeSelf(w, r, user)
}

func validFriendsVisibility(v string) bool {
	switch v {
	case "EVERYONE", "FRIENDS", "SELF":
		return true
	}
	return false
}

func validPresenceVisibility(v string) bool {
	switch v {
	case "FRIENDS", "FAVORITE_FRIENDS", "SELF":
		return true
	}
	return false
}

// parseExtrasPath splits "/extras/<scope>/<key>" or
// "/presence/extras/<scope>/<key>" into its scope and key.
func parseExtrasPath(path string) (scope, key string, ok bool) {
	for _, prefix := range []string{"/extras/", "/presence/extras/"} {
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		rest := path[len(prefix):]
		scope, key, found := strings.Cut(rest, "/")
		if !found || key == "" || !validScope(scope) {
			return "", "", false
		}
		// A JSON Pointer escapes '/' as '~1' and '~' as '~0'. Presence keys
		// contain a colon, not a slash, but decoding is correct and free.
		key = strings.ReplaceAll(key, "~1", "/")
		key = strings.ReplaceAll(key, "~0", "~")
		return scope, key, true
	}
	return "", "", false
}

func validScope(scope string) bool {
	for _, s := range db.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// ---- JSON patch ---------------------------------------------------------

// patchOp is one RFC 6902 operation.
type patchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

func (o patchOp) stringValue() (string, bool) {
	v, ok := o.Value.(string)
	return v, ok
}

func (o patchOp) boolValue() (bool, bool) {
	v, ok := o.Value.(bool)
	return v, ok
}

func (o patchOp) intValue() (int, bool) {
	switch v := o.Value.(type) {
	case float64:
		// encoding/json decodes every JSON number as float64.
		return int(v), true
	case string:
		n, err := strconv.Atoi(v)
		return n, err == nil
	}
	return 0, false
}

// maxPatchBytes caps a JSON patch body. The largest real one is a presence
// report whose appField is a game-chosen JSON string; 64 KiB is far more than
// any observed value and bounds what a malformed console can allocate.
const maxPatchBytes = 64 << 10

func parseJSONPatch(w http.ResponseWriter, r *http.Request) ([]patchOp, error) {
	var ops []patchOp
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPatchBytes))
	if err := dec.Decode(&ops); err != nil {
		return nil, err
	}
	for _, op := range ops {
		switch op.Op {
		case "add", "replace", "remove":
		default:
			return nil, errors.New("baas: unsupported patch operation " + op.Op)
		}
	}
	return ops, nil
}

// presencePatchFromOps turns a presence JSON patch into a store patch.
//
// Extras are merged into the existing block rather than replacing it, because
// the console sends only the keys that changed and dropping the rest would
// discard, for example, the app id when only the app field moved.
func presencePatchFromOps(ops []patchOp, current db.ExtrasByScope) (db.PresencePatch, error) {
	patch := db.PresencePatch{}

	extras := make(db.ExtrasByScope, len(db.Scopes))
	for _, scope := range db.Scopes {
		kv := map[string]any{}
		for k, v := range current[scope] {
			kv[k] = v
		}
		extras[scope] = kv
	}
	extrasChanged := false

	for _, op := range ops {
		switch {
		case op.Path == "/presence/state":
			v, ok := op.stringValue()
			if !ok || !validPresenceState(v) {
				return patch, errors.New("baas: invalid presence state")
			}
			patch.State = &v

		case op.Path == "/presence/logoutAt":
			// Not observed in a capture, but harmless to honour if a firmware
			// starts sending it.
			n, ok := op.intValue()
			if !ok {
				return patch, errors.New("baas: invalid logoutAt")
			}
			t := time.Unix(int64(n), 0).UTC()
			patch.LogoutAt = &t

		default:
			scope, key, ok := parseExtrasPath(op.Path)
			if !ok {
				return patch, errors.New("baas: unsupported presence patch path " + op.Path)
			}
			if op.Op == "remove" {
				delete(extras[scope], key)
			} else {
				extras[scope][key] = op.Value
			}
			extrasChanged = true

			// The console writes the app fields into the 'friends' scope, which
			// is the one its friends can read. Mirror them into the denormalised
			// columns so the dashboard and phase 2's NPLN presence service do
			// not have to walk JSON to answer "who is playing what".
			if scope != db.ScopeFriends {
				continue
			}
			switch key {
			case keyAppID:
				if v, ok := op.stringValue(); ok {
					v = strings.ToLower(v)
					patch.AppID = &v
				}
			case keyPresenceGroupID:
				if v, ok := op.stringValue(); ok {
					v = strings.ToLower(v)
					patch.PresenceGroupID = &v
				}
			case keyAppField:
				// Stored verbatim. It is the game's own format.
				if v, ok := op.stringValue(); ok {
					patch.AppField = &v
				}
			case keyACDIndex:
				if v, ok := op.intValue(); ok {
					patch.ACDIndex = &v
				}
			}
		}
	}

	if extrasChanged {
		patch.Extras = &extras
	}
	return patch, nil
}

func validPresenceState(v string) bool {
	switch v {
	case db.StateOffline, db.StateInactive, db.StateOnline, db.StatePlaying:
		return true
	}
	return false
}
