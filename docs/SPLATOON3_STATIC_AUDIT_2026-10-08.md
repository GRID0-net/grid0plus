# Splatoon 3 static audit — 2026-10-08

This audit is not a declaration that a particular hardware failure is solved.
The follow-up implementation status below records changes separately. Findings below
distinguish code defects, reference differences, and unconfirmed client paths.

## Inputs and reproducibility

- GRID0+ local main: `68adecdd723f9881525039b44d273cbe475c0e20`.
- Fresh Nextendo checkout: `1d6c062acc47656cd1a7edddecc34bb8bc06fa14`,
  at `/tmp/grid0-s3-static-reference`. The older reference in Documents is
  `a526c454b15a616112d7573faa1fd63b3c0bb214`.
- Game: the existing v11.3.0 dump, build ID
  `28C4287AEE36F7499DA60F3E68B54C70DA382D75`; flat image
  `/home/nico/switchnet-re/main.flat`, virtual base `0x7100000000`.
- Direct instruction reads used the existing
  `session-20261004/tools/dis.py` (llvm-mc). Existing decompilations were
  cross-checked where relevant; no new full Ghidra analysis was run.
- A fresh byte scan found **144 full NPLN RPC path literals** in the game.
  Inventory: `docs/audits/splat3-rpc-inventory-2026-10-08.json`.
  A literal proves a compiled stub exists, not that gameplay calls it.

Public references:

