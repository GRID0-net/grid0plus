package nplnrest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestServer(now time.Time) *Server {
	return New(Options{Now: func() time.Time { return now }})
}

// request builds an httptest request with the given Host header -- the
// signal this package's single mux uses to tell vermillion, penne and the
// frontline hosts apart, since nginx routes all of them to one upstream.
func request(method, host, path, body string) *http.Request {
	r := httptest.NewRequest(method, "https://"+host+path, strings.NewReader(body))
	r.Host = host
	return r
}

func TestGetFrontlinesNamesAKnownFrontlineHost(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	s := newTestServer(now)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, request("GET", "val.hac.lp1.penne.srv.nintendo.net", "/v1/frontlines", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	var body struct {
		CurrentTime                      string                     `json:"current_time"`
		FrontlineFQDN                    string                     `json:"frontline_fqdn"`
		PersistentConnectionParamsSimple persistentConnectionParams `json:"persistent_connection_params_simple"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.FrontlineFQDN == "" || !strings.HasPrefix(body.FrontlineFQDN, "fro-") {
		t.Errorf("frontline_fqdn = %q, want a fro-N host", body.FrontlineFQDN)
	}
	if body.PersistentConnectionParamsSimple.WaitSec != 3600 {
		t.Errorf("wait_sec = %d, want the documented 3600", body.PersistentConnectionParamsSimple.WaitSec)
	}
	if !body.PersistentConnectionParamsSimple.IgnoreRst {
		t.Error("ignore_rst = false, want the documented true")
	}
}

func TestPostLoginTicketsIssuesATicketValidForFourDays(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	s := newTestServer(now)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, request("POST", "val.hac.lp1.penne.srv.nintendo.net", "/v1/login_tickets",
		`{"id":"penne-id-1","password":"whatever"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	var body struct {
		ExpiresAt     string `json:"expires_at"`
		IssuedAt      string `json:"issued_at"`
		Ticket        string `json:"ticket"`
		FrontlineFQDN string `json:"frontline_fqdn"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Ticket == "" {
		t.Error("no ticket issued")
	}
	if body.FrontlineFQDN == "" {
		t.Error("no frontline_fqdn in the login response")
	}
	expires, err := time.Parse(time.RFC3339, body.ExpiresAt)
	if err != nil {
		t.Fatalf("parse expires_at: %v", err)
	}
	if got := expires.Sub(now); got != loginTicketTTL {
		t.Errorf("ticket TTL = %s, want the documented 4 days", got)
	}
}

func TestPostLoginTicketsRejectsAMissingID(t *testing.T) {
	s := newTestServer(time.Now())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, request("POST", "val.hac.lp1.penne.srv.nintendo.net", "/v1/login_tickets",
		`{"password":"whatever"}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400 for a request with no id", rec.Code)
	}
}

func TestVermillionAnswersHonestlyEmpty(t *testing.T) {
	s := newTestServer(time.Now())

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, request("GET", "gw.hac.lp1.vermillion.srv.nintendo.net", "/v1/accounts/config", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("config status %d: %s", rec.Code, rec.Body)
	}
	if strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Errorf("config body = %q, want an empty object", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, request("GET", "gw.hac.lp1.vermillion.srv.nintendo.net", "/v1/accounts/devices", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("devices status %d: %s", rec.Code, rec.Body)
	}
	var devices struct {
		Devices []any `json:"devices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &devices); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(devices.Devices) != 0 {
		t.Errorf("devices = %v, want none", devices.Devices)
	}
}

// TestAnUnknownVermillionEndpointIs404 covers the endpoints this package
// deliberately does not implement (notify, nearness-checks, and so on):
// they must not silently succeed with an invented response.
func TestAnUnknownVermillionEndpointIs404(t *testing.T) {
	s := newTestServer(time.Now())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, request("POST", "gw.hac.lp1.vermillion.srv.nintendo.net", "/v1/accounts/notify", "{}"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404 for an endpoint this package does not implement", rec.Code)
	}
}

// TestFrontlineHoldsTheConnectionOpenUntilTheClientGoesAway is the one
// property internal/nintendo/hosts.go's own comment establishes from a
// real error-code capture: closing this connection is read as a failure,
// so the handler must not return until the request context ends.
func TestFrontlineHoldsTheConnectionOpenUntilTheClientGoesAway(t *testing.T) {
	s := newTestServer(time.Now())
	ctx, cancel := context.WithCancel(context.Background())

	r := request("GET", "fro-1.hac.lp1.penne.srv.nintendo.net", "/", "").WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		s.Handler().ServeHTTP(rec, r)
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("the frontline handler returned before its context was cancelled")
	case <-time.After(100 * time.Millisecond):
		// Still open, as expected.
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the frontline handler did not return after its context was cancelled")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status %d, want 200 once the connection is accepted", rec.Code)
	}
}

func TestUnknownHostIs404NotAnInventedResponse(t *testing.T) {
	s := newTestServer(time.Now())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, request("GET", "not-a-real-host.nintendo.net", "/v1/frontlines", ""))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404 for a host this package does not serve", rec.Code)
	}
}
