// Package nplnrest is NPLN's JSON front end: the hosts a console talks to
// BEFORE Splatoon 3 opens a gRPC channel.
//
// "The console", not "the game": a 2026-09-20 decompile of Splatoon 3
// v11.3.0's own main found no literal "vermillion", "penne" or "frontline"
// string anywhere in the binary, despite an exhaustive search that did find
// every gRPC service Splatoon 3 itself calls later (see internal/npln's
// "confirmed RPC surface" section). That rules out Splatoon 3's own code
// making these requests directly. The 2026-08-27 tcpdump that established
// these hosts are used at all (see vermillion's doc comment below) is still
// good evidence -- a capture watches the wire, not the calling process -- it
// just means the caller is almost certainly a shared system client (the same
// npns sysmodule already cited for penne's ticket mechanism is the leading
// candidate) acting on the console's behalf during account/network setup,
// not Splatoon-3-specific code. Nothing here changes what SwitchNet needs to
// answer, since the traffic still arrives at these hosts either way -- only
// which binary would need decompiling to learn more from that side.
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
// # Nothing here is confirmed against a capture
//
// The request and response bodies of all three are undocumented. Kinnay's wiki
// names the hosts; no public implementation exists; and the reference server
// this project studied loads its captures at runtime from a directory the
// operator supplies, so its measured bytes are not in it either.
//
// CLAUDE.md rule 6 says what to do about that: implement the most defensible
// reading, mark it, and say what a capture would settle. The most defensible
// reading of "we do not know what to say" is to say as little as possible —
// a well-formed empty answer rather than a fabricated Nintendo internal — and
// to make the capture cheap. Hence Options.RecordDir: point it at a directory,
// put a console through one boot, and every request and response lands on disk
// in the order it happened. That transcript is what turns every
// TODO(capture-needed) in this package into a one-commit reconciliation.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers
package nplnrest

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

// Frontlines reports how many persistent connections are open. For the
// dashboard, and for answering "did the console get that far".
func (s *Server) Frontlines() int64 { return s.frontlines.Load() }

// ---- vermillion: device initialisation ------------------------------------

// vermillion answers the device-initialisation handshake.
//
// TODO(capture-needed): every request and response body on this host. What is
// known is that it is not telemetry: a 2026-08-27 tcpdump of a real, successful
// Nintendo Account import showed 16.5KB exchanged here mid-flow, which is why
// internal/nintendo/hosts.go reclassified it away from a stub. What it asks for
// and what satisfies it are unknown.
//
// The most defensible answer is a 200 with an empty JSON object: the console
// treats a transport failure as the network being broken, so an error is worse
// than a well-formed answer it may not like — and an invented body would be a
// plausible-looking Nintendo internal, which rule 6 rules out.
//
// A capture of one boot with Options.RecordDir set settles this host entirely.
// Change: this function.
func (s *Server) vermillion(w http.ResponseWriter, r *http.Request) {
	s.answer(w, r, http.StatusOK, map[string]any{})
}

// ---- penne: login tickets and the frontline FQDN --------------------------

