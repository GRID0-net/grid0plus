# Switch-hosted versus emulator-hosted Big Run (2026-10-14)

The operator uses Citrosis and a real Switch on separate internet connections.
The newest tests confirmed the previous readiness barrier blocks a solo host,
but exposed a persistent host-direction difference:

| Host | Session | UTC kickoff | Outcome |
| --- | --- | --- | --- |
| Jade (Switch) | `671693a3-f165-47b3-9ea0-58d01e497fee` | 19:49:28.646 | Only Jade's profile at 19:49:28.704; barrier stayed 1/2. Emulator stream closed at 19:49:45.012; Switch closed at 19:49:58.871. |
| doodlylizz (Citrosis) | `8ef6e313-804e-42e4-84c5-91b4d93ecaa2` | 19:52:05.032 | Both profiles by 19:52:05.199; both reports arrived at 19:55:51. |

Both clients authenticated, installed peer-inbox watches, and performed their
lobby writes in the failed run. The existing logs do not contain peer payload
directions or delivery versions, so they do not prove a NAT or emulator defect.
In that run the Switch's peer-inbox watch started at 19:49:24.626 and the
emulator's at 19:49:25.705; the Switch locked the room at 19:49:26.888.

## Reproduced server defect

`KeepUserSession` previously registered targets and queued initial responses
on its receive goroutine. The stream writer independently selected those
responses, live changes and periodic refreshes. Consequently it could send a
live `UPDATED` between initial `EXIST` documents and their `LISTED` boundary.
An old snapshot could follow a newer peer update, or a queued old update could
follow a new snapshot. Single-writer gRPC usage alone did not prevent this.

A paused-transport test injected a committed update during an 80-document
initial snapshot. It reproduced an interleaved live change in all three runs
against the old implementation. The corrected implementation passed repeated
runs, together with the full Gamesync race suite.

## Correction

The receive goroutine now queues requests only. The writer registers each
target, builds its snapshot and sends through `LISTED` as one indivisible
batch before selecting another event. Each live change and its `UPDATED`
boundary also stay together. Refresh batches use the same writer.

Per-target, per-document delivery watermarks reject strictly older queued
versions after a newer snapshot has been delivered. Pia's multiple senders
have independent watermarks; no sender's packet suppresses another's. Equal
timestamps remain deliverable, preserving referee republishing. Deletes and
target reuse clear watermarks. Queued changes for removed/retargeted watches
are ignored. Opaque payload bytes and sender fields are forwarded unchanged.

The actual-group startup barrier and small-player support remain in effect.
No arbitrary delay or emulator host preference was added.

## Hardware evidence still required

This fixes a reproduced stream-ordering defect. Whether that defect explains
the real Switch/Citrosis asymmetry remains unproven until another console run.
If the peer still stalls, existing opt-in lobby logging now records
`gamesync: peer transport` events for committed and sent inbox documents,
including source/recipient session IDs, payload byte count and field types.
It records no payloads, ICE addresses or credentials. A successful gRPC Send
is evidence of transport handoff, not a peer's acknowledgment or UDP gameplay.
Watch attachment, target and snapshot logs include the authenticated room and
user so delivery can be correlated in both host directions.

Repeat Big Run with Jade hosting, then doodlylizz hosting. Confirm both profiles
reach 2/2 and both clients enter the same round. If still stuck at 1/2, compare
the two directed peer exchanges rather than adding a larger matchmaking timer.

## Three-player versus follow-up (2026-10-15 Brussels time)

Production journal comparison on `f6a983d`, without another hardware test:

| Session prefix | Host account | Referee kickoff UTC (Oct 14) | Profiles observed | Guest departure |
| --- | --- | --- | --- | --- |
| `ad3bbaa1` | emulator2 | 23:11:59.910 | host only, 1/3 | 23:12:16.524 and 23:12:17.056 |
| `7295dcf0` | doodlylizz | 23:15:40.194 | 3/3 by 23:15:40.412 | streams remain past startup |
| `e5e37d79` | Doughston | 23:19:28.484 | host only, 1/3 | 23:19:45.813 and 23:19:46.484 |

All three players attached Gamesync watches in each attempt. The failed
attempts contain committed and sent peer payloads in both host/guest
directions, including 251-byte host payloads and 9/78-byte guest payloads.
These sizes also occur in the working startup. No warning or failed RPC was
logged during these startup windows. Counts differ across commits and sends
because snapshots replay the latest sender state; counts alone cannot identify
which exact payload was dropped or accepted.

The group barrier holds the host at 1/3 after the guests fail to submit their
profiles. Removing it would hide the incomplete startup rather than explain
the guests' failure. An account named emulator2 also hosted a failed attempt;
account names do not establish the physical device used, so this evidence does
not establish a Switch-only cause.

The unresolved boundary is between receiving peer/referee signalling and
submitting the guest's `@@Profile`. Existing logs record peer payload sizes
and field types, but not payload identity, client acknowledgments, UDP
connectivity, or the exact referee status sent to each recipient. No recent
packet capture was found in the VM's temporary paths or the local capture
tree for these attempts, and the TURN log predates them. Distinguishing peer
connection failure from referee-status delivery therefore remains open.

## Reproduced peer-refresh packet loss

