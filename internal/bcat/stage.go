package bcat

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Splatoon3TitleID is the title a fest's delivery cache belongs to, and the
// folder under the native seed root it is served from.
const Splatoon3TitleID = "0100c2500fc20000"

// StageNativeSeed makes seedDir/bcat-seed (a `fest build -out` folder) the
// delivery cache served to consoles, replacing root/<Splatoon 3>.
//
// The new tree is copied beside the old one and swapped in with renames, so a
// console listing the cache mid-way sees either the old fest or the new one,
// never half of each -- a half would be BcatInvalid.
func StageNativeSeed(seedDir, root string) (string, error) {
	src := filepath.Join(seedDir, "bcat-seed")
	dst := filepath.Join(root, Splatoon3TitleID)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(root, "."+Splatoon3TitleID+"-new-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if err := copyTree(src, tmp); err != nil {
		return "", fmt.Errorf("copy %s: %w", src, err)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return "", err
	}

	old := tmp + ".old"
	if err := os.Rename(dst, old); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Rename(old, dst)
		return "", err
	}
	_ = os.RemoveAll(old)
	return dst, nil
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	})
}
