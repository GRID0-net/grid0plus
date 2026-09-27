package dashboard

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
)

// writeSeed lays out a seed folder the way `switchnetctl fest build` does.
func writeSeed(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	summary := "FestID: NXTD-00042\nFestResourceID: nxtd-lovefes2\n" +
		"FestResourceRevision: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"

	for _, region := range []string{"eu-default", "jp-default"} {
		dir := filepath.Join(root, "bcat-seed", region)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		for name, body := range map[string]string{
			"summary.yml":      summary,
			"base.pack.zs.enc": "encrypted base pack for " + region,
		} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
		}
	}
	return root
}

// seedHarness is a dashboard with a seed directory.
type seedHarness struct {
	srv *Server
}

func newHarnessWithSeed(t *testing.T, seedDir string) *seedHarness {
	t.Helper()
	srv, err := New(Options{
		Store:   db.NewMemory(),
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Tenant:  "dce9377b",
		SeedDir: seedDir,
		Now:     time.Now,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return &seedHarness{srv: srv}
}

func (h *seedHarness) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestTheManifestNamesEveryFileWithADigest(t *testing.T) {
	h := newHarnessWithSeed(t, writeSeed(t))

	rec := h.get(t, "/api/seed/manifest")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var manifest seedManifest
	if err := json.Unmarshal(rec.Body.Bytes(), &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	// The three values come from the summary.yml on disk, not from the fest
	// table: what the console will actually hold is the authority, and a
	// disagreement between the two is worth seeing rather than papering over.
	if manifest.FestID != "NXTD-00042" || manifest.ResourceID != "nxtd-lovefes2" {
		t.Errorf("manifest = %+v, want the values from summary.yml", manifest)
	}
	if len(manifest.Files) != 4 {
		t.Fatalf("manifest lists %d files, want 4", len(manifest.Files))
	}
	// Sorted, so the installer's progress is predictable and two fetches of one
	// seed produce the same manifest.
	if manifest.Files[0].Path != "eu-default/base.pack.zs.enc" {
		t.Errorf("first file is %q, want the sorted first", manifest.Files[0].Path)
	}

	// The digest is what lets the installer tell a truncated pack from a whole
	// one. A truncated pack is indistinguishable from a valid one to the
	// console's filesystem, and the failure appears hours later as BcatInvalid.
	want := sha1.Sum([]byte("encrypted base pack for eu-default"))
	if manifest.Files[0].SHA1 != hex.EncodeToString(want[:]) {
		t.Errorf("digest = %q, want %q", manifest.Files[0].SHA1, hex.EncodeToString(want[:]))
	}
	if manifest.Files[0].Size != int64(len("encrypted base pack for eu-default")) {
		t.Errorf("size = %d", manifest.Files[0].Size)
	}
}

func TestASeedFileIsServedByteForByte(t *testing.T) {
	h := newHarnessWithSeed(t, writeSeed(t))

	rec := h.get(t, "/api/seed/file/eu-default/base.pack.zs.enc")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "encrypted base pack for eu-default" {
		t.Errorf("body = %q", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("content-type = %q", got)
	}
	// Range support matters: the installer resumes, and a homebrew that lost
	// its network halfway through a 30 MB pack should not start over.
	if rec.Header().Get("Accept-Ranges") == "" {
		t.Error("the response does not advertise range support")
	}
}

func TestAPathThatIsNotTwoSegmentsIsRefused(t *testing.T) {
	h := newHarnessWithSeed(t, writeSeed(t))

	// An allowlist -- a region directory and a filename -- rather than a
	// traversal check. A rule that enumerates what is allowed cannot be
	// defeated by an encoding nobody thought of.
	for _, path := range []string{
		"/api/seed/file/../../etc/passwd",
		"/api/seed/file/eu-default",
		"/api/seed/file/eu-default/nested/deep.bin",
		"/api/seed/file/./eu-default/summary.yml",
	} {
		rec := h.get(t, path)
		if rec.Code == http.StatusOK {
			t.Errorf("%s was served", path)
		}
	}
}

func TestWithNoSeedDirectoryConfiguredTheEndpointsSaySo(t *testing.T) {
	h := newHarnessWithSeed(t, "")

	for _, path := range []string{"/api/seed/manifest", "/api/seed/file/eu-default/summary.yml"} {
		rec := h.get(t, path)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s answered %d, want 404", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "seed_dir") &&
			!strings.Contains(rec.Body.String(), "seed") {
			t.Errorf("%s does not say what is missing: %q", path, rec.Body.String())
		}
	}
}

func TestASeedDirectoryWithNothingBuiltIsNotFoundRatherThanEmpty(t *testing.T) {
	// An empty manifest would have the installer report success having written
	// nothing, and the operator would go looking at the console.
	h := newHarnessWithSeed(t, t.TempDir())

	rec := h.get(t, "/api/seed/manifest")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestTheSeedEndpointsAreBehindTheToken(t *testing.T) {
	h := newHarnessWithSeed(t, writeSeed(t))
	h.srv.token = "s3cret"

	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/seed/manifest", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/seed/manifest?token=s3cret", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d with the token, want 200", rec.Code)
	}
}
