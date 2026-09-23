package nplnrest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	req := httptest.NewRequest(http.MethodPost, "/v1/login_tickets", strings.NewReader(`{"id":"pz1","password":"x"}`))
	req.Host = penneHost
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}

	if got := body["frontline_fqdn"]; got != frontlineHost {
		t.Errorf("frontline_fqdn = %v, want %q", got, frontlineHost)
	}
}

// TestPenneIssuesATicketAndSessionConfig pins the confirmed response shape
// (see the package doc comment): a real capture of a working login showed
// exactly these fields, including the two nested session-config objects
// this project's own earlier guess was missing entirely.
func TestPenneIssuesATicketAndSessionConfig(t *testing.T) {
	s := newServer(t, "")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/login_tickets", strings.NewReader(`{"id":"pz1","password":"x"}`))
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
	if expiresAt, ok := body["expires_at"].(float64); !ok || int64(expiresAt) != s.now().Unix()+345600 {
		t.Errorf("expires_at = %v, want issued_at + 345600", body["expires_at"])
	}

	params, ok := body["persistent_connection_params_simple"].(map[string]any)
	if !ok {
		t.Fatalf("persistent_connection_params_simple missing or wrong type: %v", body)
	}
	for _, key := range []string{"awake", "sleep", "ignore_rst", "retry_count", "rtt_max", "wait_sec", "wowl_timeout"} {
		if _, ok := params[key]; !ok {
			t.Errorf("persistent_connection_params_simple is missing %q: %v", key, params)
		}
	}
	if params["rtt_max"] != float64(1000) || params["wait_sec"] != float64(3600) || params["wowl_timeout"] != float64(200) {
		t.Errorf("confirmed session-config values changed: %v", params)
	}
}

func TestPenneFrontlinesReturnsTheSameFQDNAndParams(t *testing.T) {
	s := newServer(t, "")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/frontlines", nil)
	req.Host = penneHost
	s.Handler().ServeHTTP(rec, req)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if got := body["frontline_fqdn"]; got != frontlineHost {
		t.Errorf("frontline_fqdn = %v, want %q", got, frontlineHost)
	}
	if _, ok := body["persistent_connection_params_simple"]; !ok {
		t.Error("frontlines response has no persistent_connection_params_simple")
	}
	if _, ok := body["current_time"]; !ok {
		t.Error("frontlines response has no current_time")
	}
}

func TestVermillionDeviceIDIsCamelCaseAndSafeForAPathSegment(t *testing.T) {
	s := newServer(t, "")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/devices/vermillion-device-id", nil)
	req.Host = vermillionHost
	req.Header.Set("User-Agent", "NintendoSDK Firmware/22.5.0-1.0 (platform:NX; did:63671daf59441c13; eid:lp1)")
	s.Handler().ServeHTTP(rec, req)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	id, ok := body["vermillionDeviceId"].(string)
	if !ok || id == "" {
		// The wrong (snake_case) key here is not a cosmetic mismatch -- it is
		// the console never receiving a device id and looping this endpoint
		// forever. See the confirmed-bug note in nplnrest.go.
		t.Fatalf("response has no camelCase vermillionDeviceId: %v", body)
	}
	if strings.ContainsAny(id, "/+") {
		// Splatoon 3 uses this value as a literal path segment and
		// hard-crashes on either character.
		t.Errorf("vermillionDeviceId %q contains '/' or '+'", id)
	}

	// Same device id (same User-Agent) must be stable across requests, and a
	// different device must get a different id -- two consoles behind this
	// server must not collide on one identity.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/v1/devices/vermillion-device-id", nil)
	req2.Host = vermillionHost
	req2.Header.Set("User-Agent", req.Header.Get("User-Agent"))
	s.Handler().ServeHTTP(rec2, req2)
	var body2 map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &body2)
	if body2["vermillionDeviceId"] != id {
		t.Errorf("same device got a different id: %v vs %v", id, body2["vermillionDeviceId"])
	}

	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, "/v1/devices/vermillion-device-id", nil)
	req3.Host = vermillionHost
	req3.Header.Set("User-Agent", "NintendoSDK Firmware/22.5.0-1.0 (platform:NX; did:aaaaaaaaaaaaaaaa; eid:lp1)")
	s.Handler().ServeHTTP(rec3, req3)
	var body3 map[string]any
	_ = json.Unmarshal(rec3.Body.Bytes(), &body3)
	if body3["vermillionDeviceId"] == id {
		t.Error("two different devices got the same vermillion device id")
	}
}

func TestVermillionAccountsConfigGrantsOnlineLicense(t *testing.T) {
	s := newServer(t, "")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/accounts/config", nil)
	req.Host = vermillionHost
	s.Handler().ServeHTTP(rec, req)

	var envelope map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(envelope["payload"])
	if err != nil {
		t.Fatalf("payload is not base64: %v", err)
	}
	var inner map[string]any
	if err := json.Unmarshal(raw, &inner); err != nil {
		t.Fatalf("decoded payload is not JSON: %v", err)
	}
	license, ok := inner["online_license"].(map[string]any)
	if !ok || license["is_available"] != true {
		// A console that reads false here treats the account as unlicensed
		// for online play and never leaves the lobby.
		t.Errorf("online_license.is_available is not true: %v", inner["online_license"])
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

type blockingFrontlineBody struct {
	firstRead  chan struct{}
	secondRead chan struct{}
	release    chan struct{}
	first      bool
	closeOnce  sync.Once
	secondOnce sync.Once
}

func (b *blockingFrontlineBody) Read(p []byte) (int, error) {
	if !b.first {
		b.first = true
		n := copy(p, []byte("opaque-frame"))
		close(b.firstRead)
		return n, nil
	}
	b.secondOnce.Do(func() { close(b.secondRead) })
	<-b.release
	return 0, io.EOF
}

func (b *blockingFrontlineBody) Close() error {
	b.closeOnce.Do(func() { close(b.release) })
	return nil
}

func TestTheFrontlineDrainsRequestBodyAndStaysOpenUntilItEnds(t *testing.T) {
	s := newServer(t, "")
	body := &blockingFrontlineBody{
		firstRead: make(chan struct{}), secondRead: make(chan struct{}), release: make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
	req.Body = body
	req.Host = frontlineHost
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Handler().ServeHTTP(rec, req)
	}()

	select {
	case <-body.firstRead:
	case <-time.After(time.Second):
		t.Fatal("frontline handler did not consume the request body")
	}
	select {
	case <-body.secondRead:
	case <-time.After(time.Second):
		t.Fatal("frontline reader did not remain active after the first frame")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("frontline response body has %d bytes, want no unsolicited frame", rec.Body.Len())
	}

	select {
	case <-done:
		t.Fatal("frontline closed while the request body was still open")
	case <-time.After(100 * time.Millisecond):
	}

	_ = body.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("frontline did not close after the request body ended")
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
