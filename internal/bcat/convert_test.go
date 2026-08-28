package bcat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeAssets lays out a pack directory and returns its root.
func writeAssets(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, body := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	return root
}

func packNames(t *testing.T, packs Packs, pack string) []string {
	t.Helper()
	files, err := ParseSARC(packs[pack])
	if err != nil {
		t.Fatalf("parse %s: %v", pack, err)
	}
	var names []string
	for _, f := range files {
		names = append(names, f.Name)
	}
	return names
}

// TestAnUnconvertedSourceAssetIsRefusedRatherThanPacked is the trap this
// pipeline exists to close.
//
// Before it, a PNG left in an assets directory went straight into the archive.
// The archive is still valid, so the console reads it — and then cannot decode
// the texture slot. The result is a fest that loads with missing or garbled art,
// on a console, with nothing on the server to look at. Failing here costs the
// operator one message.
func TestAnUnconvertedSourceAssetIsRefusedRatherThanPacked(t *testing.T) {
	assets := writeAssets(t, map[string]string{
		"base/UI/Splashart.bfres.png": "\x89PNG not really",
		"base/Mals/Msg_EUen.byml":     "already a game format",
	})

	_, err := BuildPacks(assets)
	if err == nil {
		t.Fatal("a PNG was packed with no converter configured")
	}
	// The message has to name the file and the setting, because the operator's
	// next action depends on both.
	if !strings.Contains(err.Error(), "Splashart.bfres.png") {
		t.Errorf("the error does not name the file: %v", err)
	}
	if !strings.Contains(err.Error(), "bcat.converter") {
		t.Errorf("the error does not name the setting to change: %v", err)
	}
}

func TestASourceFileMustBeNamedForWhatItBecomes(t *testing.T) {
	// The rule is one line: append the source extension to the target name.
	// "splashart.png" does not say what it becomes, and guessing produces a
	// file the game does not recognise.
	assets := writeAssets(t, map[string]string{"base/UI/splashart.png": "x"})

	_, err := BuildPacks(assets)
	if err == nil {
		t.Fatal("a source file with no target name was accepted")
	}
	if !strings.Contains(err.Error(), "Splashart.bfres.png") {
		t.Errorf("the error does not show the naming rule: %v", err)
	}
}

// fakeConverter writes a deterministic body, so a test can tell converted output
// from passed-through input.
type fakeConverter struct {
	calls []string
	fail  bool
	empty bool
}

func (f *fakeConverter) Describe() string { return "fake" }

func (f *fakeConverter) Convert(_ context.Context, src, dst, kind, target string) error {
	f.calls = append(f.calls, filepath.Base(src)+" -> "+filepath.Base(dst)+" ("+kind+target+")")
	if f.fail {
		return os.ErrPermission
	}
	if f.empty {
		return os.WriteFile(dst, nil, 0o600)
	}
	return os.WriteFile(dst, []byte("converted:"+filepath.Base(src)), 0o600)
}

func TestAConvertedAssetEntersThePackUnderItsTargetName(t *testing.T) {
	assets := writeAssets(t, map[string]string{
		"base/UI/Splashart.bfres.png": "png bytes",
		"base/Mals/Msg_EUen.byml":     "game format",
	})
	conv := &fakeConverter{}

	packs, err := BuildPacksWith(context.Background(), assets, BuildOptions{Converter: conv})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	names := packNames(t, packs, PackBase)
	want := map[string]bool{"UI/Splashart.bfres": false, "Mals/Msg_EUen.byml": false}
	for _, n := range names {
		if _, ok := want[n]; !ok {
			t.Errorf("unexpected file %q in the pack", n)
			continue
		}
		want[n] = true
	}
	for n, seen := range want {
		if !seen {
			t.Errorf("%q is missing from the pack", n)
		}
	}

	// The converter is told the kind and the target extension, so one command
	// can dispatch rather than needing one converter per format.
	if len(conv.calls) != 1 || !strings.Contains(conv.calls[0], "(image.bfres)") {
		t.Errorf("converter calls = %v", conv.calls)
	}
}

func TestAConverterThatFailsOrWritesNothingStopsTheBuild(t *testing.T) {
	assets := writeAssets(t, map[string]string{"base/UI/Splashart.bfres.png": "png"})

	if _, err := BuildPacksWith(context.Background(), assets,
		BuildOptions{Converter: &fakeConverter{fail: true}}); err == nil {
		t.Error("a failing converter did not stop the build")
	}

}

func TestTwoFilesThatBecomeTheSameNameAreRefused(t *testing.T) {
	// Splashart.bfres.png alongside Splashart.bfres. BuildSARC would store both
	// under one name and the console would read whichever it found first.
	assets := writeAssets(t, map[string]string{
		"base/UI/Splashart.bfres":     "hand-authored",
		"base/UI/Splashart.bfres.png": "png",
	})

	_, err := BuildPacksWith(context.Background(), assets,
		BuildOptions{Converter: &fakeConverter{}})
	if err == nil {
		t.Fatal("a collision between a converted and a hand-authored file was accepted")
	}
	if !strings.Contains(err.Error(), "UI/Splashart.bfres") {
		t.Errorf("the error does not name the collision: %v", err)
	}
}

