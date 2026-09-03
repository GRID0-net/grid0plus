package bcat

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrTextureConverterNotConfigured is returned by ConvertTextures when it
// finds a .png to convert but no converter tool path was given.
var ErrTextureConverterNotConfigured = errors.New("bcat: no texture converter configured for a .png source asset")

// ConvertTextures materializes assetsDir from sourceDir, which uses the same
// per-pack layout BuildPacks itself expects (base/bh/wa/wb/wc/…): every
// .png in sourceDir is converted to a same-named .bntx in assetsDir by
// shelling out to toolPath, and every other file is copied through
// unchanged. That split lets a source tree already hold hand-authored
// .bfres/.byaml/.sarc.zs files (§2.2 of TODO.md and everything else
// BuildPacks was already built for) alongside the .png files that still
// need converting, without this function knowing or caring about the
// difference beyond the extension. BuildPacks itself is untouched — it
// still just reads a directory of already-game-ready files.
//
// toolPath is expected to follow the CLI contract
// [jam1garner/img2bntx](https://github.com/jam1garner/img2bntx) uses:
//
//	<tool> [--width W --height H] [-n NAME] <in.png> <out.bntx>
//
// That tool (and the `bntx` crate it wraps) was built from source and run
// end-to-end against a real PNG as part of developing this function — not
// taken on faith from its README. It writes a structurally valid,
// standalone BNTX container (confirmed: real "BNTX"/"NX  " magic, the given
// name in its string table), Tegra-swizzled by an implementation of the
// documented block-linear algorithm, not a guess. Two real limitations,
// both visible in its source rather than inferred: every texture comes out
// uncompressed R8G8B8A8_SRGB (no BC1/BC3/BC7), and the container holds
// exactly one texture, one mip level, no array layers.
//
// What building and running the tool cannot settle, because it needs a real
// dumped pack to check against: whether Splatoon 3's Splatfest texture
// slots actually load a standalone .bntx shaped like this one at all,
// versus a texture embedded inside a .bfres, and at what resolution. See
// docs/QUESTIONS.md — that gap, not this function, is what TODO.md's "done
// when a console loads it" bar is still waiting on.
func ConvertTextures(toolPath, sourceDir, assetsDir string) ([]string, error) {
	var converted []string

	err := filepath.WalkDir(sourceDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		if !strings.EqualFold(filepath.Ext(path), ".png") {
			return copyAsset(path, filepath.Join(assetsDir, rel))
		}

		if toolPath == "" {
			return fmt.Errorf("%w: %s", ErrTextureConverterNotConfigured, rel)
		}
		outRel := strings.TrimSuffix(rel, filepath.Ext(rel)) + ".bntx"
		outPath := filepath.Join(assetsDir, outRel)
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return err
		}

		name := strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
		cmd := exec.Command(toolPath, path, outPath, "-n", name)
		if combined, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("bcat: convert texture %s: %w: %s", rel, err, bytes.TrimSpace(combined))
		}
		converted = append(converted, outRel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return converted, nil
}

func copyAsset(srcPath, dstPath string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dstPath, data, 0o644)
}
