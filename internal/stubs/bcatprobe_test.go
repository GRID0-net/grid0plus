package stubs

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTheBCATProbeIsOffByDefault is the one that matters most here.
//
// The three bcat-* hosts are null-routed on the console precisely because
// answering them badly is worse than not answering: a 200 with an empty body,
// to a console that expected content, turns an immediate legible failure into a
// hang. Nothing should be able to turn that on by accident.
func TestTheBCATProbeIsOffByDefault(t *testing.T) {
	h := New(Config{}, slog.New(slog.DiscardHandler)).Handler()

	rec := get(t, h, "bcat-list-lp1.cdn.nintendo.net", "/api/nx/v1/list/anything")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	// The ordinary catch-all, not the probe: an empty JSON object.
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("the default answer is not the empty-object stub: %q", rec.Body.String())
	}
	if len(body) != 0 {
		t.Errorf("expected an empty object, got %v", body)
	}
}

// TestTheBCATProbeLogsWhatTheConsoleAsksFor is the experiment working.
//
// The whole point of the probe is the log line: it is the first record anyone
// would have of what Splatoon 3 actually requests from BCAT. A probe that
// answered correctly but logged nothing would be useless.
func TestTheBCATProbeLogsWhatTheConsoleAsksFor(t *testing.T) {
	var logged bytes.Buffer
	h := New(Config{BCATProbe: true},
		slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))).
		Handler()

	req := httptest.NewRequest("POST",
		"http://bcat-list-lp1.cdn.nintendo.net/api/nx/v1/list/JUEA-00210?lang=en-GB",
		bytes.NewReader([]byte{0x62, 0x63, 0x61, 0x74, 0x00, 0xff}))
	req.Host = "bcat-list-lp1.cdn.nintendo.net"
	req.Header.Set("User-Agent", "libcurl (nnBcat; SDK 15.3.0)")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("the probe answered with a body: %q -- anything but empty is an "+
			"invented protocol shape", rec.Body.String())
	}

	out := logged.String()
	for _, want := range []string{
		"/api/nx/v1/list/JUEA-00210", // the path is the finding
		"lang=en-GB",                 // and so is the query
		"libcurl (nnBcat",            // the User-Agent names the sysmodule
		"POST",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the probe log does not mention %q\n%s", want, out)
		}
	}
	// A hexdump, not a quoted string: BCAT payloads are binary containers where
	// the offsets are the whole point.
	if !strings.Contains(out, "62 63 61 74") {
		t.Errorf("the body was not hexdumped\n%s", out)
	}
}

// TestTheBCATProbeOnlyClaimsTheBCATHosts keeps an experiment from quietly
// changing what every other stubbed host answers.
func TestTheBCATProbeOnlyClaimsTheBCATHosts(t *testing.T) {
	h := New(Config{BCATProbe: true}, slog.New(slog.DiscardHandler)).Handler()

	// The connection test is the one stub whose exact body the console checks;
	// if the probe ever swallowed it, every console would decide the network
	// was broken.
	rec := get(t, h, "ctest.cdn.nintendo.net", "/")
	if rec.Body.Len() == 0 {
		t.Error("the probe swallowed the connection test")
	}

	rec = get(t, h, "api-lp1.frs.srv.nintendo.net", "/")
	if got := strings.TrimSpace(rec.Body.String()); !strings.HasPrefix(got, "[") {
		t.Errorf("friend recommendations no longer answer a list: %q", got)
	}
}

// TestABCATBodyIsNotReadWithoutLimit stops a console -- or anything else that
// reaches the port -- making the server allocate without bound during an
// experiment the operator is not watching closely.
func TestABCATBodyIsNotReadWithoutLimit(t *testing.T) {
	var logged bytes.Buffer
	h := New(Config{BCATProbe: true},
		slog.New(slog.NewTextHandler(&logged, nil))).Handler()

	req := httptest.NewRequest("POST", "http://bcat-data-lp1.cdn.nintendo.net/x",
		bytes.NewReader(bytes.Repeat([]byte{0xAB}, maxProbeBody*4)))
	req.Host = "bcat-data-lp1.cdn.nintendo.net"
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !strings.Contains(logged.String(), "body_truncated=true") {
		t.Error("an oversized body was not reported as truncated")
	}
}
