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

func TestARecorderRestartKeepsOldTranscriptsAndRecordsNewOnes(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	u, _ := url.Parse("https://bcat.example/list")
	req := &http.Request{Method: "GET", URL: u, Host: "bcat.example", ProtoMajor: 1, ProtoMinor: 1}

	first, err := New(log, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	first.Record(req, nil, http.StatusOK, "before restart")

	second, err := New(log, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	second.Record(req, nil, http.StatusOK, "after restart")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("transcripts after restart = %d, want 2", len(entries))
	}
	for i, want := range []string{"before restart", "after restart"} {
		body, err := os.ReadFile(filepath.Join(dir, entries[i].Name()))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), want) {
			t.Errorf("transcript %q lost %q", entries[i].Name(), want)
		}
	}
}
