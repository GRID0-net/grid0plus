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

// TestApplicationPolicyAnswerMatchesTheExpectedBody is the regression test for
// a host that used to fall through to the catch-all emptyObject ("{}") when a
// console expects application_policy to answer with a specific non-empty body.
func TestApplicationPolicyAnswerMatchesBothWorkingCaptures(t *testing.T) {
	h := newTestHandler(t)

	rec := get(t, h, "policy.hac.lp1.scsi.srv.nintendo.net",
		"/api/console/v2/application_policy/0100c2500fc20000/00042")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if want := "{\"policy_type\":\"ALL_OK\"}\n"; rec.Body.String() != want {
		t.Errorf("body = %q, want %q", rec.Body.String(), want)
	}
	if got := rec.Header().Get("Content-Length"); got != "25" {
		t.Errorf("Content-Length = %q, want \"25\" -- both captures agree on this exact byte count", got)
	}
}

// TestTelemetrySinkReadsAndAnswers200 is the regression test for a real hang:
// the NSO/friends applet (0100000000000816) waits synchronously for an HTTP
// response from these two hosts before loading its own main module, and
// null-routed it never gets there at all. The fix reads the body and answers 200;
// nothing about the content is stored or forwarded to real Nintendo.
func TestTelemetrySinkReadsAndAnswers200(t *testing.T) {
	h := newTestHandler(t)

	for _, host := range []string{
		"receive-lp1.dg.srv.nintendo.net",
		"receive-lp1.er.srv.nintendo.net",
	} {
		req := httptest.NewRequest("POST", "http://"+host+"/report", strings.NewReader(`{"crash":"data"}`))
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200 -- this is what hangs the NSO applet on real hardware", host, rec.Code)
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

	if rec := get(t, h, "dragons.hac.lp1.dragons.nintendo.net", "/v2/elicense_archives/publish"); rec.Code != http.StatusNotFound {
		t.Errorf("elicense_archives/publish: status = %d, want 404", rec.Code)
	}
	for _, path := range []string{
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
// deliberately leaves alone -- GRID0+'s aauth does not look at their
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
	//
	// Uses the generic empty-object fallback host, not a telemetry host:
	// receive-*.dg/er.srv.nintendo.net now goes through h.telemetrySink,
	// which answers a bare 200 with no body by design (see its own doc
	// comment) -- a different, still-correct response shape this test is
	// not about.
	h := newHandler(t, Config{LogBodies: true})

	req := httptest.NewRequest("POST", "http://unknown-host.nintendo.net/report",
		strings.NewReader("a crash report"))
	req.Host = "unknown-host.nintendo.net"

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

// TestTheBCATProbeAnswersWithoutClaimingToKnowWhatBCATWants guards the
// probe itself, reached directly (h.bcat), not through routing: bcat-list-lp1
// and bcat-data-lp1 answer 304 like bcat-topics-lp1 in normal operation
// (TestBCATHostsAnswer304NotModified), and the probe is something an operator
// swaps in deliberately. See newsTopics's own doc comment.
func TestTheBCATProbeAnswersWithoutClaimingToKnowWhatBCATWants(t *testing.T) {
	h, err := New(Config{}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	req := httptest.NewRequest("GET", "http://bcat-list-lp1.cdn.nintendo.net/api/nx/v1/list/splatoon3", nil)
	req.Host = "bcat-list-lp1.cdn.nintendo.net"
	rec := httptest.NewRecorder()
	h.bcat.ServeHTTP(rec, req)

	// 200, because an error tells the console the server is broken and it
	// may stop asking — which loses the only thing the probe collects.
	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rec.Code)
	}
	// Empty, because a fabricated container would be rejected for a reason
	// nobody can name and the experiment's result would mean nothing.
	if rec.Body.Len() != 0 {
		t.Errorf("answered %d bytes; the probe must not invent a container", rec.Body.Len())
	}
	// No content type: claiming one would be claiming to know what the
	// console expects, which is the open question.
	if got := rec.Header().Get("Content-Type"); got != "" {
		t.Errorf("claimed content type %q", got)
	}
}

// TestBCATHostsAnswer304NotModified is the regression test for a hard failure:
// leaving bcat-list-lp1 unreachable makes Splatoon 3 raise 2122-2403 (module
// 122 is bcat). All three bcat-* hosts answer the same unconditional 304 in
// normal routing. See newsTopics's own doc comment.
func TestBCATHostsAnswer304NotModified(t *testing.T) {
	h := newTestHandler(t)

	for _, host := range []string{
		"bcat-list-lp1.cdn.nintendo.net",
		"bcat-data-lp1.cdn.nintendo.net",
		"bcat-topics-lp1.cdn.nintendo.net",
	} {
		rec := get(t, h, host, "/api/nx/v1/list/splatoon3")
		if rec.Code != http.StatusNotModified {
			t.Errorf("%s answered %d, want %d -- this is what raised 2122-2403 on real hardware",
				host, rec.Code, http.StatusNotModified)
		}
		if rec.Body.Len() != 0 {
			t.Errorf("%s answered %d bytes, want 0 -- there is no data to invent", host, rec.Body.Len())
		}
	}
}

// TestNewsTopicsAnswers304NotModified is the regression test for a real
// crash: with bcat-topics-lp1.cdn.nintendo.net null-routed, the console's
// very first request of its own boot sequence failed outright, and "a
// communication error has occurred" appeared instantly on opening the hall.
// The real server answers 304 Not Modified with an empty body unconditionally,
// with no conditional-GET header in the request to justify it in the ordinary
// HTTP sense: it is the endpoint's own permanent "nothing new" answer. See
// newsTopics's own doc comment.
func TestNewsTopicsAnswers304NotModified(t *testing.T) {
	h := newTestHandler(t)

	rec := get(t, h, "bcat-topics-lp1.cdn.nintendo.net",
		"/api/nx/v1/titles/0100c2500fc20000/topics")

	if rec.Code != http.StatusNotModified {
		t.Errorf("code = %d, want %d -- this is what crashes a real console", rec.Code, http.StatusNotModified)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body has %d bytes, want 0 -- there is no news content to invent", rec.Body.Len())
	}
}

func TestTheBCATProbeWritesATranscriptWhenAskedTo(t *testing.T) {
	dir := t.TempDir()
	h, err := New(Config{BCATRecordDir: dir}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	req := httptest.NewRequest("GET",
		"http://bcat-list-lp1.cdn.nintendo.net/api/nx/v1/list/splatoon3?lang=EUen", nil)
	req.Host = "bcat-list-lp1.cdn.nintendo.net"
	h.bcat.ServeHTTP(httptest.NewRecorder(), req)

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

func TestOp2MembershipsIsTheItemsEnvelopeTheAccountParserReads(t *testing.T) {
	h := newTestHandler(t)
	const naID = "ea4b6873f0397ab3"

	rec := get(t, h, "capi.lp1.op2.nintendo.net", "/v1/users/"+naID+"/memberships")
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, rec.Body.String())
	}
	contexts := map[string]bool{}
	for _, it := range body.Items {
		if it["user_id"] != naID {
			t.Errorf("user_id = %v, want the id from the URL %q", it["user_id"], naID)
		}
		m, _ := it["membership"].(map[string]any)
		if m["active"] != true {
			t.Errorf("membership.active = %#v, want boolean true", m["active"])
		}
		ctx, _ := it["context"].(string)
		contexts[ctx] = true
	}
	if !contexts["default"] || !contexts["ex"] || len(body.Items) != 2 {
		t.Errorf("items = %s, want exactly one \"default\" and one \"ex\"", rec.Body.String())
	}

	// A malformed id is not this endpoint: the catch-all answer, not an
	// items envelope naming an id the console never asked about.
	if rec := get(t, h, "capi.lp1.op2.nintendo.net", "/v1/users/not-hex/memberships"); strings.Contains(rec.Body.String(), "items") {
		t.Errorf("malformed id got an items envelope: %s", rec.Body.String())
	}
}

// TestBCATRequestsAreRecordedWhileStillAnswered304: the recorder runs on every
// boot when bcat_record_dir is set, and recording must not change the answer.
// Anything but 304 risks the 2122-2403 hard error.
func TestBCATRequestsAreRecordedWhileStillAnswered304(t *testing.T) {
	dir := t.TempDir()
	h := newHandler(t, Config{BCATRecordDir: dir})
	rec := get(t, h, "bcat-list-lp1.cdn.nintendo.net", "/api/nx/v1/list/splatoon3")
	if rec.Code != http.StatusNotModified {
		t.Errorf("code = %d, want 304 even while recording", rec.Code)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no transcript written (%v)", err)
	}
}

// A 2xx here is nim's "this firmware is vulnerable": the console then shows
// "A system update is required" in front of account linking.
func TestEveryFirmwareVersionIsReportedSafe(t *testing.T) {
	h := newTestHandler(t)
	for _, v := range []string{"/22-5-0", "/23-0-0", "/1-0-0"} {
		if rec := get(t, h, "bvc-hac-lp1.cdn.nintendo.net", v); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", v, rec.Code)
		}
	}
}
