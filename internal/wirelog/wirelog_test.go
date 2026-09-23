package wirelog

import (
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type untouchedBody struct{ reads int }

func (b *untouchedBody) Read([]byte) (int, error) {
	b.reads++
	return 0, io.EOF
}

func (b *untouchedBody) Close() error { return nil }

func TestRecordStreamDoesNotConsumeOrPersistTheRequestBody(t *testing.T) {
	dir := t.TempDir()
	recorder, err := New(slog.New(slog.NewTextHandler(io.Discard, nil)), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	body := &untouchedBody{}
	u, _ := url.Parse("https://frontline.example/stream?mode=hall")
	req := &http.Request{
		Method: "POST", URL: u, Host: "frontline.example", Proto: "HTTP/1.1",
		ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{"Authorization": {"secret"}},
		Body: body, ContentLength: -1, TransferEncoding: []string{"chunked"},
	}

	recorder.RecordStream(req, http.StatusOK, "frontline")

	if body.reads != 0 {
		t.Fatalf("stream body was read %d times by metadata recorder", body.reads)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("transcript files = %d, want 1", len(entries))
	}
	content, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	transcript := string(content)
	for _, want := range []string{"Authorization: <redacted, 6 bytes>", "request body omitted", "Transfer-Encoding: chunked"} {
		if !strings.Contains(transcript, want) {
			t.Errorf("transcript does not contain %q:\n%s", want, transcript)
		}
	}
	if strings.Contains(transcript, "secret") {
		t.Fatal("transcript contains an authorization value")
	}
}