// penne answers the login-ticket exchange, and names the frontline host.
//
// The field names below are no longer a blind guess for the FQDN alone. A
// 2026-09-20 decompile of the operator's own console's `npns` sysmodule (a
// system service that, per this package's own doc comment, shares the exact
// same three-host "penne" mechanism with NPLN — same host family, same
// ticket-then-frontline shape) found the literal JSON keys its client-side
// parser looks for: "frontline_fqdn" (already one of the spellings sent
// below — now confirmed, not guessed), "ticket", "issued_at", "wait_sec",
// "rtt_max", "wowl_timeout", and "ignore_rst". Those last six were entirely
// absent from this response before tonight, despite this host being
// documented as a "login ticket exchange" — a response with no ticket field
// at all was always the least defensible reading, once the field name was
// known to exist somewhere in the real client.
//
// The FQDN keeps going out under every plausible spelling: they are
// additional keys in a JSON object, so a parser that wants one finds it and
// a parser that wants none is no worse off. The new fields follow the same
// "well-formed placeholder, not a fabricated Nintendo internal" rule this
// package already uses for vermillion: a ticket is issued (an opaque random
// value, since nothing here can construct or validate a real one) and the
// session-config numbers are reasonable, permissive defaults chosen not to
// trip whatever the console does with them, not reasoned-out Nintendo
// constants.
//
// The same npns decompile found the literal path npns itself uses for this
// exact mechanism: POST /v1/login_tickets. Circumstantial rather than a
// direct capture of NPLN's own traffic (npns is a system service, not an
// NPLN host, even though it shares the mechanism) -- routing here is by
// host, not path (see Handler's own comment), so this doesn't change
// behaviour, only what a future capture should expect to see.
//
// TODO(capture-needed): the request shape, and the real values for wait_sec/
// rtt_max/wowl_timeout/ignore_rst, and whether the console actually
// validates the ticket's shape at all. A capture with Options.RecordDir set
// settles all of it in one pass. Change: this function.
func (s *Server) penne(w http.ResponseWriter, r *http.Request) {
	ticket, err := randomHex(32)
	if err != nil {
		s.log.Error("nplnrest: generate login ticket", "error", err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	s.answer(w, r, http.StatusOK, map[string]any{
		"host":           s.frontlineHost,
		"fqdn":           s.frontlineHost,
		"frontline_fqdn": s.frontlineHost,
		"frontlineFqdn":  s.frontlineHost,

		"ticket":       ticket,
		"issued_at":    now().Unix(),
		"wait_sec":     30,
		"rtt_max":      3000,
		"wowl_timeout": 30,
		"ignore_rst":   true,
	})
}

// randomHex returns n random bytes, hex-encoded.
func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// ---- fro-N: the persistent connection -------------------------------------

// frontlinePoll is how often the frontline handler checks whether the console
// has gone away.
//
// It writes nothing. See the handler for why silence is the answer here.
const frontlinePoll = 5 * time.Second

// frontline holds a connection open for as long as the console wants it.
//
// **Closing this is an error on the console**, not a clean end: 2307-2103 on
// Splatoon 2 and 2321-* on Splatoon 3. So the handler does not return until the
// console hangs up or the process is shutting down.
//
// It sends NOTHING after the response headers, and that is a deliberate choice
// rather than an omission. TODO(capture-needed): the frame format. The reference
// implementation logs these frames as opaque hex, so not even it knows what they
// mean. Writing invented bytes into a stream whose framing is unknown is worse
// than writing none: a console that receives nothing is a console waiting, and a
// console that receives a malformed frame is one that has decided the server is
// broken. Silence keeps the connection in the only state that is certainly
// valid.
//
// The headers ARE flushed immediately, so the console sees the response begin.
// A response that has not started is indistinguishable from a server that is
// not there.
//
// Change: this function, once a capture shows the framing.
func (s *Server) frontline(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// Cannot stream, so the connection would be buffered until it closed —
		// which is the failure this whole handler exists to avoid.
		s.log.Error("nplnrest: the frontline response writer cannot flush")
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	s.rec.Record(r, nil, http.StatusOK, "frontline: held open")

	// Chunked, unbuffered, and not cached anywhere between here and the
	// console. Without X-Accel-Buffering an nginx in front holds the stream
	// until a buffer fills, which for a stream that writes nothing is forever.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	open := s.frontlines.Add(1)
	// Logged at info because this line answers the first question anybody asks
	// when Splatoon 3 will not connect: did the console get as far as the
	// frontline?
	s.log.Info("nplnrest: frontline connection open", "host", wirelog.HostOnly(r.Host), "open", open)
	defer func() {
		remaining := s.frontlines.Add(-1)
		s.log.Info("nplnrest: frontline connection closed by the console",
			"host", wirelog.HostOnly(r.Host), "open", remaining)
	}()

	ticker := time.NewTicker(frontlinePoll)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			// Nothing to send. The tick exists only so the goroutine notices a
			// cancelled context promptly; r.Context() already fires on a
			// disconnect, and this bounds how long a shutdown waits.
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
