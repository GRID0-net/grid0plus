package stubs

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeBCATServesOnlySeededTitlesAndLeavesTopicsAlone(t *testing.T) {
	root := t.TempDir()
	title := "0100c2500fc20000"
	dir := filepath.Join(root, title, "fesdata")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.yml"), []byte("FestID: TEST\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHandler(t, Config{BCATSeedDir: root})
	list := get(t, h, "bcat-list-lp1.cdn.nintendo.net", bcatListPrefix+title)
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d", list.Code)
	}
	if !bytes.Equal(list.Body.Bytes()[:4], []byte("bcat")) || list.Body.Bytes()[4] != 1 || list.Body.Bytes()[5] != 0 {
		t.Fatal("list is not a plaintext BCAT container")
	}
	if !bytes.Equal(list.Body.Bytes()[0x20:0x120], make([]byte, 0x100)) {
		t.Fatal("signature must remain zero")
	}
	if !bytes.Contains(list.Body.Bytes()[0x120:], []byte("summary.yml")) {
		t.Fatal("list lacks the seed filename")
	}
	for _, field := range []string{"version", "format", "semantics", "news_id"} {
		if !bytes.Contains(list.Body.Bytes()[0x120:], []byte(field)) {
			t.Errorf("list data entry lacks parser-required field %q", field)
		}
	}
	if !strings.Contains(list.Header().Get("ETag"), "\"") {
		t.Fatal("missing quoted ETag")
	}
	data := get(t, h, "bcat-data-lp1.cdn.nintendo.net", bcatDataPrefix+title+"/fesdata/summary.yml")
	if data.Code != http.StatusOK || string(data.Body.Bytes()[0x120:]) != "FestID: TEST\n" {
		t.Fatalf("data status=%d body=%q", data.Code, data.Body.String())
	}
	for _, tt := range []struct{ host, path string }{
		{"bcat-list-lp1.cdn.nintendo.net", bcatListPrefix + "01003bc0000a0000"},
		{"bcat-data-lp1.cdn.nintendo.net", bcatDataPrefix + title + "/fesdata/nope"},
		{"bcat-topics-lp1.cdn.nintendo.net", "/api/nx/v1/titles/" + title + "/topics"},
	} {
		res := get(t, h, tt.host, tt.path)
		if res.Code != http.StatusNotModified {
			t.Errorf("%s %s: got %d", tt.host, tt.path, res.Code)
		}
	}
	s2 := "01003bc0000a0000"
	s2dir := filepath.Join(root, s2, "vsdata")
	if err := os.MkdirAll(s2dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s2dir, "schedule.byml"), []byte{1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := get(t, h, "bcat-list-lp1.cdn.nintendo.net", bcatListPrefix+s2).Code; got != http.StatusOK {
		t.Fatalf("Splatoon 2 list = %d", got)
	}
}
