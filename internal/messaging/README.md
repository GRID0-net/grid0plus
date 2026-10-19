# messaging

An in-memory, per-user mailbox that delivers opaque messages and acknowledgements between
two authenticated users.

| | |
| --- | --- |
| Stands in for | the mailbox behind `nn.npln.messaging.v1.Messaging` |
| Served by | `internal/npln/messaging.go`, which holds the gRPC side |
| Reference | [NPLN Servers](https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers) |
| Needs a database | no |

This package is the queue and the routing rules. It knows nothing about gRPC or protobuf,
the same split `internal/matchmaking` has.

Splatoon 3 itself uses a different service for in-lobby messages, the `LobbyMessaging`
service in [`internal/toyohr`](../toyohr/README.md). This package serves the generic
`Messaging` service that other NPLN titles may use.

## The mailbox is in memory

A message that is undelivered when the process restarts is one the sender's own retry (or the
game's invite timeout) will send again, and a private network doesn't need invites to survive
a deploy. Each user's mailbox is capped at 200 messages and 200 acknowledgements; past that
the oldest is dropped, so a console that never connects can't make the process grow without
bound.

## The payload is never read

`Body.Fields` and `Body.MessageType` are held and handed back exactly as given. Guessing at a
game's private message format would be inventing something, so this package doesn't parse
them.

## Acknowledgements

`SendAck`'s caller is acknowledging a message, and `Ack.Ack.SenderUser` names who sent the
original, which is who the acknowledgement has to reach. `Queue.Ack` delivers it to that
sender's mailbox. `AckedByID` records who acknowledged, because one message can go to several
receivers and the sender needs to know which one is confirming.

## Resume tokens

`RecvMessageRequest` carries two independent resume tokens, one for messages and one for
acknowledgements, because a reconnecting console expects to see what it missed. An empty token
means "everything currently retained". `Drain` answers that catch-up read and `Subscribe` is
the live feed afterwards. `RecvMessage` subscribes before it drains, so nothing sent in the
gap is lost.

## Watchers can't stall the mailbox

`notify` drops a message for a subscriber whose channel is full instead of blocking the sender,
the same trade `internal/presencebus` makes: a console that stopped reading its stream mustn't
hold up delivery to everyone else. The resume token is what guarantees delivery, so a dropped
live push is made up on the next `Drain`.
