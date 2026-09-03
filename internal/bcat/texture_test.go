package bcat

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeConverter writes a tiny shell script that follows img2bntx's own CLI
// contract (<image> <out> -n <name>) closely enough to exercise
// ConvertTextures' own plumbing -- argument passing, output placement,
// error propagation -- without needing a real Rust toolchain in every
// environment these tests run in. img2bntx's own texture math (swizzling,
// the BNTX container shape) was verified separately, by building and
// running the real tool; see texture.go's doc comment.
func fakeConverter(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-img2bntx.sh")
	script := "#!/bin/sh\necho \"bntx:$1:$4\" > \"$2\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// failingConverter always exits non-zero, to exercise ConvertTextures'
// error wrapping.
func failingConverter(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-img2bntx-fail.sh")
	script := "#!/bin/sh\necho \"boom: cannot convert $1\" >&2\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestConvertTexturesConvertsPNGAndCopiesEverythingElse(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "base", "Splash.png"), []byte("not a real png"))
	writeFile(t, filepath.Join(src, "base", "Mals", "Msg.sarc.zs"), []byte("already game-ready"))

	dst := t.TempDir()
	converted, err := ConvertTextures(fakeConverter(t), src, dst)
	if err != nil {
		t.Fatalf("ConvertTextures: %v", err)
	}
	if len(converted) != 1 || converted[0] != "base/Splash.bntx" {
		t.Fatalf("converted = %v, want [base/Splash.bntx]", converted)
	}

	bntx, err := os.ReadFile(filepath.Join(dst, "base", "Splash.bntx"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(string(bntx))
	want := "bntx:" + filepath.Join(src, "base", "Splash.png") + ":Splash"
	if got != want {
		t.Errorf("fake converter's output = %q, want %q (checks the tool was called with the right image path and -n name)", got, want)
	}

	sarc, err := os.ReadFile(filepath.Join(dst, "base", "Mals", "Msg.sarc.zs"))
	if err != nil {
		t.Fatal(err)
	}
	if string(sarc) != "already game-ready" {
		t.Errorf("non-.png file was not copied through unchanged: got %q", sarc)
	}
}

func TestConvertTexturesRequiresAToolWhenThereIsAPNGToConvert(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "base", "Splash.png"), []byte("x"))

	_, err := ConvertTextures("", src, t.TempDir())
	if !errors.Is(err, ErrTextureConverterNotConfigured) {
		t.Fatalf("err = %v, want ErrTextureConverterNotConfigured", err)
	}
}

func TestConvertTexturesWithNoPNGsNeedsNoTool(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "base", "Mals", "Msg.sarc.zs"), []byte("already game-ready"))

	dst := t.TempDir()
	converted, err := ConvertTextures("", src, dst)
	if err != nil {
		t.Fatalf("ConvertTextures with nothing to convert should not require a tool: %v", err)
	}
	if len(converted) != 0 {
		t.Errorf("converted = %v, want none", converted)
	}
	if _, err := os.Stat(filepath.Join(dst, "base", "Mals", "Msg.sarc.zs")); err != nil {
		t.Errorf("file was not copied through: %v", err)
	}
}

func TestConvertTexturesPropagatesTheConvertersFailure(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "base", "Splash.png"), []byte("x"))

	_, err := ConvertTextures(failingConverter(t), src, t.TempDir())
	if err == nil {
		t.Fatal("expected the converter's failure to propagate")
	}
	if !strings.Contains(err.Error(), "base/Splash.png") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("error does not name the file or carry the tool's own message: %v", err)
	}
}

// TestConvertTexturesOutputFeedsBuildPacksDirectly is the integration
// property the whole point of ConvertTextures rests on: its output
// directory is exactly what BuildPacks already expects, with no
// translation step in between.
func TestConvertTexturesOutputFeedsBuildPacksDirectly(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "base", "Splash.png"), []byte("x"))
	writeFile(t, filepath.Join(src, "base", "Mals", "Msg.sarc.zs"), []byte("already game-ready"))

	dst := t.TempDir()
	if _, err := ConvertTextures(fakeConverter(t), src, dst); err != nil {
		t.Fatalf("ConvertTextures: %v", err)
	}

	packs, err := BuildPacks(dst)
	if err != nil {
		t.Fatalf("BuildPacks on ConvertTextures' own output: %v", err)
	}
	if _, ok := packs[PackBase]; !ok {
		t.Fatalf("got packs %v, want a %q pack", packNames(packs), PackBase)
	}
}

func packNames(packs Packs) []string {
	names := make([]string, 0, len(packs))
	for name := range packs {
		names = append(names, name)
	}
	return names
}
