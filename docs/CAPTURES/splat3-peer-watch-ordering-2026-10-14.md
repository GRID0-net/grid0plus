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
