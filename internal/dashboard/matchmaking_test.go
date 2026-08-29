package dashboard

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/n-popescu/switchnet/internal/db"
)

// withNPLN builds a dashboard pointed at a stand-in for cmd/npln's admin API.
func withNPLN(t *testing.T, npln http.Handler) *Server {
	t.Helper()

	upstream := httptest.NewServer(npln)
	t.Cleanup(upstream.Close)

	srv, err := New(Options{
		Store:          db.NewMemory(),
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		Tenant:         "dce9377b",
		PresenceTTL:    5 * time.Minute,
		Now:            time.Now,
		NPLNAdminURL:   upstream.URL,
		NPLNAdminToken: "shared-token",
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return srv
}

// TestTheMatchmakingPanelSaysWhenItIsNotConfigured.
//
// The failure this avoids is the one internal/dashboard's README already warns
// about for presence: a panel that renders an EMPTY queue when it simply cannot
// reach the queue reads as "nobody is playing", and an operator believes it.
func TestTheMatchmakingPanelSaysWhenItIsNotConfigured(t *testing.T) {
	srv, _ := newTestServer(t, "", time.Now().UTC())

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/matchmaking", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 -- an unconfigured panel is a valid state", rec.Code)
	}

	var body struct {
		Configured bool   `json:"configured"`
		Reason     string `json:"reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Configured {
		t.Error("an unconfigured dashboard claims the controls are configured")
	}
	// The reason has to name what to set, or it is just a shrug.
	for _, want := range []string{"npln.admin_listen", "npln.admin_token"} {
		if !strings.Contains(body.Reason, want) {
			t.Errorf("the reason does not mention %q: %q", want, body.Reason)
		}
	}
}

// TestTheDashboardForwardsTheAdminTokenAndThePlayersCount covers the happy path
// across the process boundary.
func TestTheDashboardForwardsTheAdminTokenAndThePlayersCount(t *testing.T) {
	var gotAuth, gotBody, gotPath string

	srv := withNPLN(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"defaultSize":8,"waiting":0,"modes":[],"sessions":[]}`))
	}))

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/matchmaking/modes/turf_war/size", strings.NewReader(`{"players":2}`)))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer shared-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotPath != "/matchmaking/modes/turf_war/size" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotBody, `"players":2`) {
		t.Errorf("body = %q", gotBody)
	}

	// The page needs one shape whether or not the controls are set up, so
	// `configured` is added to whatever npln said.
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body["configured"] != true {
		t.Error("the response does not say it is configured")
	}
	if body["defaultSize"] != float64(8) {
		t.Errorf("npln's own fields did not survive: %v", body)
	}
}

// TestAModeNameWithASlashCannotEscapeTheAdminPath.
//
// Mode strings come from the game, not from us. One containing a slash or a
// traversal must address the route it was meant to, not a different one.
func TestAModeNameWithASlashCannotEscapeTheAdminPath(t *testing.T) {
	var gotPath string

	srv := withNPLN(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// EscapedPath, because that is what an upstream router matches on.
		gotPath = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{}`))
	}))

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/matchmaking/modes/"+"..%2Fttickets%2Fx"+"/start", nil))

	if strings.Contains(gotPath, "/tickets/") {
		t.Errorf("a mode name reached a different route: %q", gotPath)
	}
	if strings.Contains(gotPath, "..") && !strings.Contains(gotPath, "%2E%2E") &&
		!strings.Contains(gotPath, ".."+"%2F") {
		// A literal ".." segment that is not escaped would be collapsed by a
		// router and could address a parent path.
		t.Errorf("an unescaped traversal reached the upstream: %q", gotPath)
	}
}

// TestNPLNBeingDownIsA502WithItsOwnReason.
//
// The dashboard is fine and the thing behind it is not, and that distinction is
// the first thing an operator needs. A 500 would point them at the wrong process.
func TestNPLNBeingDownIsA502WithItsOwnReason(t *testing.T) {
	srv := withNPLN(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nobody is queuing for turf_war", http.StatusConflict)
	}))

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/matchmaking/modes/turf_war/start", nil))

	if rec.Code != http.StatusBadGateway {
		t.Errorf("got %d, want 502", rec.Code)
	}
	// npln's own sentence, not a generic failure.
	if !strings.Contains(rec.Body.String(), "nobody is queuing for turf_war") {
		t.Errorf("the upstream reason was lost: %q", rec.Body.String())
	}
}

// TestAMismatchedAdminTokenExplainsItself, because "401" alone sends an operator
// looking in the wrong configuration file.
func TestAMismatchedAdminTokenExplainsItself(t *testing.T) {
	srv := withNPLN(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/matchmaking", nil))

	body := rec.Body.String()
	if !strings.Contains(body, "npln.admin_token") || !strings.Contains(body, "dashboard.npln_admin_token") {
		t.Errorf("the message does not name both settings that must match: %q", body)
	}
}

// TestTheMatchmakingControlsAreBehindTheDashboardToken: they start matches, so
// they must not be reachable by anyone who can reach the port when a token is set.
func TestTheMatchmakingControlsAreBehindTheDashboardToken(t *testing.T) {
	srv, _ := newTestServer(t, "s3cret", time.Now().UTC())

	for _, path := range []string{
		"/api/matchmaking",
		"/api/matchmaking/modes/turf_war/start",
		"/api/matchmaking/modes/turf_war/size",
		"/api/matchmaking/tickets/x/cancel",
	} {
		method := "POST"
		if path == "/api/matchmaking" {
			method = "GET"
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader("{}")))
		if rec.Code == http.StatusOK {
			t.Errorf("%s %s was served without the dashboard token", method, path)
		}
	}
}

// TestABareHostPortIsAcceptedAsTheAdminAddress: every other listen address in
// the configuration is host:port, so this one has to be too.
func TestABareHostPortIsAcceptedAsTheAdminAddress(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"127.0.0.1:8088", "http://127.0.0.1:8088"},
		{"http://127.0.0.1:8088", "http://127.0.0.1:8088"},
		{"https://npln.lan/", "https://npln.lan"},
		{"  127.0.0.1:8088  ", "http://127.0.0.1:8088"},
	} {
		c := newMatchmakingClient(tc.in, "t")
		if c == nil {
			t.Fatalf("%q was rejected", tc.in)
		}
		if c.base != tc.want {
			t.Errorf("%q -> %q, want %q", tc.in, c.base, tc.want)
		}
	}

	if newMatchmakingClient("", "t") != nil {
		t.Error("an empty address built a client")
	}
	if newMatchmakingClient("127.0.0.1:8088", "") != nil {
		t.Error("an empty token built a client")
	}
}
