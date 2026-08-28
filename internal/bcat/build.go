package bcat

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// BuildPacks turns a directory of game assets into the five encrypted packs of
// a seed.
//
// The layout it expects mirrors the packs themselves — one directory per pack,
// holding the files that go inside it:
//
//	assets/
//	    base/  Mals/…  RSDB/…  Event/…
//	    bh/    Mals/…
//	    wa/    wb/    wc/
//
// Assets must already be in the game's own formats (.bfres, .byaml, .sarc.zs
// and so on). Converting a PNG into a game-ready texture is a separate problem
// and a much larger one — see TODO.md, "Asset conversion".
func BuildPacks(assetsDir string) (Packs, error) {
	packs := Packs{}

	for _, name := range PackOrder {
		dir := filepath.Join(assetsDir, name)
		info, err := os.Stat(dir)
		if os.IsNotExist(err) {
			// A fest need not have every pack: a two-team fest has no wc.
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("bcat: %s: %w", dir, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("bcat: %s is not a directory", dir)
		}

		files, err := collectFiles(dir)
		if err != nil {
			return nil, err
		}
		if len(files) == 0 {
			return nil, fmt.Errorf("bcat: %s holds no files, so pack %q would be empty", dir, name)
		}

		archive, err := BuildSARC(files)
		if err != nil {
			return nil, fmt.Errorf("bcat: pack %q: %w", name, err)
		}
		packs[name] = archive
	}

	if len(packs) == 0 {
		return nil, fmt.Errorf("bcat: %s has no pack directories (expected one of %s)",
			assetsDir, strings.Join(PackOrder, ", "))
	}
	return packs, nil
}

// collectFiles walks a pack directory, naming each file by its path relative to
// that directory — which is exactly how it is addressed inside the archive.
func collectFiles(dir string) ([]SARCFile, error) {
	var out []SARCFile

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// Forward slashes regardless of host: the name is stored in the archive
		// and read on a console, where a backslash is part of the filename
		// rather than a separator.
		out = append(out, SARCFile{Name: filepath.ToSlash(rel), Data: data})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Revision derives the 40-hex revision to announce for a set of packs.
//
// Taken over the packs' own bytes, so it changes when and only when the content
// does. That is the property the console relies on: it re-downloads when the
// announced revision differs from the one its cache holds, so a revision that
// moved for no reason costs a download and one that failed to move leaves stale
// content in place.
func Revision(packs Packs) string {
	h := sha1.New()
	names := make([]string, 0, len(packs))
	for name := range packs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write(packs[name])
	}
	return hex.EncodeToString(h.Sum(nil))
}
