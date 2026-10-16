package toolbox

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The patch mirror. The Toolbox and the GRID0+ emulators install IPS patches
// from the newest patches-<serial> release of the cfw repo; when GitHub cannot
// be reached, the Toolbox asks this service for the same manifest.json and
// patches.zip instead (cfw repo, patches/README.md).
//
// The copy served is the last one that verified: every file the manifest
// lists must be in the zip with the SHA-256 the manifest gives. It is kept on
// disk, so a restart while GitHub is down still has something to serve --
// which is the only situation the mirror exists for.

const (
	patchesPath     = "/grid0plus/patches/"
	maxManifestSize = 1 << 20
	maxPatchesZip   = 32 << 20
)

var patchesTag = regexp.MustCompile(`^patches-([0-9]+)$`)

type patchSet struct {
	tag      string
	manifest []byte
	zip      []byte
	fetched  time.Time
}

type patchMirror struct {
	repo   string // "owner/name"
	dir    string
	every  time.Duration
	client *http.Client
	api    string // https://api.github.com, a test server in tests

	mu      sync.RWMutex
	current *patchSet
}

// serve answers manifest.json and patches.zip; 503 until a set has been
// fetched or loaded from disk.
func (m *patchMirror) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.RLock()
	set := m.current
	m.mu.RUnlock()
	if set == nil {
		http.Error(w, "no patch set mirrored yet", http.StatusServiceUnavailable)
		return
	}
	var body []byte
	var ctype string
	switch strings.TrimPrefix(r.URL.Path, patchesPath) {
	case "manifest.json":
		body, ctype = set.manifest, "application/json"
	case "patches.zip":
		body, ctype = set.zip, "application/zip"
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("X-Patches-Release", set.tag)
	_, _ = w.Write(body)
}

// verify checks that zipBody holds every file the manifest lists, with the
// right hash. A set that fails is never served.
func verifyPatchSet(manifest, zipBody []byte) error {
	var m struct {
		Serial  int `json:"serial"`
		Patches []struct {
			Asset  string `json:"asset"`
			SHA256 string `json:"sha256"`
		} `json:"patches"`
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	if len(m.Patches) == 0 {
		return errors.New("manifest lists no patches")
	}
	zr, err := zip.NewReader(bytes.NewReader(zipBody), int64(len(zipBody)))
	if err != nil {
		return fmt.Errorf("patches.zip: %w", err)
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	for _, p := range m.Patches {
		f, ok := files[p.Asset]
		if !ok {
			return fmt.Errorf("patches.zip has no %s", p.Asset)
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("%s: %w", p.Asset, err)
		}
		h := sha256.New()
		_, err = io.Copy(h, io.LimitReader(rc, 1<<20))
		_ = rc.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", p.Asset, err)
		}
		if hex.EncodeToString(h.Sum(nil)) != p.SHA256 {
			return fmt.Errorf("%s does not match its manifest hash", p.Asset)
		}
	}
	return nil
}

// newestTag finds the newest patches-<serial> release. The repo is public, so
// no token is sent.
func (m *patchMirror) newestTag(ctx context.Context) (string, error) {
	body, err := m.get(ctx, fmt.Sprintf("%s/repos/%s/releases?per_page=30", m.api, m.repo), maxManifestSize)
	if err != nil {
		return "", err
	}
	var releases []struct {
		TagName string `json:"tag_name"`
		Draft   bool   `json:"draft"`
	}
	if err := json.Unmarshal(body, &releases); err != nil {
		return "", fmt.Errorf("decode releases: %w", err)
	}
	best, bestSerial := "", -1
	for _, r := range releases {
		match := patchesTag.FindStringSubmatch(r.TagName)
		if r.Draft || match == nil {
			continue
		}
		if n, _ := strconv.Atoi(match[1]); n > bestSerial {
			best, bestSerial = r.TagName, n
		}
	}
	if best == "" {
		return "", errors.New("no patches release found")
	}
	return best, nil
}

func (m *patchMirror) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "grid0plus-toolbox-mirror")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s: larger than %d bytes", url, limit)
	}
	return body, nil
}

