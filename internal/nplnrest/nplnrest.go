// Package nplnrest is NPLN's JSON front end: the hosts a console talks to
// BEFORE Splatoon 3 opens a gRPC channel.
//
// "The console", not "the game": a 2026-09-20 decompile of Splatoon 3
// v11.3.0's own main found no literal "vermillion", "penne" or "frontline"
// string anywhere in the binary, despite an exhaustive search that did find
// every gRPC service Splatoon 3 itself calls later (see internal/npln's
// "confirmed RPC surface" section). That rules out Splatoon 3's own code
// making these requests directly. The caller is almost certainly a shared
// system client acting on the console's behalf during account/network
// setup, not Splatoon-3-specific code -- but the traffic still arrives at
// these hosts either way, so it does not change what this server answers.
//
// The console first speaks ordinary JSON over HTTPS to two host families and
// then holds a third connection open indefinitely:
//
//	gw.hac.lp1.vermillion.srv.nintendo.net   device initialisation, account config
//	val.hac.lp1.penne.srv.nintendo.net       login tickets, and the frontline FQDN
//	fro-N.hac.lp1.penne.srv.nintendo.net     a persistent connection, held open
//
// So this package is EARLIER in the boot sequence than internal/npln, and a
// console that cannot get past it never reaches the gRPC gateway at all.
//
// # Most of this is now confirmed, not guessed
//
// NextendoNetwork's own public Splatoon 3 NPLN server (PolyForm Shield
// licensed -- facts taken from their published, real-console-tested
// findings, never their code, the same boundary this project already draws
// for nx-dauth) documents exactly which parts of this handshake it verified
// against real captures of a real console talking to real Nintendo, and
// which parts remain its own untested guess -- labelled as such in its own
// source. Every shape and value below marked "confirmed" reproduces a fact
// stated there, reimplemented independently in this file; nothing here is
// their code. Two real bugs their capture caught, both worth stating
// plainly because either one silently strands a console at the ink loading
// screen with no useful error:
//
//   - GET .../vermillion-device-id's response key is camelCase
//     ("vermillionDeviceId"), not snake_case. The wrong key means the
//     console never receives a device id and loops this endpoint forever.
//   - That id's value, base64-decoded, must not encode to a base64 string
//     containing '/' or '+' -- Splatoon 3 uses it as a literal path segment
//     and hard-crashes on either character.
//
// What remains genuinely unconfirmed (no capture of these specific bodies
// exists anywhere, including theirs): the exact response shape for
// GET /v1/accounts/<anything but config>, POST /v1/acds/search, and
// GET /v1/nearness-checks. Each is answered with the same well-formed empty
// envelope its neighbours use, not a fabricated one -- see each handler's
// own comment.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers
package nplnrest

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/n-popescu/switchnet/internal/nintendo"
	"github.com/n-popescu/switchnet/internal/wirelog"
)

// Options configures the front end.
type Options struct {
	Log *slog.Logger

	// FrontlineHost is the hostname the console is told to open its persistent
	// connection to.
	//
	// fro-N is a family, and which member is named is the server's choice. It
	// must be one internal/nintendo/hosts.go redirects, or the console opens
	// its persistent connection to real Nintendo — which succeeds, and then
	// fails in a way that looks like SwitchNet being at fault.
	FrontlineHost string

	// RecordDir, when set, is a directory every request and response is written
	// to, in order.
	//
	// This is how the shapes in this package get settled. It is off by default
	// because it writes whatever the console sends, headers included.
	RecordDir string

	Now func() time.Time
}

// Server serves the NPLN JSON front end.
type Server struct {
	log           *slog.Logger
	frontlineHost string
	rec           *wirelog.Recorder
	now           func() time.Time

	// frontlines counts the persistent connections currently held open, for the
	// log line that says whether the console got that far.
	frontlines atomic.Int64
}

// New builds the front end.
func New(opts Options) (*Server, error) {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.FrontlineHost == "" {
		// Refused rather than defaulted. A default would be a hostname this
		// server does not know is redirected, and the console would open its
		// persistent connection to real Nintendo.
		return nil, errors.New("nplnrest: no frontline host configured")
	}
	// Fail-closed, and this is the check worth having in this package. The
	// frontline FQDN is the one value SwitchNet hands the console that tells it
	// where to connect next; if that name is not redirected, the console opens
	// its persistent connection to real Nintendo. That SUCCEEDS, so nothing
	// here fails — and then the game misbehaves in a way that looks like
	// SwitchNet's fault while its traffic is going somewhere this deployment
	// promises it will not.
	switch resolution, _, _ := nintendo.Resolve(opts.FrontlineHost); resolution {
	case nintendo.ResolveRedirect:
	default:
		return nil, fmt.Errorf(
			"nplnrest: frontline host %q is not redirected by internal/nintendo/hosts.go, "+
				"so the console would open its persistent connection to real Nintendo. "+
				"Use one of the fro-N.hac.lp1.penne.srv.nintendo.net names",
			opts.FrontlineHost)
	}
	rec, err := wirelog.New(opts.Log, opts.RecordDir, opts.Now)
	if err != nil {
		return nil, err
	}
	return &Server{
		log:           opts.Log,
		frontlineHost: opts.FrontlineHost,
		rec:           rec,
		now:           opts.Now,
	}, nil
}

