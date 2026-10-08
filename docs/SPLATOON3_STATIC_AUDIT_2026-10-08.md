# Splatoon 3 static audit — 2026-10-08

This audit is not a declaration that a particular hardware failure is solved.
The follow-up implementation status below records changes separately. Findings below
distinguish code defects, reference differences, and unconfirmed client paths.

## Follow-up: retained lobby and rematch minimum

The installed `s3smallmatch_bypass` originally patched only RVAs `0x38a420c`,
`0x32c1c7c`, and `0x3898160`. Independent disassembly of the same v11.3.0
flat image found another minimum in the lobby controller:

- `0x389f4b4` loads 8, stored at controller+`0x214` by `0x389f4bc`.
- `0x389f748` calls the same participant-count helper (`0x15173d0`) used
  by the original three sites. `0x389f750` compares its result to that minimum;
  the less-than branch resets the stable-count counter at +`0x21c`.
- `0x389f75c` also requires the current count to equal the preceding count
  at +`0x218`. That separate stability comparison is preserved.
- `0x389f864` repeats the minimum check after an indirect backend query;
  `0x389f870` requires 120 stable updates, and `0x389f8b4` requires that
  backend count to agree with the participant-count helper before transition
  to state `0x18`. Neither barrier is removed.

Toolbox 0.8.1 changes the two minimum comparisons to `cmp w0, #2` and keeps
the original three patch records. The identity of the indirect backend query
and whether these sites are the live rematch blocker remain unverified on
hardware. This patch does not fake asynchronous JoinSession success: the
separate ActivateBackFill handler at `0x38a5800` still requires operation
status 3 (`0x38a5bb8`) and retains its 1920-update timeout (`0x38a59b8`).

Capture cross-check (original files unchanged): the preserved Nextendo
`nxm.out.jsonl` has one eight-user kickoff; today's failed GRID0 capture has
one one-user kickoff. The older `again.txt` has two two-user kickoffs. The
successful retry today has both profiles in server logs, unlike the failed
attempt. These observations do not establish that lowering lobby minima
fixes the intermittent peer connection failure.

GRID0's normal grace timer operates on waiting tickets. The rematch's
retained session bypasses that queue; the dashboard does not control it.
The separate Gamesync timer now handles public Versus too, deduplicates
callers' retries, keeps the first Versus deadline, restarts for an additional
Coop caller, and cancels on a client lock. The admin API now exposes a phase
from committed battle/session events rather than labelling retained sessions
as evidence of gameplay. End-to-end rematches need a later operator test.

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


## Wider server comparison and report-integrity follow-up

The structural inventory now covers all **80 non-test Go source files outside
Nextendo's generated proto tree**, including command helpers:
`docs/audits/nextendo-s3-source-inventory-2026-10-08.json`. It records source
hashes, functions and RPC literals against the fixed reference commit above.
This is an index for auditing, not evidence that every line or every wire path
has been independently validated.

