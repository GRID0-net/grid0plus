// Package stubs answers the Nintendo endpoints GRID0+ redirects but does not
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
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/n-popescu/grid0plus/internal/bcat"
)

// Config is what the handler needs to run.
type Config struct {
	// LogBodies logs request bodies. Useful exactly once, while working out what
	// an unimplemented endpoint is being asked for; far too chatty to leave on.
	LogBodies bool

	// BCATRecordDir is where every bcat-* request is recorded, hexdumped,
	// while still being answered 304 -- TODO.md §2.4 stage 1. Empty records
	// nothing.
	BCATRecordDir string
	// BCATSeedDir contains per-title native delivery-cache files.
	BCATSeedDir string
}

// Handler serves the stub endpoints.
type Handler struct {
	cfg  Config
	log  *slog.Logger
	bcat *bcat.Probe
}

// New returns a stub handler.
func New(cfg Config, log *slog.Logger) (*Handler, error) {
	probe, err := bcat.NewProbe(log, cfg.BCATRecordDir, nil)
	if err != nil {
		return nil, err
	}
	return &Handler{cfg: cfg, log: log, bcat: probe}, nil
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
		//
		// A 2026-09-20 decompile of nim.nso confirms this host's real error-code
		// convention (literal strings "500/op2_error", "503/op2_maintenance"),
		// and a genuinely different membership-adjacent path this handler does
		// not distinguish: "/v1/my/membership/free_trial_availability". Every
		// path on this host answers the same membership() response regardless,
		// which may be the wrong shape specifically for that one -- a free-trial
		// eligibility check is a different question from "is there an active
		// membership" and this server has never seen either one asked for.
		h.membership(w, path)

	case strings.HasPrefix(host, "bvc-"):
		// The vulnerable-version list. nim reads any 2xx for
		// /<major>-<minor>-<micro> as "this firmware must update" and 404 as
		// safe (FW 22.5.0 nim, the 0x39d889 branch of the SafeSystemVersion
		// request), so the default empty object would put the update wall
		// back. A GRID0+ console must never be told to update.
		http.NotFound(w, r)

	case strings.Contains(host, "service-status"):
		// Game server availability. Everything is up; if it were not, the
		// console finding out from here would not help.
		h.serviceStatus(w)

	case strings.HasPrefix(host, "receive-"):
		// Telemetry (play/error reports). See h.telemetrySink.
		h.telemetrySink(w, r)

	case strings.HasPrefix(host, "bcat-topics"):
		// The System news feed -- confirmed shape, not the generic BCAT
		// probe (case below). Checked first: bcat-topics also matches the
		// "bcat-" prefix. See h.newsTopics.
		h.newsTopics(w, r)

	case strings.HasPrefix(host, "bcat-"):
		// The list/data hosts serve only operator-provided title files.
		// Otherwise they retain the proven 304 response: unreachable BCAT
		// caused Splatoon 3's 2122-2403 on real hardware. The probe still
		// records requests when configured.
		h.nativeBCAT(w, r, host)

	case strings.HasPrefix(host, "policy.") && strings.Contains(host, ".scsi.srv.nintendo.net"):
		// GET /api/console/v2/application_policy/<title>/<value>, called by
		// Splatoon 3 right before it would attempt a cloud save upload
		// (storage.hac.lp1.scsi's own save_data_archives/start_upload).
		// Both preserved working Nextendo hall captures answer this exact
		// body, byte for byte, decoded directly from their raw flow files
		// (facts only, not Nextendo's code): {"policy_type":"ALL_OK"}\n.
		// This host previously fell through to emptyObject's bare "{}",
		// which is not the shape either capture ever sends.
		h.applicationPolicy(w)

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

// telemetrySink answers receive-*.dg/er.srv.nintendo.net (play and error
// reports): read the body, discard it, answer 200 immediately.
//
// NextendoNetwork/Prelude's own commit history (facts only, not its code)
// documents a real hardware measurement: the NSO/friends applet
// (0100000000000816) waits SYNCHRONOUSLY for an HTTP response from these
// two hosts before it loads its own main module (lp1.nso). Null-routed,
// the connection hangs until timeout and the applet gives up without ever
// reaching lp1.nso -- no profile icon handling, plausibly more of the
// applet than that, never initialises. This is not the "answer whatever is
// safe" reasoning most of this file's other stubs use; it is a specific,
// documented synchronous dependency. Telemetry still never reaches real
// Nintendo -- discarding it here, not forwarding it, is the whole point --
// only the wait the applet blocks on is satisfied.
func (h *Handler) telemetrySink(w http.ResponseWriter, r *http.Request) {
	// The whole body, not capped: a partially-drained request body can
	// leave an HTTP/1.1 keep-alive connection in a state the next request
	// on it cannot recover from. These are trusted consoles on the
	// operator's own network, not a reason to bound the read.
	_, _ = io.Copy(io.Discard, r.Body)
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) emptyList(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, []any{})
}