// Handler returns the routed handler.
//
// Routed by host rather than by path, because the three hosts are three
// different protocols behind one listener and their paths are not known well
// enough to route on. An unrecognised host is answered rather than refused, for
// the reason internal/stubs gives: a console that gets a connection error
// decides the network is broken, which surfaces somewhere unrelated.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := wirelog.HostOnly(r.Host)

		switch {
		case strings.HasPrefix(host, "fro-"):
			s.frontline(w, r)
		case strings.Contains(host, ".vermillion."):
			s.vermillion(w, r)
		case strings.Contains(host, ".penne."):
			s.penne(w, r)
		default:
			s.log.Warn("nplnrest: request for a host this server does not serve",
				"host", host, "path", r.URL.Path)
			s.answer(w, r, http.StatusOK, map[string]any{})
		}
	})
}

// vermillionEmptyPayload is the well-formed empty answer for every
// vermillion sub-resource whose response shape is genuinely unconfirmed
// (see the package doc comment). Wraps an empty JSON object in the same
// {"payload": base64(...)} envelope GET /v1/accounts/config uses, since
// every one of these paths lives under that same account/device-config
// family and there is no evidence any of them differ in envelope shape --
// only in what is inside it, which is exactly what is not known here.
func vermillionEmptyPayload() map[string]string {
	return map[string]string{"payload": base64.StdEncoding.EncodeToString([]byte("{}"))}
}

// Frontlines reports how many persistent connections are open. For the
// dashboard, and for answering "did the console get that far".
func (s *Server) Frontlines() int64 { return s.frontlines.Load() }

// ---- vermillion: device initialisation ------------------------------------

// vermillion answers the device-initialisation handshake.
//
// Routed by path -- unlike this package's other two hosts, vermillion is
// genuinely a small REST API, not one shape repeated at every path, and the
// confirmed pieces (see the package doc comment) only cover some of it.
func (s *Server) vermillion(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/v1/devices/initialize":
		s.vermillionDeviceInitialize(w, r)
	case r.URL.Path == "/v1/devices/vermillion-device-id":
		s.vermillionDeviceID(w, r)
	case r.URL.Path == "/v1/devices/penne-id":
		s.vermillionDevicePenneID(w, r)
	case r.URL.Path == "/v1/accounts/config":
		s.vermillionAccountsConfig(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/accounts/"):
		// Any account sub-resource OTHER than config -- see
		// vermillionEmptyPayload's own comment for why this is the
		// envelope rather than a guess at real content.
		s.answer(w, r, http.StatusOK, vermillionEmptyPayload())
	case r.URL.Path == "/v1/acds/search", r.URL.Path == "/v1/nearness-checks":
		// TODO(capture-needed): no capture of either exists anywhere. The
		// same empty envelope as the other unconfirmed account paths --
		// not a fabricated shape -- since these were only ever seen
		// (never captured) alongside the same bootstrap sequence.
		s.answer(w, r, http.StatusOK, vermillionEmptyPayload())
	default:
		s.log.Warn("nplnrest: unhandled vermillion path", "path", r.URL.Path, "method", r.Method)
		s.answer(w, r, http.StatusOK, vermillionEmptyPayload())
	}
}

// vermillionDeviceInitialize answers POST /v1/devices/initialize.
//
// Confirmed: request body is {"penneId": "..."}; a real server answers 204
// No Content for a console it already recognises. This server has no
// concept of "not yet registered" to refuse in the first place -- every
// device that reaches this endpoint at all gets the same answer.
func (s *Server) vermillionDeviceInitialize(w http.ResponseWriter, r *http.Request) {
	s.rec.Record(r, nil, http.StatusNoContent, "vermillion: device initialize")
	w.WriteHeader(http.StatusNoContent)
}

