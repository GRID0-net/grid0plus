package baas

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
)

// An emulator's minute-by-minute PLAYING reports become the play activity a
// Switch shows on its profile: one play per session, minutes while it lasts.
func TestAnEmulatorsPlayBuildsTheProfilePlayLog(t *testing.T) {
	const s3 = "0100c2500fc20000"
	t0 := time.Unix(1_791_000_000, 0)
	log, ok := advancePlayLog("", db.Presence{State: db.StateOnline, ReportedAt: t0.Add(-time.Minute)}, s3, t0)
	if !ok {
		t.Fatal("empty log refused")
	}
	prev := db.Presence{State: db.StatePlaying, AppID: s3}
	for i := 1; i <= 3; i++ {
		prev.ReportedAt = t0.Add(time.Duration(i-1) * time.Minute)
		log, _ = advancePlayLog(log, prev, s3, t0.Add(time.Duration(i)*time.Minute))
	}
	// An hour later is a new session of the same game.
	prev.ReportedAt = t0.Add(3 * time.Minute)
	later := t0.Add(time.Hour)
	log, _ = advancePlayLog(log, prev, s3, later)

	var got []map[string]any
	if err := json.Unmarshal([]byte(log), &got); err != nil || len(got) != 1 {
		t.Fatalf("log %s: %v", log, err)
	}
	e := got[0]
	want := map[string]any{
		"appInfo:appId": s3, "appInfo:presenceGroupId": s3,
		"totalPlayCount": 2.0, "totalPlayTime": 3.0,
		"firstPlayedAt": float64(t0.Unix()), "lastPlayedAt": float64(later.Unix()),
	}
	for k, v := range want {
		if e[k] != v {
			t.Errorf("%s = %v, want %v (log %s)", k, e[k], v, log)
		}
	}
	if len(e) != len(want) {
		t.Errorf("unexpected keys in %s", log)
	}
}

// A log the server cannot read is left alone, never overwritten.
func TestAnUnreadablePlayLogIsNotReplaced(t *testing.T) {
	if _, ok := advancePlayLog("not json", db.Presence{}, "0100c2500fc20000", time.Now()); ok {
		t.Fatal("an unreadable log was replaced")
	}
}
