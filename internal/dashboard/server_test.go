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
	"github.com/n-popescu/switchnet/internal/presencebus"
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

// TestMatchesReturnsHistoryNewestFirst covers the dashboard's own read of
// internal/db's match history, independent of who writes to it.
func TestMatchesReturnsHistoryNewestFirst(t *testing.T) {
	now := time.Now().UTC()
	srv, store := newTestServer(t, "", now)
	ctx := context.Background()

	if err := store.CreateMatch(ctx, db.Match{
		ID: "older", Tenant: "dce9377b", Mode: "turf_war",
		HostUserID: "alice", PlayerIDs: []string{"alice", "bob"},
		CreatedAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("create match: %v", err)
	}
	if err := store.CreateMatch(ctx, db.Match{
		ID: "newer", Tenant: "dce9377b", Mode: "salmon_run",
		HostUserID: "carol", PlayerIDs: []string{"carol", "dave", "erin"},
		Forced: true, CreatedAt: now,
	}); err != nil {
		t.Fatalf("create match: %v", err)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/matches", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	var rows []matchRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d matches, want 2", len(rows))
	}
	if rows[0].ID != "newer" || rows[1].ID != "older" {
		t.Errorf("order = [%s %s], want the newer match first", rows[0].ID, rows[1].ID)
	}
	if !rows[0].Forced {
		t.Error("forced did not round-trip")
	}
	if len(rows[0].PlayerIDs) != 3 {
		t.Errorf("player_ids = %v, want 3 players", rows[0].PlayerIDs)
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

	for _, path := range []string{
		"/api/overview", "/api/players", "/api/players/x", "/api/fests", "/api/matches",
		"/api/coop-shifts", "/api/coop-reference",
		"/api/debug/stats", "/api/debug/accounts/x",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a token: %d, want 401", path, rec.Code)
		}
	}

	// The one page a browser navigates to directly sends the operator to the
	// login form instead of a bare "unauthorised" -- see guardPage's comment.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Errorf("/ without a token: %d %q, want 302 to /login", rec.Code, rec.Header().Get("Location"))
	}

	// Creating a fest, specifically.
	rec = httptest.NewRecorder()
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

// TestLoginSetsACookieThatThenAuthenticatesEveryRoute covers the point of
// the login page: submitting the right token once should mean the operator
// never has to carry ?token= in the URL again. The cookie /login sets has to
// be accepted by guard() (API routes) and guardPage() (the index page)
// alike, or the login only half-works.
func TestLoginSetsACookieThatThenAuthenticatesEveryRoute(t *testing.T) {
	srv, _ := newTestServer(t, "s3cret", time.Now().UTC())
	h := srv.Handler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/login", strings.NewReader("token=s3cret"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("login: %d %q, want 302 to /", rec.Code, rec.Header().Get("Location"))
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != authCookieName || cookies[0].Value != "s3cret" {
		t.Fatalf("login cookies: %+v, want one %s=s3cret", cookies, authCookieName)
	}
	cookie := cookies[0]
	if !cookie.HttpOnly {
		t.Error("the session cookie is not HttpOnly -- it would be readable from a script that has no business touching it")
	}

	for _, path := range []string{"/", "/api/overview"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.AddCookie(cookie)
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Errorf("%s with the login cookie: %d, want 200", path, rec.Code)
		}
	}
}

// TestLoginRefusesTheWrongToken covers the failure path: a wrong token must
// not set the cookie, and must send the operator back to the form rather
// than through to the dashboard.
func TestLoginRefusesTheWrongToken(t *testing.T) {
	srv, _ := newTestServer(t, "s3cret", time.Now().UTC())
	h := srv.Handler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/login", strings.NewReader("token=wrong"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login?failed=1" {
		t.Fatalf("wrong token: %d %q, want 302 to /login?failed=1", rec.Code, rec.Header().Get("Location"))
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("a wrong token must not set the session cookie")
	}
}

