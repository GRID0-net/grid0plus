package baas

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	"github.com/n-popescu/grid0plus/internal/bcat"
)

// An emulator has no BCAT client: a console's bcat sysmodule fetches a game's
// delivery cache (Splatoon 3's Splatfest packs, for one) from the bcat hosts,
// which GRID0+ answers from BCATSeedDir. These two calls hand an emulator the
// same files, so it can fill its own delivery cache with what a console would
// have downloaded. Only the files themselves; the console's container,
// signature and list digest are a transport an emulator never sees.

type emulatorBCATFile struct {
	Name   string `json:"name"`
	Size   int    `json:"size"`
	SHA256 string `json:"sha256"`
}

type emulatorBCATDirectory struct {
	Name  string             `json:"name"`
	Files []emulatorBCATFile `json:"files"`
}

// emulatorBCATList answers a title's delivery cache: every directory and the
// files in it, with a hash so the emulator downloads only what changed. An
// empty list means the title has no BCAT content and the emulator should
// clear what it holds.
func (s *Server) emulatorBCATList(w http.ResponseWriter, r *http.Request, _ *caller) {
	files, err := bcat.ReadNativeSeed(s.cfg.BCATSeedDir, r.PathValue("title"))
	if err != nil {
		writeInternalError(s.log, w, r, "read bcat seed", err)
		return
	}
	dirs := []emulatorBCATDirectory{}
	index := map[string]int{}
	for _, f := range files {
		i, ok := index[f.Directory]
		if !ok {
			i = len(dirs)
			index[f.Directory] = i
			dirs = append(dirs, emulatorBCATDirectory{Name: f.Directory})
		}
		sum := sha256.Sum256(f.Data)
		dirs[i].Files = append(dirs[i].Files, emulatorBCATFile{Name: f.Name, Size: len(f.Data), SHA256: hex.EncodeToString(sum[:])})
	}
	writeJSON(w, http.StatusOK, map[string]any{"directories": dirs})
}

// emulatorBCATFile answers one delivery-cache file's bytes.
func (s *Server) emulatorBCATFile(w http.ResponseWriter, r *http.Request, _ *caller) {
	files, err := bcat.ReadNativeSeed(s.cfg.BCATSeedDir, r.PathValue("title"))
	if err != nil {
		writeInternalError(s.log, w, r, "read bcat seed", err)
		return
	}
	for _, f := range files {
		if f.Directory == r.PathValue("dir") && f.Name == r.PathValue("file") {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(f.Data)
			return
		}
	}
	http.NotFound(w, r)
}
