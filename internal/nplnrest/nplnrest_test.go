package nplnrest

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	vermillionHost = "gw.hac.lp1.vermillion.srv.nintendo.net"
	penneHost      = "val.hac.lp1.penne.srv.nintendo.net"
	frontlineHost  = "fro-3.hac.lp1.penne.srv.nintendo.net"
)

func newServer(t *testing.T, recordDir string) *Server {
	t.Helper()
	s, err := New(Options{
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		FrontlineHost: frontlineHost,
		RecordDir:     recordDir,
		Now:           func() time.Time { return time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return s
}

func TestAFrontlineHostThatIsNotRedirectedIsRefusedAtStartup(t *testing.T) {
	// The console opens its persistent connection to whatever this names. A
	// host SwitchNet does not redirect sends that connection to real Nintendo,
	// where it SUCCEEDS -- so nothing fails here and the game misbehaves later
	// while its traffic goes somewhere this deployment promises it will not.
	_, err := New(Options{
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		FrontlineHost: "frontline.example.com",
	})
	if err == nil {
		t.Fatal("a frontline host outside the redirect list was accepted")
	}
	if !strings.Contains(err.Error(), "real Nintendo") {
		t.Errorf("the error does not say what goes wrong: %v", err)
	}

	if _, err := New(Options{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}); err == nil {
		t.Error("an empty frontline host was accepted")
	}
}

func TestPenneNamesTheFrontlineHost(t *testing.T) {
	s := newServer(t, "")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/login", strings.NewReader(`{}`))
	req.Host = penneHost
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}

	// The field name is unconfirmed, so the FQDN goes out under every plausible
	// spelling. What this asserts is that the value is right and reachable
	// under at least one of them -- not which one, because nobody knows.
	found := false
	for _, v := range body {
		if v == frontlineHost {
			found = true
		}
	}
	if !found {
		t.Errorf("the frontline host is not in the response: %v", body)
	}
}

// TestPenneIssuesATicketAndSessionConfig pins the fields added 2026-09-20 from
// a decompile of the operator's own console's npns sysmodule, which shares
// this exact ticket-then-frontline mechanism with NPLN: the field names are
// evidence-backed, even though the values themselves are placeholders.
func TestPenneIssuesATicketAndSessionConfig(t *testing.T) {
	s := newServer(t, "")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/login", strings.NewReader(`{}`))
	req.Host = penneHost
	s.Handler().ServeHTTP(rec, req)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}

	ticket, _ := body["ticket"].(string)
	if ticket == "" {
		t.Error("penne's response has no ticket")
	}

	issuedAt, ok := body["issued_at"].(float64)
	if !ok || int64(issuedAt) != s.now().Unix() {
		t.Errorf("issued_at = %v, want %d", body["issued_at"], s.now().Unix())
	}

	for _, key := range []string{"wait_sec", "rtt_max", "wowl_timeout", "ignore_rst"} {
		if _, ok := body[key]; !ok {
			t.Errorf("penne's response is missing %q: %v", key, body)
		}
	}
}

func TestVermillionAnswersRatherThanFailing(t *testing.T) {
	s := newServer(t, "")

	// A transport failure makes the console decide the network is broken, which
	// surfaces somewhere unrelated. A well-formed answer it may not like is the
	// lesser harm while the real shape is unknown.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/device/init", strings.NewReader(`{"x":1}`))
	req.Host = vermillionHost
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("content-type = %q, want JSON", got)
	}
}

func TestAnUnknownHostIsAnsweredRatherThanRefused(t *testing.T) {
	s := newServer(t, "")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "something.else.nintendo.net"
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 — a connection error is worse than an empty answer", rec.Code)
	}
}

