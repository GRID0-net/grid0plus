package baas

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// An emulator signed in to GRID0+ lists a game's BCAT delivery cache and
// downloads its files: the same files a console's bcat sysmodule fetches.
func TestAnEmulatorFetchesAGamesBCATFiles(t *testing.T) {
	seed := t.TempDir()
	dir := filepath.Join(seed, "0100c2500fc20000", "eu-default")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.yml"), []byte("fest: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, func(c *Config) { c.BCATSeedDir = seed })
	token := emulatorLogin(t, h, "alice", "password-a")

	var list struct {
		Directories []emulatorBCATDirectory `json:"directories"`
	}
	resp := h.do(request{method: "GET", path: "/emulator/v1/bcat/0100c2500fc20000", token: token})
	requireStatus(t, resp, 200)
	decode(t, resp, &list)
	if len(list.Directories) != 1 || list.Directories[0].Name != "eu-default" ||
		len(list.Directories[0].Files) != 1 || list.Directories[0].Files[0].Size != 8 {
		t.Fatalf("list = %+v", list)
	}

	resp = h.do(request{method: "GET", path: "/emulator/v1/bcat/0100c2500fc20000/eu-default/summary.yml", token: token})
	requireStatus(t, resp, 200)
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "fest: 1\n" {
		t.Errorf("file = %q", body)
	}

	resp = h.do(request{method: "GET", path: "/emulator/v1/bcat/0100c2500fc20000", token: ""})
	if resp.StatusCode == 200 {
		t.Error("the BCAT list answered without a login")
	}
}
