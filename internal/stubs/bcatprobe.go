package stubs

import (
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
)

// The BCAT probe: an instrument, not a service.
//
// SwitchNet cannot serve BCAT. Nintendo's delivery container carries an
// RSA-2048 signature, and whether the console VERIFIES it is unknown --
// switchbrew documents the field and says nothing about a verification step or
// what happens on failure. A console running SwitchNet already has sigpatches,
// so it is entirely possible the check is bypassed and BCAT could be served
// directly. Nobody has established either way, and the `list` response body is
// undocumented regardless.
//
// This answers the FIRST half of that: does the console reach us at all, and
// what does it ask for? It logs every request in full and answers 200 with an
// empty body. The log is the first record anyone has of what Splatoon 3
// actually requests from BCAT.
//
// It is off by default and must stay that way. Answering 200-empty to a console
// that expected content turns an immediate, legible failure into a hang, which
// is exactly why the three bcat-* hosts are null-routed in the first place. See
// TODO.md §2.4 for the experiment this exists to make cheap, and §5.2 for why
// the null route is contingent rather than a rule.

// maxProbeBody caps how much of a request body is hexdumped.
//
// Larger than maxLoggedBody because this is a deliberate one-off investigation
// rather than routine logging, but still bounded: a console that uploads
// something large must not be able to make the server allocate without limit.
const maxProbeBody = 64 << 10

// isBCATHost reports whether a request arrived on one of the BCAT hosts.
func isBCATHost(host string) bool {
	return strings.HasPrefix(host, "bcat-")
}

// bcatProbe logs one BCAT request in full and answers 200 with an empty body.
//
// The empty 200 is deliberate and is NOT a guess at the protocol. It is the
// least-committal answer that still proves the connection completed: anything
// with a body would be an invented shape, which is what CLAUDE.md's rule about
// not fabricating Nintendo internals forbids. What the console does NEXT --
// retry, give up, report an error code -- is itself the finding.
func (h *Handler) bcatProbe(w http.ResponseWriter, r *http.Request) {
	body, truncated := readProbeBody(r)

	attrs := []any{
		"method", r.Method,
		"host", hostOnly(r.Host),
		"path", r.URL.Path,
		"query", r.URL.RawQuery,
		"headers", headerLines(r.Header),
		"body_bytes", len(body),
	}
	if truncated {
		attrs = append(attrs, "body_truncated", true)
	}
	if len(body) > 0 {
		// A hexdump rather than a string: BCAT payloads are binary containers,
		// and a %q of one is unreadable where the offsets are the whole point.
		attrs = append(attrs, "body_hexdump", "\n"+hex.Dump(body))
	}

	// Warn rather than Info so it stands out in a log the operator is watching
	// live. This only ever fires during a deliberate experiment.
	h.log.Warn("bcat probe: console reached BCAT", attrs...)

	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
}

func readProbeBody(r *http.Request) (body []byte, truncated bool) {
	if r.Body == nil {
		return nil, false
	}
	defer func() { _ = r.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(r.Body, maxProbeBody+1))
	if err != nil {
		return body, false
	}
	if len(body) > maxProbeBody {
		return body[:maxProbeBody], true
	}
	return body, false
}

// headerLines renders the request headers in a stable order.
//
// Sorted because two runs of the same experiment have to be diffable, and Go's
// header map ranges in a different order each time.
func headerLines(hdr http.Header) string {
	names := make([]string, 0, len(hdr))
	for name := range hdr {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	for _, name := range names {
		for _, v := range hdr[name] {
			b.WriteString("\n  ")
			b.WriteString(name)
			b.WriteString(": ")
			b.WriteString(v)
		}
	}
	return b.String()
}

// LogBCATProbeState says at start-up when the probe is on, and nothing when it
// is off.
//
// Loud in one direction only. On, it is a deliberate experiment and the
// operator wants confirmation the flag took, plus the reminder that the flag
// alone does nothing. Off is the normal state of every other run, and a line
// saying so on every start-up is noise nobody reads.
func LogBCATProbeState(log *slog.Logger, enabled bool) {
	if !enabled {
		return
	}
	log.Warn("bcat probe is ON: BCAT requests will be logged in full and answered " +
		"200 with an empty body. This is an experiment (TODO.md 2.4), not a service. " +
		"It only takes effect if the console's hosts file also points the bcat-* " +
		"hosts here -- generate one with 'switchnetctl hosts -bcat-probe'.")
}
