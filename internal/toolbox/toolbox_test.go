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
		"natcheck": "UDP, not HTTP: no /healthz to ask",
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

// TestUpdatesLatestBuildsTheDownloadURLFromConfig is the regression test for
// the bug this shape replaced: browser_download_url used to come from the
// incoming request (r.TLS, r.Host), which behind the real nginx.toolbox.conf
// is always plain HTTP with the port stripped by nginx's $host -- so every
// real response came back "http://host/..." instead of
// "https://host:8443/...". PublicBaseURL removes the request from the
// equation entirely; this test's httptest.NewRequest deliberately carries no
// Host override, unlike the old version of this test, to prove the response
// no longer depends on it.
func TestUpdatesLatestBuildsTheDownloadURLFromConfig(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("github request missing bearer token: got %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v0.4.0",
			"assets": []map[string]any{
				{"id": 12345, "name": "GRID0+-Toolbox-v0.4.0.nro", "size": 400000},
			},
		})
	}))
	defer gh.Close()

	h := New(Config{
		GitHubOwner: "n-popescu", GitHubRepo: "switchnet-nro", GitHubToken: "test-token",
		PublicBaseURL: "https://toolbox.example:8443",
	}, discardLogger())
	// Point the handler at the fake GitHub server instead of the real one.
	h.githubAPIBase = gh.URL

	req := httptest.NewRequest(http.MethodGet, "/updates/latest", nil)
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
	want := "https://toolbox.example:8443/updates/download?asset=12345"
	if out.BrowserDownloadURL != want {
		t.Errorf("browser_download_url = %q, want %q", out.BrowserDownloadURL, want)
	}
}

// TestUpdatesLatestRequiresPublicBaseURL checks the fourth field joined the
// existing all-or-nothing gate: GitHub credentials alone are not enough to
// build a download URL a console can actually use.
func TestUpdatesLatestRequiresPublicBaseURL(t *testing.T) {
	h := New(Config{
		GitHubOwner: "o", GitHubRepo: "r", GitHubToken: "t", // no PublicBaseURL
	}, discardLogger())

	req := httptest.NewRequest(http.MethodGet, "/updates/latest", nil)
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d (updates not configured)", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestNroAssetRejectsMultipleCandidates checks nroAsset actually refuses an
// ambiguous release rather than silently taking the first match -- the
// property its own doc comment claims.
func TestNroAssetRejectsMultipleCandidates(t *testing.T) {
	rel := githubRelease{
		TagName: "v0.5.0",
		Assets: []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
			Size int64  `json:"size"`
		}{
			{ID: 1, Name: "GRID0+-Toolbox-v0.5.0.nro", Size: 1},
			{ID: 2, Name: "GRID0+-Toolbox-v0.5.0-debug.nro", Size: 2},
		},
	}
	if _, _, err := rel.nroAsset(); err == nil {
		t.Error("nroAsset() with two .nro assets returned no error, want one naming the ambiguity")
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
		GitHubOwner: "o", GitHubRepo: "r", GitHubToken: "t",
		PublicBaseURL: "https://toolbox.example:8443", ReleaseCacheTTL: time.Minute,
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

// ---- GET /updates/latest/download ---------------------------------------

func TestUpdatesLatestDownloadSuccess(t *testing.T) {
	nroBytes := []byte("fake-nro-content")
	latest := map[string]any{
		"tag_name": "v0.9.0",
		"assets": []map[string]any{
			{"id": 999, "name": "GRID0+-Toolbox-v0.9.0.nro", "size": int64(len(nroBytes))},
			{"id": 1000, "name": "README.txt", "size": 10},
		},
	}
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("github request missing bearer token: got %q", got)
		}
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
				t.Errorf("latest Accept = %q, want application/vnd.github+json", got)
			}
			_ = json.NewEncoder(w).Encode(latest)
			return
		}
		if strings.Contains(r.URL.Path, "/releases/assets/999") {
			if got := r.Header.Get("Accept"); got != "application/octet-stream" {
				t.Errorf("asset Accept = %q, want application/octet-stream", got)
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", "16")
			if _, err := w.Write(nroBytes); err != nil {
				t.Fatalf("write asset: %v", err)
			}
			return
		}
		http.NotFound(w, r)
	}))
	defer gh.Close()

	h := New(Config{
		GitHubOwner: "o", GitHubRepo: "r", GitHubToken: "test-token",
		PublicBaseURL: "https://toolbox.example:8443",
	}, discardLogger())
	h.githubAPIBase = gh.URL

	req := httptest.NewRequest(http.MethodGet, "/updates/latest/download", nil)
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "switchnet.nro") {
		t.Errorf("Content-Disposition = %q, want to contain switchnet.nro", got)
	}
	if rec.Body.String() != string(nroBytes) {
		t.Errorf("body = %q, want %q", rec.Body.String(), string(nroBytes))
	}
	if strings.Contains(rec.Body.String(), "test-token") {
		t.Error("response leaked GitHub token")
	}
	if strings.Contains(rec.Header().Get("Content-Disposition"), "test-token") {
		t.Error("header leaked GitHub token")
	}
}

