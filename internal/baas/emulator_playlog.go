package baas

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
)

// A console writes its own play activity, the list a friend sees on its
// profile, as extras.everyone.playLog: a JSON string holding one entry per
// game. An emulator has no system module that writes it, so its profile
// showed no games at all. GRID0+ keeps that list for an emulator from the
// presence it reports once a minute, in the console's own shape (read from a
// stored console record on 2026-10-11):
//
//	[{"appInfo:appId":"0100c2500fc20000","appInfo:presenceGroupId":"0100c2500fc20000",
//	  "totalPlayCount":155,"totalPlayTime":1086,"firstPlayedAt":1790536430,"lastPlayedAt":1791739223}]
//
// totalPlayTime is in minutes: 1086 across 155 sessions fits that, seconds
// would be under ten minutes in total.

// emulatorSessionGap is how long without a report ends a play session. The
// forks report every 60 s; a missed report or two is still the same session.
const emulatorSessionGap = 3 * time.Minute

// recordEmulatorPlay updates the caller's play log for one presence report.
// prev is the presence before this report. A failure is logged, not returned:
// the play log is cosmetic and must never cost the presence update.
func (s *Server) recordEmulatorPlay(ctx context.Context, userID string, prev db.Presence, state, appID string, now time.Time) {
	appID = strings.ToLower(appID)
	if state != db.StatePlaying || strings.Trim(appID, "0") == "" {
		return
	}
	user, err := s.store.GetUser(ctx, userID)
	if err != nil {
		s.log.Warn("baas: emulator play log: load user", "user", userID, "error", err)
		return
	}
	extras := user.Extras
	if extras == nil {
		extras = db.EmptyExtras()
	}
	if extras[db.ScopeEveryone] == nil {
		extras[db.ScopeEveryone] = map[string]any{}
	}
	raw, _ := extras[db.ScopeEveryone]["playLog"].(string)
	log, ok := advancePlayLog(raw, prev, appID, now)
	if !ok {
		return
	}
	extras[db.ScopeEveryone]["playLog"] = log
	if _, err := s.store.UpdateUser(ctx, userID, db.UserPatch{Extras: &extras}); err != nil {
		s.log.Warn("baas: emulator play log: store", "user", userID, "error", err)
	}
}

// advancePlayLog returns the play log after one PLAYING report of appID.
// Entries keep any key this code does not know, so a field a console wrote
// survives. ok is false when the stored log cannot be read; it is then left
// as it is rather than replaced.
func advancePlayLog(raw string, prev db.Presence, appID string, now time.Time) (string, bool) {
	var entries []map[string]any
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &entries); err != nil {
			return "", false
		}
	}
	var entry map[string]any
	for _, e := range entries {
		if id, _ := e["appInfo:appId"].(string); id == appID {
			entry = e
			break
		}
	}
	if entry == nil {
		entry = map[string]any{
			"appInfo:appId":           appID,
			"appInfo:presenceGroupId": appID,
			"totalPlayCount":          0,
			"totalPlayTime":           0,
			"firstPlayedAt":           now.Unix(),
		}
		entries = append(entries, entry)
	}
	since := now.Sub(prev.ReportedAt)
	continuing := prev.State == db.StatePlaying && strings.EqualFold(prev.AppID, appID) &&
		!prev.ReportedAt.IsZero() && since >= 0 && since <= emulatorSessionGap
	if continuing {
		entry["totalPlayTime"] = number(entry["totalPlayTime"]) + int64(math.Round(since.Minutes()))
	} else {
		entry["totalPlayCount"] = number(entry["totalPlayCount"]) + 1
	}
	entry["lastPlayedAt"] = now.Unix()
	out, err := json.Marshal(entries)
	if err != nil {
		return "", false
	}
	return string(out), true
}

func number(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
}
