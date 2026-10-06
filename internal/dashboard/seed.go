package dashboard

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/n-popescu/grid0plus/internal/bcat"
)

// Serving the BCAT delivery cache to the console-side installer.
//
// GRID0+ builds the delivery cache rather than serving BCAT (see
// internal/bcat), which leaves the question of how the files get onto a console.
// Doing it by hand with JKSV works and does not scale past the second fest, so
// the answer is a small homebrew that fetches the seed and writes it into
// Splatoon 3's BCAT save data — switch/bcat-installer/ — and these two endpoints
// are what it fetches from.
//
// They are behind the dashboard's token like everything else. The installer is
// configured with it.

// seedManifest is what the installer reads first.
type seedManifest struct {
	// FestID, ResourceID and Revision are copied from the seed's own
	// summary.yml rather than from the fest table.
	//
	// Deliberately: the summary.yml on disk is what the console will actually
	// hold, and the whole class of failure this avoids is announcing one thing
	// and delivering another. If the two disagree, that is worth seeing in the
	// dashboard rather than papering over here.
	FestID     string `json:"festId"`
	ResourceID string `json:"resourceId"`
	Revision   string `json:"revision"`

	Files []seedFile `json:"files"`
}

// seedFile is one file in the cache.
type seedFile struct {
	// Path is relative to the bcat-seed directory, e.g.
	// "eu-default/base.pack.zs.enc".
	Path string `json:"path"`
	Size int64  `json:"size"`
	// SHA1 lets the installer verify what it received.
	//
	// Worth the cost here in a way it usually is not: the installer writes into
	// save data, a truncated pack is indistinguishable from a valid one to the
	// filesystem, and the resulting failure appears hours later as BcatInvalid
	// on a console.
	SHA1 string `json:"sha1"`
}

func (s *Server) handleSeedManifest(w http.ResponseWriter, r *http.Request) {
	if s.seedDir == "" {
		http.Error(w, "bcat.seed_dir is not configured", http.StatusNotFound)
		return
	}

	root := s.seedRoot()
	manifest := seedManifest{Files: []seedFile{}}

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha1.Sum(raw)
		manifest.Files = append(manifest.Files, seedFile{
			Path: rel,
			Size: int64(len(raw)),
			SHA1: hex.EncodeToString(sum[:]),
		})

		if filepath.Base(rel) == "summary.yml" && manifest.FestID == "" {
			id, resource, revision := parseSummary(raw)
			manifest.FestID, manifest.ResourceID, manifest.Revision = id, resource, revision
		}
		return nil
	})
	if os.IsNotExist(err) || (err == nil && len(manifest.Files) == 0) {
		http.Error(w, "no seed has been built yet", http.StatusNotFound)
		return
	}
	if err != nil {
		s.fail(w, "read seed", err)
		return
	}

	// Sorted so the installer's progress is predictable and two fetches of the
	// same seed produce the same manifest.
	sort.Slice(manifest.Files, func(i, j int) bool {
		return manifest.Files[i].Path < manifest.Files[j].Path
	})
	writeJSON(w, manifest)
}

// handleSeedFile serves one file from the seed.
func (s *Server) handleSeedFile(w http.ResponseWriter, r *http.Request) {
	if s.seedDir == "" {
		http.Error(w, "bcat.seed_dir is not configured", http.StatusNotFound)
		return
	}

	rel := r.PathValue("path")
	safe, ok := safeSeedPath(rel)
	if !ok {
		// The path comes from a client. Refused rather than cleaned, because a
		// cleaned path that happens to resolve inside the directory would serve
		// a file the caller did not name and nobody would notice.
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}

	full := filepath.Join(s.seedRoot(), filepath.FromSlash(safe))
	f, err := os.Open(full)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "no such file", http.StatusNotFound)
			return
		}
		s.fail(w, "open seed file", err)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.Error(w, "no such file", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	// ServeContent rather than io.Copy: the installer resumes, and a homebrew
	// that lost its network halfway through a 30 MB pack should not start over.
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

// safeSeedPath validates a client-supplied relative path.
//
// An allowlist of what a seed path can look like — two segments, a region
// directory and a filename — rather than a traversal check. A rule that
// enumerates what is allowed cannot be defeated by an encoding nobody thought
// of; a rule that rejects "..'" can.
func safeSeedPath(rel string) (string, bool) {
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" || rel != path.Clean(rel) {
		return "", false
	}
	parts := strings.Split(rel, "/")
	if len(parts) != 2 {
		return "", false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, `\:`) {
			return "", false
		}
	}
	return rel, true
}

// parseSummary reads the three fields a seed's summary.yml carries.
//
// By hand rather than through a YAML library, matching how internal/bcat writes
// it: three fixed keys, and a parser has nothing to gain from generality.
func parseSummary(raw []byte) (festID, resourceID, revision string) {
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "FestID":
			festID = value
		case "FestResourceID":
			resourceID = value
		case "FestResourceRevision":
			revision = value
		}
	}
	return festID, resourceID, revision
}

// seedRoot is the folder holding the region directories. seed_dir may be a
// `fest build -out` folder, with them under bcat-seed/, or the folder BAAS
// serves consoles from: the root, with Splatoon 3's folder in it, or that
// folder itself.
// Pointing the dashboard at the served folder means it always shows the fest
// consoles are actually getting, with nothing to update per fest.
func (s *Server) seedRoot() string {
	for _, nested := range []string{
		filepath.Join(s.seedDir, "bcat-seed"),
		filepath.Join(s.seedDir, bcat.Splatoon3TitleID),
	} {
		if info, err := os.Stat(nested); err == nil && info.IsDir() {
			return nested
		}
	}
	return s.seedDir
}

// seedStatus is what the dashboard's own panel shows: whether a seed has been
// built, and which fest the files on disk actually belong to.
func (s *Server) seedStatus() map[string]any {
	if s.seedDir == "" {
		return map[string]any{"configured": false}
	}
	root := s.seedRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		return map[string]any{"configured": true, "built": false}
	}

	regions := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			regions = append(regions, e.Name())
		}
	}
	sort.Strings(regions)

	out := map[string]any{"configured": true, "built": len(regions) > 0, "regions": regions}
	if len(regions) > 0 {
		if raw, err := os.ReadFile(filepath.Join(root, regions[0], "summary.yml")); err == nil {
			id, resource, revision := parseSummary(raw)
			out["festId"], out["resourceId"], out["revision"] = id, resource, revision
		}
	}
	return out
}
