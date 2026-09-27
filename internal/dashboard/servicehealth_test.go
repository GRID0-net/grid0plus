package dashboard

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
)

// healthServer builds a dashboard whose probes point at the given handlers.
func healthServer(t *testing.T, addrs func(map[string]string) ServiceAddresses,
	handlers map[string]http.Handler) *Server {
	t.Helper()

	urls := map[string]string{}
	for name, h := range handlers {
		s := httptest.NewServer(h)
		t.Cleanup(s.Close)
		urls[name] = strings.TrimPrefix(s.URL, "http://")
	}

	srv, err := New(Options{
		Store:       db.NewMemory(),
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Tenant:      "dce9377b",
		PresenceTTL: 5 * time.Minute,
		Now:         time.Now,
		Addresses:   addrs(urls),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return srv
}

func healthOf(t *testing.T, srv *Server) map[string]ServiceHealth {
	t.Helper()

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Services []ServiceHealth `json:"services"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	out := map[string]ServiceHealth{}
	for _, s := range body.Services {
		out[s.Name] = s
	}
	return out
}

// TestAHealthyServiceIsUpAndADeadOneIsDown is the base case.
func TestAHealthyServiceIsUpAndADeadOneIsDown(t *testing.T) {
	healthy := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","service":"dauth","uptimeSeconds":42}`))
	})

	srv := healthServer(t, func(u map[string]string) ServiceAddresses {
		return ServiceAddresses{
			DAuth: u["dauth"],
			// A port nothing is listening on.
			AAuth: "127.0.0.1:1",
		}
	}, map[string]http.Handler{"dauth": healthy})

	got := healthOf(t, srv)

	if got["dauth"].Status != "ok" {
		t.Errorf("dauth = %q, want ok (%s)", got["dauth"].Status, got["dauth"].Detail)
	}
	if got["dauth"].UptimeSeconds != 42 {
		t.Errorf("uptime = %d, want 42", got["dauth"].UptimeSeconds)
	}
	if got["aauth"].Status != "down" {
		t.Errorf("aauth = %q, want down", got["aauth"].Status)
	}
	// Naming the address is what makes a wrong port distinguishable from a
	// dead service.
	if !strings.Contains(got["aauth"].Detail, "127.0.0.1:1") {
		t.Errorf("the detail does not name the address probed: %q", got["aauth"].Detail)
	}
}

// TestADegradedServiceCarriesTheDependencyThatFailed.
//
// "baas is degraded" is not actionable. "baas cannot reach the database" is,
// and the whole point of asking each service rather than just opening a socket
// is to get the second one.
func TestADegradedServiceCarriesTheDependencyThatFailed(t *testing.T) {
	degraded := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"degraded","service":"baas","checks":` +
			`{"database":{"ok":false,"error":"connection refused","tookMs":3}}}`))
	})

	srv := healthServer(t, func(u map[string]string) ServiceAddresses {
		return ServiceAddresses{BAAS: u["baas"]}
	}, map[string]http.Handler{"baas": degraded})

	got := healthOf(t, srv)["baas"]

	if got.Status != "degraded" {
		t.Fatalf("status = %q, want degraded", got.Status)
	}
	if !strings.Contains(got.Detail, "database") || !strings.Contains(got.Detail, "connection refused") {
		t.Errorf("the detail does not say what failed: %q", got.Detail)
	}
	if check, ok := got.Checks["database"]; !ok || check.OK {
		t.Errorf("the failing check did not survive: %+v", got.Checks)
	}
}

// TestAServiceThatCannotBeProbedIsUnknownNotDown.
//
// natcheck answers a UDP protocol nothing here speaks. Calling that "down"
// would have an operator chase a service that is fine, and calling it "ok"
// would be a green light for something never looked at.
func TestAServiceThatCannotBeProbedIsUnknownNotDown(t *testing.T) {
	srv := healthServer(t, func(map[string]string) ServiceAddresses {
		return ServiceAddresses{NATCheck: ":10025"}
	}, nil)

	got := healthOf(t, srv)["natcheck"]

	if got.Status != "unknown" {
		t.Errorf("status = %q, want unknown", got.Status)
	}
	if got.Detail == "" {
		t.Error("an unknown status with no explanation is just a shrug")
	}
}

// TestAnOldBuildWithoutHealthzIsUnknownNotDown.
//
// A service from before /healthz existed answers 404. It is serving consoles
// perfectly well, and reporting it as down would send the operator to restart
// something that is working.
func TestAnOldBuildWithoutHealthzIsUnknownNotDown(t *testing.T) {
	old := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})

	srv := healthServer(t, func(u map[string]string) ServiceAddresses {
		return ServiceAddresses{Stubs: u["stubs"]}
	}, map[string]http.Handler{"stubs": old})

	got := healthOf(t, srv)["stubs"]

	if got.Status != "unknown" {
		t.Errorf("status = %q, want unknown", got.Status)
	}
	if !strings.Contains(got.Detail, "healthz") {
		t.Errorf("the detail does not explain why: %q", got.Detail)
	}
}

// TestTheHealthViewAnswersEvenWhenTheDatabaseIsDown is the bug this was found
// with, and the one that matters most.
//
// The health page is what an operator opens BECAUSE something is broken. It
// must not need the database, because "the database is down" is the single most
// likely thing it is being used to diagnose.
func TestTheHealthViewAnswersEvenWhenTheDatabaseIsDown(t *testing.T) {
	srv := healthServer(t, func(map[string]string) ServiceAddresses {
		return ServiceAddresses{DAuth: "127.0.0.1:1"}
	}, nil)

	// A store that fails everything, standing in for a database that has gone.
	srv.store = brokenStore{}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("the health view failed with a broken database: %d %s",
			rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "dauth") {
		t.Errorf("no services were reported: %s", rec.Body.String())
	}
}

// TestEveryServiceIsProbedInParallel.
//
// Sequentially, six services timing out at three seconds each is eighteen
// seconds before the page says anything -- during an outage, which is when
// somebody is watching it.
func TestEveryServiceIsProbedInParallel(t *testing.T) {
	slow := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(400 * time.Millisecond)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	srv := healthServer(t, func(u map[string]string) ServiceAddresses {
		return ServiceAddresses{
			DAuth: u["a"], AAuth: u["b"], BAAS: u["c"], Stubs: u["d"],
		}
	}, map[string]http.Handler{"a": slow, "b": slow, "c": slow, "d": slow})

	started := time.Now()
	healthOf(t, srv)
	took := time.Since(started)

	// Four 400ms probes: ~400ms in parallel, ~1600ms sequentially.
	if took > time.Second {
		t.Errorf("four 400ms probes took %v; they are not running in parallel", took)
	}
}

// TestAListenAddressIsTurnedIntoOneThatCanBeDialled: ":8083" means every
// interface to a listener and nothing at all to a dialler.
func TestAListenAddressIsTurnedIntoOneThatCanBeDialled(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{":8083", "127.0.0.1:8083"},
		{"0.0.0.0:8083", "127.0.0.1:8083"},
		{"127.0.0.1:8083", "127.0.0.1:8083"},
		{"baas:8083", "baas:8083"},
	} {
		if got := dialAddr(tc.in); got != tc.want {
			t.Errorf("dialAddr(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// brokenStore stands in for a database that has gone away. It embeds a Store
// only to satisfy the interface; the health view must never call it, which is
// what the test above proves -- any method reached here would nil-panic.
type brokenStore struct{ db.Store }
