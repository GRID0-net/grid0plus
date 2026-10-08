package npln

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// adminHarness is a server with the admin API turned on.
func adminHarness(t *testing.T) (*harness, http.Handler) {
	t.Helper()
	h := newHarness(t)
	h.srv.adminToken = "test-admin-token"

	admin := h.srv.AdminHandler()
	if admin == nil {
		t.Fatal("AdminHandler returned nil with a token set")
	}
	return h, admin.Handler()
}

func adminDo(t *testing.T, h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestTheAdminAPIIsNotServedWithoutAToken is the one that matters most.
//
// These controls start matches and change how many players one takes. There is
// no version of them that is safe to hand to whoever can reach the port, so an
// operator who has not set a token has not opted in and gets nothing at all.
func TestTheAdminAPIIsNotServedWithoutAToken(t *testing.T) {
	h := newHarness(t)
	h.srv.adminToken = ""

	if admin := h.srv.AdminHandler(); admin != nil {
		t.Error("the admin API was built with no token configured")
	}
}

// TestEveryAdminRouteRequiresTheToken guards each route rather than trusting
// that they all went through the same wrapper.
func TestEveryAdminRouteRequiresTheToken(t *testing.T) {
	_, admin := adminHarness(t)

	for _, tc := range []struct{ method, path string }{
		{"GET", "/matchmaking"},
		{"POST", "/matchmaking/modes/turf_war/start"},
		{"POST", "/matchmaking/modes/turf_war/size"},
		{"POST", "/matchmaking/tickets/anything/cancel"},
	} {
		for _, token := range []string{"", "wrong-token"} {
			rec := adminDo(t, admin, tc.method, tc.path, token, `{"players":2}`)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s with token %q: got %d, want 401",
					tc.method, tc.path, token, rec.Code)
			}
		}
	}
}