// refresh fetches the newest release if it is not the one already served.
func (m *patchMirror) refresh(ctx context.Context, downloadBase string) error {
	tag, err := m.newestTag(ctx)
	if err != nil {
		return err
	}
	m.mu.RLock()
	same := m.current != nil && m.current.tag == tag
	m.mu.RUnlock()
	if same {
		return nil
	}
	base := fmt.Sprintf("%s/%s/releases/download/%s/", downloadBase, m.repo, tag)
	manifest, err := m.get(ctx, base+"manifest.json", maxManifestSize)
	if err != nil {
		return err
	}
	zipBody, err := m.get(ctx, base+"patches.zip", maxPatchesZip)
	if err != nil {
		return err
	}
	if err := verifyPatchSet(manifest, zipBody); err != nil {
		return fmt.Errorf("%s: %w", tag, err)
	}
	set := &patchSet{tag: tag, manifest: manifest, zip: zipBody, fetched: time.Now()}
	if err := m.save(set); err != nil {
		return fmt.Errorf("save %s: %w", tag, err)
	}
	m.mu.Lock()
	m.current = set
	m.mu.Unlock()
	return nil
}

// save writes the set to dir/<tag>/ and points dir/current at it, so a crash
// mid-write leaves the previous set as the one loaded on restart.
func (m *patchMirror) save(set *patchSet) error {
	if m.dir == "" {
		return nil
	}
	dir := filepath.Join(m.dir, set.tag)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, body := range map[string][]byte{"manifest.json": set.manifest, "patches.zip": set.zip} {
		tmp := filepath.Join(dir, name+".tmp")
		if err := os.WriteFile(tmp, body, 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	tmp := filepath.Join(m.dir, "current.tmp")
	if err := os.WriteFile(tmp, []byte(set.tag+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(m.dir, "current")); err != nil {
		return err
	}
	// Older sets are no longer served; keep only the current one.
	entries, _ := os.ReadDir(m.dir)
	for _, e := range entries {
		if e.IsDir() && e.Name() != set.tag && patchesTag.MatchString(e.Name()) {
			_ = os.RemoveAll(filepath.Join(m.dir, e.Name()))
		}
	}
	return nil
}

// RunPatchMirror loads the saved set, then refreshes from GitHub at once and
// every PatchesRefresh until ctx ends. A failed refresh keeps serving the
// set it had.
func (h *Handler) RunPatchMirror(ctx context.Context) {
	m := h.patches
	if m == nil {
		return
	}
	if err := m.load(); err != nil {
		h.log.Warn("toolbox: saved patch set unusable", "error", err)
	}
	for {
		if err := m.refresh(ctx, h.githubDownloadBase); err != nil {
			if ctx.Err() != nil {
				return
			}
			h.log.Warn("toolbox: patch mirror refresh failed, still serving the previous set", "error", err)
		} else {
			m.mu.RLock()
			tag := m.current.tag
			m.mu.RUnlock()
			h.log.Info("toolbox: patch mirror current", "release", tag)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(m.every):
		}
	}
}

// load restores the last saved set, verifying it again.
func (m *patchMirror) load() error {
	if m.dir == "" {
		return nil
	}
	tagBytes, err := os.ReadFile(filepath.Join(m.dir, "current"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	tag := strings.TrimSpace(string(tagBytes))
	if !patchesTag.MatchString(tag) {
		return fmt.Errorf("current names %q, not a patches release", tag)
	}
	manifest, err := os.ReadFile(filepath.Join(m.dir, tag, "manifest.json"))
	if err != nil {
		return err
	}
	zipBody, err := os.ReadFile(filepath.Join(m.dir, tag, "patches.zip"))
	if err != nil {
		return err
	}
	if err := verifyPatchSet(manifest, zipBody); err != nil {
		return fmt.Errorf("saved %s: %w", tag, err)
	}
	m.mu.Lock()
	m.current = &patchSet{tag: tag, manifest: manifest, zip: zipBody}
	m.mu.Unlock()
	return nil
}
