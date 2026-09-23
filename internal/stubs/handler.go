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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/n-popescu/switchnet/internal/bcat"
)

// Config is what the handler needs to run.
type Config struct {
	// LogBodies logs request bodies. Useful exactly once, while working out what
	// an unimplemented endpoint is being asked for; far too chatty to leave on.
	LogBodies bool

	// BCATRecordDir is where the BCAT probe writes its transcript.
	//
	// The three bcat-* hosts are null-routed by default, so nothing reaches the
	// probe until an operator flips them in internal/nintendo/hosts.go -- see
	// internal/bcat.Probe and TODO.md §2.4. Setting this without flipping them
	// does nothing; flipping them without setting this still logs every request,
	// which is enough to answer "did the console reach us at all".
	BCATRecordDir string
}

// Handler serves the stub endpoints.
type Handler struct {
	cfg  Config
	log  *slog.Logger
	bcat http.Handler
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
		h.membership(w)

	case strings.Contains(host, "service-status"):
		// Game server availability. Everything is up; if it were not, the
		// console finding out from here would not help.
		h.serviceStatus(w)

	case strings.HasPrefix(host, "bcat-"):
		// Unreachable until an operator flips the three bcat-* hosts away from
		// ServiceBlackhole. Wired up in advance because the experiment those
		// hosts exist for is a five-minute job once a console is available and
		// weeks of nothing if the server has to be written first -- see
		// internal/bcat.Probe.
		h.bcat.ServeHTTP(w, r)

	case strings.Contains(host, ".dragons.nintendo.net"):
		// eLicenses. Most paths here are a fine empty object; one is not.
		// See h.dragons.
		h.dragons(w, path)

	case strings.HasPrefix(host, "god.") && strings.HasSuffix(host, ".penne.srv.nintendo.net") &&
		r.Method == http.MethodPost && isNotificationTokensPath(path):
		// The one penne account path whose answer the console actually uses.
		// See h.notificationToken.
		h.notificationToken(w, r, path)

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
		// answer regardless -- SwitchNet has nothing to publish -- so nothing
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
		// elicense_archives/<id>/report paths: SwitchNet's aauth accepts
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

// isNotificationTokensPath matches /v1/accounts/<id>/notification_tokens.
func isNotificationTokensPath(path string) bool {
	rest, ok := strings.CutPrefix(path, "/v1/accounts/")
	if !ok {
		return false
	}
	id, ok := strings.CutSuffix(rest, "/notification_tokens")
	return ok && id != "" && !strings.Contains(id, "/")
}

// notificationToken answers POST /v1/accounts/<id>/notification_tokens on
// god.hac.lp1.penne.srv.nintendo.net.
//
// This cannot be the generic empty object. Decompiling the account sysmodule
// (fw 22.5.0) shows the Nintendo Account link ends by registering a push
// channel: FUN_71000c8450 logs in, asks npns over IPC for a notification
// token (FUN_710015b5d0 -- this request is what npns sends to get one), and
// only if that succeeds does it PUT /1.0.0/push_channels/<user>/<device> with
// the token as deviceToken. Answered {}, npns has no token to hand back and
// the PUT is never made -- and every hardware link attempt on record stops at
// exactly this request, one step short of the push_channels PUT a working
// server's capture shows next. Whether that is also what the "couldn't be
// linked" dialog reports is not proven (FUN_71000c8450 returns void); that it
// is the next request a working flow makes and ours cannot, is.
//
// Shape from Kinnay's wiki (Penne-Servers, "POST
// /v1/accounts/<id>/notification_tokens"): one field, notification_token, 36
// hex digits. A working server's push_channels body carries exactly such a
// value ("0009c95be1ac9b2c1e9d9d9fc8bf3302c7f5",
// docs/CAPTURES/raw/nextendo-working-nalink-2026-08-27.flow).
//
// The token is derived from the account id and application id rather than
// random, so a console that registers twice gets the same token back and the
// push channel BAAS stores stays the one npns knows about. Nothing on this
// network ever delivers a push notification to it, so its only job is to be a
// well-formed, stable value.
//
// TODO(capture-needed): whether the leading bytes ("0009..." in the one real
// sample) are structure npns checks, rather than part of an opaque value.
func (h *Handler) notificationToken(w http.ResponseWriter, r *http.Request, path string) {
	account := strings.TrimSuffix(strings.TrimPrefix(path, "/v1/accounts/"), "/notification_tokens")

	// application_id is part of the identity when present. The body may
	// already have been consumed by LogBodies, in which case the account id
	// alone still gives a stable answer.
	var body struct {
		ApplicationID string `json:"application_id"`
	}
	if r.Body != nil {
		if raw, err := io.ReadAll(io.LimitReader(r.Body, maxLoggedBody)); err == nil {
			_ = json.Unmarshal(raw, &body)
		}
	}

	sum := sha256.Sum256([]byte("penne-notification-token\x00" + account + "\x00" + body.ApplicationID))
	writeJSON(w, http.StatusOK, map[string]any{
		"notification_token": hex.EncodeToString(sum[:18]),
	})
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