`TestPeerRefreshCannotHideANewerQueuedPacket` reproduces another server
ordering defect: a periodic refresh replays an older peer packet but replaces
its update time with the current clock. A genuinely newer committed packet
queued for delivery can then fall below that artificial watermark and be
discarded as stale. The test fails before the correction and passes after it.

Peer refreshes now preserve their committed update times. Recomputed synthetic
roster and referee responses retain their existing refresh-time behavior.
This repairs a demonstrated loss path; the historical journal cannot establish
that this race caused the three-player failures above.

Opt-in diagnostics now include a truncated SHA-256 payload fingerprint to
correlate committed and sent peer packets without logging their contents.
Outgoing referee documents also log their recipient and status, allowing a
future trace to distinguish missing status delivery from missing guest
profiles. No additional console test was available during this investigation.

## Decrypted kickoff comparison

Re-read the preserved `nextendo-ryujinx-match-20260930` Gamesync flows and
their `gamesync-streams/*.grpc-body` sidecars without modifying the originals.
The reference sends `@@RefereeRequest` at 15:32:38.790854 UTC and its own
`@@Profile` at 15:32:53.806565. The intervening watched referee responses
have status 0, consistent with the previously documented 15-second fallback.
There is no separately named Battle Time RPC in that captured interval.
This does not reveal the contents of any UDP gameplay/start message.

The current failed and successful Grid0 journal windows contain identical
host `__gs/m` writes: cp=false, ebf=false, ip=false. The failed hosts submit
their referee requests about 1.75 seconds afterward; doodlylizz submits at
4.44 seconds. This supports investigating premature readiness, but cannot
establish it: the guests' internal state and packet acceptance are not logged.
The Toolbox's current WaitFullmember trampoline already checks connected
stations against a cached session-player count; that count's correctness for
these three-player attempts is not available from the journal.

The decrypted reference also writes `docs/__mt/nat_traversal`, with `a` an
array of maps containing lu, ru, p2p, rc and re. The observed entries carry
rc=1 and re=false. Grid0 previously stored this request without logging it.
Opt-in lobby diagnostics now log these raw per-peer fields under the
authenticated reporter's user/session. Their semantics remain unconfirmed;
neither a false re nor rc=1 is treated as proof of a working connection.

Decoder caveat: protojson rejects the captured FieldMask path `*`. Binary
protobuf decoding succeeds; JSON export must omit masks on a temporary copy
(explicitly labelled) or use reflection/prototext, otherwise most writes,
including the profile, disappear from the decoded analysis. Keep raw bytes.

## Same-PC three-player reproduction (2026-10-15)

The operator reproduced a failed forced three-player regular match with the
Switch hosting, followed by a successful forced match with the same host.
Ryujinx and Citron ran on the operator's PC. Private captures and journals are
preserved under `docs/CAPTURES/raw/grid0-three-player-20261015/` (ignored).

- Failed session `c251e54f`: forced at 10:23:29.238 UTC; host referee request
  at 10:23:37.455; only host profile at 10:23:37.519 (1/3). Guests detached
  at 10:23:53.874 and 10:23:53.997. The operator clarified that their minus
  buttons disappeared, then reappeared; cancellation afterward was manual.
- Successful session `1a298caa`: forced at 10:30:16.021 UTC; host request
  at 10:30:25.500; all three profiles by 10:30:25.583. All three entered the
  match; Citron subsequently froze/disconnected, a separate observation.
- Every distinct committed peer payload fingerprint in the failed journal
  has a matching sent record. There were 15 commits/21 sends versus 12/18
  in the successful startup; refresh replay explains extra sends. This is
  server handoff evidence, not proof of client acceptance or UDP delivery.
- Guests' raw nat_traversal rc=1/re=false reports appear in both runs; these
  fields do not distinguish success from failure here.
- Ryujinx's module base was 0x8506000. Instructions at base+0x38a420c
  confirm the small-match compare and trampoline are installed. An auto-
  continuing breakpoint there logged w0=3, w24=8 during the successful
  attempt. It was absent during the failed one. Frequent remote breakpoints
  perturb execution, so success under this probe does not establish a fix.
  The breakpoint was removed and GDB detached cleanly afterward.
- Citron had severe memory pressure and verbose socket logging. These are
  test confounders, not established causes of the Switch-hosted failure.

The VM trace contains NAT probes but no gameplay relay flow for these starts.
Direct same-network UDP is outside its observation. Next reproduction should
record PC UDP alongside the VM trace, initially without a high-frequency GDB
breakpoint, then inspect the connected count only at a targeted transition.
No readiness timeout or protocol response was changed from this comparison.

### Cross-network comparison

The uninstrumented same-Wi-Fi attempt `df147454` also reached 3/3 profiles
at 10:36:17.266 UTC. Local UDP shows all three pairings directly, including
Switch 192.168.1.41 and both emulator ports on 192.168.1.61.

Moving only the Switch to another Wi-Fi reproduced the failure in session
`9014b587`: host referee request 10:39:24.068 UTC, host-only profile at
10:39:24.141, guest detachments at 10:39:48.075/48.165. First captured TURN
setup to port 3479 is at 10:39:25.554436, after the host request. Both guests
later reported rc=2/re=true for the host and rc=1/re=false for each other;
the local trace shows bidirectional UDP to relay port 50093. These observations
support premature host readiness, not a claim that relay traffic is blocked.
Actual connected stations and the lobby +0x490 count on the Switch at the
WaitFullmember trampoline remain unmeasured. Do not replace that missing
measurement with an arbitrary sleep or interpret rc/re as a full game-ready
acknowledgment. Successful and failed traces are preserved separately.

