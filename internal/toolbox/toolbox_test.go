package toolbox

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestStatusReportsOkForAHealthyService(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer healthy.Close()

	h := New(Config{StatusTargets: map[string]string{
		"baas": strings.TrimPrefix(healthy.URL, "http://"),
	}}, discardLogger())

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)

	var body struct {
		Services []ServiceStatus `json:"services"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Services) != 1 || body.Services[0].Name != "baas" || body.Services[0].Status != "ok" {
		t.Fatalf("unexpected services: %+v", body.Services)
	}
}

func TestStatusReportsDownForAnUnreachableService(t *testing.T) {
	h := New(Config{StatusTargets: map[string]string{
		"dauth": "127.0.0.1:1", // nothing listens on port 1
	}}, discardLogger())

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)

	var body struct {
		Services []ServiceStatus `json:"services"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Services) != 1 || body.Services[0].Status != "down" {
		t.Fatalf("expected down, got %+v", body.Services)
	}
}

// TestStatusNeverGuessesAnUnprobedService is the honesty rule from
// internal/dashboard/servicehealth.go, applied here: a service this package
// cannot probe reports "unknown" with a reason, never a colour it did not
// earn.
func TestStatusNeverGuessesAnUnprobedService(t *testing.T) {
	h := New(Config{Unprobed: map[string]string{
		"dns": "UDP, not HTTP: no /healthz to ask",
	}}, discardLogger())

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)

	var body struct {
		Services []ServiceStatus `json:"services"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Services) != 1 || body.Services[0].Status != "unknown" || body.Services[0].Reason == "" {
		t.Fatalf("expected unknown with a reason, got %+v", body.Services)
	}
}

func TestUpdatesLatestRewritesTheDownloadURLToItself(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("github request missing bearer token: got %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v0.4.0",
			"assets": []map[string]any{
				{"id": 12345, "name": "SwitchNet-Toolbox-v0.4.0.nro", "size": 400000},
			},
		})
	}))
	defer gh.Close()

	h := New(Config{
		GitHubOwner: "n-popescu", GitHubRepo: "switchnet-nro", GitHubToken: "test-token",
	}, discardLogger())
	// Point the handler at the fake GitHub server instead of the real one.
	h.githubAPIBase = gh.URL

	req := httptest.NewRequest(http.MethodGet, "/updates/latest", nil)
	req.Host = "toolbox.example:8443"
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		TagName            string `json:"tag_name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.TagName != "v0.4.0" {
		t.Errorf("tag_name = %q, want v0.4.0", out.TagName)
	}
	if out.Size != 400000 {
		t.Errorf("size = %d, want 400000", out.Size)
	}
	want := "http://toolbox.example:8443/updates/download?asset=12345"
	if out.BrowserDownloadURL != want {
		t.Errorf("browser_download_url = %q, want %q", out.BrowserDownloadURL, want)
	}
}

func TestReleaseIsCachedWithinTTL(t *testing.T) {
	calls := 0
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v0.4.0",
			"assets":   []map[string]any{{"id": 1, "name": "a.nro", "size": 1}},
		})
	}))
	defer gh.Close()

	h := New(Config{
		GitHubOwner: "o", GitHubRepo: "r", GitHubToken: "t", ReleaseCacheTTL: time.Minute,
	}, discardLogger())
	h.githubAPIBase = gh.URL

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/updates/latest", nil)
		rec := httptest.NewRecorder()
		h.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d: status = %d", i, rec.Code)
		}
	}
	if calls != 1 {
		t.Fatalf("github called %d times, want 1 (should have been served from cache)", calls)
	}
}