func TestTheFestPlaceholderIsSubstitutedInInPackPaths(t *testing.T) {
	// A real custom pack had every internal file named for the fest it was
	// DERIVED from, while announcing a different one. Nobody has established
	// what the game keys on, so this is the decision in one place: use the
	// placeholder and get consistent naming, or do not and keep source names.
	assets := writeAssets(t, map[string]string{
		"base/Mals/Msg_EUen.Fest_{FEST}_Bh.sarc.zs": "messages",
		"base/RSDB/Tag.dev.rstbl.byml.zs":           "tables",
	})

	packs, err := BuildPacksWith(context.Background(), assets, BuildOptions{
		Naming: Naming{FestID: "NXTD-00042"},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	names := packNames(t, packs, PackBase)
	var found bool
	for _, n := range names {
		if n == "Mals/Msg_EUen.Fest_NXTD-00042_Bh.sarc.zs" {
			found = true
		}
		if strings.Contains(n, FestPlaceholder) {
			t.Errorf("%q still carries the placeholder", n)
		}
	}
	if !found {
		t.Errorf("the placeholder was not substituted: %v", names)
	}
}

func TestWithNoFestIDThePlaceholderIsLeftVisible(t *testing.T) {
	// Left in place rather than stripped: a file literally called
	// Fest_{FEST}_Bh is obviously wrong in a directory listing, whereas
	// Fest__Bh looks like a real name and would be shipped.
	assets := writeAssets(t, map[string]string{
		"base/Mals/Msg_EUen.Fest_{FEST}_Bh.sarc.zs": "messages",
	})

	packs, err := BuildPacksWith(context.Background(), assets, BuildOptions{})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	names := packNames(t, packs, PackBase)
	if len(names) != 1 || !strings.Contains(names[0], FestPlaceholder) {
		t.Errorf("names = %v, want the placeholder left visible", names)
	}
}

func TestAPackBuildIsReproducible(t *testing.T) {
	// The revision is a digest of the pack's bytes, so the bytes must depend on
	// the contents and not on the order the filesystem walked them -- otherwise
	// the announced revision moves for no reason and every console re-downloads.
	assets := writeAssets(t, map[string]string{
		"base/a.byml":     "a",
		"base/z/b.byml":   "b",
		"base/m/c/d.byml": "d",
	})

	first, err := BuildPacks(assets)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	second, err := BuildPacks(assets)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if Revision(first) != Revision(second) {
		t.Errorf("two builds of the same assets gave %s and %s",
			Revision(first), Revision(second))
	}
}

func TestConvertedOutputDoesNotLandInTheOperatorsAssetsDirectory(t *testing.T) {
	// A build that wrote converted files back into the source tree would leave
	// the next build treating them as hand-authored -- and then a collision, or
	// worse, a stale texture nobody remembers generating.
	assets := writeAssets(t, map[string]string{"base/UI/Splashart.bfres.png": "png"})

	if _, err := BuildPacksWith(context.Background(), assets,
		BuildOptions{Converter: &fakeConverter{}}); err != nil {
		t.Fatalf("build: %v", err)
	}
	if _, err := os.Stat(filepath.Join(assets, "base", "UI", "Splashart.bfres")); !os.IsNotExist(err) {
		t.Error("the build left converted output in the assets directory")
	}
}

func TestAConfiguredCommandThatDoesNotExistIsRefusedAtStartup(t *testing.T) {
	// Resolved when configured rather than when used, so a missing tool is an
	// error at startup and not halfway through an operator building a fest.
	if _, err := NewCommandConverter("definitely-not-a-real-converter-binary", time.Minute); err == nil {
		t.Error("a converter command that does not exist was accepted")
	}
	if _, err := NewCommandConverter("   ", time.Minute); err == nil {
		t.Error("an empty converter command was accepted")
	}
}

func TestTheCommandConverterReportsWhatItRanWhenItFails(t *testing.T) {
	conv, err := NewCommandConverter("false", time.Minute)
	if err != nil {
		t.Skipf("no /bin/false available: %v", err)
	}
	dir := t.TempDir()
	err = conv.Convert(context.Background(),
		filepath.Join(dir, "in.png"), filepath.Join(dir, "out.bfres"), "image", ".bfres")
	if err == nil {
		t.Fatal("a converter that exits non-zero was reported as success")
	}
	// A conversion that half-worked is much harder to diagnose from the pack
	// than from the command line that produced it.
	if !strings.Contains(err.Error(), "false") {
		t.Errorf("the error does not say what was run: %v", err)
	}
}

func TestACommandThatExitsZeroWithoutWritingIsCaught(t *testing.T) {
	conv, err := NewCommandConverter("true", time.Minute)
	if err != nil {
		t.Skipf("no /bin/true available: %v", err)
	}
	dir := t.TempDir()
	err = conv.Convert(context.Background(),
		filepath.Join(dir, "in.png"), filepath.Join(dir, "out.bfres"), "image", ".bfres")
	if err == nil {
		t.Fatal("exit zero with no output file was reported as success")
	}
	// Otherwise the pack is silently missing a file, and on the console that is
	// a fest with an invisible splashart and nothing to say why.
	if !strings.Contains(err.Error(), "wrote no") {
		t.Errorf("the error does not say the output is missing: %v", err)
	}
}