### Readiness patch hardening prepared for hardware test

The Toolbox, Ryujinx and Citron source trees initially shipped identical
s3smallmatch IPS bytes (SHA-256 11fc9aaa3b4703daf5c22285fad3f1b9ef4b6cb470843ba9f460f612e347bcb5).
The installed Switch SD copies matched those bytes too.
The existing WaitFullmember trampoline allowed a null lobby or zero expected
count to proceed, and used signed min for the expected count. Revised copies
route missing state to WAIT_MORE (resetting the original stability counter),
and use unsigned min so a negative sentinel cannot permit undersized startup.
144 Unicorn execution cases pass, alongside the existing 16 rematch cases;
all original patch sites/caves were verified against the operator ELF.

The analyzed project is `ghidra_projects/splat3_main`, program `main`;
`splat3_nso_native` did not have the relevant functions defined. Decompilation
of FUN_71038a4104 confirms the 60-update readiness counter and its reset path.
FUN_71038a05c0 compares FUN_71014f9760(manager,1,0) and
FUN_71014f9760(manager,1,1) to manager+0x490 before req_match_step.
The field is explicitly cleared at RVA 0x14f527c. These are evidence for a
real roster/readiness count, but do not prove its runtime value on the failed
Switch host. No arbitrary delay was added; the existing 60-update counter
remains unchanged.

Both revised IPS filename variants were backed up, copied and verified on
the Switch SD on 2026-10-15, then the FAT partition was safely unmounted.
Installed SHA-256: ba1aee8aa3411047e78e0c7f4728813037445ba3deb3ba39587d67cc393c15af.
Backups are private under the raw capture directory. Emulator source copies
are synchronized, but running emulator binaries are unchanged. No release
was published; reapplying the currently released Toolbox would overwrite this
host-side probe. A fresh cross-network Switch-hosted test is required.

### Revised patch cross-network result

Session `6577666f` was forced at 10:57:31.724 UTC. All three attached by
10:57:35.015. No referee request or startup profile was logged; guests left
at 10:58:06.379/06.437 and host at 10:58:13.004. The operator saw everyone
return to matchmaking, with no solo host Battle Time. This is a changed
failure mode, not a successful fix.

TURN allocation and channel bindings succeeded. The Switch control endpoint
exchanged 472 outbound and 502 inbound ChannelData packets on port 3479;
the two guests exchanged bidirectional UDP with its relay port 50075.
These packet counts establish transport activity, not Pia readiness or
acceptance. The original failure could still involve a partially connected
Pia group. The revised gate could also be waiting on a permanently zero
expected count; that value has not been measured on this Switch.
Next control: revised patch, same-Wi-Fi setup previously known to work.

### Same-Wi-Fi control rejected the revised gate

Session `e9ab3462` was forced at 11:03:40.453 UTC. The operator reports
both guests returned to matchmaking, then the Switch followed. No referee
request/profile was logged. Guests' peer reports again carried rc=1/re=false.
Unlike the earlier successful direct-network tests, this run used the revised
Switch gate. The experiment therefore regressed a known-working setup;
missing-state fail-closed behavior cannot be shipped as a fix based on these
observations. Do not infer the host's runtime field value solely from this
failure: null/zero/sentinel, wrong session state and connected-count semantics
still need a direct measurement.

The experiment is archived privately under `rejected-ready-patch/` in the raw
capture directory; changes were reverted in Toolbox, Ryujinx and Citron source
trees. Both SD IPS filename variants were restored from the verified backups
(SHA-256 11fc9aaa3b4703daf5c22285fad3f1b9ef4b6cb470843ba9f460f612e347bcb5).
No experimental release was published. The current SD system_settings.ini
has no active enable_standalone_gdbstub setting; the documented 2026-09-25
native-debugging setup must be explicitly re-established before native GDB
measurement. Existing settings were inspected but not changed in this rollback.

### Direct measurement disproves the old expected-count assumption

Both native Switch and Ryujinx were attached with scripts/gdb/splat3_readiness.py
for session `1cb7cd4f`. The original patch was installed. On the Switch,
readiness entry at 11:15:23.569 UTC observed connected=3, full=8, stable=0,
manager+0x490=0; readiness passed at 11:15:24.586 with stable=61 and the
same zero field. Ryujinx recorded the same values at entry 11:15:24.227 and
pass 11:15:25.237. The server reached 3/3 profiles by 11:15:27.688, and the
operator saw Battle Time on all three. The singleton field watch recorded no
change from its initial zero. Thus the original trampoline's zero-state
bypass is exercised during a successful three-player match on both clients.
It is incorrect to describe that trampoline as reliably checking actual
matchmaking participants in this flow. The rejected stricter gate explains
the control regression; a valid expected-roster source must be found first.

### Offline roster-source trace and next measurement

