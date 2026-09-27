package presence

import (
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
)

const (
	reportTTL  = 5 * time.Minute
	consoleTTL = 5 * time.Minute
)

// TestAFreshReportIsUsedExactlyIncludingOffline is the rule that lets a
// console signing out actually read as offline immediately, rather than being
// overridden by the liveness heartbeat below.
func TestAFreshReportIsUsedExactlyIncludingOffline(t *testing.T) {
	now := time.Now().UTC()
	p := db.Presence{
		State:      db.StateOffline,
		ReportedAt: now.Add(-time.Minute),
		LastSeenAt: now,
		Extras:     db.EmptyExtras(),
	}
	got := Resolve(p, now, reportTTL, consoleTTL)
	if got.State != db.StateOffline {
		t.Errorf("state = %q, want OFFLINE (a fresh report must not be overridden by liveness)", got.State)
	}
}

// TestAnIdleConsoleReadsAsOnlineNotOffline is the rule a console sitting in
// the Home Menu depends on: it reports nothing there, but keeps polling.
func TestAnIdleConsoleReadsAsOnlineNotOffline(t *testing.T) {
	now := time.Now().UTC()
	p := db.Presence{
		State:      db.StateOffline,
		LastSeenAt: now,
		Extras:     db.EmptyExtras(),
	}
	got := Resolve(p, now, reportTTL, consoleTTL)
	if got.State != db.StateOnline {
		t.Errorf("state = %q, want ONLINE from request traffic alone", got.State)
	}
}

// TestAStaleReportAndNoRecentTrafficReadsAsOffline is the default: nothing
// fresh reported, and nothing recently seen.
func TestAStaleReportAndNoRecentTrafficReadsAsOffline(t *testing.T) {
	now := time.Now().UTC()
	p := db.Presence{
		State:      db.StatePlaying,
		ReportedAt: now.Add(-time.Hour),
		LastSeenAt: now.Add(-time.Hour),
		Extras:     db.EmptyExtras(),
	}
	got := Resolve(p, now, reportTTL, consoleTTL)
	if got.State != db.StateOffline {
		t.Errorf("state = %q, want OFFLINE once both signals are stale", got.State)
	}
}

// TestAppFieldsAreClearedWheneverTheEffectiveStateIsNotPlaying covers a
// console that closes a game and reports ONLINE without clearing the app
// fields itself.
func TestAppFieldsAreClearedWheneverTheEffectiveStateIsNotPlaying(t *testing.T) {
	now := time.Now().UTC()
	extras := db.EmptyExtras()
	extras[db.ScopeFriends][KeyAppID] = "0100c2500fc20000"
	extras[db.ScopeFriends][KeyAppField] = `{"mode":"ranked"}`
	extras[db.ScopeFriends]["untouched"] = "kept"

	p := db.Presence{
		State:      db.StateOnline,
		AppID:      "0100c2500fc20000",
		ReportedAt: now,
		Extras:     extras,
	}
	got := Resolve(p, now, reportTTL, consoleTTL)
	if got.AppID != "" {
		t.Errorf("AppID = %q, want cleared for a non-PLAYING state", got.AppID)
	}
	if v := got.Extras[db.ScopeFriends][KeyAppField]; v != nil {
		t.Errorf("appField extra = %v, want cleared", v)
	}
	if v := got.Extras[db.ScopeFriends]["untouched"]; v != "kept" {
		t.Errorf("an unrelated extras key was dropped: %v", v)
	}
}

// TestPlayingStateKeepsItsAppFields is the converse: a fresh PLAYING report
// must not be stripped of the fields that make the applet show it.
func TestPlayingStateKeepsItsAppFields(t *testing.T) {
	now := time.Now().UTC()
	extras := db.EmptyExtras()
	extras[db.ScopeFriends][KeyAppID] = "0100c2500fc20000"

	p := db.Presence{
		State:      db.StatePlaying,
		AppID:      "0100c2500fc20000",
		ReportedAt: now,
		Extras:     extras,
	}
	got := Resolve(p, now, reportTTL, consoleTTL)
	if got.AppID != "0100c2500fc20000" {
		t.Errorf("AppID = %q, want kept for a fresh PLAYING report", got.AppID)
	}
	if v := got.Extras[db.ScopeFriends][KeyAppID]; v != "0100c2500fc20000" {
		t.Errorf("appId extra = %v, want kept", v)
	}
}