| Reference area | GRID0 counterpart and outcome |
| --- | --- |
| Auth, token JWTs, account identity, bans and client IP history | `internal/npln`, `internal/baas`: signed caller identity and persistent accounts; Nextendo account-service integration and operator tooling are architecture differences. Never import identity fallbacks. Anonymous/bootstrap method coverage still differs. |
| Gateway, local transport, gRPC framing, capture replay and diagnostics | `internal/penne`, `internal/vermillion`, NPLN interceptors and `internal/splatdiag`: explicit live handlers rather than a generic capture-replay server. A replayed method in Nextendo is not automatically implemented in GRID0. |
| Friends, presence and attributes | `internal/npln/friends.go`, `presence_service.go`, `presence_attributes.go`: shared BAAS graph, empty-selector friend enumeration, visibility checks and stream-based attributes already present. Keepalive pacing matches preserved hall captures. |
| Matchmaking, parties, waiting thresholds, room/session addressing | `internal/matchmaking` and NPLN rooms/delegation: explicit party membership and configurable grace periods. Nextendo's relaxation policy is not the user's requested GRID0 policy. Successful startup/backfill/retirement still needs a complete multi-console trace. |
| Gamesync document/transaction, Pia mailboxes and result delivery | `internal/gamesync`: fixes above plus authenticated report ownership on ordinary, lazy, transactional and deferred writes. Whole-roster personal results retain only the reporter's entry, stamp its verified account and reject contradictory identities. |
| VS results and Coop point cards | `internal/ugcstore`: later individual reports now build history from accumulated verdict participants; durable report callbacks are serialized so stale snapshots cannot overwrite newer history. Individual report fields remain the input to scoring/deduplication. |
| Ugcstore absence, RunQuery, drawings and public plaza | Core document/query/absence paths exist. `IssueUploadUri` is still unimplemented; Canola/Locker `SelectDocuments` still return empty. Nextendo reads publicly published locker/card documents, capped at 31/6, rather than treating private save contents as a source of lockers. These are real feature gaps, not established matchmaking blockers. |
| Cloud saves, save administration and search | `internal/toyohr/cloud_save.go` and save store: live caller-owned delta merging and advancing versions exist. Both implementations receive a version timestamp but do not currently enforce optimistic concurrency; a reference comment alone is not proof of enforcement. Nextendo starter grants are operator policy, not protocol requirements. |
| VS rotations, Challenge schedules and rotation APIs | GRID0 generates deterministic slot-based rotations. Nextendo additionally accepts a validated operator JSON file for timed Challenges; GRID0 does not expose equivalent event configuration. Empty schedules differ from an actively configured Challenge, not from a missing normal rotation. |
| Fest schedule/keys, entries, teams, votes, midterm, final scoring, BCAT and news | GRID0 has separate event/BCAT publishing and scoring. The 90/45 vote, 70/35 popularity, 120/60 regular/challenge and 180/90 tricolor weights are already present. Nextendo's latest BCAT archive auto-discovery is a deployment mechanism, not permission to activate a cache-derived event. The removed fest remains disabled. |
| Dashboard, flags, stats and source utilities | GRID0's own dashboard/configuration and DB-backed tooling replace Nextendo's file-backed operators and debug helpers. Capture/key-log helpers are diagnostic tooling, not gameplay dependencies. |

The report regression tests reject another user's report through all four write
paths, preserve a transaction after a refused write, reject a contradictory
identity without a partial batch write, and verify that two successive personal
reports retain both players in VS and Coop histories. Full checks and race tests
passed; Gamesync/UGC race tests were repeated after the final regression was added.

Remaining limits: no claim of complete behavioral parity, full decompilation or
successful match startup. Missing upload/public-plaza/Challenge features need
separate implementations and validation. Session retirement and peer readiness
must be compared with a successful multi-console capture before changing them.

## Follow-up: Battle Time startup delay

Direct disassembly of v11.3.0 found a referee-state wait in
`0x71038985e8`. At `0x71038986a0`, referee state 1 selects the timer path;
`0x710389875c` compares the converted timer against 301, then
`0x7103898788` periodically calls `0x71014ffa90`. At
`0x710389878c` a 900 threshold selects a fallback to state 9 through
`0x7103898794`. Assuming 60 update units per second, these thresholds are
approximately 5 and 15 seconds. The timer's unit and mapping to the visible
BATTLE TIME card still need live confirmation; these are not instructions
that explicitly sleep for seconds.

The existing referee decompile and fresh instruction reads agree:
`0x71014fe4b0` maps response status -1 to state 2 when the current state is
below 2 (`0x71014fea64`); status 0 instead maps state 3 to 4
(`0x71014fea78`). GRID0 immediately emits status 0. An early intermediate
referee response is therefore a concrete investigation target, not permission
to fabricate status -1 without a wire or live-client check.

