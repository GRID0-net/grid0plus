package stubs

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	return newHandler(t, Config{})
}

func newHandler(t *testing.T, cfg Config) http.Handler {
	t.Helper()
	h, err := New(cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	return h.Handler()
}

// get sends a request with a Host header, which is how the stub server decides
// what to answer: one process sits behind several Nintendo hostnames.
func get(t *testing.T, h http.Handler, host, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "http://"+host+path, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestConnectionTestAnswer is the one stub whose exact body matters.
//
// The console's connectivity check looks for a specific body and the
// X-Organization header, and decides it is behind a captive portal otherwise.
// That surfaces as "this feature is not available" rather than as a network
// error, so a JSON object here would be a confusing failure.
func TestConnectionTestAnswer(t *testing.T) {
	h := newTestHandler(t)

	for _, host := range []string{
		"ctest.cdn.nintendo.net",
		"api.hac.lp1.ctest.srv.nintendo.net",
		"conntest.nintendowifi.net",
	} {
		rec := get(t, h, host, "/")
		if rec.Code != 200 {
			t.Errorf("%s: status = %d, want 200", host, rec.Code)
			continue
		}
		if got := rec.Header().Get("X-Organization"); got != "Nintendo" {
			t.Errorf("%s: X-Organization = %q, want Nintendo", host, got)
		}
		if body := rec.Body.String(); body != "ok" {
			t.Errorf("%s: body = %q, want \"ok\"", host, body)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: Content-Type = %q, want text/html", host, ct)
		}
	}
}

func TestFriendRecommendationsAndInvitationsReturnEmptyLists(t *testing.T) {
	h := newTestHandler(t)

	// A JSON object where the console expects a list is a decode failure, which
	// it reports as a fault on the friends screen. An empty list is the correct
	// answer: there is nobody to recommend on a private network.
	for _, host := range []string{
		"api-lp1.frs.srv.nintendo.net",
		"app.lp1.five.nintendo.net",
	} {
		rec := get(t, h, host, "/v1/anything")
		if rec.Code != 200 {
			t.Errorf("%s: status = %d, want 200", host, rec.Code)
			continue
		}
		var list []any
		if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
			t.Errorf("%s: body %q is not a JSON list: %v", host, rec.Body.String(), err)
			continue
		}
		if len(list) != 0 {
			t.Errorf("%s: list has %d entries, want 0", host, len(list))
		}
	}
}

func TestMembershipVerificationReportsAMembership(t *testing.T) {
	h := newTestHandler(t)

	rec := get(t, h, "capi.lp1.op2.nintendo.net", "/api/v1/verify")
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body["active"] != true {
		t.Errorf("active = %v, want true: every registered user on a private "+
			"network is entitled to play", body["active"])
	}
}

func TestServiceStatusReportsEverythingUp(t *testing.T) {
	h := newTestHandler(t)

	rec := get(t, h, "service-status-lp1.cdn.nintendo.net", "/")
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body["status"] != "normal" {
		t.Errorf("status = %v, want normal", body["status"])
	}
	// Empty lists, not null: a console decoding null into a list type fails.
	for _, key := range []string{"maintenance", "incidents"} {
		list, ok := body[key].([]any)
		if !ok {
			t.Errorf("%s = %v, want an empty list", key, body[key])
			continue
		}
		if len(list) != 0 {
			t.Errorf("%s has %d entries, want 0", key, len(list))
		}
	}
}

// TestElicenseExerciseAnswersWithATrulyEmptyBody covers dragons' one path
// that is not a safe empty object: "exercising" a licence is the online-play
// eligibility check, and answering it any other way 404s, which the console
// reads as having no right to play online at all.
func TestElicenseExerciseAnswersWithATrulyEmptyBody(t *testing.T) {
	h := newTestHandler(t)

	rec := get(t, h, "dragons.hac.lp1.dragons.nintendo.net", "/v2/elicenses/exercise")
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.Bytes(); len(got) != 0 {
		t.Errorf("body = %q, want zero bytes -- an empty JSON object here is the wrong shape of empty", got)
	}
}

func TestElicenseMigrationStateAndArchivePublishAnswerWithTheirOwnFields(t *testing.T) {
	h := newTestHandler(t)

	rec := get(t, h, "dragons.hac.lp1.dragons.nintendo.net", "/v2/elicenses/migration_state")
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var state map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if state["state"] != "COMPLETED" || state["migration_state"] != "NONE" {
		t.Errorf("migration_state body = %v, want state=COMPLETED migration_state=NONE", state)
	}

	for _, path := range []string{
		"/v2/elicense_archives/publish",
		"/v2/rights/publish_device_linked_elicenses",
	} {
		rec := get(t, h, "dragons.hac.lp1.dragons.nintendo.net", path)
		if rec.Code != 200 {
			t.Errorf("%s: status = %d, want 200", path, rec.Code)
			continue
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Errorf("%s: body is not JSON: %v", path, err)
			continue
		}
		archives, ok := body["elicense_archives"].([]any)
		if !ok {
			t.Errorf("%s: elicense_archives = %v, want an empty list", path, body["elicense_archives"])
			continue
		}
		if len(archives) != 0 {
			t.Errorf("%s: elicense_archives has %d entries, want 0", path, len(archives))
		}
	}
}

// TestOtherDragonsPathsStillGetAnEmptyObject covers the paths dragons
// deliberately leaves alone -- SwitchNet's aauth does not look at their
// content either way.
func TestOtherDragonsPathsStillGetAnEmptyObject(t *testing.T) {
	h := newTestHandler(t)

	for _, path := range []string{
		"/v2/contents_authorization_token_for_aauth/issue",
		"/v2/elicense_archives/some-id/report",
	} {
		rec := get(t, h, "dragons.hac.lp1.dragons.nintendo.net", path)
		if rec.Code != 200 {
			t.Errorf("%s: status = %d, want 200", path, rec.Code)
			continue
		}
		if got := rec.Body.String(); got != "{}" {
			t.Errorf("%s: body = %q, want {}", path, got)
		}
	}
}

func TestAnUnknownHostGetsAnEmptyJSONObject(t *testing.T) {
	h := newTestHandler(t)

	// A hostname that was redirected on the console but has no specific answer
	// here must still get well-formed JSON rather than a 404, because the
	// console treats a failure as a fault.
	rec := get(t, h, "something.unexpected.srv.nintendo.net", "/some/path")
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "{}" {
		t.Errorf("body = %q, want {}", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestHostMatchingIgnoresThePort(t *testing.T) {
	h := newTestHandler(t)

	// Behind nginx the Host header carries whatever the console sent, which may
	// include a port. Matching on the raw value would miss.
	rec := get(t, h, "ctest.cdn.nintendo.net:443", "/")
	if rec.Body.String() != "ok" {
		t.Errorf("body = %q with a port in the Host header, want the connection "+
			"test answer", rec.Body.String())
	}
}

func TestPostBodiesAreAcceptedAndNotRequired(t *testing.T) {
	h := newTestHandler(t)

	// Telemetry and reporting endpoints POST. They must not get a 405.
	req := httptest.NewRequest("POST", "http://api-lp1.znc.srv.nintendo.net/v3/Account/Login",
		strings.NewReader(`{"parameter":{}}`))
	req.Host = "api-lp1.znc.srv.nintendo.net"
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestLogBodiesDoesNotBreakTheResponse(t *testing.T) {
	// LogBodies consumes the request body. A handler that then tried to read it
	// would see nothing; the stub handlers do not, but that has to stay true.
	h := newHandler(t, Config{LogBodies: true})

	req := httptest.NewRequest("POST", "http://receive-lp1.er.srv.nintendo.net/report",
		strings.NewReader("a crash report"))
	req.Host = "receive-lp1.er.srv.nintendo.net"

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "{}" {
		t.Errorf("body = %q, want {}", body)
	}
}

// TestTheBCATProbeAnswersWithoutClaimingToKnowWhatBCATWants guards the handler
// TODO.md §2.4's experiment depends on.
//
// The hosts are null-routed, so nothing reaches this in a normal deployment.
// The point is that it works the moment they are flipped, because that
// experiment needs a console and the console-time is the scarce thing — not the
// server.
func TestTheBCATProbeAnswersWithoutClaimingToKnowWhatBCATWants(t *testing.T) {
	h := newTestHandler(t)

	for _, host := range []string{
		"bcat-list-lp1.cdn.nintendo.net",
		"bcat-data-lp1.cdn.nintendo.net",
		"bcat-topics-lp1.cdn.nintendo.net",
	} {
		rec := get(t, h, host, "/api/nx/v1/list/splatoon3")

		// 200, because an error tells the console the server is broken and it
		// may stop asking — which loses the only thing the probe collects.
		if rec.Code != http.StatusOK {
			t.Errorf("%s answered %d, want 200", host, rec.Code)
		}
		// Empty, because a fabricated container would be rejected for a reason
		// nobody can name and the experiment's result would mean nothing.
		if rec.Body.Len() != 0 {
			t.Errorf("%s answered %d bytes; the probe must not invent a container",
				host, rec.Body.Len())
		}
		// No content type: claiming one would be claiming to know what the
		// console expects, which is the open question.
		if got := rec.Header().Get("Content-Type"); got != "" {
			t.Errorf("%s claimed content type %q", host, got)
		}
	}
}

func TestTheBCATProbeWritesATranscriptWhenAskedTo(t *testing.T) {
	dir := t.TempDir()
	h := newHandler(t, Config{BCATRecordDir: dir})

	req := httptest.NewRequest("GET",
		"http://bcat-list-lp1.cdn.nintendo.net/api/nx/v1/list/splatoon3?lang=EUen", nil)
	req.Host = "bcat-list-lp1.cdn.nintendo.net"
	h.ServeHTTP(httptest.NewRecorder(), req)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read transcript dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("wrote %d transcripts, want 1", len(entries))
	}
	raw, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	// The path and query are the whole point: they are the first record anyone
	// has of what the console asks BCAT for.
	if !strings.Contains(string(raw), "/api/nx/v1/list/splatoon3?lang=EUen") {
		t.Errorf("the transcript does not carry the request line:\n%s", raw)
	}
}