func TestUpdatesLatestDownloadSelectsSingleNro(t *testing.T) {
	nroBytes := []byte("nro")
	var requestedAsset string
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v0.9.1",
				"assets": []map[string]any{
					{"id": 111, "name": "notes.txt", "size": 5},
					{"id": 222, "name": "GRID0+-Toolbox-v0.9.1.nro", "size": 3},
					{"id": 333, "name": "other.zip", "size": 10},
				},
			})
			return
		}
		if strings.Contains(r.URL.Path, "/releases/assets/") {
			requestedAsset = r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			_, _ = w.Write(nroBytes)
			return
		}
		http.NotFound(w, r)
	}))
	defer gh.Close()

	h := New(Config{GitHubOwner: "o", GitHubRepo: "r", GitHubToken: "test-token", PublicBaseURL: "https://toolbox.example:8443"}, discardLogger())
	h.githubAPIBase = gh.URL

	req := httptest.NewRequest(http.MethodGet, "/updates/latest/download", nil)
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if requestedAsset != "222" {
		t.Errorf("requested asset = %q, want 222 (the .nro)", requestedAsset)
	}
}

func TestUpdatesLatestDownloadMultipleNroReturns502(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v0.9.0",
			"assets": []map[string]any{
				{"id": 1, "name": "a.nro", "size": 1},
				{"id": 2, "name": "b.nro", "size": 2},
			},
		})
	}))
	defer gh.Close()

	h := New(Config{GitHubOwner: "o", GitHubRepo: "r", GitHubToken: "t", PublicBaseURL: "https://toolbox.example:8443"}, discardLogger())
	h.githubAPIBase = gh.URL

	req := httptest.NewRequest(http.MethodGet, "/updates/latest/download", nil)
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	if strings.Contains(rec.Body.String(), "Bearer") {
		t.Error("response leaked token")
	}
}

func TestUpdatesLatestDownloadNoNroReturns502(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v0.9.0",
			"assets": []map[string]any{
				{"id": 1, "name": "README.md", "size": 10},
			},
		})
	}))
	defer gh.Close()

	h := New(Config{GitHubOwner: "o", GitHubRepo: "r", GitHubToken: "t", PublicBaseURL: "https://toolbox.example:8443"}, discardLogger())
	h.githubAPIBase = gh.URL

	req := httptest.NewRequest(http.MethodGet, "/updates/latest/download", nil)
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
}

func TestUpdatesLatestDownloadRequiresConfig(t *testing.T) {
	h := New(Config{GitHubOwner: "o", GitHubRepo: "r", GitHubToken: "t"}, discardLogger())
	req := httptest.NewRequest(http.MethodGet, "/updates/latest/download", nil)
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}

	// Also when nothing is configured.
	h2 := New(Config{}, discardLogger())
	rec2 := httptest.NewRecorder()
	h2.Handler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/updates/latest/download", nil))
	if rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("empty config: status = %d, want %d", rec2.Code, http.StatusServiceUnavailable)
	}
}

