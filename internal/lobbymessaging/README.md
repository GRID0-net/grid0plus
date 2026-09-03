# internal/lobbymessaging

An in-memory broadcast topic per lobby, for Splatoon 3's
`nn.npln.toyohr.v1.LobbyMessaging`.

| | |
| --- | --- |
| Reimplements | `RecvMessage`/`SendLobbyMessage` behind `nn.npln.toyohr.v1.LobbyMessaging` |
| Served by | `internal/toyohr/lobby_messaging.go`, which is where the gRPC surface lives |
| Reference | [NPLN Servers](https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers) |
| Needs a database | no |

Same split `internal/messaging` and `internal/matchmaking` have: this package
is the topic and the resume-token bookkeeping, and knows nothing about gRPC
or protobuf.

## Membership is "who is listening", not a roster

SwitchNet has no separate record of which console is in which lobby, and
building one here would be a second copy of state nothing else in the
codebase maintains -- the exact class of bug ROADMAP.md warns about for
presence and friends, just for lobby membership instead.

So a lobby's membership is defined the same way `internal/presencebus`'s
live feed and `internal/matchmaking`'s ticket `Watch` already define
membership for their own purposes: you are "in" the lobby for delivery
purposes exactly as long as you are subscribed to its topic.
`RecvMessage(User, Lobby, ...)` naming a lobby and staying connected **is**
the membership, not a side effect of some other join call that does not
exist.

## `SendMessage` is deliberately not implemented here

`LobbyMessaging.SendMessage(User, ReceiverUsers[], Body)` addresses specific
users directly and, unlike `SendLobbyMessage`, carries no `Lobby` field at
all -- there is nothing in the request naming which topic a receiver might
be listening on. Routing it correctly would mean SwitchNet tracking which
lobby each user last tuned into as a side effect of their own `RecvMessage`
calls, and delivering into that -- a real design, but one this pass chose
not to guess at building without a capture to check it against. See the
`TODO(capture-needed)` on `Service.SendMessage` in
`internal/toyohr/lobby_messaging.go`.

## The payload is never read

Same as `internal/messaging.Body`: `MessageType` and `Fields` are the game's
own vocabulary, held and handed back exactly as given. Parsing them would be
guessing at an undocumented, game-private format.

## Resume tokens and retention

Same shape as `internal/messaging`: an empty resume token means "everything
currently retained", `Drain` answers the catch-up read, `Subscribe` is the
live feed for afterward -- called in that order so nothing sent in the gap
between the two is lost. Each topic is capped at `maxTopicDepth` (200)
messages; beyond that the oldest is dropped, so a lobby nobody drains cannot
grow this process without bound. In memory, for the same reason
`internal/messaging`'s mailbox is: nothing here needs to survive a restart
that a console's own reconnect does not already paper over.
