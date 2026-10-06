package bcat

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStagingReplacesTheServedFestWholesale(t *testing.T) {
	out, root := t.TempDir(), t.TempDir()
	write := func(p, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The fest being replaced has a region the new one does not; it must go,
	// or consoles would be served a mix of two fests.
	write(filepath.Join(root, Splatoon3TitleID, "jp-default", "summary.yml"), "old")
	write(filepath.Join(out, "bcat-seed", "eu-default", "summary.yml"), "new")

	dst, err := StageNativeSeed(out, root)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "eu-default", "summary.yml")); string(got) != "new" {
		t.Errorf("served summary = %q, want the new fest's", got)
	}
	if _, err := os.Stat(filepath.Join(dst, "jp-default")); !os.IsNotExist(err) {
		t.Errorf("the old fest's region is still served")
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Errorf("root holds %d entries, want only the title folder", len(entries))
	}
}