func TestUpdatesLatestDownloadGitHubFailure(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer gh.Close()

	h := New(Config{GitHubOwner: "o", GitHubRepo: "r", GitHubToken: "t", PublicBaseURL: "https://toolbox.example:8443"}, discardLogger())
	h.githubAPIBase = gh.URL

	// /updates/latest should 502 on GH failure.
	req := httptest.NewRequest(http.MethodGet, "/updates/latest", nil)
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("/updates/latest: status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	if strings.Contains(rec.Body.String(), "Bearer") {
		t.Error("leaked token in /updates/latest error")
	}

	// Same GH failure via the new endpoint must use same behavior.
	req2 := httptest.NewRequest(http.MethodGet, "/updates/latest/download", nil)
	rec2 := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusBadGateway {
		t.Fatalf("/updates/latest/download: status = %d, want %d", rec2.Code, http.StatusBadGateway)
	}
	if strings.Contains(rec2.Body.String(), "test-token") {
		t.Error("leaked token in /updates/latest/download error")
	}
}

func TestUpdatesLatestDownloadGitHubAssetFailure(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v0.9.0",
				"assets":   []map[string]any{{"id": 999, "name": "a.nro", "size": 1}},
			})
			return
		}
		if strings.Contains(r.URL.Path, "/releases/assets/") {
			http.Error(w, "upstream down", http.StatusBadGateway)
			return
		}
		http.NotFound(w, r)
	}))
	defer gh.Close()

	h := New(Config{GitHubOwner: "o", GitHubRepo: "r", GitHubToken: "test-token", PublicBaseURL: "https://toolbox.example:8443"}, discardLogger())
	h.githubAPIBase = gh.URL

	req := httptest.NewRequest(http.MethodGet, "/updates/latest/download", nil)
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	if strings.Contains(rec.Body.String(), "test-token") {
		t.Error("leaked GitHub token in asset failure response")
	}
	if strings.Contains(rec.Header().Get("Authorization"), "test-token") {
		t.Error("leaked token in response header")
	}
}

func TestUpdatesLatestBehaviorUnchanged(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v0.4.0",
			"assets":   []map[string]any{{"id": 12345, "name": "GRID0+-Toolbox-v0.4.0.nro", "size": 400000}},
		})
	}))
	defer gh.Close()

	h := New(Config{GitHubOwner: "o", GitHubRepo: "r", GitHubToken: "t", PublicBaseURL: "https://toolbox.example:8443"}, discardLogger())
	h.githubAPIBase = gh.URL

	req := httptest.NewRequest(http.MethodGet, "/updates/latest", nil)
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		TagName string `json:"tag_name"`
		URL     string `json:"browser_download_url"`
		Size    int64  `json:"size"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.TagName != "v0.4.0" || out.Size != 400000 {
		t.Fatalf("unexpected latest body: %+v", out)
	}
	if out.URL != "https://toolbox.example:8443/updates/download?asset=12345" {
		t.Errorf("browser_download_url = %q", out.URL)
	}
}

func TestUpdatesDownloadBehaviorUnchanged(t *testing.T) {
	nroBytes := []byte("nro-bytes")
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/releases/assets/123") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(nroBytes)
	}))
	defer gh.Close()

	h := New(Config{GitHubOwner: "o", GitHubRepo: "r", GitHubToken: "t", PublicBaseURL: "https://toolbox.example:8443"}, discardLogger())
	h.githubAPIBase = gh.URL

	req := httptest.NewRequest(http.MethodGet, "/updates/download?asset=123", nil)
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != string(nroBytes) {
		t.Errorf("body = %q, want %q", rec.Body.String(), string(nroBytes))
	}
	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q", got)
	}
	// Must still reject missing/invalid asset.
	for _, path := range []string{"/updates/download", "/updates/download?asset=", "/updates/download?asset=abc"} {
		rec2 := httptest.NewRecorder()
		h.Handler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, path, nil))
		if rec2.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d", path, rec2.Code, http.StatusBadRequest)
		}
	}
	// Must still 503 when not configured.
	h2 := New(Config{}, discardLogger())
	rec3 := httptest.NewRecorder()
	h2.Handler().ServeHTTP(rec3, httptest.NewRequest(http.MethodGet, "/updates/download?asset=123", nil))
	if rec3.Code != http.StatusServiceUnavailable {
		t.Errorf("unconfigured download: status = %d, want %d", rec3.Code, http.StatusServiceUnavailable)
	}
}
