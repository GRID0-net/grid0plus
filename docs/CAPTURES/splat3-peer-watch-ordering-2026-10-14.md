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
