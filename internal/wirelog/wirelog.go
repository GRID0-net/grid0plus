// Package wirelog records the HTTP requests a console makes, so that an
// undocumented protocol becomes a transcript instead of a guess.
//
// Several parts of GRID0+ answer hosts whose request and response shapes are
// not documented anywhere: NPLN's JSON front end (internal/nplnrest) and the
// BCAT probe (internal/bcat). CLAUDE.md rule 6 says to implement the most
// defensible reading and say what a capture would settle — and the cheapest way
// to make that capture happen is for the server to write it while a console is
// talking to it.
//
// So: point a Recorder at a directory, put a console through one boot, and every
// request and response lands on disk hexdumped, in order, with credentials
// redacted. That transcript is what gets written up under docs/CAPTURES/.
//
// Recording is off unless a directory is configured, because it writes whatever
// the console sends.
package wirelog

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// Recorder logs requests, and writes a transcript when one is wanted.
type Recorder struct {
	log *slog.Logger
	dir string
	now func() time.Time

	// seq numbers exchanges, so the transcript reads in the order the console
	// produced it rather than in filesystem order.
	seq atomic.Uint64
}

// New builds a recorder. An empty dir logs without writing anything.
func New(log *slog.Logger, dir string, now func() time.Time) (*Recorder, error) {
	if log == nil {
		log = slog.Default()
	}
	if now == nil {
		now = time.Now
	}
	if dir != "" {
		// 0700: the transcript holds whatever a console sent, including request
		// bodies nobody has vetted.
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("wirelog: %s: %w", dir, err)
		}
	}
	return &Recorder{log: log, dir: dir, now: now}, nil
}

// Recording reports whether a transcript is being written.
func (r *Recorder) Recording() bool { return r.dir != "" }

// maxBody caps how much of a request body is read, so a client cannot make the
// server allocate without limit.
const maxBody = 1 << 20

// Record logs one exchange, and writes it to the transcript directory when one
// is configured.
//
// Always logs, because the log line is what an operator reads while a console is
// booting. The file is what gets written up afterwards.
func (r *Recorder) Record(req *http.Request, response []byte, status int, note string) {
	body, truncated := readBody(req)

	r.log.Info("request",
		"host", HostOnly(req.Host), "method", req.Method, "path", req.URL.Path,
		"query", req.URL.RawQuery, "bytes", len(body), "truncated", truncated,
		"status", status, "note", note)
	r.writeTranscript(req, response, status, note, body, truncated, false)
}

// RecordStream records request metadata without touching the body. It is for
// long-lived or streaming requests whose body must remain owned by the handler.
func (r *Recorder) RecordStream(req *http.Request, status int, note string) {
	r.log.Info("request stream",
		"host", HostOnly(req.Host), "method", req.Method, "path", req.URL.Path,
		"query", req.URL.RawQuery, "content_length", req.ContentLength,
		"transfer_encoding", strings.Join(req.TransferEncoding, ","),
		"status", status, "note", note)
	r.writeTranscript(req, nil, status, note, nil, false, true)
}

func (r *Recorder) writeTranscript(req *http.Request, response []byte, status int, note string, body []byte, truncated, bodyOmitted bool) {
	if r.dir == "" {
		return
	}

	n := r.seq.Add(1)
	// Both the host and the path are attacker-controlled -- the Host header is
	// whatever reaches the port -- so both are slugged. A separator or a
	// traversal surviving into either would write outside the directory.
	name := fmt.Sprintf("%04d-%s-%s.txt", n, Slug(HostOnly(req.Host)), Slug(req.URL.Path))
	f, err := os.OpenFile(filepath.Join(r.dir, name),
		os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		r.log.Error("wirelog: write transcript", "error", err)
		return
	}
	defer f.Close()

	fmt.Fprintf(f, "# %s\n", r.now().UTC().Format(time.RFC3339Nano))
	fmt.Fprintf(f, "%s %s HTTP/%d.%d\n", req.Method, req.URL.RequestURI(),
		req.ProtoMajor, req.ProtoMinor)
	fmt.Fprintf(f, "Host: %s\n", req.Host)
	if len(req.TransferEncoding) != 0 {
		fmt.Fprintf(f, "Transfer-Encoding: %s\n", strings.Join(req.TransferEncoding, ", "))
	}
	if req.ContentLength >= 0 {
		fmt.Fprintf(f, "Content-Length: %d\n", req.ContentLength)
	}
	for _, key := range sortedHeaderKeys(req.Header) {
		for _, v := range req.Header[key] {
			// Bearer tokens and device credentials go through these hosts. The
			// value is replaced rather than the header dropped, because WHICH
			// credentials the console presents is part of what a capture is
			// for; their contents are not, and a capture written up under
			// docs/CAPTURES/ has to be redactable without re-reading it.
			if sensitiveHeader(key) {
				fmt.Fprintf(f, "%s: <redacted, %d bytes>\n", key, len(v))
				continue
			}
			fmt.Fprintf(f, "%s: %s\n", key, v)
		}
	}
	if bodyOmitted {
		fmt.Fprintln(f, "\n# request body omitted: streaming body remains owned by the handler")
	} else {
		fmt.Fprintf(f, "\n%s\n", Hexdump(body))
	}
	if truncated {
		fmt.Fprintf(f, "\n# body truncated at %d bytes\n", maxBody)
	}
	if note != "" {
		fmt.Fprintf(f, "\n# %s\n", note)
	}
	if response != nil {
		fmt.Fprintf(f, "\n--- response %d ---\n%s\n", status, Hexdump(response))
	}
}

// readBody reads a request body up to the cap.
func readBody(r *http.Request) (body []byte, truncated bool) {
	if r.Body == nil {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return body, false
	}
	if len(body) > maxBody {
		return body[:maxBody], true
	}
	return body, false
}

// HostOnly strips the port from a Host header.
func HostOnly(host string) string {
	if h, _, ok := strings.Cut(host, ":"); ok {
		return h
	}
	return host
}

// Slug turns an untrusted string into one filename component.
//
// An allowlist, and a deliberately narrow one: letters, digits and a hyphen. A
// dot is excluded even though it reads better, because allowing it means
// reasoning about ".." — and a rule that needs reasoning about is a rule that
// eventually gets it wrong. Bounded too, because many filesystems cap a
// component at 255 bytes and the input is whatever reached the port.
func Slug(s string) string {
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

func sensitiveHeader(key string) bool {
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
	sort.Strings(out)
	return out
}

// Hexdump renders bytes as offset, hex and printable ASCII.
//
// A hexdump rather than a string, because these bodies are not known to be text.
// BCAT's container certainly is not, nor are NPLN's frontline frames, and a body
// written as a Go string would silently mangle every byte above 0x7f in exactly
// the capture somebody is relying on.
func Hexdump(b []byte) string {
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
