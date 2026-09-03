// Package nplnrest is Splatoon 3's JSON front end.
//
// The game does NOT open with gRPC: it first talks ordinary JSON over
// HTTPS to two hosts -- vermillion, then penne -- and only once penne hands
// it a frontline hostname and a login ticket does it open the NPLN gRPC
// channel internal/npln serves. This package answers that first stage.
//
// Reference:
//   - https://github.com/kinnay/NintendoClients/wiki/Penne-Servers
//   - https://github.com/kinnay/NintendoClients/wiki/Vermillion-Server
package nplnrest

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	sjwt "github.com/n-popescu/switchnet/internal/jwt"
	"github.com/n-popescu/switchnet/internal/nintendo"
)

// loginTicketTTL is given directly by the wiki: "A ticket is valid for 4
// days." -- not derived or guessed.
const loginTicketTTL = 4 * 24 * time.Hour

// Options configures the server.
type Options struct {
	Log *slog.Logger
	// Now is overridable in tests; nil defaults to time.Now.
	Now func() time.Time
}

// Server serves vermillion's and penne's JSON endpoints.
type Server struct {
	log *slog.Logger
	now func() time.Time
	// frontlineFQDN is the one fro-N hostname this server hands out to
	// consoles. There are several (internal/nintendo/hosts.go lists the
	// whole family, because a console already told about one must not fall
	// back to real Nintendo if it is ever pointed at another), but nothing
	// requires this server to use more than one of them -- it is the only
	// value that ever appears in a response this package writes.
	frontlineFQDN string
}

// New builds the server.
func New(opts Options) *Server {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Server{log: log, now: now, frontlineFQDN: firstFrontlineHost()}
}

// firstFrontlineHost and frontlineHosts both read from
// internal/nintendo/hosts.go rather than naming a fro-N host directly, so
// this package cannot drift from the one place hostnames are supposed to
// live (CLAUDE.md).
func firstFrontlineHost() string {
	if hosts := frontlineHosts(); len(hosts) > 0 {
		return hosts[0]
	}
	return ""
}

func frontlineHosts() []string {
	for _, h := range nintendo.ServiceHosts(nintendo.ServiceNPLNRest) {
		if h.Wildcard {
			return h.TLSNames
		}
	}
	return nil
}

// Handler returns the HTTP handler. It is one handler for both vermillion
// and penne (and the frontline hosts): nginx routes every ServiceNPLNRest
// hostname to the same upstream, so the Host header is what tells this
// package which service is actually being asked for.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET val.hac.lp1.penne.srv.nintendo.net/v1/frontlines", s.getFrontlines)
	mux.HandleFunc("POST val.hac.lp1.penne.srv.nintendo.net/v1/login_tickets", s.postLoginTickets)

	mux.HandleFunc("GET gw.hac.lp1.vermillion.srv.nintendo.net/v1/accounts/config", s.getAccountsConfig)
	mux.HandleFunc("GET gw.hac.lp1.vermillion.srv.nintendo.net/v1/accounts/devices", s.getAccountsDevices)

	for _, name := range frontlineHosts() {
		mux.HandleFunc(name+"/", s.serveFrontline)
	}

	mux.HandleFunc("/", s.notFound)
	return mux
}

// ---- val.hac.lp1.penne.srv.nintendo.net ------------------------------------

type persistentConnectionParams struct {
	Awake      awakeSleepParams `json:"awake"`
	IgnoreRst  bool             `json:"ignore_rst"`
	RetryCount int              `json:"retry_count"`
	Sleep      awakeSleepParams `json:"sleep"`
	WaitSec    int              `json:"wait_sec"`
}

type awakeSleepParams struct {
	Count    int `json:"count"`
	Idle     int `json:"idle"`
	Interval int `json:"interval"`
}

// defaultPersistentConnectionParams is the literal values the wiki
// documents for this field -- not defaults SwitchNet picked, Nintendo's
// own, given directly: awake count 2, sleep count 1440, idle 60, interval
// 10, ignore_rst true, retry_count 2, wait_sec 3600.
func defaultPersistentConnectionParams() persistentConnectionParams {
	return persistentConnectionParams{
		Awake:      awakeSleepParams{Count: 2, Idle: 60, Interval: 10},
		IgnoreRst:  true,
		RetryCount: 2,
		Sleep:      awakeSleepParams{Count: 1440, Idle: 60, Interval: 10},
		WaitSec:    3600,
	}
}

