# npln

The NPLN gRPC gateway: the game server Splatoon 3 talks to once it has authenticated. This is where the account
layer hands over to the game server.

| | |
| --- | --- |
| Stands in for | `t-dce9377b-lp1.lp1.t.npln.srv.nintendo.net` (the Splatoon 3 tenant) |
| Reference | [NPLN Servers](https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers) |
| Definitions | [kinnay/NPLN-Protocols](https://github.com/kinnay/NPLN-Protocols), generated into `proto/` |
| Needs a database | yes |

## This isn't the first thing the game talks to

Splatoon 3 doesn't open with gRPC. It first speaks ordinary JSON over HTTPS to the vermillion and penne hosts
([`internal/nplnrest`](../nplnrest/README.md)), and only then opens a gRPC channel here. They need different nginx
treatment: `proxy_pass` with HTTP/1.1 for the JSON front end, `grpc_pass` with HTTP/2 here. Routing either through the
other's block completes the TLS handshake and then fails with nothing in either log. `internal/nintendo/hosts.go`
keeps them apart and the generator tests assert it.

## Files

| File | What |
| --- | --- |
| `server.go` | the gRPC server, the interceptors and the allowlist of unauthenticated methods |
| `metadata.go` | the `npln-tenant-id`, `authorization` and `uid` metadata |
| `auth.go` | the BAAS id-token seam, token issue and identity resolution |
| `services.go` | registration of auth, maintenance, Ugcstore and the toyohr and gamesync services |
| `matchmaking.go`, `rooms.go` | `Matchmaker` and `GameSessionService` over the queue in `internal/matchmaking`, and private rooms |
| `friends.go` | `Friends`, over the same friendship rows BAAS reads |
| `presence_service.go` | `PresenceService`, over the same rules BAAS reads (`internal/presence`) |
| `messaging.go` | `Messaging`, over `internal/messaging`'s in-memory mailbox |
| `names.go` | resource name builders |
| `errors.go` | how a refusal is shaped |

## Identity fails closed, and `uid` isn't a credential

Every call carries a `uid` metadata field. It is the client saying who it believes it is. The access token is what
proves it. `uid` is compared against the token's subject and otherwise ignored, and a mismatch is refused rather than
resolved in either direction. A gateway that resolved its caller from a client-supplied field would reproduce the
"every console is one person" bug directly.

So on every authenticated call:

- the token must verify against this server's ES256 key;
- its subject must still exist as a user;
- its device account must still exist and still belong to that subject.

The last one matters because a token stays cryptographically valid until it expires. Without re-reading, a console
whose device account was deleted or moved to another user would keep a working session for hours.
`TestAUIDThatDisagreesWithTheTokenIsRefused`, `TestATokenStopsWorkingWhenItsUserIsDeleted` and
`TestATokenStopsWorkingWhenItsDeviceAccountMovesToAnotherUser` are the tests. Weakening one of them reintroduces the bug.

## Where the account layer meets the game server

`IssueToken` is where the two halves meet. The game presents the id token BAAS issued it (as
`ExternalIdToken.nsa_id_token`) and receives an NPLN access token signed with a different key and a different algorithm.
That separation runs both ways:

- a **BAAS access token** presented here is refused. An access token acts on the player's own account, and an id token
  is meant to be handed to a game server. If this accepted an access token, a copy reaching a game server would be
  usable against the account it belongs to.
- an **NPLN access token** is ES256 and BAAS's are RS256, verified by separate verifier types that each accept exactly
  one algorithm. See [`internal/jwt`](../jwt/README.md).

**Malformed metadata is refused unhelpfully, on purpose.** Nintendo answers metadata it can't parse with
`Unimplemented` (12) on production and `Unavailable` (14) on development, and neither means what happened. The game
reacts to the code, so GRID0+ matches it, and the log line on this side says what was actually wrong.

## Matchmaking

`matchmaking.go` serves `Matchmaker` and `GameSessionService`. The queue and its rules are in
[`internal/matchmaking`](../matchmaking/README.md), which knows nothing about gRPC.

`TrackMatchmakingTicket` is how a console learns a match was formed, and there is no polling path, so:

- **the current state is sent immediately** on attaching, or a stream that arrives after its ticket already matched
  waits for an event that has already happened;
- **the stream stays open after `SUCCEEDED`** until the client releases it, because closing it straight away raced the
  client's session setup on hardware;
- **each recipient receives only its own matched user session**, and the real roster is read from the session host.

Things it refuses, each the same shape as the bug this repository is built around:

- a ticket is created for the **token's** user, never for the `UserDefinitions` the request names;
- a ticket may only be tracked or cancelled by the console that made it, and somebody else's answers `NotFound` rather
  than `PermissionDenied`, because whether it exists isn't that caller's to learn;
- a game session may only be read or joined by a player who was matched into it, because the players list names other
  people. It answers `NotFound`, not `PermissionDenied`, for the same reason;
- a ticket carrying **user delegation tokens** is refused rather than served for one player. Honouring an unverified
  delegation would let a console queue as anybody, and quietly queueing one of two players would leave the console
  waiting for somebody who was never in the queue.

Joining a session you weren't matched into is backfill or a short alias. Neither is implemented, so it is refused.
`CreateAcceptance` is also left unimplemented, since GRID0+ never puts a ticket into the state where a formed match
waits for each player to agree.

`SyncGameSession` lets a matched player always read a session back, but only the **host** may change it. The one thing
there is to change is the port the host published (`Session.HostPort` in `internal/matchmaking`), because it lives on
the host's own console and nowhere else. Everything else in the request is ignored in favour of what the server already
knows.

### Private rooms

`CreateGameSessionCreationTicket`, `TrackGameSessionCreationTicket`, `CancelGameSessionCreationTicket`,
`CreateGameSessionShortAlias` and `GetGameSessionShortAlias` are the private-room flow: a private battle, a Salmon Run
room or a squad opened for friends (`rooms.go`). Left unimplemented, the first one makes the game report `2321-4224` the
moment a player presses "create room". A room is a `matchmaking.Session` with its host as the only player, so
`GetGameSession`, `JoinGameSession` and gamesync treat it like a matched session once it exists. The differences are:
anyone holding its name may read and join it, its capacity is the mode's rather than the player count, and it expires from
its last activity.

### RPCs and their state

`Matchmaker` has four RPCs and implements three. `CreateAcceptance` answers `Unimplemented`.

`GameSessionService` has fifteen RPCs. Everything is implemented except `IssueMatchmakingIdToken` and
`ListLatencyMeasurementServers`, which answer `Unimplemented`. `IssueUserDelegationToken` is implemented (`delegation.go`)
but isn't read by matchmaking yet. `BatchGetGameSessions`, `ListUserSessions` and `GetUserSession` are `GetGameSession`'s
own authorization and lookup, reused rather than duplicated.

`SyncGameSession` is a real RPC, defined in the same upstream proto set, though its path doesn't appear in the game's
binary. See its comment in `matchmaking.go`. [`internal/toyohr`](../toyohr/README.md) has the equivalent table for the
Splatfest service.

Nothing in the protocol removes a player. `GamesyncMemberGone` does, when
gamesync reports that a player's last watch stream on the session closed and
stayed closed (see `internal/gamesync/departures.go`) -- for every session,
since a matched session reopened by "Again!" is backfilled from its players
exactly as a room is joined. A room everybody has left is closed; a host
leaving a room others are still in is only removed, since what the game does
with such a room has not been seen.

`again_backfill.go` connects the two for a Salmon Run "Again!": gamesync's
window opens the job to `matchmaking.OpenBackfill`, every sweep and ticket
reports who joined it (`applyBackfills`), and the lock closes it.

Other services registered here:

- `nn.npln.auth.v1.UserService`: `GetUser` is implemented (`user_service.go`). The other two methods stay unimplemented,
  because nothing here uses either id space they map onto.
- `nn.npln.globalcounter.v1.GlobalCounterService`: `GetSeason` is implemented (`globalcounter.go`). A special Big Run reads
  its community golden-egg total from it, kept by `internal/ugcstore/bigrun_eggs.go`.
- `nn.npln.leaderboard.v1.LeaderboardService`: `BrowseLeaderboard` answers an empty board for the season asked about
  (`leaderboard.go`), since GRID0+ keeps no rankings. Every request is logged in full.

Services the game never calls (`hydro`, `ikaros`, `timber`) aren't registered.

## Not implemented, and how you find out

An RPC with no handler answers `Unimplemented`, and the interceptor logs it once, naming the method. That log is the
to-do list, which is why it is a mechanism here instead of a line in each handler, and why `internal/toyohr` registers
every service, including the empty ones. An RPC on an unregistered service fails identically with nothing in the log.

The check matches the generated message, not the code alone. `codes.Unimplemented` is also what the server deliberately
answers for a flow it won't serve (a delegated ticket, malformed metadata), and those are decisions rather than gaps.

`grep -rn 'UNCONFIRMED' .` lists the shapes that are reasoned rather than known.

## Friends and presence

**There is one friend graph, not two.** `friends.go` reads the friendship store directly, the same rows
`internal/baas/friends.go` reads, rather than keeping a copy. `presence_service.go` resolves presence through
[`internal/presence`](../presence/README.md), because a second implementation of the time-limit rules is how "friends
were never online" happens again in a new place. Two things are easy to miss, and both are handled:

- **`FriendUser.Relationship` sets `presence_deliverable` and `presence_receivable`** from `presence.Visible`, in each
  direction. Left `false`, the client would neither show a friend's presence nor publish its own while the friend list
  itself looked healthy.
- **`SubscribeFriendUsers` and `SubscribePresences` stay warm.** A keep-alive timer resends on its own, and a fresh
  presence report on the presence bus (when `redis.url` is set) pushes an update immediately.

`SubscribePresences` only serves the caller's own presence and their friends'. Anything else named in the request is
dropped instead of refusing the whole call.

Every authenticated call counts as liveness: `enter()` in `server.go` touches presence on every request, as
`internal/baas/router.go` does for HTTP. Without it, a console that only touches BAAS at login would never look alive.

`Presence.Attributes` is what each player's own game publishes over `KeepAlive` (`GameStatus`, `SessionId`, participant
counts and so on), merged per player and handed to their friends untouched (`presence_attributes.go`). It is the only
channel for in-game detail, because the `State` enum has no `PLAYING`, so `PLAYING` is read down to `ONLINE`.

