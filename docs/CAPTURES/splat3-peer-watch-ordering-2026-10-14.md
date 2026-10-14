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
