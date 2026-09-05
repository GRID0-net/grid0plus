// Package stubs answers the Nintendo endpoints SwitchNet redirects but does not
// implement.
//
// Many console services treat a failed request as a fault rather than as "that
// feature is unavailable", and the failure surfaces somewhere unhelpful: a
// connection test that cannot reach its server makes the console believe it is
// offline, and a friend-recommendation call that 404s puts an error on the
// friends screen. Answering them with a well-formed empty result is what keeps
// the console quiet about things that are genuinely not part of this stack.
//
// Every stubbed request is logged. The log is the to-do list: an endpoint that
// turns up here repeatedly is one the operator may need to implement for real.
package stubs

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Config is what the handler needs to run.
type Config struct {
	// LogBodies logs request bodies. Useful exactly once, while working out what
	// an unimplemented endpoint is being asked for; far too chatty to leave on.
	LogBodies bool

	// BCATProbe answers the bcat-* hosts, logging each request in full, instead
	// of leaving them to the null route. An experiment, not a service --
	// see bcatprobe.go and TODO.md 2.4.
	BCATProbe bool
}

// Handler serves the stub endpoints.
type Handler struct {
	cfg Config
	log *slog.Logger
}

// New returns a stub handler.
func New(cfg Config, log *slog.Logger) *Handler {
	return &Handler{cfg: cfg, log: log}
}

// maxLoggedBody caps how much of a body is read for logging, so LogBodies
// cannot be used to make the server allocate without limit.
const maxLoggedBody = 8 << 10

// Handler returns the routed handler.
//
// Routing is by host, because one stub server sits behind several Nintendo
// hostnames and they need different answers. Anything unrecognised falls through
// to an empty JSON object, which is the safest thing to say to a client whose
// expectations are unknown.
func (h *Handler) Handler() http.Handler {
	mux := http.NewServeMux()

	// ---- connection test ----
	//
	// This is the one stub that must not be a JSON object. The console's
	// connection test looks for a specific body and the X-Organization header;
	// anything else and it decides the network is broken, which shows up as
	// "this feature is not available" rather than as a network error.
	//
	// Reference: https://github.com/kinnay/NintendoClients/wiki/Connection-Test
	mux.HandleFunc("/", h.route)

	return h.withLogging(mux)
}

func (h *Handler) route(w http.ResponseWriter, r *http.Request) {
	host := hostOnly(r.Host)
	path := r.URL.Path

	switch {
	case isConnectionTest(host, path):
		h.connectionTest(w)

	case h.cfg.BCATProbe && isBCATHost(host):
		// Only when the operator turned it on. Without the flag a BCAT host
		// should not be reaching this server at all -- it is null-routed on the
		// console -- and if one does, the default empty object below is a
		// safer answer than a deliberate 200.
		h.bcatProbe(w, r)

	case strings.Contains(host, ".frs.srv.nintendo.net"):
		// Friend recommendations. An empty list is the correct answer: there is
		// nobody to recommend on a private network.
		h.emptyList(w)

	case strings.Contains(host, ".five.nintendo.net"):
		// Online play invitations. Phase 1 does not implement them, so there are
		// no invitation groups to report.
		h.emptyList(w)

	case strings.Contains(host, ".op2.nintendo.net"):
		// Nintendo Switch Online membership verification. Every registered user
		// on a private network is entitled to play.
		h.membership(w)

	case strings.Contains(host, "service-status"):
		// Game server availability. Everything is up; if it were not, the
		// console finding out from here would not help.
		h.serviceStatus(w)

	case strings.Contains(host, ".dragons.nintendo.net"):
		// eLicenses. Most paths here are a fine empty object; one is not.
		// See h.dragons.
		h.dragons(w, path)

	default:
		h.emptyObject(w)
	}
}

// isConnectionTest matches the hosts and paths the console's connection test uses.
func isConnectionTest(host, path string) bool {
	return strings.Contains(host, "ctest") ||
		strings.Contains(host, "conntest") ||
		path == "/generate_204"
}

// connectionTest answers the console's connectivity check.
//
// The body and the X-Organization header are both load-bearing: the console
// checks that it reached Nintendo's server and not a captive portal.
func (h *Handler) connectionTest(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html")
	w.Header().Set("X-Organization", "Nintendo")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok")
}

