// Package toolbox is the public, unauthenticated API the SwitchNet Toolbox
// homebrew app (github.com/n-popescu/switchnet-nro) talks to: whether a
// newer build exists, and a coarse status of the other SwitchNet services.
//
// This is deliberately the one SwitchNet service any console can reach with
// no credential at all, so it is held to a stricter rule than the rest of
// the stack: it never repeats a dependency's error text back to the caller.
// internal/dashboard's operator-facing /api/health does that, behind its own
// token; this does not, because "database connection refused at
// 10.0.4.12:5432" is not something a random console on the network should
// learn.
//
// The GitHub token this package holds is the whole reason it exists rather
// than the console calling GitHub directly: switchnet-nro's source repo is
// private, so an unauthenticated request from the console gets a 404, and
// embedding a credential in a binary that ships to every console is the
// exact thing this avoids. The token stays on this box and is never returned
// in any response.
package toolbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// probeTimeout bounds one service's /healthz call. Same reasoning as
// internal/dashboard's probeTimeout: these are loopback calls, and anything
// slower than this is a service that is not answering.
const probeTimeout = 2 * time.Second

// githubTimeout bounds one call to the GitHub API. Longer than probeTimeout:
// this leaves the box, and a slow-but-working GitHub is not the same failure
// as a service that is actually down.
const githubTimeout = 10 * time.Second

// Config is what the handler needs to run.
type Config struct {
	// GitHubOwner and GitHubRepo identify the private repository releases are
	// read from. Both required for /updates/* to answer anything but 503.
	GitHubOwner string
	GitHubRepo  string

	// GitHubToken is a fine-grained personal access token scoped to ONLY
	// GitHubOwner/GitHubRepo, permission Contents: Read-only, nothing else.
	// Required for /updates/* to answer anything but 503.
	GitHubToken string

	// ReleaseCacheTTL bounds how often releases/latest is actually called.
	// Several consoles checking for an update within the same window should
	// cost GitHub one request, not one per console. Zero means no caching.
	ReleaseCacheTTL time.Duration

	// StatusTargets is every service to probe for /status, name to loopback
	// "host:port" address, e.g. {"dauth": "127.0.0.1:8081"}. Built from the
	// same config the services themselves listen on -- see
	// AddressesFromServiceMap in cmd/toolbox/main.go -- so a changed port
	// cannot leave this probing the old one.
	StatusTargets map[string]string

	// Unprobed names one more service to list in /status without dialling it,
	// each with the reason it cannot be. dns and natcheck are UDP, not HTTP,
	// so there is no /healthz to ask; see internal/dashboard/servicehealth.go
	// for the same honesty rule applied to a real protocol probe instead of a
	// guess: a status that says "unknown, and here is why" is worth more to
	// whoever is reading it than a green light this package never earned.
	Unprobed map[string]string
}

// ServiceStatus is one service's status, as /status reports it.
type ServiceStatus struct {
	Name string `json:"name"`
	// Status is "ok", "degraded", "down", or "unknown". See Config.Unprobed's
	// doc comment for what "unknown" means here.
	Status string `json:"status"`
	// Reason is set only for "unknown" -- never a dependency's error text,
	// which is what makes this safe to show to an unauthenticated caller.
	Reason string `json:"reason,omitempty"`
}

// Handler serves the toolbox endpoints.
type Handler struct {
	cfg    Config
	log    *slog.Logger
	client *http.Client

	// githubAPIBase is "https://api.github.com" in production; tests point it
	// at an httptest server so no real GitHub call is ever needed to verify
	// this package's own logic.
	githubAPIBase string

	mu     sync.Mutex
	cached *cachedRelease
}

type cachedRelease struct {
	body      []byte
	expiresAt time.Time
}

// New returns a toolbox handler.
func New(cfg Config, log *slog.Logger) *Handler {
	return &Handler{
		cfg:           cfg,
		log:           log,
		client:        &http.Client{Timeout: githubTimeout},
		githubAPIBase: "https://api.github.com",
	}
}

// Handler returns the routed handler.
func (h *Handler) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", h.handleStatus)
	mux.HandleFunc("GET /updates/latest", h.handleUpdatesLatest)
	mux.HandleFunc("GET /updates/download", h.handleUpdatesDownload)
	return mux
}

// ---- status -----------------------------------------------------------

func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	names := make([]string, 0, len(h.cfg.StatusTargets)+len(h.cfg.Unprobed))
	for name := range h.cfg.StatusTargets {
		names = append(names, name)
	}
	for name := range h.cfg.Unprobed {
		names = append(names, name)
	}
	sort.Strings(names)

	results := make([]ServiceStatus, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			if reason, ok := h.cfg.Unprobed[name]; ok {
				results[i] = ServiceStatus{Name: name, Status: "unknown", Reason: reason}
				return
			}
			results[i] = ServiceStatus{Name: name, Status: h.probe(r.Context(), h.cfg.StatusTargets[name])}
		}(i, name)
	}
	wg.Wait()

	writeJSON(w, map[string]any{"services": results})
}

// probe asks one service's own /healthz and reduces it to ok/degraded/down --
// never the dependency detail that answer carries, which belongs only behind
// internal/dashboard's operator token.
func (h *Handler) probe(ctx context.Context, addr string) string {
	if addr == "" {
		return "down"
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+dialAddr(addr)+"/healthz", nil)
	if err != nil {
		return "down"
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return "down"
	}
	defer func() { _ = resp.Body.Close() }()

	var report struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&report); err != nil {
		// It answered, but not with a health report -- treat as reachable
		// rather than guessing at what actually went wrong.
		return "unknown"
	}
	if report.Status == "" {
		return "unknown"
	}
	return report.Status
}

// ---- updates ------------------------------------------------------------