From the analyzed `splat3_main` project, FUN_71015173d0 selects session key
1 from the registry at `*(*GOT 0x5f1e298)`, then invokes FUN_71014dfe50 to
copy a typed property. Its vtable region is reached through GOT 0x5f229e8;
method RVA 0x3d23180 returns the property ID 0xc056fc0f. The copy method
FUN_7103d22b40 copies a ring collection of 0x48-byte records. The property's
stored payload must be present, type/size field 800 and ready flag +0x25 set.
Payload fields are array pointer +0x20, capacity +0x28, head +0x2c and count
+0x30. Member identifier is 10 bytes at record+0x30. The connected-count
routine filters the local marker at GOT 0x5f229e0 and looks up corresponding
station identifiers in the table through GOT 0x5f3ca80 -> +0x50 -> +0xe8;
entries with flag+0x20 set, missing entries and state 3 are not counted.
The numeric state semantics are not established beyond that exact behavior.

This explains how to read the candidate source; it does NOT establish that
its count already includes a not-yet-connected participant. That timing
property still requires a live measurement. No replacement IPS is installed
or published, and no arbitrary startup delay was added.

`scripts/gdb/splat3_roster_source.py` implements a bounded read-only traversal,
logging counts, numeric states and truncated member-ID hashes. The readiness
script now snapshots it at first readiness entry and at the 61-update pass.
It uses no inferior function calls or game-memory writes. Both previous GDB
connections had already closed when the operator went AFK; their local GDB
sessions were then quit and breakpoints cleared.

Next test after the operator returns:
1. Keep original Switch patch and all three on the PC Wi-Fi. Open the game
   on Switch and Ryujinx before attaching; do not queue until both probes and
   the server capture are confirmed ready.
2. Run the readiness script on both devices with fresh private JSONL paths:
   GRID0_GDB_KIND=switch, GRID0_GDB_TARGET=192.168.1.41:22225, and
   GRID0_GDB_KIND=ryujinx, GRID0_GDB_TARGET=127.0.0.1:55555. Set
   GRID0_GDB_LOG for each and use `gdb -q -nx -x scripts/gdb/splat3_readiness.py`.
   Discover module bases automatically and check instruction fingerprints.
3. Force a fresh three-player Switch-hosted match. Confirm candidate roster
   count, member-state calculation and connected register agree with this
   known-working roster. Save the exact emitted statuses on failure too.
4. To observe delayed/relayed peer setup with native GDB still reachable,
   use a temporary, explicitly scoped UDP-only block between PC and Switch
   while keeping TCP/22225 reachable, or a second PC network interface with
   no default-route change. Neither setup has been applied yet. Keep it
   paired with local UDP and server captures; restore the network afterward.
5. Only if the candidate includes all admitted players before connections
   complete, design and execute-test a replacement gate. If it grows with
   connected players, it cannot supply the expected count; trace the ticket
   or Gamesync roster instead. Retest both direct and relayed startup before
   publishing anything.

Targeted decompilations are preserved privately in the ignored capture
folder's `analysis-private/`; never commit them or the operator game ELF.

### Validated occupied-slot baseline and local candidate

Attempt 8 native Switch and Ryujinx logs both show a ready property with
capacity=10, head=0, count=10, but only three non-marker member identifiers.
Seven IDs match the empty marker. All three real IDs have station state 2,
flag 0; computed connected=3 agrees with w0=3 on both clients. The old
manager field is still zero. The operator confirms all three entered the
game. Raw ring count 10 is therefore not the expected-player count; the
candidate must count non-marker IDs independently of station readiness.

The local-only `scripts/research/splat3_roster_gate_candidate.py` prototype
reuses the original helper loop. It initializes saved w28, increments it only
on the existing non-marker path, and returns that occupied count in volatile
w1 alongside unchanged w0. The caller requires positive connected count and
equality to occupied count before its original stability counter. Four
existing verified padding blocks replace the old lobby-count trampoline.
No candidate IPS has been installed, released, or synchronized to emulators.

`test_splat3_roster_gate.py` executes the operator's actual original and
patched ARM64 helper with only the roster-property copy call mocked. 135
cases cover 1..10 real members, all connected subsets, wrapped rings, missing
stations, state-3/flagged exclusions, empty/property-unavailable cases, and
callee-saved register/SP/refcount preservation. The original connected return
matches the candidate for every subset. With three occupied IDs but only one
matching station, the old gate proceeds and the candidate waits. These are
execution checks, not hardware proof that all IDs arrive before connection.
The follow-up delayed-peer attempt below falsified this candidate as an
expected-player source; do not use it as a release candidate.

The follow-up run used `scripts/gdb/capture-delayed-peer.sh`, which temporarily
blocked direct UDP to/from Switch 192.168.1.41 for 90 seconds while capturing
local UDP. Treat the communication errors during this deliberate disruption as
expected and not as a reproduction of the ordinary cross-network failure.

Attempt 9 falsified the candidate roster assumption. At 13:15:27.245 UTC, the
Switch entered `FUN_71038a4104` with `w0=1`; the property ring contained one
non-marker ID and nine marker IDs, so both live-connected and occupied counts
were one. It passed the 61-update check at 13:15:28.547 with the same count.
The previous attempt's candidate would therefore also have passed early.

The server had formed a three-player session at 13:15:21.297 UTC. By
13:15:24.641, all three GameSync clients each received an initial
`docs/__us` collection snapshot reporting three documents. Each also received
the `docs/__gs` collection with six documents. Their local P2P helper still
reported one connected member at the Switch's readiness check. Thus the server
session roster and the local transport-connected roster are distinct sources;
counting non-marker IDs in the latter cannot gate on the former.

