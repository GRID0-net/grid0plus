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
// Stage 1 of the experiment: **does the console reach us at all?** Flip the
// three `bcat-*` hosts in internal/nintendo/hosts.go from ServiceBlackhole to
// ServiceStubs, regenerate the DNS and nginx configuration, and open Splatoon 3.
// If requests arrive here, the paths are right and DNS and TLS work — and the
// resulting log, with `stubs.bcat_record_dir` set, is the first record anyone has
// of what the console actually asks for.
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
// Field NAMES are confirmed; field SEMANTICS mostly are not -- what `mode`'s
// values mean, what `deletion_priority` orders, what `conditions.serial[*]`
// matches against, are all still open. `news_id` also appears alongside
// `data_list`'s other fields in the same decompile, but that is System
// News's own use of the same delivery-cache mechanism bleeding into the
// same string table, not a Splatfest-relevant field.
//
// TODO(capture-needed): field semantics above, and the delivery container's
// header layout (still only known as 0x120 bytes with an RSA-2048 signature
// at offset 0x20) -- the JSON list response and the binary container it
// points `url` at are two separate things this decompile did not equally
// resolve. Change: this file, and TODO.md §2.4's second stage becomes
// writable for the JSON half regardless.
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
// content. That is why the hosts stay null-routed by default: null-routing fails
// immediately, and a server that answers without delivering anything risks
// turning that into a wait.
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