// vermillionDeviceID answers GET /v1/devices/vermillion-device-id.
//
// Confirmed response shape: {"vermillionDeviceId": "<base64 of 16 bytes>"}.
// The key is camelCase -- getting this wrong is not a cosmetic mismatch,
// it is the console never receiving a device id at all and looping this
// endpoint indefinitely, never reaching penne login. See the package doc
// comment.
//
// Derived deterministically from the console's own device id (read out of
// the "did:<id>" token the SDK places in its own User-Agent header, the
// same convention this project's other services already parse device
// identity from) rather than returned as one fixed value for every
// console, so two consoles behind this server do not collide on one
// identity. Re-derived up to 64 times with an incrementing counter if the
// result's base64 contains '/' or '+', which is not cosmetic either:
// Splatoon 3 uses this value as a literal path segment and hard-crashes on
// either character. Falls back to a fixed, known-safe value only if no
// attempt within that bound avoids them, which is astronomically unlikely
// for a uniformly distributed 16-byte hash truncation.
func (s *Server) vermillionDeviceID(w http.ResponseWriter, r *http.Request) {
	id := vermillionDeviceIDFor(deviceIDFromUserAgent(r.Header.Get("User-Agent")))
	s.answer(w, r, http.StatusOK, map[string]string{"vermillionDeviceId": id})
}

// vermillionFallbackDeviceID is used only if vermillionDeviceIDFor's search
// space is exhausted without landing on a '/'- and '+'-free result, or if
// no device id could be read from the request at all.
const vermillionFallbackDeviceID = "QdbC8zfuKXqYMAKjmdQsXg=="

// vermillionDeviceIDFor derives a per-device vermillion device id.
func vermillionDeviceIDFor(deviceID string) string {
	if deviceID == "" {
		return vermillionFallbackDeviceID
	}
	for n := 0; n < 64; n++ {
		sum := sha256.Sum256([]byte(fmt.Sprintf("switchnet-vermillion:%s:%d", deviceID, n)))
		v := base64.StdEncoding.EncodeToString(sum[:16])
		if !strings.ContainsAny(v, "/+") {
			return v
		}
	}
	return vermillionFallbackDeviceID
}

// deviceIDFromUserAgent reads the "did:<id>" token the SDK places in its own
// User-Agent, e.g. "NintendoSDK Firmware/22.5.0-1.0 (platform:NX;
// did:63671daf59441c13; eid:lp1)".
func deviceIDFromUserAgent(ua string) string {
	i := strings.Index(ua, "did:")
	if i < 0 {
		return ""
	}
	rest := ua[i+len("did:"):]
	if j := strings.IndexAny(rest, "; )"); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}

// vermillionDevicePenneID answers PUT /v1/devices/penne-id -- the console
// associating this device with the penne login identity it just generated.
//
// Not itself confirmed by a capture of this exact body, but 204 No Content
// is the same answer the confirmed devices/initialize gives, and an
// unhandled path here was what left a console looping device
// initialisation forever, never reaching penne login at all -- answering
// something here matters far more than which 2xx it is.
func (s *Server) vermillionDevicePenneID(w http.ResponseWriter, r *http.Request) {
	s.rec.Record(r, nil, http.StatusNoContent, "vermillion: device penne-id")
	w.WriteHeader(http.StatusNoContent)
}