func (h *Handler) emptyObject(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{})
}

// applicationPolicyBody is the exact bytes both preserved working Nextendo
// hall captures answer policy.hac.lp1.scsi.srv.nintendo.net's
// application_policy request with -- decoded directly from their raw flow
// files, trailing newline included, not passed through writeJSON's own
// json.Marshal (which would omit it and produce a 24-byte body a real
// console has never been observed to receive).
var applicationPolicyBody = []byte(`{"policy_type":"ALL_OK"}` + "\n")

func (h *Handler) applicationPolicy(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(applicationPolicyBody)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(applicationPolicyBody)
}

// newsTopics answers every bcat-* host (list, data and topics) with an
// unconditional 304 Not Modified, empty body.
//
// Named for its first confirmed case -- GET /api/nx/v1/titles/<id>/topics,
// the System news feed a title polls right at the start of its own boot
// sequence -- but not specific to it any more. A real capture against
// Nextendo (facts only, not its code) on 2026-09-24 --
// `docs/CAPTURES/raw/nextendo-second-20260923T210513Z` -- caught the topics
// exchange in full: the console's request carries no conditional-GET
// header at all (no If-None-Match, no If-Modified-Since), just an
// X-Nintendo-DenebEdgeToken it expects nobody to verify (CLAUDE.md is
// explicit that a Nintendo-issued credential is accepted without being
// looked at, never forged or checked), and the real server's answer is 304
// with an empty body regardless. This is not conditional-GET cache
// validation in the ordinary HTTP sense -- there is nothing here to
// validate against -- it is the endpoint's own permanent "nothing new"
// answer, confirmed on the wire, not inferred.
//
// NextendoNetwork/Prelude's own commit history (facts only, not its code)
// independently confirms the same 304-always shape for bcat-list-lp1 and
// bcat-data-lp1 too, and documents why it matters even without content:
// Splatoon 3 raised a hard error, 2122-2403 (module 122 = bcat), when
// bcat-list-lp1 was left unreachable instead. "Your cache is current" is
// not the same as "here is your data" -- a player with nothing in their
// BCAT cache still gets nothing -- it only stops that absence from being a
// hard failure the game surfaces to the player.
//
// Reproduced on GRID0+'s own hardware on 2026-09-24 for bcat-topics-lp1
// specifically: null-routed (ServiceBlackhole), the console's very first
// request of its own boot sequence failed outright, and "a communication
// error has occurred" appeared instantly on opening the hall -- before
// IssuePrearrangedUserToken even fires. Answering 304 costs nothing to get
// right: there is no body to invent, and GRID0+ has no real BCAT or news
// content of its own to serve regardless.
func (h *Handler) newsTopics(w http.ResponseWriter, r *http.Request) {
	if h.cfg.BCATRecordDir != "" {
		h.bcat.Observe(r, http.StatusNotModified, "bcat: answered 304; see internal/stubs newsTopics")
	} else {
		h.log.Info("stubs: bcat host answered 304", "host", hostOnly(r.Host), "path", r.URL.Path)
	}
	w.WriteHeader(http.StatusNotModified)
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
		// prior behaviour, and GRID0+'s before this), the path 404s and the
		// console concludes it has no right to play online -- the actual
		// blocker behind an "cannot use online features" message, not a
		// cosmetic mismatch.
		w.WriteHeader(http.StatusOK)

	case strings.HasSuffix(path, "/elicenses/migration_state"):
		// The path and the "migration_state" field name are now independently
		// confirmed, not just matched against nx-dauth: a 2026-09-20 decompile
		// of the operator's own console's nim sysmodule found the literal path
		// "/{version}/elicenses/migration_state" and the JSONPath
		// "$.migration_state" the client reads out of the response. Still
		// unconfirmed: whether "COMPLETED"/"NONE" are the right values as
		// opposed to some other real state the console also accepts -- nim's
		// binary has the field name, not its enum of valid values.
		// TODO(capture-needed): the real "state"/"migration_state" values.
		writeJSON(w, http.StatusOK, map[string]any{
			"state": "COMPLETED", "migration_state": "NONE",
		})

	case strings.HasSuffix(path, "/elicense_archives/publish"),
		strings.HasSuffix(path, "/rights/publish_device_linked_elicenses"):
		// Same confirmation as migration_state above: the same nim decompile
		// found the literal path "/{version}/elicense_archives/publish" and
		// the "elicense_archives" key name is used elsewhere in the same
		// binary for a real elicense-archive-shaped response, not just
		// pattern-matched from nx-dauth. An empty list is still the right
		// answer regardless -- GRID0+ has nothing to publish -- so nothing
		// behavioral changes here.
		//
		// The same decompile also surfaced a much larger elicenses/rights
		// schema this server does not and should not implement: full
		// available_elicenses[]/elicenses[] field shapes, revoke/extend/prune
		// paths, a nonce-signed elicenses request body. That is Phase 3's
		// territory -- CLAUDE.md is explicit that "rights and content-delivery
		// layers" belong to the separate, not-yet-started eShop module, not
		// here. Knowing the shape now does not change the scope boundary;
		// resist the temptation to build it just because it is now knowable.
		writeJSON(w, http.StatusOK, map[string]any{"elicense_archives": []any{}})

	default:
		// Includes contents_authorization_token_for_aauth/issue and the
		// elicense_archives/<id>/report paths: GRID0+'s aauth accepts
		// whatever is in that field without looking at it, so an empty
		// success is enough to get past those specifically.
		//
		// Also includes PUT /v2/penne_id -- confirmed real (not previously
		// documented anywhere in this project) by NextendoNetwork's own
		// public Splatoon 3 NPLN server, which found a real console PUTting
		// its penne id here as part of NPLN's vermillion/penne bootstrap,
		// before the console will attempt a penne login (see
		// internal/nplnrest.go's package doc comment for the boundary on
		// what is taken from their PolyForm-Shield-licensed project: facts,
		// never code). This empty-object default already matches their own,
		// independently-arrived-at answer for this exact path.
		h.emptyObject(w)
	}
}

