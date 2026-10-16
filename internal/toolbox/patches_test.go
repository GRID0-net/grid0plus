package toolbox

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeRelease is one patches release as GitHub would serve it.
type fakeRelease struct {
	tag      string
	files    map[string][]byte // asset name -> bytes in the zip
	manifest []byte
}

func newFakeRelease(t *testing.T, tag string, patch []byte, corrupt bool) fakeRelease {
	t.Helper()
	sum := sha256.Sum256(patch)
	asset := hex.EncodeToString(sum[:]) + ".ips"
	manifest, _ := json.Marshal(map[string]any{
		"serial":  1,
		"patches": []map[string]any{{"asset": asset, "sha256": hex.EncodeToString(sum[:])}},
	})
	inZip := patch
	if corrupt {
		inZip = append(append([]byte{}, patch...), '!')
	}
	return fakeRelease{tag: tag, files: map[string][]byte{asset: inZip}, manifest: manifest}
}

func (r fakeRelease) zip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range r.files {
		w, _ := zw.Create(name)
		_, _ = w.Write(data)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeGitHub serves the release list and release downloads for releases.
func fakeGitHub(t *testing.T, releases *[]fakeRelease, up *bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !*up {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		if r.URL.Path == "/repos/o/r/releases" {
			var list []map[string]any
			list = append(list, map[string]any{"tag_name": "v0.9.0", "draft": false})
			for _, rel := range *releases {
				list = append(list, map[string]any{"tag_name": rel.tag, "draft": false})
			}
			_ = json.NewEncoder(w).Encode(list)
			return
		}
		for _, rel := range *releases {
			switch r.URL.Path {
			case "/o/r/releases/download/" + rel.tag + "/manifest.json":
				_, _ = w.Write(rel.manifest)
				return
			case "/o/r/releases/download/" + rel.tag + "/patches.zip":
				_, _ = w.Write(rel.zip(t))
				return
			}
		}
		http.NotFound(w, r)
	}))
}

func newMirrorHandler(srv *httptest.Server, dir string) *Handler {
	h := New(Config{PatchesRepo: "o/r", PatchesDir: dir}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.githubAPIBase, h.githubDownloadBase = srv.URL, srv.URL
	h.patches.api = srv.URL
	return h
}

func getPatchFile(t *testing.T, h *Handler, name string) (int, string, []byte) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, patchesPath+name, nil))
	return rec.Code, rec.Header().Get("X-Patches-Release"), rec.Body.Bytes()
}

func TestThePatchMirrorAnswers503UntilItHasASet(t *testing.T) {
	releases, up := []fakeRelease{}, false
	srv := fakeGitHub(t, &releases, &up)
	defer srv.Close()
	h := newMirrorHandler(srv, t.TempDir())
	if err := h.patches.refresh(context.Background(), srv.URL); err == nil {
		t.Fatal("refresh against a down GitHub succeeded")
	}
	if code, _, _ := getPatchFile(t, h, "manifest.json"); code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503 before any set", code)
	}
}

func TestThePatchMirrorServesTheNewestVerifiedRelease(t *testing.T) {
	releases, up := []fakeRelease{
		newFakeRelease(t, "patches-1", []byte("PATCHone"), false),
		newFakeRelease(t, "patches-2", []byte("PATCHtwo"), false),
	}, true
	srv := fakeGitHub(t, &releases, &up)
	defer srv.Close()
	h := newMirrorHandler(srv, t.TempDir())
	if err := h.patches.refresh(context.Background(), srv.URL); err != nil {
		t.Fatal(err)
	}
	code, tag, body := getPatchFile(t, h, "manifest.json")
	if code != http.StatusOK || tag != "patches-2" || !bytes.Equal(body, releases[1].manifest) {
		t.Fatalf("got %d %q, want patches-2's manifest", code, tag)
	}
	if code, _, body := getPatchFile(t, h, "patches.zip"); code != http.StatusOK || verifyPatchSet(releases[1].manifest, body) != nil {
		t.Fatalf("patches.zip: %d, or it does not verify", code)
	}
}

func TestAPatchReleaseThatFailsItsHashesIsNeverServed(t *testing.T) {
	releases, up := []fakeRelease{newFakeRelease(t, "patches-1", []byte("PATCHgood"), false)}, true
	srv := fakeGitHub(t, &releases, &up)
	defer srv.Close()
	h := newMirrorHandler(srv, t.TempDir())
	if err := h.patches.refresh(context.Background(), srv.URL); err != nil {
		t.Fatal(err)
	}
	releases = append(releases, newFakeRelease(t, "patches-2", []byte("PATCHbad"), true))
	if err := h.patches.refresh(context.Background(), srv.URL); err == nil {
		t.Fatal("a corrupt release verified")
	}
	if _, tag, _ := getPatchFile(t, h, "manifest.json"); tag != "patches-1" {
		t.Fatalf("serving %q, want the last good set patches-1", tag)
	}
}

func TestThePatchMirrorStillAnswersAfterARestartWhileGitHubIsDown(t *testing.T) {
	releases, up := []fakeRelease{newFakeRelease(t, "patches-3", []byte("PATCHkept"), false)}, true
	srv := fakeGitHub(t, &releases, &up)
	defer srv.Close()
	dir := t.TempDir()
	if err := newMirrorHandler(srv, dir).patches.refresh(context.Background(), srv.URL); err != nil {
		t.Fatal(err)
	}

	up = false
	h := newMirrorHandler(srv, dir)
	if err := h.patches.load(); err != nil {
		t.Fatal(err)
	}
	if code, tag, _ := getPatchFile(t, h, "manifest.json"); code != http.StatusOK || tag != "patches-3" {
		t.Fatalf("after restart: %d %q, want the saved patches-3", code, tag)
	}
}