func (h *Handler) emptyList(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, []any{})
}

func (h *Handler) emptyObject(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{})
}

// dragons answers the eLicense host by path, rather than one blanket empty
// object: NextendoNetwork/nx-dauth (a from-scratch dauth/aauth reimplementation
// tested against real hardware; PolyForm Shield-licensed, so only these facts
// are taken, never its code) documents that most of dragons' paths tolerate an
// empty object, but the eligibility check specifically does not.
func (h *Handler) dragons(w http.ResponseWriter, path string) {
	switch {
	case strings.HasSuffix(path, "/elicenses/exercise"):
		// "Exercising" a licence is the online-play eligibility check. A real
		// server answers 200 with a genuinely EMPTY body -- zero bytes, not an
		// empty JSON object. nx-dauth's own operational note is explicit about
		// what getting this wrong looks like: answered any other way (their
		// prior behaviour, and SwitchNet's before this), the path 404s and the
		// console concludes it has no right to play online -- the actual
		// blocker behind an "cannot use online features" message, not a
		// cosmetic mismatch.
		w.WriteHeader(http.StatusOK)

	case strings.HasSuffix(path, "/elicenses/migration_state"):
		// TODO(capture-needed): field names match nx-dauth's; not independently
		// confirmed against a Nintendo capture. Answered with the specific
		// fields this path looks for rather than an empty object with neither,
		// since "nothing to migrate" is a real, known answer and a safer
		// default than an absent field the console has to guess a zero value
		// for.
		writeJSON(w, http.StatusOK, map[string]any{
			"state": "COMPLETED", "migration_state": "NONE",
		})

	case strings.HasSuffix(path, "/elicense_archives/publish"),
		strings.HasSuffix(path, "/rights/publish_device_linked_elicenses"):
		// TODO(capture-needed): field name matches nx-dauth's for both paths;
		// not independently confirmed. An empty list of archives to publish,
		// under the key an archives-shaped response would actually carry,
		// rather than an object with no such key at all.
		writeJSON(w, http.StatusOK, map[string]any{"elicense_archives": []any{}})

	default:
		// Includes contents_authorization_token_for_aauth/issue and the
		// elicense_archives/<id>/report paths: SwitchNet's aauth accepts
		// whatever is in that field without looking at it, so an empty
		// success is enough to get past those specifically.
		h.emptyObject(w)
	}
}

// membership reports an active Nintendo Switch Online membership.
//
// TODO(capture-needed): the op2 response shape is undocumented. What is here is
// the minimum a "has a membership" answer would carry. If the console rejects
// it, the request and response are worth capturing — but note that with
// aauth.online_play_policy set to FREE (the default) the console never asks this
// at all, which is why it has not been a blocker.
func (h *Handler) membership(w http.ResponseWriter) {
	expires := time.Now().Add(365 * 24 * time.Hour)
	writeJSON(w, http.StatusOK, map[string]any{
		"active":    true,
		"expiresAt": expires.Unix(),
	})
}

// serviceStatus reports that no service is down and no maintenance is planned.
func (h *Handler) serviceStatus(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "normal",
		"maintenance": []any{},
		"incidents":   []any{},
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	buf, err := json.Marshal(body)
	if err != nil {
		http.Error(w, "{}", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(buf)))
	w.WriteHeader(status)
	_, _ = w.Write(buf)
}

func hostOnly(host string) string {
	if i := strings.IndexByte(host, ':'); i >= 0 {
		return host[:i]
	}
	return host
}

func (h *Handler) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attrs := []any{
			"host", hostOnly(r.Host),
			"method", r.Method,
			"path", r.URL.Path,
			"agent", r.Header.Get("User-Agent"),
		}
		if h.cfg.LogBodies && r.Body != nil {
			raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxLoggedBody))
			if err == nil && len(raw) > 0 {
				attrs = append(attrs, "body", string(raw))
			}
			// The body was consumed; the stub handlers do not read it.
			r.Body = http.NoBody
		}
		h.log.Info("stub request", attrs...)
		next.ServeHTTP(w, r)
	})
}