// TestLoginPageStepsAsideWhenNoTokenIsConfigured mirrors guard()'s own
// rule: with no token configured there is nothing to authenticate, so the
// login form -- which could never succeed or fail meaningfully -- must not
// be shown, and it must not become a second, weaker way in were a token
// added later.
func TestLoginPageStepsAsideWhenNoTokenIsConfigured(t *testing.T) {
	srv, _ := newTestServer(t, "", time.Now().UTC())
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/login", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Errorf("/login with no token configured: %d %q, want 302 to /", rec.Code, rec.Header().Get("Location"))
	}
}

// TestLogoutClearsTheCookie covers the other direction: the cookie /login
// set has to actually go away, not just expire from the browser's point of
// view eventually -- a MaxAge in the past, not merely an empty value the
// browser would otherwise keep sending.
func TestLogoutClearsTheCookie(t *testing.T) {
	srv, _ := newTestServer(t, "s3cret", time.Now().UTC())
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/logout", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("logout: %d %q, want 302 to /login", rec.Code, rec.Header().Get("Location"))
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != authCookieName || cookies[0].MaxAge >= 0 {
		t.Fatalf("logout cookies: %+v, want one %s with a negative MaxAge", cookies, authCookieName)
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

// TestPlayerDetailAssemblesFriendsAndDeviceAccounts covers the Players tab's
// detail panel: everything it needs is assembled from stores that already
// exist (friendship, device accounts, presence) rather than a new one.
func TestPlayerDetailAssemblesFriendsAndDeviceAccounts(t *testing.T) {
	now := time.Now().UTC()
	srv, store := newTestServer(t, "", now)
	ctx := context.Background()

	addUser(t, store, "alice", "Alice")
	addUser(t, store, "bob", "Bob")
	if err := store.AddFriendship(ctx, "alice", "bob", now.Add(-time.Hour)); err != nil {
		t.Fatalf("add friendship: %v", err)
	}
	if err := store.TouchPresence(ctx, "bob", now); err != nil {
		t.Fatalf("touch presence: %v", err)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/players/alice", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	var got playerDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "alice" || got.Nickname != "Alice" {
		t.Errorf("player = %+v", got.Player)
	}
	if len(got.Friends) != 1 || got.Friends[0].ID != "bob" || got.Friends[0].Nickname != "Bob" {
		t.Fatalf("friends = %+v, want [bob]", got.Friends)
	}
	if got.Friends[0].State != "ONLINE" {
		t.Errorf("bob's state = %q, want ONLINE (a console that just touched presence "+
			"is online the same way an idle Home Menu console is)", got.Friends[0].State)
	}
	if len(got.DeviceAccounts) != 1 || got.DeviceAccounts[0].ID != "da-alice" {
		t.Fatalf("device accounts = %+v, want [da-alice]", got.DeviceAccounts)
	}
	if got.DeviceAccounts[0].CreatedAt == "" {
		t.Error("device account has no createdAt")
	}
}

// TestPlayerDetailWithNoFriendsIsAnEmptyListNotEveryUser is a regression
// guard for db.UserQuery's IDs semantics: a nil slice means "no id filter",
// which for a friendless player would silently return every user on the
// server as their "friends".
func TestPlayerDetailWithNoFriendsIsAnEmptyListNotEveryUser(t *testing.T) {
	now := time.Now().UTC()
	srv, store := newTestServer(t, "", now)
	addUser(t, store, "loner", "Loner")
	addUser(t, store, "someoneElse", "Someone Else")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/players/loner", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got playerDetail
	json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Friends) != 0 {
		t.Fatalf("friends = %+v, want none", got.Friends)
	}
}

func TestPlayerDetailOfAnUnknownPlayerIs404(t *testing.T) {
	srv, _ := newTestServer(t, "", time.Now().UTC())
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/players/ffffffffffffffff", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
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

// TestTheMatchmakingTileTellsNoDataApartFromAnEmptyQueue.
//
// The whole reason this tile was left unwired rather than showing zero: on a
// tile, "nobody is queuing" and "cmd/npln is not running, or redis.url is unset
// in one of the two processes" look identical, and only one of them is worth
// acting on. So the API returns null for the second and a number for the first.
func TestTheMatchmakingTileTellsNoDataApartFromAnEmptyQueue(t *testing.T) {
	now := time.Now().UTC()
	srv, _ := newTestServer(t, "", now)
	ctx := context.Background()

	// No bus at all: the dashboard is a separate process and nothing is
	// publishing.
	var body map[string]any
	getJSON(t, srv, "/api/overview", &body)
	if body["matchmaking"] != nil {
		t.Errorf("matchmaking = %v with no bus, want null", body["matchmaking"])
	}

	bus := presencebus.NewLocal()
	defer bus.Close()
	srv.bus = bus

	// A bus with nothing published is still no data.
	getJSON(t, srv, "/api/overview", &body)
	if body["matchmaking"] != nil {
		t.Errorf("matchmaking = %v with no snapshot, want null", body["matchmaking"])
	}

	// An empty queue is a number.
	if err := bus.PublishStats(ctx, presencebus.Stats{Matchmaking: map[string]int{}}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	getJSON(t, srv, "/api/overview", &body)
	mm, ok := body["matchmaking"].(map[string]any)
	if !ok {
		t.Fatalf("matchmaking = %v with an empty snapshot, want an object", body["matchmaking"])
	}
	if mm["waiting"] != float64(0) {
		t.Errorf("waiting = %v, want 0", mm["waiting"])
	}

	// And a populated one is summed across modes, with the breakdown kept.
	if err := bus.PublishStats(ctx, presencebus.Stats{
		Matchmaking: map[string]int{"turf_war": 3, "salmon_run": 1},
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	getJSON(t, srv, "/api/overview", &body)
	mm = body["matchmaking"].(map[string]any)
	if mm["waiting"] != float64(4) {
		t.Errorf("waiting = %v, want 4", mm["waiting"])
	}
	byMode := mm["byMode"].(map[string]any)
	if byMode["turf_war"] != float64(3) {
		t.Errorf("byMode = %v", byMode)
	}
}

// TestTheMatchListOutlivesTheAccountsInIt guards the match log against being
// tied to the users in it: nickname resolution happens client-side (dashboard.js's
// playerName()), so a player id with no account still comes back rather than
// being dropped or blanked -- history that disappears when somebody leaves is
// not history.
func TestTheMatchListOutlivesTheAccountsInIt(t *testing.T) {
	now := time.Now().UTC()
	srv, store := newTestServer(t, "", now)
	ctx := context.Background()

	addUser(t, store, "alice", "Alice")
	if err := store.CreateMatch(ctx, db.Match{
		ID: "m1", Mode: "turf_war", HostUserID: "alice",
		// "ghost" has no account.
		PlayerIDs: []string{"alice", "ghost"}, CreatedAt: now,
	}); err != nil {
		t.Fatalf("create match: %v", err)
	}

	var rows []matchRow
	getJSON(t, srv, "/api/matches", &rows)
	if len(rows) != 1 {
		t.Fatalf("got %d matches, want 1", len(rows))
	}
	if rows[0].HostUserID != "alice" {
		t.Errorf("hostUserId = %q, want alice", rows[0].HostUserID)
	}
	if len(rows[0].PlayerIDs) != 2 || rows[0].PlayerIDs[1] != "ghost" {
		t.Errorf("playerIds = %v", rows[0].PlayerIDs)
	}
}

func TestThePlayerPageShowsThePresenceTimestampsSeparately(t *testing.T) {
	now := time.Now().UTC()
	srv, store := newTestServer(t, "", now)
	ctx := context.Background()

	addUser(t, store, "alice", "Alice")
	// Reported OFFLINE a while ago, but seen just now. That combination is the
	// one an operator needs to be able to read: the effective state is ONLINE
	// from the heartbeat while the console's own last word was OFFLINE, and
	// showing only one of the three timestamps hides which case it is.
	state := "OFFLINE"
	reportedAt := now.Add(-2 * time.Hour)
	if _, err := store.UpdatePresence(ctx, "alice", db.PresencePatch{
		State:      &state,
		ReportedAt: &reportedAt,
	}); err != nil {
		t.Fatalf("update presence: %v", err)
	}
	if err := store.TouchPresence(ctx, "alice", now); err != nil {
		t.Fatalf("touch presence: %v", err)
	}

	var detail playerDetail
	getJSON(t, srv, "/api/players/alice", &detail)

	if detail.State != "ONLINE" {
		t.Errorf("effective state = %q, want ONLINE from the heartbeat", detail.State)
	}
	if detail.ReportedState != "OFFLINE" {
		t.Errorf("reported state = %q, want the console's own last word", detail.ReportedState)
	}
	if detail.ReportedAt == "" || detail.LastSeenAt == "" {
		t.Error("both timestamps must be shown, or which case this is cannot be read")
	}
	if len(detail.DeviceAccounts) != 1 {
		t.Fatalf("got %d device accounts, want 1", len(detail.DeviceAccounts))
	}
}

func TestThePlayerPageListsFriendsFavouritesFirst(t *testing.T) {
	now := time.Now().UTC()
	srv, store := newTestServer(t, "", now)
	ctx := context.Background()

	addUser(t, store, "alice", "Alice")
	for _, who := range []string{"bob", "carol", "dave"} {
		addUser(t, store, who, who)
		if err := store.AddFriendship(ctx, "alice", who, now); err != nil {
			t.Fatalf("add friendship: %v", err)
		}
	}
	if err := store.SetFavorite(ctx, "alice", "dave", true); err != nil {
		t.Fatalf("set favourite: %v", err)
	}

	var detail playerDetail
	getJSON(t, srv, "/api/players/alice", &detail)

	if len(detail.Friends) != 3 {
		t.Fatalf("got %d friends, want 3", len(detail.Friends))
	}
	// What the operator is looking for should not be buried alphabetically.
	if detail.Friends[0].ID != "dave" || !detail.Friends[0].Favorite {
		t.Errorf("first friend = %+v, want the favourite", detail.Friends[0])
	}
}

func TestAPlayerWhoDoesNotExistIsNotFound(t *testing.T) {
	srv, _ := newTestServer(t, "", time.Now())

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/players/nobody", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// getJSON drives one endpoint and decodes its response.
func getJSON(t *testing.T, srv *Server, path string, into any) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d: %s", path, rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
		t.Fatalf("%s: decode: %v", path, err)
	}
}

// TestAConsoleThatHasNeverReportedDoesNotLookLikeOneThatReportedOffline.
//
// The stored state column defaults to OFFLINE while reported_at stays NULL, so a
// freshly registered account has state=OFFLINE and has never said anything. This
// page exists to tell those two apart -- claiming a report that never happened is
// the same conflation that makes a friend list permanently offline.
//
// Found by running the dashboard against a real account, not by a test.
func TestAConsoleThatHasNeverReportedDoesNotLookLikeOneThatReportedOffline(t *testing.T) {
	now := time.Now().UTC()
	srv, store := newTestServer(t, "", now)

	addUser(t, store, "fresh", "Fresh")
	// Request traffic only: registered, polling, never reported.
	if err := store.TouchPresence(context.Background(), "fresh", now); err != nil {
		t.Fatalf("touch presence: %v", err)
	}

	var detail playerDetail
	getJSON(t, srv, "/api/players/fresh", &detail)

	if detail.State != "ONLINE" {
		t.Errorf("effective state = %q, want ONLINE from the heartbeat", detail.State)
	}
	if detail.ReportedState != "" {
		t.Errorf("reportedState = %q, but this console has never reported", detail.ReportedState)
	}
	if detail.ReportedAt != "" {
		t.Errorf("reportedAt = %q, but this console has never reported", detail.ReportedAt)
	}
	if detail.LastSeenAt == "" {
		t.Error("lastSeenAt is empty, so there is nothing to explain the ONLINE")
	}
}
