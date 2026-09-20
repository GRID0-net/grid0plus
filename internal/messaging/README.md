# internal/messaging

An in-memory, per-user mailbox: opaque message and acknowledgement delivery
between two authenticated users.

| | |
| --- | --- |
| Reimplements | the mailbox behind `nn.npln.messaging.v1.Messaging` |
| Served by | `internal/npln/messaging.go`, which is where the gRPC surface lives |
| Reference | [NPLN Servers](https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers) |
| Needs a database | no |

This package is the queue and the routing rules; it knows nothing about gRPC
or protobuf, the same split `internal/matchmaking` has. `internal/npln`'s
`messaging.go` is where a `Message` or `Ack` becomes a `RecvMessageResponse`.

**Splatoon 3 does not appear to use this service at all.** A 2026-09-21
decompile of Splatoon 3 v11.3.0 found zero references anywhere in the
binary to `/nn.npln.messaging.v1.Messaging/{SendMessage,RecvMessage,
SendAck}` (checked against the fully decompressed in-memory image, not the
raw on-disk NSO — see `internal/npln/README.md`'s equivalent note for
`hydro`/`ikaros`/`timber` on why that distinction matters). What Splatoon 3
actually uses for in-lobby messages is a completely different, toyohr-
namespaced service instead —
`/nn.npln.toyohr.v1.LobbyMessaging/{SendLobbyMessage,SendMessage,
RecvMessage}`, already implemented in `internal/toyohr/messaging.go`. This
package and `internal/npln/messaging.go` remain a correct, real
reimplementation of the documented generic `Messaging` service, worth
keeping since other NPLN titles may use it, but any future work motivated
specifically by Splatoon 3 (friend invites, in-game chat) should look at
`internal/toyohr`'s `LobbyMessaging` first, not here.

## The mailbox is in memory, and that is deliberate

Same tradeoff as `internal/matchmaking`'s queue, for the same reason: a
message undelivered when this process restarts is one the sender's own retry
(or the game's own invite-flow timeout) resends, and there is no
cross-restart invite a private network needs to survive a deploy for.

Each user's mailbox is capped at `maxMailboxDepth` (200) messages and 200
acks; beyond that the oldest is dropped, so a console that never connects to
drain its mailbox cannot grow this process without bound.

## The payload is never read

`Body.Fields` and `Body.MessageType` are opaque to this package — held and
handed back exactly as given, never parsed. Guessing at a game's private
message format is exactly what CLAUDE.md warns against for an undocumented
shape; the same reason `internal/baas` never parses a game's `appField`.

## Acks route on what the client already has

`SendAck`'s caller is acknowledging a message; `Ack.Ack.SenderUser` names who
sent the ORIGINAL message, which is who this acknowledgement has to reach.
`Queue.Ack` delivers it to that original sender's mailbox — nothing here has
to have tracked the message itself first. `AckedByID` records who
acknowledged it, since one message can go to several receivers and the
sender needs to know which one is confirming.

## Resume tokens, not a live-only stream

`RecvMessageRequest` carries two independent resume tokens — one for new
messages, one for acks — because a console reconnects and expects to see
what it missed while disconnected, not just what arrives from here on. An
empty token means "everything currently retained": this is the invite
mailbox a session opens early in boot, and a pending invite sent while the
console was off has to still be there.

`Drain` answers that catch-up read; `Subscribe` is the live feed for
whatever arrives afterward. `internal/npln/messaging.go`'s `RecvMessage`
calls `Subscribe` before `Drain`, not after, so nothing sent in the gap
between the two is lost.

## Watchers cannot stall the mailbox

`notify` drops a message on a subscriber whose channel is full rather than
blocking the sender — the same trade `internal/presencebus` and
`internal/matchmaking`'s `Watch` make, for the same reason: a console that
stopped reading its stream must not hold up delivery to everyone else. The
resume token is what actually guarantees delivery; a dropped live push is
just made up on the next `Drain`.