// vermillionAccountsConfig answers GET /v1/accounts/config.
//
// Confirmed envelope: {"payload": base64(<json>)}. The inner JSON's
// online_license.is_available must be true -- a console that reads false
// here treats the account as unlicensed for online play and never leaves
// the lobby, which reaches the player as a generic communication error
// with nothing in this server's own log to explain it.
func (s *Server) vermillionAccountsConfig(w http.ResponseWriter, r *http.Request) {
	inner, err := json.Marshal(map[string]any{
		"version":          map[string]int{"major": 1, "minor": 1, "micro": 0},
		"online_license":   map[string]bool{"is_available": true},
		"activity":         map[string]bool{"has_received_guidance": true, "has_inserted": true, "has_transferred_to_vphym": true},
		"disabled_content": map[string]any{"content_meta_ids": []string{}},
		"hidden_key":       map[string]any{"application_ids": []string{}},
	})
	if err != nil {
		s.log.Error("nplnrest: encode accounts/config payload", "error", err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	s.answer(w, r, http.StatusOK, map[string]string{"payload": base64.StdEncoding.EncodeToString(inner)})
}

// ---- penne: login tickets and the frontline FQDN --------------------------

// penne answers the login-ticket exchange, and names the frontline host.
//
// Routed by path -- POST /v1/login_tickets and GET /v1/frontlines are two
// different, confirmed responses (see below), not one shape for the whole
// host.
func (s *Server) penne(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/v1/login_tickets":
		s.penneLoginTickets(w, r)
	case "/v1/frontlines":
		s.penneFrontlines(w, r)
	default:
		s.log.Warn("nplnrest: unhandled penne path", "path", r.URL.Path, "method", r.Method)
		s.answer(w, r, http.StatusOK, map[string]any{})
	}
}

// penneConnectionParams is persistent_connection_params_simple, confirmed
// field-for-field and value-for-value against a real capture of a working
// login (see the package doc comment for provenance). Both nested objects
// (awake, sleep) were entirely absent from this project's own earlier guess
// -- a response with a ticket and an FQDN but no session-keepalive
// parameters at all was always the least defensible reading, once these
// exact values were known to exist somewhere in a real response.
func penneConnectionParams() map[string]any {
	return map[string]any{
		"awake":        map[string]any{"count": 2, "disable": false, "idle": 60, "interval": 10},
		"ignore_rst":   true,
		"retry_count":  2,
		"rtt_max":      1000,
		"sleep":        map[string]any{"count": 1440, "disable": true, "idle": 60, "interval": 10},
		"wait_sec":     3600,
		"wowl_timeout": 200,
	}
}

// penneLoginTickets answers POST /v1/login_tickets.
//
// Confirmed request shape: {"id": "<penneId>", "password": "..."}. Confirmed
// response shape and every value below except the ticket itself and the
// two timestamps (necessarily per-request): expires_at is issued_at plus
// 345600 seconds (four days), matching the delta a real capture showed
// between the two. The ticket itself is an opaque random value, since
// nothing here can construct or validate a real Nintendo-signed one, and
// nothing found anywhere confirms the console validates its shape at all.
func (s *Server) penneLoginTickets(w http.ResponseWriter, r *http.Request) {
	ticket, err := randomToken(36)
	if err != nil {
		s.log.Error("nplnrest: generate login ticket", "error", err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	issuedAt := now().Unix()
	s.answer(w, r, http.StatusOK, map[string]any{
		"expires_at":                          issuedAt + 345600,
		"frontline_fqdn":                      s.frontlineHost,
		"issued_at":                           issuedAt,
		"persistent_connection_params_simple": penneConnectionParams(),
		"ticket":                              ticket,
	})
}

// penneFrontlines answers GET /v1/frontlines, the same frontline FQDN and
// session-config parameters as login_tickets but as a standalone refresh
// (e.g. after the frontline connection drops) rather than tied to issuing a
// new ticket.
func (s *Server) penneFrontlines(w http.ResponseWriter, r *http.Request) {
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	s.answer(w, r, http.StatusOK, map[string]any{
		"current_time":                        now().Unix(),
		"frontline_fqdn":                      s.frontlineHost,
		"persistent_connection_params_simple": penneConnectionParams(),
	})
}

// randomToken returns n random bytes, base64url encoded without padding --
// an opaque value in the same encoding family this project already uses
// for its other generated tokens (see internal/baas/models.go's newHexID
// sibling), not a confirmed real ticket encoding.
func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// ---- fro-N: the persistent connection -------------------------------------

// frontline sends the observed opening frame, then leaves the response open
// until the client disconnects. The captured POST has no body: request EOF is
// normal and must not terminate the server-to-client stream.
func (s *Server) frontline(w http.ResponseWriter, r *http.Request) {
	controller := http.NewResponseController(w)
	// HTTP/1 otherwise drains an unfinished upload before flushing headers.
	// HTTP/2 already supports concurrent reads/writes and may report unsupported.
	if err := controller.EnableFullDuplex(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		s.log.Error("nplnrest: enable frontline duplex", "error", err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	s.rec.RecordStream(r, http.StatusOK, "frontline: opening frame and persistent response")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	if _, err := w.Write(frontlineOpeningFrame(now)); err != nil {
		return
	}
	if err := controller.Flush(); err != nil {
		return
	}
	open := s.frontlines.Add(1)
	s.log.Info("nplnrest: frontline connection open", "host", wirelog.HostOnly(r.Host), "open", open)
	reason := "request_context_done"
	defer func() {
		s.log.Info("nplnrest: frontline connection closed", "host", wirelog.HostOnly(r.Host), "open", s.frontlines.Add(-1), "reason", reason)
	}()
	type bodyReadResult struct {
		bytes int64
		err   error
	}
	var bodyDone chan bodyReadResult
	if r.Body != nil && r.Body != http.NoBody {
		bodyDone = make(chan bodyReadResult, 1)
		// Capture the channel value: the select disables its own copy on EOF.
		go func(done chan<- bodyReadResult) {
			n, err := io.Copy(io.Discard, r.Body)
			done <- bodyReadResult{n, err}
		}(bodyDone)
	}
	for {
		select {
		case result := <-bodyDone:
			bodyDone = nil
			s.log.Info("nplnrest: frontline request body ended", "bytes", result.bytes, "error", result.err)
			if result.err != nil {
				reason = "request_body_error"
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

// ---- answering ------------------------------------------------------------

func (s *Server) answer(w http.ResponseWriter, r *http.Request, status int, body any) {
	encoded, err := json.Marshal(body)
	if err != nil {
		s.log.Error("nplnrest: encode response", "error", err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	s.rec.Record(r, encoded, status, "")

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}