### KeepAlive acknowledgements are throttled

The client pings on this bidirectional stream and sends its next ping the instant it receives a reply to the last one.
Acknowledging every ping immediately would make the exchange spin as fast as the round trip allows, and a real Nintendo
server is far enough away that latency alone keeps it slow. GRID0+ can't assume that. On hardware, an unthrottled
server produced about 50 ping and ack round trips per second, and the console then reset all its long-lived NPLN streams
and showed "a communication error has occurred" with nothing actually disconnected.

`presenceKeepAliveAckMinInterval` (`presence_service.go`) delays each reply until at least 50 ms after the previous one,
in real time. It never drops a ping, because the client waits for the reply before sending the next. The opening reply
is sent before any ping arrives and announces the contract: a 30-second interval and a 50-second deadline.

`SubscribeMaintenanceSchedules` carries a `KeepAlive` message in its own oneof, which is the protocol saying outright
that silence isn't a valid state. GRID0+ has no maintenance windows but can't express that by returning, because
returning closes the stream, which the client reads as the connection failing. It sends keep-alives instead. The
maintenance, friends and presence streams send lightweight pulses about every 20 seconds, while friend responses
advertise 50 seconds and presence heartbeats 30.

## Messaging

`messaging.go` relays opaque payloads between two authenticated users: `SendMessage` fans one out to any number of
receivers, `SendAck` acknowledges one back to its original sender, and `RecvMessage` streams the caller's own mailbox
(what is retained since its resume tokens, then live). `message_type` and `fields` are never read. Routing an ack needs
nothing the server tracked, because `AckBody` already carries the original message's sender. See
[`internal/messaging`](../messaging/README.md).

`Friends/ActivateUser` accepts `tenants/current/users/current` as the already authenticated caller. An explicit
different user is refused, because the alias can't establish identity without a valid token.
