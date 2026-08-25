package stubs

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	return New(Config{}, slog.New(slog.DiscardHandler)).Handler()
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
	h := New(Config{LogBodies: true}, slog.New(slog.DiscardHandler)).Handler()

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