- [Current Nextendo source](https://github.com/NextendoNetwork/splatoon-3/tree/1d6c062acc47656cd1a7edddecc34bb8bc06fa14)
- [Kinnay NPLN server documentation](https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers)
- [Kinnay NPLN protobufs](https://github.com/kinnay/NPLN-Protocols)

Only behavioral facts were used from Nextendo; no implementation or captured
Nintendo payload was copied into this report or the server.

## Client referee: independently checked transition gates

At `0x71014fe61c`, the callback stores the received status at object+`0x444`,
adds one, bounds-checks it, then dispatches through a halfword-offset jump
table at `0x71050b82f2`, relative to `0x71014fe64c`.

| Wire status | Branch target | Accepted transition of object+0x440 |
| --- | --- | --- |
| -1 | 0x71014fea64 | state < 2 becomes 2 |
| 0 | 0x71014fea78 | only 3 becomes 4 |
| 1 | 0x71014fea8c | only 5 becomes 6 |

The final-result path runs when state is 6 (`0x71014fe68c` onward). It reads
`personal_result` (`0x71014fe6c4`, string `0x7104f27155`), walks its entries,
reads `npln_user_id` (string `0x7104f1cb3d`), compares against a local identity,
and reads `deemed_result` (string `0x7104f087a6`, call at `0x71014fe7e0`).
The accepted deemed-result range is -1 through 3. This establishes actual
client consumption of these fields; it does not prove the full upstream
recipient-filtering policy is required in every mode.

The already documented kickoff timeout is supported by existing decompilation
`session-20261003/re/gs/7103898e98.c`: it errors after the timer exceeds 1800.
The Again/backfill wait compares against float 1920 at `0x71038a59b8`.
Those are frame-style counters in prior analysis; wall-clock estimates assume
60 updates per second, not an unconditional real-time guarantee.

**Implication:** verdict timing and retries matter, as well as contents.
An early status 0 or 1 does not automatically advance the referee.

## Confirmed differences and code defects

### 1. Final verdict delivery is not recipient-aware

GRID0+: `internal/gamesync/referee_verdict.go:218` sets status 1 from the
first complete report and `:239` broadcasts that document to all subscribers
in the game session. The watcher does not tailor it by recipient.

Nextendo: `gamesync_result_delivery.go`, `resultForRecipient`, returns a
status-0 copy of a final VS result until that recipient's npln_user_id exists
in personal_result. It is used in collection snapshots, document reads and
live delivery (`gamesync.go:2122`, `:2591`, `:2985`).

This is a substantive difference around the client result gate above.
It can affect results/rematches; it is not evidence for a pre-match failure.
Validate with two staggered final reports and two independently authenticated
watchers before changing production delivery.

### 2. Report ownership and per-player result provenance are not enforced

GRID0+ authenticates Gamesync and scopes the store by game session, but
WriteDocuments/LazyWriteDocuments/CommitTransaction do not validate the owner
of a `docs/@@RefereeReport/<battle>/User/<user-session>` path. The verdict
merges all entries supplied in personal_result. `observeRefereeReports`
also does not check that the battle suffix belongs to the caller's session,
although `publishRefereeVerdicts` does.

Nextendo's current write authorization binds the report path to the caller,
and `personalResultForReporter` accepts only the authenticated reporter's
own entry, refusing a contradictory npln_user_id. It also explicitly checks
peer-document writes, with narrowly defined peer-state exceptions.

Authenticated identity alone therefore does not provide equivalent document
ownership protection in GRID0+. This is both a correctness and isolation gap.
Any fix must preserve legitimate Pia peer-state writes instead of banning
all cross-peer paths indiscriminately.

### 3. Post-write behavior differs between RPCs

`WriteDocuments` calls both `resendKickoffVerdictAfterProfile` and `noteAgain`.
`LazyWriteDocuments` (`gamesync.go:612–620`) and `CommitTransaction`
(`:671–685`) publish documents/verdicts and record reports without these hooks.
The lazy path also skips room-document seeding.

The same logical profile update consequently has different side effects
depending on the transport operation. Periodic verdict resend partly mitigates
the immediate-resend omission, so this should not be called a proven timeout
cause. The binary contains all three RPC stubs; which path is used for a
particular action still needs capture evidence.

### 4. Transactions are bound only to the account

`transaction` stores userID and startedAt (`gamesync.go:626`), not game-session
or user-session identity. Commit checks userID, then applies to the committing
caller's current store. A transaction begun in room A can therefore be committed
in room B by that same account if it retains the token. Transaction deferments
are always rejected as Unimplemented, unlike ordinary write deferments.

Bind the transaction to its original scope and test cross-room rejection.
This is a server code defect, not a demonstrated Splatoon matchmaking cause.

### 5. Again timer restart can race an already running callback

`again_queue.go:67` stops an old timer but does not check whether its callback
has started. An old callback can acquire the mutex after a replacement timer
was stored, delete that replacement (`:78`), and lock the session early (`:81`).
Use a generation or deadline check in the callback. Also, the handler notices
only UpdateDocument, not MergeDocument. The existing bfmin-0 experiment remains
explicitly unconfirmed and previously did not fix hardware behavior.

### 6. Battle histories are built from the latest individual report

`observeRefereeReports` passes the committed individual report as Fields and
the accumulated verdict separately. `RecordVsResults` and `RecordCoopResults`
build personal rows from report.personal_result, not the merged verdict's
personal_result. VS writes each report's rows to every listed participant's
history; subsequent reports can replace those rows with different/incomplete
personal data. This becomes especially relevant if provenance is tightened
to one reporter per write, as in current Nextendo.

Use accumulated verified personal results for history assembly and ensure
retries cannot double-count point cards or fest contributions. Those systems
have their own deduplication paths; do not replace them blindly.

## Feature coverage beyond Gamesync

| Area | GRID0+ coverage | Difference / limitation |
| --- | --- | --- |
| Auth | Prearranged, IssueToken, refresh, validation | Nextendo additionally has typed CreateUser and IssueAnonymousUserToken handlers; GRID0+ inherits Unimplemented. Both stubs are in the game; no active call proven here. |
| Friends/presence | Registered services, live friend and presence paths | Present at subsystem level; this pass does not establish byte-for-byte parity for every event. |
| Matchmaking/private rooms | Tickets, tracking, creation, joining, delegation, ICE, roster reads | Backfill into a match by an outsider is explicitly refused in GRID0+. Current Nextendo preserves ticket backfill metadata. QueryGameSessions is empty in both, so it is not itself a newly discovered mismatch. |
| Schedules | Generated VS/Coop, seasons, params, Big Run, Splatfest | Nextendo accepts validated operator Challenge schedule JSON. GRID0+ SelectLeagueSchedules is empty except for a private diagnostic replay path; no normal Challenge configuration. |
| Locker/Canola | Registration acknowledged, selects empty | Nextendo serves actual public locker/card documents from opted-in players. GRID0+ does not publish these to other players. |
| Ugcstore | GetDocument, queries, commits, field transforms, persistent documents | Client contains additional bulk/attachment/short-alias RPC stubs. Absence of typed methods is not enough to claim parity failure because Nextendo routes some through capture replay. Each requires a reachability/response check. |
| GameRecord | Attributes, challenge/power/tag handlers including recovered calls | Some response fields remain intentionally sparse/TODO. Nextendo has capture-backed dynamic handlers, so matching method names alone is insufficient. |
| Replay/CoopScenario | Resolve returns NotFound; replay reservations empty | Registration remains unimplemented in GRID0+. Do not mistake Nextendo's replayHandler (captured RPC responder) for a complete battle-replay feature. |
| Rankings/counters | Empty BrowseLeaderboard; Big Run GetSeason | Most leaderboard/globalcounter methods inherit Unimplemented. Many appear in the game because SDK stubs are linked; active feature use was not established. |
| BCAT/fests | Native cache serving, emulator delivery, fest build/activation and scoring | Both now have real BCAT/fest subsystems. End-to-end delivery/key/cache parity was not re-proven by this static pass. |

`SelectFestBreakingNews` is dynamically registered in GRID0+ despite lacking
a named Go RPC method. The initial handler-name scan correctly required manual
reconciliation; it must not be reported as missing.

## Recommended work order

1. Add two-player referee tests for recipient readiness and merged histories,
   then implement the corresponding independently written behavior.
2. Enforce report ownership and session suffix checks consistently across all
   write paths, preserving documented Pia exceptions.
3. Unify post-commit hooks, bind transaction scopes and remove the timer race.
4. Compare the captured failed action with the matching successful Nextendo
   action before selecting a matchmaking/backfill fix.
5. Treat public lockers/cards and timed Challenges as separate missing features,
   rather than possible fixes for an unrelated connection error.

This audit covers the main network/server subsystems and targeted referee/client
landmarks. It is not an exhaustive decompilation, an all-method wire comparison,
or proof that GRID0+ has everything Nextendo has. Raw capture folders, emulator
work and deployment state were left untouched.

## Follow-up implementation

The follow-up fixes retain the latest committed Pia inbox state per authenticated
sender and replay all senders when the recipient attaches a watch. Reconnecting
a watch keeps that state until the room session expires. The snapshot send queue
now applies cancellation-aware backpressure instead of dropping responses; the
live notification queue signals overflow explicitly rather than hiding lost updates.

Ordinary, lazy and transactional writes share the post-commit hooks. Lazy merge
writes also recognize Salmon Run reopening. Again timers have an identity check
under the queue mutex, so a stopped callback cannot lock a replacement window.
Transactions are bound to the verified game-session scope as well as the account.
Versus final verdicts remain pending for recipients whose own personal result
has not arrived, on reads, snapshots and live notifications; Coop is unchanged.

Regression tests drive real gRPC for multi-sender snapshots, large snapshots,
recipient verdicts and cross-room transaction rejection. An explicit queue-overflow
check and a stale-timer callback check cover the concurrency failure boundaries.

Remaining audit leads include report ownership/provenance, accumulated history,
and session retirement versus reconnect semantics. The existing ghost-roster
probe still reproduces the latter: closing the last stream does not retire a
member synthesized from the matchmaking roster. It is not safe to remove token
sessions indiscriminately: refreshed tokens and overlapping watches share one
user-session identity. A successful multi-console capture must validate retirement
and readiness behavior before claiming the private-match startup issue is solved.

### Validation and commits

- `219c447`: peer replay cache, committed-payload delivery, consistent hooks,
  complete initial watch snapshots and explicit live overflow.
- `a6d80c3`: Again timer generation and merge reopening.
- `aac351d`: transaction room scope and durable report scope guard.
- `923de36`: recipient-specific Versus final-result readiness.
- Full `make check`, full `make test-race`, `make build`, host generation and
  tracked-secret checks passed. The final Gamesync tree was also rerun under
  the race detector after the final edits. Socket-based full tests required
  execution outside the restricted sandbox; the first sandbox-only attempt
  failed to bind test listeners, not an application assertion.
- The raw capture folders were not changed. No successful private-match
  startup is claimed until consoles have exercised these changes together.
