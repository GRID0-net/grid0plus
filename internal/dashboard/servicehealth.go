package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/n-popescu/switchnet/config"
	"github.com/n-popescu/switchnet/internal/health"
)

// The service health view.
//
// Every SwitchNet service is its own process, and the usual way to find out one
// of them is down is that a console stops working in a way that names none of
// them. This asks each one directly.
//
// What it does NOT do is guess. A service that cannot be probed says so rather
// than being coloured green, because a page that shows green for "we did not
// look" is worse than one with a gap in it -- the operator stops checking.

// probeTimeout bounds one service probe.
//
// These are all on loopback or a private network. Anything slower than this is
// a service that is not answering, which is the finding.
const probeTimeout = 3 * time.Second

// ServiceHealth is one service's state, as the page renders it.
type ServiceHealth struct {
	Name string `json:"name"`
	// Addr is where it was probed, so a wrong port is visible rather than
	// showing up as "down".
	Addr string `json:"addr"`
	// Status is "ok", "degraded", "down", or "unknown".
	//
	// "unknown" is its own state and not a failure: some services have no way
	// to be probed from here, and calling that "down" would have the operator
	// chasing a service that is fine.
	Status string `json:"status"`
	// Detail explains anything that is not "ok".
	Detail string `json:"detail,omitempty"`
	// Kind is "http" or "none" -- how it was checked, so the page can say
	// what "unknown" actually means.
	Kind string `json:"kind"`
	// UptimeSeconds comes from the service's own /healthz when it has one.
	UptimeSeconds int `json:"uptimeSeconds,omitempty"`
	// Checks are the service's own dependency checks.
	Checks  map[string]health.CheckResult `json:"checks,omitempty"`
	TookMs  int                           `json:"tookMs"`
	Managed bool                          `json:"managed"`
}

// probe describes how to reach one service.
type probe struct {
	name string
	addr string
	kind string
	// why explains an "unknown" -- only set when kind is "none".
	why string
}

// probes works out what to check from the configuration.
//
// Addresses come from the same config the services listen on, so a port changed
// in one place cannot leave the page probing the old one. A ":8083"-style
// address is dialled on loopback: that is how a service reaches its neighbours
// when everything runs on one host, and how the compose network resolves it
// when they do not.
func probes(cfg ServiceAddresses) []probe {
	out := []probe{
		{name: "dauth", addr: cfg.DAuth, kind: "http"},
		{name: "aauth", addr: cfg.AAuth, kind: "http"},
		{name: "baas", addr: cfg.BAAS, kind: "http"},
		{name: "stubs", addr: cfg.Stubs, kind: "http"},
		{name: "npln", addr: cfg.NPLNAdmin, kind: "http"},
		{
			name: "natcheck", addr: cfg.NATCheck, kind: "none",
			// A real probe would have to speak Pia's NAT-check protocol and
			// answer from a second address. That is a genuine piece of work and
			// it is not done, so this says so instead of pretending.
			why: "no probe: answering it would mean speaking Pia's NAT-check protocol",
		},
	}

	filtered := out[:0]
	for _, p := range out {
		if p.addr != "" {
			filtered = append(filtered, p)
		}
	}
	return filtered
}

// ServiceAddresses is where each service listens, taken from the config.
type ServiceAddresses struct {
	DAuth     string
	AAuth     string
	BAAS      string
	Stubs     string
	NPLNAdmin string
	NATCheck  string
}

// AddressesFrom reads the listen addresses out of a full configuration.
func AddressesFrom(cfg config.Config) ServiceAddresses {
	return ServiceAddresses{
		DAuth:     cfg.DAuth.Listen,
		AAuth:     cfg.AAuth.Listen,
		BAAS:      cfg.BAAS.Listen,
		Stubs:     cfg.Stubs.Listen,
		NPLNAdmin: cfg.NPLN.AdminListen,
		NATCheck:  cfg.NATCheck.PrimaryListen,
	}
}

// handleHealth probes every service and reports what it found.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	list := probes(s.addresses)

	results := make([]ServiceHealth, len(list))
	var wg sync.WaitGroup

	// In parallel, because a page that waits for six sequential three-second
	// timeouts takes eighteen seconds to tell the operator everything is down.
	for i, p := range list {
		wg.Add(1)
		go func(i int, p probe) {
			defer wg.Done()
			results[i] = s.probeOne(r.Context(), p)
		}(i, p)
	}
	wg.Wait()

	// The dashboard itself, answered directly rather than over a socket to
	// itself: it is obviously up if this response is being written, and dialling
	// your own listener to prove it can deadlock a single-threaded server.
	results = append(results, ServiceHealth{
		Name: "dashboard", Addr: "(this process)", Status: "ok", Kind: "http",
	})

	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	writeJSON(w, map[string]any{"services": results})
}

func (s *Server) probeOne(ctx context.Context, p probe) ServiceHealth {
	started := time.Now()
	out := ServiceHealth{Name: p.name, Addr: p.addr, Kind: p.kind}

	switch p.kind {
	case "http":
		s.probeHTTP(ctx, p, &out)
	default:
		out.Status = "unknown"
		out.Detail = p.why
	}

	out.TookMs = int(time.Since(started).Milliseconds())
	return out
}

// probeHTTP asks a service's own /healthz.
func (s *Server) probeHTTP(ctx context.Context, p probe, out *ServiceHealth) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	url := "http://" + dialAddr(p.addr) + health.Path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		out.Status, out.Detail = "down", err.Error()
		return
	}

	resp, err := s.probeClient.Do(req)
	if err != nil {
		out.Status = "down"
		out.Detail = "not answering at " + dialAddr(p.addr)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	var report health.Report
	if err := json.Unmarshal(raw, &report); err != nil {
		// It answered, but not with health. Almost always an older build that
		// predates /healthz, and saying that beats "down" for a service that is
		// serving consoles perfectly well.
		out.Status = "unknown"
		out.Detail = fmt.Sprintf("answered %d but not with a health report; "+
			"probably a build from before /healthz existed", resp.StatusCode)
		return
	}

	out.Status = report.Status
	out.UptimeSeconds = report.UptimeSeconds
	out.Checks = report.Checks
	if report.Status != "ok" {
		var failed []string
		for name, check := range report.Checks {
			if !check.OK {
				failed = append(failed, name+": "+check.Error)
			}
		}
		sort.Strings(failed)
		out.Detail = strings.Join(failed, "; ")
	}
}

// dialAddr turns a listen address into one that can be dialled.
//
// ":8083" means "every interface" to a listener and nothing at all to a dialler,
// so it becomes loopback. That is right when the dashboard shares a host with
// the services; when it does not, the operator sets a real host in the address.
func dialAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	if strings.HasPrefix(addr, "0.0.0.0:") {
		return "127.0.0.1:" + strings.TrimPrefix(addr, "0.0.0.0:")
	}
	return addr
}
