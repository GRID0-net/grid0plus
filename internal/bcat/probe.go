package bcat

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/n-popescu/switchnet/internal/wirelog"
)

// Probe answers the three bcat-* hosts, logging everything the console asks for.
//
// # What this is for
//
// Nobody has documented BCAT's `list` response body, and no public
// implementation exists — so SwitchNet builds the delivery cache directly
// instead of serving BCAT (see the package README). Whether it *could* serve
// BCAT is an open question with a cheap experiment behind it, recorded in
// TODO.md §2.4 and docs/QUESTIONS.md, and this handler is that experiment's
// server.
//
// Stage 1 of the experiment: **what does the console ask for?** The three
// `bcat-*` hosts already reach the stubs service, which answers 304. With
// `stubs.bcat_record_dir` set, every one of those requests is also recorded
// here (Observe), hexdumped, without changing the answer. That transcript is
// the first record anyone has of the list/data paths the console requests.
//
// Stage 2 — answering with a container that has a deliberately invalid signature,
// to find out whether the console verifies one — is **not** built, and cannot
// honestly be. It needs the container's header shape to be right so that a
// refusal means "the signature was checked" rather than "the header was wrong",
// and the only public fact about that header is that it is 0x120 bytes with an
// RSA-2048 signature at offset 0x20. Stage 1's hexdumps are how the rest gets
// known. Building it from a guess would produce an experiment whose negative
// result means nothing.
//
// The `/api/nx/v1/list/<name>` response body is now confirmed, not guessed:
// predicted below (and in the git history of this comment) that settling it
// would need decompiling `bcat.nso` directly rather than any single game,
// since Splatoon 3 only calls the generic SDK
// (`nn::bcat::MountDeliveryCacheStorage`, `RequestSyncDeliveryCache`,
// `EnumerateDeliveryCacheDirectory`, `DeliveryCacheFile::Open`/`Read`) and
// never anything resembling `/api/nx/v1/list` or a signature check -- that
// whole exchange happens inside the OS's own `bcat` sysmodule. A 2026-09-20
// decompile of `bcat.nso` itself (extracted from the SYSTEM partition of the
// operator's own eMMC dump, title id `010000000000000C`) found the full
// JSONPath field list its own response parser reads:
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
// Field NAMES are confirmed; most field SEMANTICS are not, but
// `deletion_priority` is a partial exception: the same decompile's System
// News applet (which shares this delivery-cache mechanism -- see `news_id`
// below) keeps its own local cache in a SQLite table with a
// `deletion_priority` column, evicted `ORDER BY deletion_priority DESC,
// received_at ASC` -- lower priority is evicted first, oldest-received
// breaks ties. Whether Splatfest's own client-side cache eviction follows
// the identical rule is not directly confirmed (News's schema is News's own
// code, not bcat's), but it is the same field name feeding the same
// mechanism, so this is a strong lead rather than a guess. `mode`'s values
// and what `conditions.serial[*]` matches against are still fully open.
// `news_id` appears alongside `data_list`'s other fields in the same
// decompile because News shares this same string table -- not a
// Splatfest-relevant field.
//
// One more open question the same decompile raises rather than settles: the
// local cache path `bcat-dl:/applications/%016llx/list.msgpack.bafmeta`
// names the cached list as MessagePack, not JSON. The field names above are
// still real regardless of encoding -- a msgpack map still has string keys,
// and the JSONPath strings are the client's own generic field-lookup syntax,
// not proof of wire format -- but whether the actual HTTP response body is
// JSON text or msgpack-encoded is unconfirmed. `bcat-dl:` is a local mount,
// not a network trace, so this decompile cannot tell the two apart; a
// capture of the real HTTP exchange (Content-Type header alone would settle
// it) is the only way to know which this server should actually answer.
//
// TODO(capture-needed): field semantics above, the JSON-vs-msgpack wire
// encoding question, and the delivery container's header layout (still only
// known as 0x120 bytes with an RSA-2048 signature at offset 0x20) -- the
// list response's wire encoding and the binary container its `url` field
// points at are two separate things this decompile did not equally resolve.
// Change: this file, and TODO.md §2.4's second stage becomes writable for
// the field-shape half regardless of which encoding turns out to be real.
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
