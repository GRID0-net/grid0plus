package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/n-popescu/switchnet/internal/db"
	"github.com/n-popescu/switchnet/internal/toyohr"
)

func newTestServer(t *testing.T, token string, now time.Time) (*Server, *db.Memory) {
	t.Helper()
	store := db.NewMemory()
	srv, err := New(Options{
		Store:       store,
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Tenant:      "dce9377b",
		PresenceTTL: 5 * time.Minute,
		Token:       token,
		Now:         func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return srv, store
}

// newTestServerWithTTLs is newTestServer, but with PresenceTTL and
// ConsolePresenceTTL set independently -- for the regression test below,
// which needs them to actually differ.
func newTestServerWithTTLs(t *testing.T, reportTTL, consoleTTL time.Duration, now time.Time) (*Server, *db.Memory) {
	t.Helper()
	store := db.NewMemory()
	srv, err := New(Options{
		Store:              store,
		Log:                slog.New(slog.NewTextHandler(io.Discard, nil)),
		Tenant:             "dce9377b",
		PresenceTTL:        reportTTL,
		ConsolePresenceTTL: consoleTTL,
		Now:                func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return srv, store
}

// TestPresenceUsesConsolePresenceTTLNotPresenceTTLForLiveness is a regression
// test for a bug where the dashboard resolved BOTH the report TTL and the
// console-liveness TTL from the same field, so an operator who configured
// them differently (as BAAS itself allows) got a dashboard that disagreed
// with the friends applet about who was online.
func TestPresenceUsesConsolePresenceTTLNotPresenceTTLForLiveness(t *testing.T) {
	now := time.Now().UTC()
	// A report TTL so short it is already stale, and a console TTL long
	// enough that the last-seen heartbeat is still fresh.
	srv, store := newTestServerWithTTLs(t, time.Second, time.Hour, now)
	ctx := context.Background()

	addUser(t, store, "idle", "idle")
	store.TouchPresence(ctx, "idle", now.Add(-time.Minute))

	players, err := srv.players(ctx)
	if err != nil {
		t.Fatalf("players: %v", err)
	}
	if len(players) != 1 || players[0].State != "ONLINE" {
		t.Fatalf("got %+v, want a single ONLINE player -- ConsolePresenceTTL (1h) "+
			"should have covered a 1-minute-old heartbeat even though PresenceTTL (1s) would not",
			players)
	}
}

func addUser(t *testing.T, store *db.Memory, id, nick string) {
	t.Helper()
	now := time.Now()
	if err := store.CreateUser(context.Background(),
		db.User{ID: id, Nickname: nick},
		db.DeviceAccount{ID: "da-" + id, UserID: id, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("create user: %v", err)
	}
}

// TestPresenceIsResolvedTheSameWayTheFriendsApplerSeesIt.
//
// The dashboard applies BAAS's rule rather than reading the stored state
// directly. If the two disagree, the operator sees somebody online whose
// friends see them offline, and goes hunting for a bug that is only here.
func TestPresenceIsResolvedTheSameWayTheFriendsAppletSeesIt(t *testing.T) {
	now := time.Now().UTC()
	srv, store := newTestServer(t, "", now)
	ctx := context.Background()

	addUser(t, store, "playing", "playing")
	addUser(t, store, "idle", "idle")
	addUser(t, store, "gone", "gone")
	addUser(t, store, "signed-out", "signed-out")

	p := func(s string) *string { return &s }

	// Reported PLAYING, recently: shows as playing, with the app.
	store.UpdatePresence(ctx, "playing", db.PresencePatch{
		State: p("PLAYING"), AppID: p("0100c2500fc20000"), ReportedAt: &now})

	// Never reported, but seen recently. This is the Home Menu case: the
	// console reports nothing there but keeps polling, and without the
	// last-seen branch every idle player reads as offline.
	store.TouchPresence(ctx, "idle", now)

	// Seen, but long ago.
	old := now.Add(-2 * time.Hour)
	store.TouchPresence(ctx, "gone", old)

	// Reported OFFLINE just now. A fresh report wins over the heartbeat: a
	// console reporting OFFLINE is signing out and must not be overridden.
	store.UpdatePresence(ctx, "signed-out", db.PresencePatch{State: p("OFFLINE"), ReportedAt: &now})
	store.TouchPresence(ctx, "signed-out", now)

	players, err := srv.players(ctx)
	if err != nil {
		t.Fatalf("players: %v", err)
	}
	got := map[string]Player{}
	for _, pl := range players {
		got[pl.ID] = pl
	}

	for id, want := range map[string]string{
		"playing":    "PLAYING",
		"idle":       "ONLINE",
		"gone":       "OFFLINE",
		"signed-out": "OFFLINE",
	} {
		if got[id].State != want {
			t.Errorf("%s resolved to %s, want %s", id, got[id].State, want)
		}
	}

	if got["playing"].Activity != "Splatoon 3" {
		t.Errorf("playing activity = %q, want Splatoon 3", got["playing"].Activity)
	}
	// The app fields must be cleared whenever the state is not PLAYING: a
	// console that closes a game reports ONLINE without clearing them.
	if got["idle"].AppID != "" {
		t.Errorf("an ONLINE player carries an app id: %q", got["idle"].AppID)
	}
}

func TestTheOverviewCountsOnlineAndPlaying(t *testing.T) {
	now := time.Now().UTC()
	srv, store := newTestServer(t, "", now)
	ctx := context.Background()

	addUser(t, store, "a", "a")
	addUser(t, store, "b", "b")
	addUser(t, store, "c", "c")
	p := func(s string) *string { return &s }
	store.UpdatePresence(ctx, "a", db.PresencePatch{State: p("PLAYING"), AppID: p("0100c2500fc20000"), ReportedAt: &now})
	store.TouchPresence(ctx, "b", now)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/overview", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}

	var body struct {
		Players, Online, Playing int
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// A player in game counts as online too -- they are, and an operator
	// reading "1 online, 1 in game" as two people would be wrong.
	if body.Players != 3 || body.Online != 2 || body.Playing != 1 {
		t.Errorf("players=%d online=%d playing=%d, want 3/2/1", body.Players, body.Online, body.Playing)
	}
}

// TestTheFormNormalisesWhatItSafelyCanAndRefusesWhatItCannot.
//
// Two different things, and the split matters:
//
// A misaligned start and a capitalised resource are FIXED, because there is
// exactly one right answer in both cases and the form says so up front. The
// slot grid is not negotiable and the resource is lowercase in every real
// summary.yml, so refusing would make the operator do arithmetic to satisfy a
// rule the server already knows.
//
// A revision that is not 40 hex characters is REFUSED, because there is no
// right answer to substitute -- it has to come from the pack -- and inventing
// one produces a fest the console cannot load.
func TestTheFormNormalisesWhatItSafelyCanAndRefusesWhatItCannot(t *testing.T) {
	now := time.Now().UTC()
	srv, store := newTestServer(t, "", now)
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/fests", strings.NewReader(
		`{"startAt":"2026-09-05T13:01:35Z","regions":["JP"],"resource":"JUEA-LoveFes2"}`)))
	if rec.Code != 200 {
		t.Fatalf("a fixable fest was refused: %d %s", rec.Code, rec.Body)
	}
	var created struct{ ID, Resource string }
	json.Unmarshal(rec.Body.Bytes(), &created)
	if created.Resource != "juea-lovefes2" {
		t.Errorf("resource = %q, want it lowercased", created.Resource)
	}
	row, err := store.GetFest(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.StartAt.Unix()%7200 != 0 {
		t.Errorf("stored start %s is not on a two-hour slot boundary", row.StartAt)
	}

	// A revision that cannot be guessed at is refused, with the reason.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/fests", strings.NewReader(
		`{"regions":["JP"],"revision":"nope"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a bad revision was accepted: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "40 hex") {
		t.Errorf("the error does not say what is wrong: %q", rec.Body.String())
	}
}

func TestAFestCanBeScheduledToggledAndDeleted(t *testing.T) {
	now := time.Now().UTC()
	srv, store := newTestServer(t, "", now)
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/fests",
		strings.NewReader(`{"regions":["JP","EU"],"enabled":false}`)))
	if rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var created struct{ ID string }
	json.Unmarshal(rec.Body.Bytes(), &created)
	if created.ID == "" {
		t.Fatal("no fest id returned")
	}

	// Announce it.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/fests/"+created.ID+"/enabled",
		strings.NewReader(`{"enabled":true}`)))
	if rec.Code != 200 {
		t.Fatalf("toggle: %d %s", rec.Code, rec.Body)
	}
	row, err := store.GetFest(context.Background(), created.ID)
	if err != nil || !row.Enabled {
		t.Errorf("fest not enabled: %+v %v", row, err)
	}

	// And delete it.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/fests/"+created.ID, nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if _, err := store.GetFest(context.Background(), created.ID); err == nil {
		t.Error("the fest survived deletion")
	}
}

// TestTheTokenGuardsEveryRouteThatIsNotAnAsset.
//
// The fest controls schedule and cancel real events. Leaving them open because
// the read-only views happen to be harmless is how a control surface ends up
// on the network with nothing in front of it.
func TestTheTokenGuardsEveryRouteThatIsNotAnAsset(t *testing.T) {
	srv, _ := newTestServer(t, "s3cret", time.Now().UTC())
	h := srv.Handler()

	for _, path := range []string{"/", "/api/overview", "/api/players", "/api/fests"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a token: %d, want 401", path, rec.Code)
		}
	}

	// Creating a fest, specifically.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/fests", strings.NewReader(`{}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("fest creation without a token: %d, want 401", rec.Code)
	}

	// Both ways of presenting it work.
	for name, req := range map[string]*http.Request{
		"query":  httptest.NewRequest("GET", "/api/overview?token=s3cret", nil),
		"bearer": httptest.NewRequest("GET", "/api/overview", nil),
	} {
		if name == "bearer" {
			req.Header.Set("Authorization", "Bearer s3cret")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Errorf("%s token: %d, want 200", name, rec.Code)
		}
	}

	// A wrong token of the same length is still refused.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/overview?token=s3cre7", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("a wrong token was accepted: %d", rec.Code)
	}
}

func TestTheFestListReportsItsPhaseAndWhetherItHasKeys(t *testing.T) {
	now := time.Now().UTC()
	srv, store := newTestServer(t, "", now)

	// Mid-fest, with keys.
	live := toyohr.NewFest(toyohr.AlignToSlot(now.Add(-6*time.Hour)), []string{"JP"})
	store.UpsertFest(context.Background(), toyohr.RowFromFest(live, true,
		[]string{"000102030405060708090a0b0c0d0e0f"}))

	rows, err := srv.festRows(context.Background())
	if err != nil {
		t.Fatalf("festRows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows", len(rows))
	}
	if rows[0].Phase != "first_half" {
		t.Errorf("phase = %q, want first_half", rows[0].Phase)
	}
	if !rows[0].HasKeys {
		t.Error("hasKeys is false for a fest with a key; the operator would not be warned correctly")
	}
	if rows[0].Timetable.Start == "" {
		t.Error("the timetable is not reported")
	}
}

func TestAssetsAreServedWithoutAToken(t *testing.T) {
	srv, _ := newTestServer(t, "s3cret", time.Now().UTC())
	for _, path := range []string{"/assets/dashboard.css", "/assets/dashboard.js"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Errorf("%s: %d -- the page cannot style itself if assets need the token", path, rec.Code)
		}
	}
}
