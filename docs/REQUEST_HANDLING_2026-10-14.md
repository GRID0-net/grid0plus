# Request handling and concurrency, 2026-10-14

This pass starts at `8462e6e`. It improves backend request handling without
changing matchmaking sizes, grace periods, scoring rules, authentication,
client patches, or proxy framing. It does not establish that distant players
can now complete an eight-player match.

## Reproduced defects

- A Gamesync reader could retain a document after the store released its lock.
  A later write mutated that same document and field map while the reader
  serialized it. `TestDocumentReadersKeepAStableCommittedVersion` both failed
  its version assertion and reported data races on the original source.
  Writes now replace the document shell and field index, retaining immutable
  unchanged field values. Atomic batches still hold the room's store lock.
- `observeRefereeReports` took the server-wide report lock even for ordinary
  lobby and Pia writes. A blocked history callback stalled unrelated rooms.
  `TestASlowHistoryWriteDoesNotBlockAnotherRoomsWrites` reproduced the stall
  before the change. Only reports take the durable-history lock now, and
  referee merging and history recording each serialize per room.
- Presence KeepAlive updated PostgreSQL on every ping, ahead of acknowledging
  it. Its 50 ms reply floor permits about 20 updates/second/player. Liveness
  persistence now runs at most once per ten seconds per stream, or a quarter
  of the configured console TTL when shorter. Failed writes remain retryable.
  Every ping is still acknowledged; authenticated RPCs still verify ownership
  and revocation and update liveness as before.

## Hot paths

Readiness checks test profile existence instead of cloning its content.
Watch subscribers are indexed by verified game session, and document target
matching precedes recipient-specific projection. Collection listing sorts
only matching documents, preserving the same lexicographic result order.

Same-machine A/B benchmarks: Go 1.24.7, Apple M2, `-cpu=1`, three runs,
median values. These are synthetic component costs, not whole-server latency.

| Workload | Before | After | Ratio | Allocation before/after |
| --- | ---: | ---: | ---: | ---: |
| Startup readiness, 32 KiB profile | 7.094 us | 0.658 us | 10.8x | 33,848 / 592 B |
| Eight-player roster amid 2,048 retained documents | 234.209 us | 19.963 us | 11.7x | 41,080 / 176 B |
| One-room update with 128 rooms of eight subscribers | 17.431 us | 1.214 us | 14.4x | 9,568 / 160 B |

At the default 300-second TTL, sustained KeepAlive liveness writes decline
from about 20/s/player to 0.1/s/player (200-fold reduction on this one path).
Stream-open writes and other authenticated traffic still write separately.
The live-ping regression acknowledges twelve successive pings and observes
only two writes: stream opening and first ping. Short TTL and failed-write
retry regressions also pass.

## Validation

- Changed Gamesync and NPLN suites passed with the race detector.
- Real gRPC transport tests exercised 2, 4, and 8 concurrent players, each
  sending six batches to every other player. Payloads are 32 KiB and one
  receiver drains with a 10 ms delay. All sender/recipient identities,
  packet counts and payload markers survive. Tests repeated three times
  under the race detector; this is backpressure testing, not simulated WAN
  RTT or a verified Pia handshake.
- Eight simultaneous result reports retain all eight personal results.
  Existing ownership, transaction scope, kickoff readiness, replay and
  explicit overflow checks remain in the suite.
- Full local ordinary and race suites passed except six unchanged NAT-check tests:
  macOS cannot bind the unconfigured `127.0.0.2` loopback address. The
  original source fails those tests identically. No tests used production
  PostgreSQL/Redis; integration checks needing those stores are skipped.
- `go vet ./...`, formatting, and all-package compilation passed locally.
  Linux ARM64 NPLN compilation also passed.

## Measuring a hardware retry

NPLN and Gamesync unary completion logs now carry `duration_us`, measured
from before authentication through handler completion. Existing log cadence
and protocol responses are preserved. Backend time excludes WAN travel and
does not measure long-lived stream send stalls.

On grid0, after a 4/8-player attempt:

```sh
cd /opt/switchnet
python3 scripts/request-latency.py --since '15 minutes ago'
```

The report prints counts, errors, p50/p95/p99/max per RPC without credentials
or player identifiers. Compare the same game action with two and then four
or eight players. Correlate slow backend calls with the console's actual
failure time; a quiet, fast backend alongside a stalled peer connection
requires a separate network/client investigation.

The VM has one available CPU core and about 5 GiB available RAM in the initial
snapshot. Its cloud keepalive uses `Nice=19`, `CPUWeight=1`; it was preserved.
No proxy buffer/window changes were justified by this pass. gRPC flow control
may block a streaming send for a slow receiver; see the
[official flow-control guide](https://grpc.io/docs/guides/flow-control/).