// getFrontlines answers with the one frontline host this server hands out,
// and the console's connection parameters for it.
func (s *Server) getFrontlines(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"current_time":                        s.now().UTC().Format(time.RFC3339),
		"frontline_fqdn":                      s.frontlineFQDN,
		"persistent_connection_params_simple": defaultPersistentConnectionParams(),
	})
}

type loginTicketRequest struct {
	ID       string `json:"id"`
	Password string `json:"password"`
}

// postLoginTickets issues an opaque ticket for whatever Penne id and
// password the console presents.
//
// TODO(capture-needed): what a Penne id and password actually are, and how
// a console obtains them in the first place. The wiki marks the only two
// endpoints that plausibly create one -- god's POST /v1/immigrate and POST
// /v1/penne_ids -- with an unconfirmed HTTP method, and god is not served
// by this package at all yet (internal/nintendo/hosts.go still classifies
// it ServiceStubs). Accepting whatever is presented without verifying it
// follows the same rule dauth and aauth already apply to every other
// Nintendo-issued credential (CLAUDE.md's second ground rule): this is not
// a credential SwitchNet issued, so it is treated as an opaque label, never
// checked against anything. Change: this function, once god's provisioning
// flow has a capture behind it.
func (s *Server) postLoginTickets(w http.ResponseWriter, r *http.Request) {
	var body loginTicketRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	now := s.now().UTC()
	s.log.Info("nplnrest: issued a penne login ticket", "penne_id", body.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"expires_at":                          now.Add(loginTicketTTL).Format(time.RFC3339),
		"frontline_fqdn":                      s.frontlineFQDN,
		"issued_at":                           now.Format(time.RFC3339),
		"persistent_connection_params_simple": defaultPersistentConnectionParams(),
		"ticket":                              sjwt.NewTokenID(),
	})
}

// ---- gw.hac.lp1.vermillion.srv.nintendo.net --------------------------------

// getAccountsConfig answers with an empty configuration object.
//
// TODO(capture-needed): the wiki names this endpoint but documents no
// response fields for it at all, unlike every one of Penne's endpoints,
// which each have a full field table. An empty object is the least this
// response could honestly claim -- not a guess at real content -- and
// whether the console requires specific keys here to proceed past device
// initialisation is unconfirmed. See also docs/QUESTIONS.md: whether
// Splatoon 3's boot sequence needs anything from vermillion at all, as
// opposed to this host being contacted for reasons unrelated to NPLN, is
// itself unconfirmed. Change: this function.
func (s *Server) getAccountsConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{})
}

// getAccountsDevices answers with no registered virtual game card devices.
//
// TODO(capture-needed): same gap as getAccountsConfig -- no field table
// exists for this response either, including whether the wrapper is an
// object with a "devices" key (used here) or a bare array. SwitchNet does
// no virtual-game-card device management at all, so an empty list is
// honest regardless of which shape is right; only the wrapper is a guess.
// Change: this function.
func (s *Server) getAccountsDevices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"devices": []any{}})
}

// ---- fro-N.hac.lp1.penne.srv.nintendo.net (frontline) ----------------------

// serveFrontline holds the console's persistent connection open.
//
// TODO(capture-needed): the actual wire framing. Nothing public documents
// what a frontline connection carries once open -- not even whether it is
// a single long HTTP response, a WebSocket upgrade, or something else.
// internal/nintendo/hosts.go already establishes, from a real error-code
// capture, that CLOSING it is read by the console as a failure
// (2307-2103 on Splatoon 2, 2321-* on Splatoon 3) -- so this does the one
// thing that is confirmed safe: accept the connection and hold it open,
// rather than closing it or inventing a message format to send. Change:
// this function, entirely, once a capture exists.
func (s *Server) serveFrontline(w http.ResponseWriter, r *http.Request) {
	s.log.Info("nplnrest: frontline connection opened", "host", r.Host, "remote", r.RemoteAddr)
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	<-r.Context().Done()
	s.log.Info("nplnrest: frontline connection closed", "host", r.Host, "remote", r.RemoteAddr)
}

// ---- helpers ----------------------------------------------------------

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.log.Warn("nplnrest: unrecognised request", "host", r.Host, "method", r.Method, "path", r.URL.Path)
	http.NotFound(w, r)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