// githubRelease is the subset of GitHub's release JSON this package reads.
type githubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// nroAsset picks the .nro release asset out of a release's asset list. A
// release is expected to carry exactly one; if it carries several (or
// none), which one the updater would fetch must not be a silent guess.
func (rel githubRelease) nroAsset() (id int64, size int64, ok bool) {
	for _, a := range rel.Assets {
		if len(a.Name) > 4 && a.Name[len(a.Name)-4:] == ".nro" {
			return a.ID, a.Size, true
		}
	}
	return 0, 0, false
}

// handleUpdatesLatest answers the same three JSON keys the console's
// updater already parses from a real GitHub response -- tag_name,
// browser_download_url, size -- so source/update.c needs no changes beyond
// pointing at this host instead of api.github.com. browser_download_url is
// rewritten to this service's own /updates/download: the console must never
// see, and cannot use, the token that made this answer possible.
func (h *Handler) handleUpdatesLatest(w http.ResponseWriter, r *http.Request) {
	if h.cfg.GitHubToken == "" || h.cfg.GitHubOwner == "" || h.cfg.GitHubRepo == "" {
		http.Error(w, "updates not configured", http.StatusServiceUnavailable)
		return
	}

	rel, err := h.latestRelease(r.Context())
	if err != nil {
		h.log.Error("toolbox: fetch latest release", "error", err)
		http.Error(w, "could not reach the release source", http.StatusBadGateway)
		return
	}

	id, size, ok := rel.nroAsset()
	if !ok {
		h.log.Error("toolbox: latest release has no .nro asset", "tag", rel.TagName)
		http.Error(w, "latest release has no .nro asset", http.StatusBadGateway)
		return
	}

	downloadURL := fmt.Sprintf("%s://%s/updates/download?asset=%d", schemeOf(r), r.Host, id)
	writeJSON(w, map[string]any{
		"tag_name":             rel.TagName,
		"browser_download_url": downloadURL,
		"size":                 size,
	})
}

func schemeOf(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// latestRelease returns the cached release JSON, refreshing it from GitHub
// once ReleaseCacheTTL has elapsed.
func (h *Handler) latestRelease(ctx context.Context) (githubRelease, error) {
	body, err := h.latestReleaseBody(ctx)
	if err != nil {
		return githubRelease{}, err
	}
	var rel githubRelease
	if err := json.Unmarshal(body, &rel); err != nil {
		return githubRelease{}, fmt.Errorf("decode release: %w", err)
	}
	if rel.TagName == "" {
		return githubRelease{}, fmt.Errorf("release response had no tag_name")
	}
	return rel, nil
}

func (h *Handler) latestReleaseBody(ctx context.Context) ([]byte, error) {
	h.mu.Lock()
	if h.cached != nil && time.Now().Before(h.cached.expiresAt) {
		body := h.cached.body
		h.mu.Unlock()
		return body, nil
	}
	h.mu.Unlock()

	url := fmt.Sprintf("%s/repos/%s/%s/releases/latest", h.githubAPIBase, h.cfg.GitHubOwner, h.cfg.GitHubRepo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	h.setGitHubAuth(req, "application/vnd.github+json")

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github: %s: %s", resp.Status, truncate(body, 200))
	}

	if h.cfg.ReleaseCacheTTL > 0 {
		h.mu.Lock()
		h.cached = &cachedRelease{body: body, expiresAt: time.Now().Add(h.cfg.ReleaseCacheTTL)}
		h.mu.Unlock()
	}
	return body, nil
}

// handleUpdatesDownload streams one release asset from GitHub, authenticated
// with the token this package holds. The console's request carries no
// credential and needs none: the numeric asset id from /updates/latest is
// not a secret, only useful for fetching a public-shaped release binary that
// happens to live in a private repository.
func (h *Handler) handleUpdatesDownload(w http.ResponseWriter, r *http.Request) {
	if h.cfg.GitHubToken == "" || h.cfg.GitHubOwner == "" || h.cfg.GitHubRepo == "" {
		http.Error(w, "updates not configured", http.StatusServiceUnavailable)
		return
	}

	assetID := r.URL.Query().Get("asset")
	if _, err := strconv.ParseInt(assetID, 10, 64); err != nil || assetID == "" {
		http.Error(w, "missing or invalid asset id", http.StatusBadRequest)
		return
	}

	url := fmt.Sprintf("%s/repos/%s/%s/releases/assets/%s",
		h.githubAPIBase, h.cfg.GitHubOwner, h.cfg.GitHubRepo, assetID)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.setGitHubAuth(req, "application/octet-stream")

	resp, err := h.client.Do(req)
	if err != nil {
		h.log.Error("toolbox: download asset", "asset", assetID, "error", err)
		http.Error(w, "could not reach the release source", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		h.log.Error("toolbox: download asset", "asset", assetID, "status", resp.Status, "body", string(body))
		http.Error(w, "could not fetch the release asset", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
	// Streamed straight through rather than buffered: the whole point of a
	// proxy-through download is not holding a second copy of the .nro in
	// this process's memory per concurrent console.
	_, _ = io.Copy(w, resp.Body)
}

func (h *Handler) setGitHubAuth(req *http.Request, accept string) {
	req.Header.Set("Authorization", "Bearer "+h.cfg.GitHubToken)
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
}

// dialAddr turns a listen address into one that can be dialled, the same way
// internal/dashboard/servicehealth.go's dialAddr does: ":8083" means "every
// interface" to a listener and nothing at all to a dialler, so it becomes
// loopback.
func dialAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	if strings.HasPrefix(addr, "0.0.0.0:") {
		return "127.0.0.1:" + strings.TrimPrefix(addr, "0.0.0.0:")
	}
	return addr
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