func TestTheFrontlineConnectionIsHeldOpenUntilTheConsoleHangsUp(t *testing.T) {
	s := newServer(t, "")
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/frontline", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Host = frontlineHost

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer resp.Body.Close()

	// The headers arrive immediately, so the console sees the response begin. A
	// response that has not started is indistinguishable from a server that is
	// not there.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if s.Frontlines() != 1 {
		t.Errorf("the server reports %d frontline connections, want 1", s.Frontlines())
	}

	// And then nothing is sent. Closing it, or writing an invented frame into a
	// stream whose framing nobody has documented, are both worse than silence.
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 1)
		_, _ = resp.Body.Read(buf)
	}()
	select {
	case <-done:
		t.Error("the frontline connection sent something or closed; it must do neither")
	case <-time.After(250 * time.Millisecond):
	}

	cancel()
	<-done

	// The count drops when the console goes away, which is what makes it usable
	// as "did the console get this far".
	deadline := time.Now().Add(2 * time.Second)
	for s.Frontlines() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.Frontlines() != 0 {
		t.Errorf("the frontline count stayed at %d after the console hung up", s.Frontlines())
	}
}

func TestRecordingWritesATranscriptWithTheCredentialsRedacted(t *testing.T) {
	dir := t.TempDir()
	s := newServer(t, dir)

	req := httptest.NewRequest(http.MethodPost, "/v1/login?region=EU",
		strings.NewReader("\x00\x01binary\xff"))
	req.Host = penneHost
	req.Header.Set("Authorization", "Bearer a-real-token")
	req.Header.Set("User-Agent", "NPLN/1.0")
	s.Handler().ServeHTTP(httptest.NewRecorder(), req)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read record dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("recorded %d files, want 1", len(entries))
	}
	raw, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	got := string(raw)

	// A capture has to be redactable without re-reading it, so the credential
	// never lands on disk -- but WHICH credentials the console presents is part
	// of what the capture is for, so the header itself stays.
	if strings.Contains(got, "a-real-token") {
		t.Error("the bearer token was written to the transcript")
	}
	if !strings.Contains(got, "Authorization: <redacted") {
		t.Error("the transcript does not record that an Authorization header was sent")
	}
	if !strings.Contains(got, "User-Agent: NPLN/1.0") {
		t.Error("an ordinary header was dropped")
	}

	// A hexdump, not a Go string: these bodies are not known to be text, and a
	// string would mangle every byte above 0x7f in exactly the capture somebody
	// is relying on.
	if !strings.Contains(got, "00000000  00 01 62 69 6e 61 72 79") {
		t.Errorf("the body was not hexdumped:\n%s", got)
	}
	if !strings.Contains(got, "region=EU") {
		t.Error("the query string was not recorded")
	}
	if !strings.Contains(got, "--- response 200 ---") {
		t.Error("the response was not recorded alongside the request")
	}
}

func TestRecordedFilesAreNumberedInTheOrderTheConsoleProducedThem(t *testing.T) {
	dir := t.TempDir()
	s := newServer(t, dir)

	for _, path := range []string{"/first", "/second", "/third"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = vermillionHost
		s.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read record dir: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("recorded %d files, want 3", len(entries))
	}
	// os.ReadDir sorts by name, so a zero-padded sequence number means the
	// transcript reads in the order it happened rather than alphabetically by
	// path.
	want := []string{"first", "second", "third"}
	for i, e := range entries {
		if !strings.Contains(e.Name(), want[i]) {
			t.Errorf("file %d is %q, want the one for %q", i, e.Name(), want[i])
		}
	}
}

func TestAPathThatWouldEscapeTheRecordDirectoryCannot(t *testing.T) {
	dir := t.TempDir()
	s := newServer(t, dir)

	// The path is whatever reaches the port. A separator or a traversal
	// surviving into a filename would let it write outside the directory.
	for _, path := range []string{"/..%2f..%2fescaped", "/a/b/c", "/" + strings.Repeat("x", 400)} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = vermillionHost
		s.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read record dir: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("recorded %d files, want 3 — one escaped the directory", len(entries))
	}
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("%q is a directory: a path separator survived into the filename", e.Name())
		}
		if strings.Contains(e.Name(), "..") || strings.Contains(e.Name(), "/") {
			t.Errorf("%q keeps a traversal or a separator", e.Name())
		}
		if len(e.Name()) > 180 {
			t.Errorf("%q is %d bytes, which some filesystems refuse", e.Name(), len(e.Name()))
		}
	}
}