The preserved Nextendo match has kickoff at epoch 1790782358.7908537 and
profile at 1790782373.806565, a 15.016-second interval. The October 8 GRID0
failed match similarly has kickoff at 15:35:06.502664Z and profile at
15:35:21.516672Z, 15.014 seconds. A profile write is not a timestamp for
when the card disappears or loading starts. Both intervals show that the
long interval is also present in the reference; no synchronized screen
recording exists here to assign it entirely to the card.

Server timer review: normal queue grace precedes session delivery; the live
VM config has Versus grace 10 seconds and Coop grace 20 seconds. The rematch
Gamesync grace is 20 seconds. Track's optional minimum hold defaults to zero
and is not configured on the VM; synthetic PLACING adds one second only if
needed. Gamesync refresh republishes every three seconds. Referee publication
itself has no delayed timer. No timing bypass was added from this finding.

Reference source cross-check: Nextendo `gamesync.go` documents an official
capture with referee statuses -1, then 0, then 1. Its current arbitration
implementation nevertheless emits 0 for a request and 1 for a report, with
no intermediate -1 publication in that path. This corroborates the missing
early-phase hypothesis and explains why copying the reference's present
behavior would not distinguish this delay. The correct publication point
and transition timing remain open; a blind back-to-back -1/0 pair could be
coalesced or consumed before the game enters state 3.

Implementation follow-up: the client request builder at `0x71014feeb0`
sets referee state 1 at `0x71014ff2dc`; the profile builder at
`0x71014ff540` sets state 3. GRID0 now stores the initial verdict with
status -1 and projects status 0 only for an authenticated user session whose
exact `@@Profile/<battle>/User/<user-session>` document exists. Live watches,
initial snapshots and direct reads use the same projection. Profile submission
re-publishes the verdict immediately; no timer or readiness bypass is added.
Final report behavior remains status 1. Tests cover wire reads and live watches
for two recipients with different readiness. Hardware timing is still pending.

## Follow-up: Salmon Run lobby weapon versus wave-one assignment

The dashboard now uses the short label Salmon Run; Next Wave was Nintendo's
Splatoon 3 subtitle, not a distinct server mode. This presentation change is
committed locally and held from deployment at the operator's request.

No per-player wave weapon is synthesized by GRID0's referee path. The normal
schedule advertises four `main_weapons`, matching the reference's schedule
role; `publishRefereeVerdict` copies the client's job context. Normal uses
schedule kind 1, Big Run 2 and Eggstra Work 3. Do not randomize the schedule
per request: all players must see the same rotation. Eggstra Work permits
weapon selection, unlike the suspected normal-job issue; Nintendo describes
that exception at https://www.nintendo.com/au/news-and-articles/splatoon-3-the-first-eggstra-work-online-event-is-almost-here/.

Direct reads from v11.3.0 found `SelectedWeaponIndex` at 0x7104fc0cc7,
referenced at 0x7103505020, 0x71035051d4, 0x7103505240,
0x71035076ec and 0x710368eca0. The first reader stores a parameter at
object+0x3c; 0x710368eca0 registers a property at object+0x20. Neither is
yet proof of wave-one assignment. `CoopMainWeaponArray` at 0x7104fb620d
has readers at 0x7103301ef4/0x710330237c/0x710330295c. The `random_seed`
string at 0x7104fa26d0 has a reader at 0x7103327fc8; its role in the
specific normal-job assignment is not established. `job_id` is written by
the client at 0x71032c045c. These landmarks must not be treated as evidence
that changing one of these fields will safely shuffle wave weapons.

Open: a normal-job startup trace needs to distinguish selected practice
weapon, peer-agreed job state and the first-wave weapon, across more than
one run. Existing Versus startup captures cannot settle this. No weapon
assignment patch has been made from the observation alone. The preceding
early-referee fix is already deployed but its visible timing benefit also
awaits a hardware observation. Further deployment is explicitly on hold.
