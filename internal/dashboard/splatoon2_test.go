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

func withNEX(t *testing.T, nex http.Handler) *Server {
	t.Helper()
	upstream := httptest.NewServer(nex)
	t.Cleanup(upstream.Close)
	srv, err := New(Options{
		Store:         db.NewMemory(),
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		Tenant:        "dce9377b",
		Now:           time.Now,
		NEXAdminURL:   upstream.URL,
		NEXAdminToken: "nex-token",
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return srv
}

func TestTheSplatoon2PanelSaysWhenItIsNotConfigured(t *testing.T) {
	srv, _ := newTestServer(t, "", time.Now().UTC())
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/splatoon2", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 -- an unconfigured panel is a valid state", rec.Code)
	}
	var body struct {
		Configured bool   `json:"configured"`
		Reason     string `json:"reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Configured || !strings.Contains(body.Reason, "nex.admin_token") ||
		!strings.Contains(body.Reason, "dashboard.nex_admin_url") {
		t.Fatalf("unconfigured panel said %+v", body)
	}
}

func TestTheSplatoon2PanelForwardsTokenPathAndOrder(t *testing.T) {
	var gotAuth, gotMethod, gotPath, gotBody string
	srv := withNEX(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotMethod, gotPath = r.Header.Get("Authorization"), r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		_, _ = w.Write([]byte(`{"players":[{"userId":"0123456789abcdef"}]}`))
	}))

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/splatoon2/players/0123456789abcdef/gear-order",
		strings.NewReader(`{"kind":1,"id":21001,"skill":3,"price":4500}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer nex-token" || gotMethod != "POST" || gotPath != "/splatoon2/players/0123456789abcdef/gear-order" {
		t.Fatalf("upstream saw %q %q %q", gotAuth, gotMethod, gotPath)
	}
	var order map[string]int
	if err := json.Unmarshal([]byte(gotBody), &order); err != nil || order["id"] != 21001 || order["price"] != 4500 {
		t.Fatalf("upstream body %q", gotBody)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["configured"] != true || out["players"] == nil {
		t.Fatalf("dashboard answered %v", out)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/splatoon2/players/0123456789abcdef/gear-order", nil))
	if rec.Code != http.StatusOK || gotMethod != "DELETE" {
		t.Fatalf("cancel: %d, upstream %s", rec.Code, gotMethod)
	}
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/splatoon2/players/0123456789abcdef/disconnect", nil))
	if rec.Code != http.StatusOK || gotPath != "/splatoon2/players/0123456789abcdef/disconnect" {
		t.Fatalf("disconnect: %d, upstream %s", rec.Code, gotPath)
	}
}

func TestTheSplatoon2SectionsReachOnlyTheirOwnRoutes(t *testing.T) {
	var paths []string
	srv := withNEX(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		_, _ = w.Write([]byte(`{"posts":[]}`))
	}))
	for _, section := range []string{"fests", "leagues", "plaza", "gear-orders", "uploads"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/splatoon2/"+section, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"configured":true`) {
			t.Errorf("%s: %d %s", section, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/splatoon2/players", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("an unlisted section got %d, want 404", rec.Code)
	}
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/splatoon2/plaza/123/hidden",
		strings.NewReader(`{"hidden":true}`)))
	if rec.Code != http.StatusOK {
		t.Errorf("hide: %d", rec.Code)
	}
	want := []string{"GET /splatoon2/fests", "GET /splatoon2/leagues", "GET /splatoon2/plaza",
		"GET /splatoon2/gear-orders", "GET /splatoon2/uploads", "POST /splatoon2/plaza/123/hidden"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Errorf("upstream saw %v", paths)
	}
}

func TestAWrongNEXTokenNamesTheTwoSettings(t *testing.T) {
	srv := withNEX(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/splatoon2", nil))
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "dashboard.nex_admin_token") {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}