// TestForcingAMatchThroughTheAdminAPIStartsItUndersized is the operator's
// button working end to end.
func TestForcingAMatchThroughTheAdminAPIStartsItUndersized(t *testing.T) {
	h, admin := adminHarness(t)

	// A mode that wants far more players than will turn up.
	if _, err := h.srv.queue.SetModeSize("turf_war", 8); err != nil {
		t.Fatalf("SetModeSize: %v", err)
	}
	for _, user := range []string{"alice", "bob"} {
		if _, _, err := h.srv.queue.Enqueue(user, "turf_war"); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	rec := adminDo(t, admin, "POST", "/matchmaking/modes/turf_war/start", "test-admin-token", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Waiting int `json:"waiting"`
		Started *struct {
			Players []string `json:"players"`
			Host    string   `json:"host"`
			Forced  bool     `json:"forced"`
			Mode    string   `json:"mode"`
		} `json:"started"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if body.Started == nil {
		t.Fatal("the response does not say what it started")
	}
	if len(body.Started.Players) != 2 {
		t.Errorf("players = %d, want 2", len(body.Started.Players))
	}
	if body.Started.Host != "alice" {
		t.Errorf("host = %q, want alice", body.Started.Host)
	}
	// The page shows this, and "was this match full?" changes what a console
	// misbehaving afterwards means.
	if !body.Started.Forced {
		t.Error("a 2-of-8 match is not marked forced")
	}
	if body.Waiting != 0 {
		t.Errorf("%d players were left in the queue", body.Waiting)
	}

	// The dashboard's match history is what this button is for as much as the
	// live queue is: an operator checking "did that actually work" later has
	// nothing else to look at once the session ages out of the queue.
	history, err := h.store.ListMatches(t.Context(), 10)
	if err != nil {
		t.Fatalf("ListMatches: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("got %d matches in history, want 1", len(history))
	}
	if !history[0].Forced {
		t.Error("a force-started match is not recorded as forced")
	}
	if history[0].Mode != "turf_war" || history[0].HostUserID != "alice" {
		t.Errorf("recorded match = %+v, want mode turf_war, host alice", history[0])
	}
}

// TestForcingAnEmptyQueueSaysSoRatherThanFailing: an operator pressing a button
// for a queue that just emptied should read an explanation, not a stack trace.
func TestForcingAnEmptyQueueSaysSoRatherThanFailing(t *testing.T) {
	_, admin := adminHarness(t)

	rec := adminDo(t, admin, "POST", "/matchmaking/modes/turf_war/start", "test-admin-token", "")
	if rec.Code != http.StatusConflict {
		t.Errorf("got %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "turf_war") {
		t.Errorf("the message does not name the mode: %q", rec.Body.String())
	}
}

// TestSettingAModeSizeThroughTheAdminAPI covers the durable control, including
// that it forms the matches the new size allows.
func TestSettingAModeSizeThroughTheAdminAPI(t *testing.T) {
	h, admin := adminHarness(t)

	h.srv.queue.SetModeSize("turf_war", 8)
	for _, user := range []string{"a", "b"} {
		h.srv.queue.Enqueue(user, "turf_war")
	}

	rec := adminDo(t, admin, "POST", "/matchmaking/modes/turf_war/size",
		"test-admin-token", `{"players":2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Waiting int `json:"waiting"`
		Formed  []struct {
			Players []string `json:"players"`
		} `json:"formed"`
		Modes []struct {
			Mode       string `json:"mode"`
			Size       int    `json:"size"`
			Overridden bool   `json:"overridden"`
		} `json:"modes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(body.Formed) != 1 {
		t.Fatalf("formed %d matches, want 1", len(body.Formed))
	}
	if body.Waiting != 0 {
		t.Errorf("%d still waiting", body.Waiting)
	}
	// The mode stays visible with its override, so the control does not vanish
	// the moment it works.
	found := false
	for _, m := range body.Modes {
		if m.Mode == "turf_war" {
			found = true
			if m.Size != 2 || !m.Overridden {
				t.Errorf("turf_war = size %d overridden %v, want 2/true", m.Size, m.Overridden)
			}
		}
	}
	if !found {
		t.Error("turf_war disappeared from the snapshot after its queue emptied")
	}

	// A nonsense size is refused with a reason.
	rec = adminDo(t, admin, "POST", "/matchmaking/modes/turf_war/size",
		"test-admin-token", `{"players":-1}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a negative size got %d, want 400", rec.Code)
	}
	rec = adminDo(t, admin, "POST", "/matchmaking/modes/turf_war/size",
		"test-admin-token", `not json`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a malformed body got %d, want 400", rec.Code)
	}
}

// TestCancellingATicketThroughTheAdminAPI covers dropping a console that queued
// and went away.
func TestCancellingATicketThroughTheAdminAPI(t *testing.T) {
	h, admin := adminHarness(t)

	ticket, _, err := h.srv.queue.Enqueue("ghost", "turf_war")
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	rec := adminDo(t, admin, "POST", "/matchmaking/tickets/"+ticket.ID+"/cancel",
		"test-admin-token", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Waiting int `json:"waiting"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Waiting != 0 {
		t.Errorf("%d still waiting after the cancel", body.Waiting)
	}

	// Cancelling it again has nothing to do.
	rec = adminDo(t, admin, "POST", "/matchmaking/tickets/"+ticket.ID+"/cancel",
		"test-admin-token", "")
	if rec.Code != http.StatusConflict {
		t.Errorf("a second cancel got %d, want 409", rec.Code)
	}
}

// TestTheSnapshotShowsWaitsInSecondsNotDurationStrings pins the wire shape the
// page depends on: a Go duration would serialise as nanoseconds or as "1m30s"
// depending on the type, and the page formats it itself.
func TestTheSnapshotShowsWaitsInSecondsNotDurationStrings(t *testing.T) {
	h, admin := adminHarness(t)

	h.srv.queue.Enqueue("alice", "turf_war")
	h.now = h.now.Add(90 * 1e9) // 90 seconds; the queue reads the same clock.

	rec := adminDo(t, admin, "GET", "/matchmaking", "test-admin-token", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}

	var body struct {
		DefaultSize int `json:"defaultSize"`
		Modes       []struct {
			Waiting []struct {
				UserID            string `json:"userId"`
				WaitingForSeconds int    `json:"waitingForSeconds"`
			} `json:"waiting"`
		} `json:"modes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.DefaultSize == 0 {
		t.Error("defaultSize is missing")
	}
	// Every known mode is listed, waiting or not; exactly one has alice.
	var waiting []struct {
		UserID            string `json:"userId"`
		WaitingForSeconds int    `json:"waitingForSeconds"`
	}
	for _, m := range body.Modes {
		waiting = append(waiting, m.Waiting...)
	}
	if len(waiting) != 1 {
		t.Fatalf("expected one waiting ticket, got %+v", body.Modes)
	}
	if got := waiting[0].WaitingForSeconds; got != 90 {
		t.Errorf("waitingForSeconds = %d, want 90", got)
	}
	if waiting[0].UserID != "alice" {
		t.Errorf("userId = %q", waiting[0].UserID)
	}
}

func TestTheAdminSnapshotReportsTheObservedRematchPhase(t *testing.T) {
	h, admin := adminHarness(t)
	h.srv.SetSessionPhaseReader(func(scope string, players int) string {
		if scope == "" || players != 2 {
			t.Fatalf("phase lookup = %q/%d", scope, players)
		}
		return "rematch_waiting"
	})
	h.srv.queue.SetModeSize("turf_war", 8)
	for _, user := range []string{"alice", "bob"} {
		if _, _, err := h.srv.queue.Enqueue(user, "turf_war"); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := h.srv.queue.ForceMatch("turf_war"); err != nil || !ok {
		t.Fatalf("force match: %v, %v", ok, err)
	}
	rec := adminDo(t, admin, "GET", "/matchmaking", "test-admin-token", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot: %d", rec.Code)
	}
	var body struct {
		Sessions []struct {
			Phase string `json:"phase"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Sessions) != 1 || body.Sessions[0].Phase != "rematch_waiting" {
		t.Fatalf("wire phase = %+v", body.Sessions)
	}
}

func TestCompletedBattlesLeaveTheLiveViewWithoutDeletingTheirRematchSession(t *testing.T) {
	h, admin := adminHarness(t)
	phase := "results"
	h.srv.SetSessionPhaseReader(func(string, int) string { return phase })
	h.srv.queue.SetModeSize("turf_war", 8)
	for _, user := range []string{"alice", "bob"} {
		if _, _, err := h.srv.queue.Enqueue(user, "turf_war"); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := h.srv.queue.ForceMatch("turf_war"); err != nil || !ok {
		t.Fatalf("force match: %v, %v", ok, err)
	}
	read := func(want int) {
		t.Helper()
		rec := adminDo(t, admin, "GET", "/matchmaking", "test-admin-token", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("snapshot: %d", rec.Code)
		}
		var body struct {
			Sessions []adminSession `json:"sessions"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Sessions) != want {
			t.Fatalf("phase %s: %d live sessions, want %d", phase, len(body.Sessions), want)
		}
	}
	read(0)
	phase = "rematch_waiting"
	read(1)
	phase = "starting"
	read(1)
	phase = "results"
	read(0)
}