// membership reports an active Nintendo Switch Online membership.
//
// GET /v1/users/<na id>/memberships has a known shape, read out of the
// account sysmodule's own parser (fw 22.5.0: FUN_7100113d90 roots it at
// $.items[0], FUN_7100113ed0 names the fields, FUN_7100113c00/FUN_7100114070
// type them, FUN_7100113f70/FUN_7100113e10 judge the result):
//
//   - each item needs all of user_id, context and membership.active, or the
//     whole answer is 0x18647c (2124-3122, "no membership");
//   - user_id is exactly 16 hex digits and must equal the id in the URL;
//   - context "default" is the base membership and "ex" the Expansion Pack,
//     and an answer is only accepted when it has one item of each;
//   - an active "ex" with an inactive "default" is its own error, 0x18667c.
//
// The old catch-all {"active": true, "expiresAt": ...} had no items at all,
// so every membership check the console made failed -- it retried each one
// four times in a row, right before the friends applet reported an error.
//
// Every other op2 path keeps the old answer.
// TODO(capture-needed): what those are asked, and whether any is.
func (h *Handler) membership(w http.ResponseWriter, path string) {
	if naID, ok := membershipsPathUser(path); ok {
		item := func(context string) map[string]any {
			return map[string]any{
				"user_id":    naID,
				"context":    context,
				"membership": map[string]any{"active": true},
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"items": []any{item("default"), item("ex")},
		})
		return
	}

	expires := time.Now().Add(365 * 24 * time.Hour)
	writeJSON(w, http.StatusOK, map[string]any{
		"active":    true,
		"expiresAt": expires.Unix(),
	})
}

// membershipsPathUser returns the Nintendo Account id from
// /v1/users/<16 hex digits>/memberships.
func membershipsPathUser(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "/v1/users/")
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(rest, "/memberships")
	if !ok || len(id) != 16 {
		return "", false
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "", false
	}
	return strings.ToLower(id), true
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
