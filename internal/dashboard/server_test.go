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

func TestTheMatchListShowsNicknamesAndOutlivesTheAccountsInIt(t *testing.T) {
	now := time.Now().UTC()
	srv, store := newTestServer(t, "", now)
	ctx := context.Background()

	addUser(t, store, "alice", "Alice")
	if err := store.RecordMatch(ctx, db.Match{
		ID: "m1", Mode: "turf_war", Host: "alice",
		// "ghost" has no account: the match log is deliberately not tied to
		// users, because history that disappears when somebody leaves is not
		// history.
		Players: []string{"alice", "ghost"}, CreatedAt: now,
	}); err != nil {
		t.Fatalf("record match: %v", err)
	}

	var rows []matchRow
	getJSON(t, srv, "/api/matches", &rows)
	if len(rows) != 1 {
		t.Fatalf("got %d matches, want 1", len(rows))
	}
	if rows[0].Host != "Alice" {
		t.Errorf("host = %q, want the nickname", rows[0].Host)
	}
	// An id with no user is shown as itself rather than blank, so the row still
	// says who played.
	if len(rows[0].Players) != 2 || rows[0].Players[1] != "ghost" {
		t.Errorf("players = %v", rows[0].Players)
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
