// Package nplnrest is NPLN's JSON front end: the hosts Splatoon 3 talks to
// BEFORE it opens a gRPC channel.
//
// The game does not start with gRPC. It first speaks ordinary JSON over HTTPS
// to two host families and then holds a third connection open indefinitely:
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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/n-popescu/switchnet/internal/nintendo"
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
	recordDir     string
	now           func() time.Time

	// seq numbers recorded exchanges, so the transcript reads in the order the
	// console produced it rather than in filesystem order.
	seq atomic.Uint64

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
	if opts.RecordDir != "" {
		if err := os.MkdirAll(opts.RecordDir, 0o700); err != nil {
			return nil, fmt.Errorf("nplnrest: record dir: %w", err)
		}
	}
	return &Server{
		log:           opts.Log,
		frontlineHost: opts.FrontlineHost,
		recordDir:     opts.RecordDir,
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
		host := hostOnly(r.Host)

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
// TODO(capture-needed): the request shape, the response shape, and — most
// importantly — THE FIELD NAME the frontline FQDN is returned under. That last
// one is the reason this host matters more than vermillion: the console learns
// where to open its persistent connection from this response, and a name it
// cannot find leaves it with no frontline at all.
//
// The FQDN is sent under several plausible spellings at once. That is not
// hedging for its own sake: they are additional keys in a JSON object, a parser
// that wants one of them finds it, and a parser that wants none of them is no
// worse off than with an empty object. The alternative is picking one spelling
// and having the console silently not find it — which is indistinguishable from
// every other failure in this path.
//
// Change: this function, and it collapses to a single field the moment a
// capture shows which one.
func (s *Server) penne(w http.ResponseWriter, r *http.Request) {
	s.answer(w, r, http.StatusOK, map[string]any{
		"host":           s.frontlineHost,
		"fqdn":           s.frontlineHost,
		"frontline_fqdn": s.frontlineHost,
		"frontlineFqdn":  s.frontlineHost,
	})
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

	s.record(r, nil, http.StatusOK, "frontline: held open")

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
	s.log.Info("nplnrest: frontline connection open", "host", hostOnly(r.Host), "open", open)
	defer func() {
		remaining := s.frontlines.Add(-1)
		s.log.Info("nplnrest: frontline connection closed by the console",
			"host", hostOnly(r.Host), "open", remaining)
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

// ---- answering and recording ----------------------------------------------

// maxRecordedBody caps how much of a request body is read, so a console (or
// anything else reaching this port) cannot make the server allocate without
// limit.
const maxRecordedBody = 1 << 20

func (s *Server) answer(w http.ResponseWriter, r *http.Request, status int, body any) {
	encoded, err := json.Marshal(body)
	if err != nil {
		s.log.Error("nplnrest: encode response", "error", err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	s.record(r, encoded, status, "")

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}

// record logs one exchange, and writes it to RecordDir when one is configured.
//
// Always logs; only writes when recording. The log line is what an operator
// reads while a console is booting; the file is what gets written up under
// docs/CAPTURES/ afterwards.
func (s *Server) record(r *http.Request, response []byte, status int, note string) {
	body, truncated := readBody(r)

	s.log.Info("nplnrest: request",
		"host", hostOnly(r.Host), "method", r.Method, "path", r.URL.Path,
		"query", r.URL.RawQuery, "bytes", len(body), "truncated", truncated,
		"status", status, "note", note)

	if s.recordDir == "" {
		return
	}

	n := s.seq.Add(1)
	// Both the host and the path are attacker-controlled -- the Host header is
	// whatever reaches the port -- so both are slugged. A separator or a
	// traversal surviving into either would write outside the directory.
	name := fmt.Sprintf("%04d-%s-%s.txt", n, slug(hostOnly(r.Host)), slug(r.URL.Path))
	f, err := os.OpenFile(filepath.Join(s.recordDir, name),
		os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		s.log.Error("nplnrest: record", "error", err)
		return
	}
	defer f.Close()

	fmt.Fprintf(f, "# %s\n", s.now().UTC().Format(time.RFC3339Nano))
	fmt.Fprintf(f, "%s %s HTTP/%d.%d\n", r.Method, r.URL.RequestURI(), r.ProtoMajor, r.ProtoMinor)
	fmt.Fprintf(f, "Host: %s\n", r.Host)
	for _, key := range sortedHeaderKeys(r.Header) {
		for _, v := range r.Header[key] {
			// Bearer tokens and device credentials go through these hosts. The
			// value is replaced rather than the header dropped, because WHICH
			// credentials the console presents is part of what a capture is
			// for; their contents are not, and a capture written up in
			// docs/CAPTURES/ has to be redactable without re-reading it.
			if isSensitiveHeader(key) {
				fmt.Fprintf(f, "%s: <redacted, %d bytes>\n", key, len(v))
				continue
			}
			fmt.Fprintf(f, "%s: %s\n", key, v)
		}
	}
	fmt.Fprintf(f, "\n%s\n", hexdump(body))
	if truncated {
		fmt.Fprintf(f, "\n# body truncated at %d bytes\n", maxRecordedBody)
	}
	if note != "" {
		fmt.Fprintf(f, "\n# %s\n", note)
	}
	if response != nil {
		fmt.Fprintf(f, "\n--- response %d ---\n%s\n", status, response)
	}
}

// readBody reads a request body up to the cap, and restores it so a handler
// after this one can still read it.
func readBody(r *http.Request) (body []byte, truncated bool) {
	if r.Body == nil {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRecordedBody+1))
	if err != nil {
		return body, false
	}
	if len(body) > maxRecordedBody {
		return body[:maxRecordedBody], true
	}
	return body, false
}

func hostOnly(host string) string {
	if h, _, ok := strings.Cut(host, ":"); ok {
		return h
	}
	return host
}

// slug turns an untrusted string into one filename component.
//
// An allowlist, and a deliberately narrow one: letters, digits and a hyphen.
// A dot is excluded even though it reads better, because allowing it means
// reasoning about ".." — and a rule that needs reasoning about is a rule that
// eventually gets it wrong. Bounded, too, because many filesystems cap a
// component at 255 bytes and the input is whatever reached the port.
func slug(s string) string {
	s = strings.Trim(s, "/")
	if s == "" {
		return "root"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if len(out) > 80 {
		out = out[:80]
	}
	return out
}

func isSensitiveHeader(key string) bool {
	switch http.CanonicalHeaderKey(key) {
	case "Authorization", "Cookie", "Set-Cookie", "X-Nintendo-Denebedgetoken":
		return true
	}
	return false
}

func sortedHeaderKeys(h http.Header) []string {
	out := make([]string, 0, len(h))
	for k := range h {
		out = append(out, k)
	}
	// Deterministic, so two recordings of the same request diff cleanly.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// hexdump renders bytes as offset, hex and printable ASCII.
//
// A hexdump rather than a string, because these bodies are not known to be
// text. The frontline frames certainly are not, and a body written as a Go
// string would silently mangle every byte above 0x7f in exactly the capture
// somebody is relying on.
func hexdump(b []byte) string {
	if len(b) == 0 {
		return "# empty body"
	}
	var out strings.Builder
	for off := 0; off < len(b); off += 16 {
		end := off + 16
		if end > len(b) {
			end = len(b)
		}
		line := b[off:end]

		fmt.Fprintf(&out, "%08x  ", off)
		for i := 0; i < 16; i++ {
			if i < len(line) {
				fmt.Fprintf(&out, "%02x ", line[i])
			} else {
				out.WriteString("   ")
			}
			if i == 7 {
				out.WriteByte(' ')
			}
		}
		out.WriteString(" |")
		for _, c := range line {
			if c >= 0x20 && c < 0x7f {
				out.WriteByte(c)
			} else {
				out.WriteByte('.')
			}
		}
		out.WriteString("|\n")
	}
	return strings.TrimRight(out.String(), "\n")
}