Ryujinx hit two generic error-reporting paths at 13:15:26.429 and .443 UTC,
before the Switch's readiness check. These breakpoints identify calls into a
shared error handler, not a decoded network error code. The interruption test
cannot establish the natural failure's cause. The server later froze its
startup group at three players (13:15:30.387 UTC), so the capture also does not
show that the server released the kickoff early.

Next investigation should compare the unblocked failed cross-network capture
with the unblocked successful same-Wi-Fi capture, focusing on the time from
GameSync's three-user snapshot through peer transport, referee `@@Profile`
writes, and the host's readiness entry. Any game patch needs a count sourced
from the admitted match group, or a server-controlled kickoff condition; the
Pia connected-member helper is insufficient. Do not install the local
`scripts/research/splat3_roster_gate_candidate.py` prototype.

### Kickoff roster mismatch and an independent count source

Decoded the unblocked plaintext HTTP/2 DATA in the original VM captures,
reassembled gRPC frames per TCP connection/direction/stream, and parsed
`WriteDocumentsRequest` directly (without protojson's FieldMask limitation).
Both failed starts have only one entry in the kickoff request's `users`
array despite the server assigning three: session `c251e54f` at
10:23:37.455 UTC and cross-network session `9014b587` at 10:39:24.068 UTC.
The successful same-Wi-Fi session `df147454` has three entries in each of
the three consoles' requests at 10:36:17.127/.187/.190 UTC. This is an
observable request-content difference, consistent with the patched host
advancing while its local peer group contains only itself. It does not
establish whether the premature request prevents later peer completion.

The actual referee watcher has its vtable at RVA `0x5c378f8`, reached via
GOT `0x5f22d38` plus 0x10. Its update method is `0x14fe060`, and document
parser `0x14fe4b0` reads `request_id` and `status`: -1 advances states 0/1
to 2, 0 advances state 3 to 4, and 1 advances state 5 to 6. In state 2 it
stores the `users` array length at watcher+0xa38. That count inherits the
already-incomplete kickoff request, so it cannot fix this startup gate.
The object at global `0x605c780`+0x160 is broader session state, distinct
from this watcher (stored at the lobby manager+0xe20); its virtual getters
must not be labelled referee-status getters without tracing their source.

Raw ELF segment search finds `__us` at RVA `0x4fcee4f` (the previous search
of defined Ghidra strings missed it). Getter `0x43294c` has real callers.
`0x14f9760(manager, group, include_all)` walks the received Gamesync document
tree and counts documents in this collection. With include_all=0 it filters
through another local-ID map; include_all=1 counts all `__us` documents.
This is a promising assigned-roster source independent of Pia's connected
property, but its timing/group selection needs live validation before use
in an IPS gate.

The read-only `assigned_snapshot()` probe in `splat3_roster_source.py`
follows global RVA `0x61e18a0` -> +0x80 -> group array +0x12b0. Each
0x150-byte group entry's +0x140 points to its document watcher. Ready state
is watcher+0x124 == 2, selected buffer is +0x1a4 (0/1), and the tree root
is +0x138 + buffer*0x20. Tree children are +8/+0x10; node+0x28 points to
a document whose first pointer addresses its libc++ collection-name string.
The probe counts only `__us`, traverses at most 256 nodes, logs both groups,
and never calls inferior code or writes game memory. Synthetic-memory checks
passed for three user documents plus an unrelated document, an empty group,
and cycle rejection. `splat3_readiness.py` now records this alongside the
existing connected-property snapshot at readiness entry/pass.

Next required measurement: confirm the secondary group's assigned count is
three at the readiness entry, including while connected count is still one
or two. Keep the original IPS for that measurement. Only then replace the
old zero/+0x490 bypass with the verified assigned count and validate both
direct and relayed startup. No replacement IPS or deployment was made during
this analysis. Private decodes and the reproducible request-count decoder
are preserved in the raw capture folder's `analysis-private/`.

### Assigned-document gate prepared, not installed

`scripts/research/splat3_assigned_roster_gate.py` prepares a second, distinct
candidate. At the original WaitFullmember hook, it saves connected x0 and LR
on an aligned stack frame, calls the game's native
`0x14f9760(lobby_manager, 1, 1)`, restores both, and advances only when the
assigned count equals a positive connected count. It leaves the original
60-update stability counter and all unrelated patch records intact. A missing
count (-1) waits. This source does not use the rejected occupied-Pia-slot
experiment or the zero manager+0x490 fallback.

The five existing WaitFullmember padding blocks are reused, plus alignment
RVA 0x3885214 (12 zero bytes, preceded by RET and followed by a function
prologue). The analyzed project has no references to its three instruction
addresses. Independent Capstone disassembly confirms the call arguments,
destinations and stack instructions. Unicorn execution covers 132 combinations
of connected=0..10 and assigned=-1..10; all passed. The count call is mocked
only in this execution test, deliberately clobbering caller-saved registers.
The test also verifies the original bytes at every patch site, original
connected return, LR, SP, and callee-saved registers. These checks do not prove
the native count's timing or its group semantics on hardware.

Native count has additional availability conditions: secondary active flag at
lobby+0x40, watcher state 2, and a special-mode shortcut at lobby+0x1881 that
returns one. The read-only probe now logs these flags and an estimated native
secondary count alongside both raw collection counts, so a three-document
snapshot is not mistaken for a usable native return. A live original-patch
run must validate this before the candidate is installed. The standalone IPS
is staged privately under `assigned-roster-candidate/` in the ignored raw
folder; Toolbox, emulator embedded patches, SD contents and deployed server
were not changed.

### Attempt 10 rejects the secondary-group candidate

Original IPS, Switch-hosted three-player forced match at 16:50:31.718 UTC,
session `5a202b28`. Operator reports Switch Battle Time, Citron freezing,
and communication error. The freeze prevents treating the match outcome as
a clean comparison, but the source measurements are usable.

Switch readiness entry at 16:50:37.662: connected=1; primary group ready,
one __us document; secondary watcher state=1, zero documents;
secondary_active=0, special=0. Pass at 16:50:39.158 had the same values.
Ryujinx entry at 16:50:56.499: connected=1 while its ready primary group
had three __us documents; secondary remained inactive. Pass at 16:50:57.628
was unchanged. Native secondary-count estimate is -1 on both. Therefore
the staged secondary-group candidate would block this flow indefinitely;
it is rejected and was never installed. Merely changing the argument to
primary is insufficient: the host's primary count initially contains one.

Server logs independently explain that count: host attached at 16:50:35.015
and its initial docs/__us snapshot at .074 contained one document. Guest
streams attached at 16:50:40.082 and 16:50:46.696; the first guest's later
snapshot contained two, and Ryujinx subsequently saw three. Host kickoff
was already submitted at 16:50:41.006, with only its profile at .073 (1/3).
The initial Gamesync document roster is an attachment roster, not a fixed
assigned matchmaking roster. The next patch needs the assigned ticket/session
roster or an explicit server admission barrier, rather than either of these
locally growing collections. Both GDB probes were detached and the bounded
VM capture stopped; attempt10-server.pcap/journal and both JSONL files are
preserved privately.

### Complete __pus membership map located for next probe

The complete group is already delivered independently of live __us membership:
attempt 10 host target 7 watches docs/__pgn/All/__pus and received three
documents at 16:50:35.935 UTC, before its readiness entry at 16:50:37.662.
`matchedPlayerDocuments` deliberately includes unattached assigned players
in __pus, while omitting them from public __us. This makes __pus a stronger
candidate than either rejected count source; client acceptance/timing still
needs measurement.

Raw ELF search located the pooled string /__pus at RVA 0x4f05ddb (searching
the suffix __pus alone missed the reference). Path builder 0x4267b4 builds
__pgn/<group>/__pus. Its caller 0x41bf20 installs collection callbacks whose
function objects use table 0x5b19860 (update) and 0x5b198b8 (completion).
Update entry 0x424ae0 obtains the base peer-group object from lambda+0x10.
Group name is its libc++ string at +0x1a8. Accepted member updates insert
into its map at +0xa0. Map insertion 0x4252c0 increments map+0x18, yielding
the member count at group+0xb8; removal decrements the same map. Own member
acceptance marks group+0x1fa. Completion callback 0x4255fc marks +0x1fb.

The readiness probe now observes bounded update callbacks, remembers only
groups named All, and reads their counts and completion flags at readiness
entry/pass. It performs no inferior calls or writes and observes at most
64 callbacks per attachment. Synthetic-memory checks passed for the lambda
pointer, live count changes, completion flags and oversized-count rejection.
This does not prove a stable singleton path suitable for an IPS: the next
measurement must show the relevant All object has all three members before
connected peers catch up, then resolve that object's owning session handle.
The source decompilations are preserved privately as pus-*.c. No new patch
was installed or deployed.

### Attempt 11: complete peer-group roster confirmed (2026-10-15)

The user reported Battle Time, but an emulator froze and the match did not
start. This is not a successful hardware validation. The read-only probes did
confirm an independent complete roster before the original readiness gate:

- Switch at 17:08:09.246 UTC: `All` peer group has 3 members, own-member-seen
  and initial-snapshot-done both 1; Pia connected count is only 1.
- Switch at 17:08:11.160 UTC: readiness passes after 61 updates despite Pia
  still reporting 1; complete peer group still reports 3.
- Ryujinx at 17:08:14.882 UTC: complete peer group also reports 3 with both
  flags set, while Pia property is unready (`w0=0xffffffff`). It passes at
  17:08:15.880 with that property still unready.

This confirms the `__pus` group supplies an assigned roster independently of
Pia connectivity. It also demonstrates that the existing bypass allows an
unready Pia property through. Next: locate a stable owning-session path to the
peer-group object and require a completed snapshot plus a valid connected count
matching the assigned roster. A callback-derived pointer alone is not yet a
safe IPS implementation. No new patch was installed in this attempt.

Private evidence: `raw/grid0-three-player-20261015/attempt11-*-peer-group.jsonl`,
`attempt11-server.pcap`, and `attempt11-server.journal`.

### Current-session owner path traced after attempt 11

Raw direct-branch scanning found the native API-to-group accessor used at
0x44e5450 and 0x44e54a0 (inside the true function entry 0x44e52f0).
This path selects manager+0x12b8, falling back to index zero if out of range:

```
root = *(main + 0x61e18a0)
manager = *(root + 0x80)
entry = *(manager + 0x12b0) + selected * 0x150
box = *(entry + 8)                  // only if entry's leading word is nonzero
api_weak_handle = *box
api = *api_weak_handle
impl = *(api + 0x10)
group = *(impl + 0x30)
```

The native code takes manager+0x2d0's mutex while acquiring the session owner,
then FUN_71004026d0 locks the API weak owner at handle+8. FUN_71004054c0
calls FUN_71004028dc, which locks the group weak owner at impl+0x38 and
returns impl+0x30. Therefore simply caching the group callback pointer is
insufficient for a shipping patch: native shared/weak ownership must be
respected, especially when disconnecting or replacing a session.

The read-only GDB probe now follows this path independently and reports
`owned_peer_group` alongside the callback-derived group. It rejects missing
sessions and expired weak owners (64-bit strong count -1), bounds array and
member counts, and reports whether both paths agree. All six synthetic-memory
tests passed, including an inactive selected session and expired owners.
This establishes probe behavior, not a validated live pointer chain or a safe
concurrent IPS. Next hardware measurement must compare the independently
resolved object with the callback's `All` object at readiness entry/pass.

### Attempt 12: successful startup validates current-session ownership

Operator confirms all three entered the battle, with the original IPS unchanged.
Switch readiness entry at 17:16:03.910 UTC and pass at 17:16:06.106 both
report connected=3. Ryujinx entry at 17:16:04.230 and pass at 17:16:05.215
also report connected=3. Both have complete All groups with three members,
own-member-seen=1 and initial-snapshot-done=1. The independent session-owner
path selects index zero and resolves exactly the callback-observed group on
both devices (`matches_callback=true`). Before matchmaking, both instead
selected index one and reported no API handle, demonstrating why stale
callback pointers must not be reused between sessions.

This is a working reference, not evidence a fix was installed: original patch
and server remained unchanged. Compared with attempt 11, the concrete startup
difference is valid connected=3 instead of Switch=1 / Ryujinx=unready, despite
both attempts already having complete three-member peer groups. The owner path
is now live-confirmed; a replacement gate must preserve native owner lifetime
and wait for valid connectivity matching the completed assigned roster.
Private evidence: attempt12-*-owner.jsonl and attempt12-server.pcap/journal.

### Peer-group readiness candidate installed for hardware validation

Candidate generator `scripts/research/splat3_peer_group_gate.py` replaces only
legacy readiness trampoline records; other small-match fixes are retained.
It uses 48 RET/alignment padding spans verified against the operator's image,
excluding legacy sites, direct branch destinations and ELF pointer references.
It locks the session-manager mutex, acquires API and group ownership using the
native weak-lock/accessor routines, releases the mutex, checks completed `All`
membership against valid connected count, then releases both owners. The
original connected return, LR, SP and callee-saved registers are restored.
The existing 61-update stability check remains unchanged.

284 Unicorn execution cases passed across full sizes 4 and 8, missing/expired
owners, incomplete snapshots, incorrect group names and invalid counts. Tests
also simulate the session dropping its baseline references after the mutex is
released: both last-owner destructors and weak releases execute correctly.
Native routines are simulated in these tests; hardware validation is pending.

Both SD small-match IPS filenames were replaced with candidate SHA256
`a855cfec093a8c8f0cb441ceb46da44f8afbb07d293fba102f14ee2415310fa6`.
Original SHA256 `11fc9aaa3b4703daf5c22285fad3f1b9ef4b6cb470843ba9f460f612e347bcb5`
is backed up in the private raw directory under `sd-before-peer-group-*`.
Existing SD `s3_bypass_member` and `s3tricolor_pair_test` overlap only identical
retained instructions; no differing overlap was found. Toolbox distribution,
emulator patches and deployed server were not changed. Restart the game to
load this candidate, then measure Switch-hosted cross-network startup before
considering it validated or publishing it.

### Attempt 13: candidate succeeds with Switch host on hotspot

Operator moved Switch to hotspot (no debugger attached), then force-started a
three-player battle with Ryujinx/Citron on the PC. Reports Battle Time for all
three: cancel indicator disappeared first on Switch, then emulators, and the
match began afterward. This is the first cross-network hardware success with
the candidate, consistent with the gate waiting for all assigned connections.

Server session `436effe0-1795-473e-9ac9-cbe83ef77057`: three watch streams
attached at 17:33:54.230/.276/.298 UTC. Referee profiles progressed 1/3,
2/3, 3/3 at 17:34:10.327/.332/.338 UTC for the same battle. Contrast previous
failed startups with only one referee user/profile. This run validates a
successful tested configuration, not every mode or repeated reliability.
No GDB timing perturbation was present. Capture stopped and saved privately as
attempt13-server.pcap and attempt13-server.journal. Candidate remains installed
only on the operator Switch; distribution/emulator patches remain unchanged.

### Distribution and cancel-control timing follow-up

Tested IPS bundled and pushed to Toolbox commit 3709200, Ryujinx 1b0d77055,
and Citron 8194d1295. Toolbox 0.8.10 published; downloaded NRO contains two
exact copies of the candidate IPS. Emulator release workflows dispatched for
Ryujinx 1.4.9 (run 38779593267) and Citron 1.4.1 (run 38779597892); completion
must be checked before claiming those binaries published. All three source
copies use SHA256 a855cfec093a8c8f0cb441ceb46da44f8afbb07d293fba102f14ee2415310fa6.
Toolbox now carries readable assembly, fixed cave allocation, checked-in
readiness records and the 284-case execution test. Regeneration reproduces
49 readiness records exactly; existing 16 rematch cases also pass.

Attempt 13 timing: watch streams attached at 17:33:54.230-.298 UTC; TURN
Allocate requests reached VM at 17:34:00.697/.724/.920, and server replies
followed in under 1 ms. First captured Send Indication was 17:34:01.755;
referee profiles arrived 17:34:10.327-.338. Thus a slow TURN Allocate response
at the VM does not explain the entire startup delay. These timestamps do not
establish when the UI minus controls disappeared or which client-side timer
was active; no new timing patch is justified from them alone.

Static UI landmarks: L_HoldCancelBtn_00 is bound to object+0x330 in
FUN_710413f6e0; sibling controls are +0x338/+0x340/+0x348. Helper 0x41483e0
updates six controls from visibility/enabled byte pairs and their animation
state, rather than directly waiting on a network timer. The enclosing state
machine registers Matching at callbacks 0x41435b0 (entry), 0x4143c5c (update)
and 0x4143c60 (exit). These are UI landmarks, not yet proof this is the exact
minus prompt used in the tested Versus scene. A live visibility/state probe
is needed to tie the control to the operator's observed disappearance before
altering it. Decompilations preserved privately as minus-*.c.

### Exact hold-minus control and state setter located

`L_HoldCancel_00` in FUN_71041c17d0 binds the control at object+0x258,
with animations `MeterUp_Minus` and `MeterDecide_Minus`. This is distinct
from the earlier generic L_HoldCancelBtn_00..04 family. FUN_71041c1650
changes visibility from argument w1 bit zero, remembers it at object+0x28d,
and returns immediately for an unchanged value. The caller wrapper
FUN_71038ea888 receives the visibility value in w0, selects the lobby UI
widget, locks its UI mutex, and invokes the actual setter at 0x38ea974.

Controller callback 0x38f4aac shows it on state entry; callbacks 0x38f5114,
0x38f57d8 and 0x38f6cd8 hide it on state entry. The callback table is assembled
at true entry 0x38f3c9c using ADR (not ADRP+ADD), with corresponding update
callbacks 0x38f4b70, 0x38f5180 and 0x38f59c0. The 0x38f57d8 update can later
advance its network controller (param_1+0xcc8) to state 0x17, distinct from
the UI's earlier hide request. Static analysis thus separates UI state entry
from eventual network readiness; it does not measure their runtime delay.

The read-only GDB probe now logs both wrapper requests and actual widget
visibility transitions, bounded to 64 hits each, alongside Pia connectivity
and current owned peer-group state. Actual setter suppresses unchanged values.
No inferior calls or new game patch are used. Syntax and existing synthetic
roster tests pass. Next run should correlate the emulator's hide caller and
roster state with its Gamesync/TURN capture. This is needed before claiming
which phase caused the operator's observed inter-client delay.

Analysis hygiene: an exploratory decompile at interior address 0x41c1c00
was discarded as invalid; it is not used as evidence or preserved as a
standalone function. Verified constructor entry is 0x41c17d0.

Ryujinx 1.4.9 release completed successfully with all platform assets.
Citron 1.4.1 Windows/macOS jobs passed; Linux failed on an upstream TLS
connection reset downloading xcb-util-renderutil. Failed jobs alone were
retried, keeping the successful builds. No speculative timing change shipped.

### Published artifact verification and serialization note

Citron's successful Windows/macOS packages from run 38779597892 were downloaded
and their executable IPS arrays parsed. Both contain the exact hardware-tested
1035-byte IPS, SHA256 a855cfec093a8c8f0cb441ceb46da44f8afbb07d293fba102f14ee2415310fa6,
including all 48 new cave blocks. The regenerated Toolbox IPS has SHA256
80c0c81cabf0109dc1a0253518d252431a4e55873d7960dc2c0b2d803f305be2:
it emits the same 61 records in a different order (readiness before retained
rematch blocks). Parsed address-to-byte writes compare equal, with no differing
records. Earlier wording that every distribution's whole-file hash is identical
was too strong; the memory writes are identical. The downloaded Toolbox NRO
contains two exact copies of its regenerated IPS. This is serialization order,
not a missing readiness patch or a compiler transformation.

Citron commit aa9d0851a adds a SHA256-pinned Debian mirror for the identical
xcb-util-renderutil 0.3.10 upstream tarball, after xcb.freedesktop.org TLS resets
on CI and local timeouts. Download checksum matches Debian's .dsc source
manifest and the original archive MD5. A lightweight CMake configuration probe
verified mirror priority, hash/timeout forwarding and no leakage to other
components. Linux-only build run 38813894582 has publication disabled so its
artifact can be combined with the successful Windows/macOS packages before
publishing. Previous run was canceled to avoid competing publication. Pending:
verify Linux build, gather all three artifacts and publish Citron 1.4.1.

Minus probe synthetic checks passed for wrapper w0 vs widget w1 ABI, actual
visibility changes, unchanged-value suppression and 64-hit bounds. Operator
can test later; no debugger or capture is running for this next measurement.
