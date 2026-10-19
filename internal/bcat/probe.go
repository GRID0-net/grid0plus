package bcat

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/n-popescu/grid0plus/internal/wirelog"
)

// Probe answers the three bcat-* hosts, logging everything the console asks for.
//
// GRID0+ serves BCAT natively (native.go). The probe stays as a diagnostic that
// records what a console asks a bcat-* host, for a title or firmware not seen
// before. With `stubs.bcat_record_dir` set, every request the stubs service
// receives on those hosts is also recorded (Observe) and hexdumped, without
// changing the answer, which stays 304.
//
// The `/api/nx/v1/list/<name>` response body is a JSON document with this shape:
//
//	{
//	  "directories": [{
//	    "name": "...", "mode": "...", "digest": "...",
//	    "data_list": [{
//	      "data_id": "...", "filename": "...", "digest": "...", "size": N,
//	      "url": "...", "default_language": "...", "deletion_priority": N,
//	      "publishing_time": N, "version": {"format": N, "semantics": N},
//	      "conditions": {"serial": [...]},
//	      "languages": [{
//	        "language": "...", "data_id": "...", "size": N, "url": "...",
//	        "overwrite": ..., "digest": "..."
//	      }]
//	    }]
//	  }]
//	}
//
// The list body is itself a `bcat` container whose decrypted payload is
// MessagePack. The module verifies an RSA-2048 signature over each decrypted
// container and refuses on failure, so serving GRID0+'s own containers needs a
// firmware patch to skip the check (never a forged signature). See
// docs/SPLATFESTS.md.
//
// UNCONFIRMED: what `mode`'s values mean, what `conditions.serial` matches, and
// which hash the 16-byte `digest` is. Change: this file.
type Probe struct {
	log *slog.Logger
	rec *wirelog.Recorder
}

// NewProbe builds the probe handler.
//
// recordDir is where the transcript is written. Empty logs without writing,
// which is enough to answer "did the console reach us" but not enough to write
// a capture up afterwards.
func NewProbe(log *slog.Logger, recordDir string, now func() time.Time) (*Probe, error) {
	if log == nil {
		log = slog.Default()
	}
	rec, err := wirelog.New(log.With("component", "bcat-probe"), recordDir, now)
	if err != nil {
		return nil, err
	}
	return &Probe{log: log, rec: rec}, nil
}

// Observe logs and records a request that something else is answering, with
// the status it was given. This is how stage 1 runs day to day: the stubs
// service keeps answering 304 (anything else risks the 2122-2403 hard error)
// and the probe only watches.
func (p *Probe) Observe(r *http.Request, status int, note string) {
	p.log.Info("bcat: the console asked BCAT for something",
		"host", wirelog.HostOnly(r.Host), "method", r.Method,
		"path", r.URL.Path, "query", r.URL.RawQuery, "answered", status,
		"recording", p.rec.Recording())
	p.rec.Record(r, nil, status, note)
}

// ServeHTTP logs the request and answers 200 with an empty body.
//
// An empty 200 rather than an error, and rather than a fabricated container.
//
// An error would tell the console the server is broken, and it may then stop
// asking — which loses the only thing this handler exists to collect. A
// fabricated container would be worse: the console would reject it for a reason
// nobody can name, and the experiment's result would be uninterpretable. An
// empty body is the one answer that is honestly "nothing here" while still
// completing the request.
//
// **Not a substitute for serving BCAT.** A console pointed at this gets no
// content, and an empty 200 where it expected a list risks a hang; day to day
// the hosts answer 304 instead and the probe only Observes.
func (p *Probe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Warn, not info: every line here is a request nobody has documented, and
	// the operator running this experiment is watching for exactly them.
	p.log.Warn("bcat: the console asked BCAT for something",
		"host", wirelog.HostOnly(r.Host), "method", r.Method,
		"path", r.URL.Path, "query", r.URL.RawQuery,
		"recording", p.rec.Recording())

	p.rec.Record(r, nil, http.StatusOK,
		"bcat probe: answered 200 with an empty body; see internal/bcat.Probe")

	// No content type. Claiming one would be claiming to know what the console
	// expects, which is the whole open question.
	w.WriteHeader(http.StatusOK)
}
