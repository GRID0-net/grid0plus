package bcat

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// BuildOptions configures a pack build.
type BuildOptions struct {
	// Converter turns source assets into game formats. Nil means there is none,
	// and a source asset is then an error rather than a file packed as-is —
	// see BuildPacks.
	Converter Converter

	// Naming decides in-pack paths, including what {FEST} becomes.
	Naming Naming
}

// BuildPacks turns a directory of assets into the packs of a seed.
//
// The layout it expects mirrors the packs themselves — one directory per pack,
// holding the files that go inside it:
//
//	assets/
//	    base/  Mals/…  RSDB/…  Event/…
//	    bh/    Mals/…
//	    wa/    wb/    wc/
//
// A file already in one of the game's formats (.bfres, .byaml, .sarc.zs) is
// packed as it is. A file with a source extension — see SourceSuffix — is
// converted first, and **a source file with no converter configured is an
// error.**
//
// That refusal is the point. Packing a PNG produces an archive the game reads
// and a texture slot it cannot decode: a fest that loads with missing or
// garbled art, on a console, with nothing on the server to look at. Failing at
// build time costs the operator one message; failing on the console costs an
// evening.
func BuildPacks(assetsDir string) (Packs, error) {
	return BuildPacksWith(context.Background(), assetsDir, BuildOptions{})
}

// BuildPacksWith builds packs with conversion and naming applied.
func BuildPacksWith(ctx context.Context, assetsDir string, opts BuildOptions) (Packs, error) {
	packs := Packs{}

	// One scratch directory for every converted file in this build, removed
	// afterwards. Converted output is not written back into the operator's
	// assets directory: a build must not leave artefacts that the next build
	// then treats as hand-authored input.
	scratch, err := os.MkdirTemp("", "switchnet-bcat-")
	if err != nil {
		return nil, fmt.Errorf("bcat: scratch directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()

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

		files, err := collectFiles(ctx, dir, filepath.Join(scratch, name), opts)
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

// collectFiles walks a pack directory, converting what needs converting.
//
// Each file is named by its path relative to that directory, which is exactly
// how it is addressed inside the archive.
func collectFiles(ctx context.Context, dir, scratch string, opts BuildOptions) ([]SARCFile, error) {
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
		// Forward slashes regardless of host: the name is stored in the archive
		// and read on a console, where a backslash is part of the filename
		// rather than a separator.
		rel = filepath.ToSlash(rel)

		target, kind, isSource := convertSource(rel)
		if !isSource {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out = append(out, SARCFile{Name: opts.Naming.Apply(rel), Data: data})
			return nil
		}

		if target == "" {
			return fmt.Errorf("bcat: %s: a source file must be named for what it becomes, "+
				"with the source extension appended — e.g. Splashart.bfres.png, not %s",
				rel, filepath.Base(rel))
		}
		if opts.Converter == nil {
			return fmt.Errorf("bcat: %s needs converting to %s and no converter is configured. "+
				"Set bcat.converter, or supply assets already in the game's formats. "+
				"Packing it unconverted would give a fest that loads on the console with "+
				"art the game cannot decode and nothing here to look at",
				rel, filepath.Base(target))
		}

		dst := filepath.Join(scratch, filepath.FromSlash(target))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := opts.Converter.Convert(ctx, path, dst, kind, filepath.Ext(target)); err != nil {
			return err
		}
		data, err := os.ReadFile(dst)
		if err != nil {
			return err
		}
		out = append(out, SARCFile{Name: opts.Naming.Apply(target), Data: data})
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Sorted, so a pack's bytes -- and therefore its revision -- depend on its
	// contents and not on the order the filesystem happened to walk them.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	// A converted file and a hand-authored one can collide: Splashart.bfres.png
	// alongside Splashart.bfres. Caught here because BuildSARC would store both
	// under one name and the console would read whichever it found first.
	for i := 1; i < len(out); i++ {
		if out[i].Name == out[i-1].Name {
			return nil, fmt.Errorf("bcat: two files both become %q in the pack; "+
				"remove one, or the console reads whichever it finds first", out[i].Name)
		}
	}
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
