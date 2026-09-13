// Package health answers "is this service working", for the dashboard and for
// anything else watching.
//
// Every HTTP service gets /healthz for free: internal/service.ServeHTTP mounts
// it ahead of the service's own router, so there is one place it can be added
// and no service can be missed. The path is not a Nintendo one, so it never
// collides with something a console asks for.
//
// A check is a real question, not a flag. "The process is up" is already
// answered by the connection succeeding; what the operator needs to know is
// whether the things this service DEPENDS ON are reachable — a baas that cannot
// see PostgreSQL is running and useless, and it is the second of those that
// wastes an afternoon.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Path is where every service answers.
const Path = "/healthz"

// checkTimeout bounds one dependency check.
//
// Short on purpose. A health endpoint that hangs because the thing it is
// checking hangs is worse than one that says "the database did not answer in
// two seconds", which is the actual finding.
const checkTimeout = 2 * time.Second

// Check reports whether one dependency is usable. A nil error means it is.
type Check func(context.Context) error

// Handler answers health for one service.
type Handler struct {
	service string
	started time.Time

	mu     sync.RWMutex
	checks map[string]Check
}

// New builds a health handler for a named service.
func New(service string) *Handler {
	return &Handler{
		service: service,
		started: time.Now(),
		checks:  map[string]Check{},
	}
}

// Add registers a dependency check. Safe to call before serving.
func (h *Handler) Add(name string, check Check) *Handler {
	if h == nil || check == nil {
		return h
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checks[name] = check
	return h
}

// Report is what /healthz answers.
//
// Deliberately small, because this endpoint sits behind the same nginx that
// faces the consoles. It says whether the service works and what is wrong if
// not; it does not describe the deployment.
type Report struct {
	// Status is "ok" when every check passed, "degraded" otherwise.
	//
	// Two values, not three. A service whose database is unreachable is not
	// "warning" — it cannot do its job, and a page that shows amber for that
	// teaches the operator that amber is fine.
	Status  string `json:"status"`
	Service string `json:"service"`
	// UptimeSeconds is how long this process has been running. The cheapest way
	// to notice a service that is crash-looping: it is up, and it is always
	// young.
	UptimeSeconds int                    `json:"uptimeSeconds"`
	Checks        map[string]CheckResult `json:"checks,omitempty"`
}

// CheckResult is one dependency's answer.
type CheckResult struct {
	OK bool `json:"ok"`
	// Error is the failure, verbatim. It is the whole diagnosis, and replacing
	// it with "failed" would send the operator to a log to find out what this
	// already knew.
	Error string `json:"error,omitempty"`
	// TookMs is how long the check took. A database answering in 1900ms is
	// about to become a database that does not answer.
	TookMs int `json:"tookMs"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	report := h.Report(r.Context())

	w.Header().Set("Content-Type", "application/json")
	// A degraded service answers 503 so anything watching status codes rather
	// than bodies -- a load balancer, a uptime check -- agrees with the page.
	if report.Status != "ok" {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(report)
}

// Report runs every check and summarises them.
func (h *Handler) Report(ctx context.Context) Report {
	h.mu.RLock()
	names := make([]string, 0, len(h.checks))
	for name := range h.checks {
		names = append(names, name)
	}
	checks := make(map[string]Check, len(h.checks))
	for name, check := range h.checks {
		checks[name] = check
	}
	h.mu.RUnlock()

	sort.Strings(names)

	report := Report{
		Status:        "ok",
		Service:       h.service,
		UptimeSeconds: int(time.Since(h.started).Seconds()),
	}
	if len(names) > 0 {
		report.Checks = make(map[string]CheckResult, len(names))
	}

	for _, name := range names {
		started := time.Now()

		// Each check gets its own deadline rather than sharing one, so a slow
		// first check cannot make a healthy second one look timed out.
		checkCtx, cancel := context.WithTimeout(ctx, checkTimeout)
		err := checks[name](checkCtx)
		cancel()

		result := CheckResult{OK: err == nil, TookMs: int(time.Since(started).Milliseconds())}
		if err != nil {
			result.Error = err.Error()
			report.Status = "degraded"
		}
		report.Checks[name] = result
	}

	return report
}
