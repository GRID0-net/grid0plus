# internal/npln

The NPLN gRPC gateway: the game server Splatoon 3 talks to once it has
authenticated. **This is where phase 1 hands over to phase 2.**

| | |
| --- | --- |
| Reimplements | `https://t-dce9377b-lp1.lp1.t.npln.srv.nintendo.net` (the Splatoon 3 tenant) |
| Reference | [NPLN Servers](https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers) |
| Definitions | [kinnay/NPLN-Protocols](https://github.com/kinnay/NPLN-Protocols), generated into `proto/` |
| Needs a database | yes |

## This is not the first thing the game talks to

Splatoon 3 does **not** open with gRPC. It first speaks ordinary JSON over
HTTPS to `gw.hac.lp1.vermillion…` and `val.hac.lp1.penne…`, and only then opens
a gRPC channel here. That front end is a different package and a different nginx
treatment — `proxy_pass` and HTTP/1.1, against this package's `grpc_pass` and
HTTP/2.

Routing either through the other's block completes the TLS handshake and then
fails with nothing in either log: DNS resolves, the port is open, TLS succeeds,
the game reports a generic communication error. `internal/nintendo/hosts.go`
keeps them apart and `cmd/switchnetctl`'s generator tests assert it.

## Files

| File | What |
| --- | --- |
| `server.go` | the gRPC server, the interceptors, and the unauthenticated-method allowlist |
| `metadata.go` | the `npln-tenant-id` / `authorization` / `uid` metadata |
| `auth.go` | the BAAS id-token seam, token issue, and identity resolution |
| `services.go` | auth, maintenance, Ugcstore (`internal/ugcstore`) and toyohr/gamesync registration |
| `matchmaking.go` | `Matchmaker` and `GameSessionService`, over `internal/matchmaking`'s queue |
| `friends.go` | `Friends`, over `internal/db`'s friendship store — the same rows BAAS reads |
| `presence_service.go` | `PresenceService`, over `internal/presence`'s TTL rules — the same rules BAAS reads |
| `messaging.go` | `Messaging`, over `internal/messaging`'s in-memory mailbox |
| `names.go` | the one NPLN resource name still built outside its own file — `userResourceName` |
| `errors.go` | how a refusal is shaped |

## Identity is fail-closed, and `uid` is not a credential

Every call carries a `uid` metadata field. **It is the client saying who it
believes it is.** The access token is what proves it.

`uid` is compared against the token's subject and otherwise ignored. A mismatch
is refused rather than resolved in either direction — a client sending someone
else's uid is broken or probing, and neither deserves an answer.

This is the same bug this repository is shaped around, in a new place. The
production server GRID0+ replaces resolved a console by a cached credential
and fell back to a process-wide "last account seen" when the lookup missed;
every console then acted as one person. A gateway that resolved its caller from
a client-supplied field would reproduce it exactly, and more directly.

So, on every authenticated call:

- the token must verify against **this server's** ES256 key;
- its subject must still exist as a user;
- its device account must still exist and still belong to that subject.

The last one matters because a token stays cryptographically valid until it
expires. Without re-reading, a console whose device account was deleted or moved
to another user would keep a working session for hours.

`TestAUIDThatDisagreesWithTheTokenIsRefused`,
`TestATokenStopsWorkingWhenItsUserIsDeleted` and
`TestATokenStopsWorkingWhenItsDeviceAccountMovesToAnotherUser` are the tests.
**Weakening one of them reintroduces the bug.**

## The phase-1 seam

`IssueToken` is where the two halves of GRID0+ meet. The game presents the
id token BAAS issued it — as `ExternalIdToken.nsa_id_token` — and receives an
NPLN access token signed with a different key **and a different algorithm**.

That separation is the point, and it runs in both directions:

- a **BAAS access token** presented here is refused. An access token acts on the
  player's own account; an id token is meant to be handed to a game server. If
  this accepted one, a copy reaching a game server would be usable against the
  account it belongs to.
- an **NPLN access token** is ES256 and BAAS's are RS256, verified by separate
  verifier types that each accept exactly one algorithm. See `internal/jwt`'s
  README for why that is not one verifier with two options.

## Two things about the wire

**Malformed metadata is refused unhelpfully, on purpose.** Nintendo answers
metadata it cannot parse with `Unimplemented` (12) on production and
`Unavailable` (14) on development — neither of which means what happened. The
game reacts to the code, so GRID0+ matches it; the log line on this side says
what was actually wrong.

## Matchmaking: the console lives on the stream

`TrackMatchmakingTicket` is how a console learns a match was formed. There is no
polling path, so three things follow:

- **the current state is sent immediately** on attaching, or a stream that
  arrives after its ticket already matched waits for an event that has happened;
- **the stream stays open after `SUCCEEDED`** until the client releases it;
  closing it immediately raced the client's session setup in hardware tests;
- **each recipient receives only its own matched user session**, as in the
  measured ticket shape. The actual roster is read from the session host.

Two refusals worth knowing about. A ticket carrying **user delegation tokens**
is refused rather than served for one player: honouring an unverified delegation
would let a console queue as anybody, and quietly queueing one of two players
leaves the console waiting for somebody who was never in the queue. And a
**session is invisible to anybody not in it** — `GetGameSession` answers
`NotFound`, not `PermissionDenied`, because who is playing with whom is not
public on a server whose point is a private group.

`CreateAcceptance` is left unimplemented deliberately: acceptance is the flow
where a formed match waits for each player to agree, GRID0+ never puts a
ticket into that state, and answering would mean inventing a response for a flow
that cannot occur.

**A stream that goes quiet is a dead stream.** `SubscribeMaintenanceSchedules`
carries a `KeepAlive` message in its own oneof, which is the protocol saying
outright that silence is not a valid state. GRID0+ has no maintenance
windows, but it cannot express that by returning — returning closes the stream,
which the client reads as the connection failing. It sends keep-alives instead.
ROADMAP §2.5 records the same rule for the friends streams.

## Not yet implemented, and how you find out

An RPC with no handler answers `Unimplemented`, and the interceptor **logs it
once, naming the method**. That log is the to-do list (ROADMAP §2.4), which is
why it is a mechanism here rather than a line in each handler — and why
`internal/toyohr` registers every service including the empty ones. An RPC on an
unregistered service fails identically with nothing in the log.

The check matches the *generated* message, not the code alone:
`codes.Unimplemented` is also what this server deliberately answers for a flow it
will not serve — a delegated matchmaking ticket, malformed metadata — and those
are decisions rather than gaps.

`grep -rn 'TODO(capture-needed)' .` for the shapes that are reasoned rather than
known — resource-name shapes among them, collected in `names.go`/each
service's own file so that one capture settles the lot — and whether the
game ever calls `RefreshToken`.

A 2026-09-20 decompile of Splatoon 3 v11.3.0's own main found the full list
of NPLN service *names* (not just methods) it links against, cross-checked
against kinnay's `NPLN-Servers` wiki page for the complete list any NPLN
title can use. That surfaced three services that were not registered here
at all -- the silent failure mode this section warns about, not the logged
one:

- `nn.npln.auth.v1.UserService` -- confirmed real and linked. Now
  registered with `GetUser` implemented (see `user_service.go`);
  `QueryUserExternalIds`/`QueryUserShortIds` stay on the embedded
  `Unimplemented` default, since nothing in this server has a consumer for
  either id space they map onto.
- `nn.npln.globalcounter.v1.GlobalCounterService` -- `GetSeason` is
  implemented (`globalcounter.go`): a special Big Run reads its community
  golden-egg total from it, kept by `internal/ugcstore/bigrun_eggs.go`. Every
  other method stays `Unimplemented`.
- `nn.npln.leaderboard.v1.LeaderboardService` -- `BrowseLeaderboard` answers
  an empty board for the season asked about (`leaderboard.go`), since GRID0+
  keeps no rankings; every request is logged in full so the season names can
  be read off the log. The rest stays `Unimplemented`.
  TODO(capture-needed): what a real board looks like.

`nn.npln.hydro.v1.Datastore`, also on the wiki's general-services list, does
not appear anywhere in Splatoon 3's binary and stays unregistered -- no
evidence this title uses it at all.

`nn.npln.ikaros.v1.CompetitionService` and both `nn.npln.timber.v1`
services (`CompetitionService`, `TradeBoxService`) join it: a 2026-09-21
decompile of Splatoon 3 v11.3.0 found zero references to any of their RPCs
anywhere in the binary (verified against the fully decompressed in-memory
image, not just the raw NSO file, since NSO segments can be LZ4-compressed
on disk and a naive `strings`/raw-byte search over the compressed file
produces false negatives for strings that are genuinely present once
loaded). None of the three are registered here.

## Matchmaking

`matchmaking.go` serves the two services; the queue and its rules are
`internal/matchmaking`, which knows nothing about gRPC.

Three things it refuses, each the same shape as the bug this repository is built
around:

- a ticket is created for the **token's** user, never for the `UserDefinitions`
  the request names;
- a ticket may only be tracked or cancelled by the console that made it, and
  somebody else's answers `NotFound` rather than `PermissionDenied` — whether it
  exists is not that caller's to learn;
- a game session may only be read or joined by a player who was actually matched
  into it, because the players list names other people.

Joining a session you were *not* matched into is backfill or a short alias.
Neither is implemented and neither shape is documented, so it is refused rather
than guessed at.

`SyncGameSession` adds a fourth: a matched player may always read a session
back (`KeepAliveOnly: true` needs no special standing beyond membership,
already checked above), but only the **host** may actually change it. The
one thing there is to change is the port the host published --
`internal/matchmaking`'s `Session.HostPort` -- because it is the one piece
of session state that genuinely lives on the host's own console and nowhere
else; everything else the request carries (participant counts, state, who
the host is) is ignored in favour of what this server already knows, same
as `AllocateIceServerSet` ignoring the request's tenant and user fields.

Resource names (`tenants/<t>/matchmakingTickets/<id>`, `.../gameSessions/<id>`)
are confirmed, not guessed -- see `ticketProto`'s doc comment in
`matchmaking.go` for the decompile evidence. `lastSegment` still parses
whatever the console echoes back leniently, including a bare id, so a wrong
prefix elsewhere in this package costs a cosmetic mismatch rather than
making every `Track` and `Cancel` answer `NotFound`.

### The confirmed RPC surface

A 2026-09-20 decompile of Splatoon 3 v11.3.0's own `main` (extracted from a
real, user-owned base+update dump; see `docs/CAPTURES/README.md` for how) gives
something no capture alone does: the exhaustive list of RPCs this specific
game links against, as literal path strings in its own `.rodata` -- not just
the ones it happened to call during whatever flow got captured.

`Matchmaker` (4 real RPCs, 3 implemented):

| RPC | Implemented |
| --- | --- |
| `CreateMatchmakingTicket` | yes |
| `TrackMatchmakingTicket` | yes |
| `CancelMatchmakingTicket` | yes |
| `CreateAcceptance` | **no** -- answers `Unimplemented` |

`GameSessionService` (15 real RPCs, 13 implemented):

| RPC | Implemented |
| --- | --- |
| `GetGameSession` | yes |
| `QueryGameSessions` | yes |
| `JoinGameSession` | yes |
| `AllocateIceServerSet` | yes |
| `BatchGetGameSessions` | yes |
| `ListUserSessions` | yes |
| `GetUserSession` | yes |
| `SyncGameSession` (this server's) | see below -- not one of the 15 |
| `CancelGameSessionCreationTicket` | yes -- private rooms, `rooms.go` |
| `CreateGameSessionCreationTicket` | yes -- private rooms, `rooms.go` |
| `CreateGameSessionShortAlias` | yes -- private rooms, `rooms.go` |
| `GetGameSessionShortAlias` | yes -- private rooms, `rooms.go` |
| `IssueMatchmakingIdToken` | **no** |
| `IssueUserDelegationToken` | yes -- `delegation.go`; not yet read by matchmaking |
| `ListLatencyMeasurementServers` | **no** |
| `TrackGameSessionCreationTicket` | yes -- private rooms, `rooms.go` |

`BatchGetGameSessions`, `ListUserSessions` and `GetUserSession` needed no new
server-side concept to answer: each is exactly `GetGameSession`'s own
authorization and lookup, or a slice of what `sessionProto` already builds,
reused rather than duplicated (see `matchmaking.go`). The remaining eight
fall through to the embedded `UnimplementedGameSessionServiceServer`, which
is the existing, correct behaviour for an RPC of genuinely unknown shape --
see "Not yet implemented, and how you find out" above. What the decompile
adds is knowing *in advance* which eight method names to expect in that log
on a real console, instead of being surprised by them one at a time.
`CreateGameSessionCreationTicket` / `TrackGameSessionCreationTicket` /
`CancelGameSessionCreationTicket` / `CreateGameSessionShortAlias` /
`GetGameSessionShortAlias` are the private-room flow: a private battle, a
Salmon Run room, or a squad opened for friends. Left `Unimplemented`, the
first of them made the game report `2321-4224` the moment a player pressed
"create room". They are now implemented in `rooms.go`, on the shapes a real
Nintendo capture shows as recorded in `NextendoNetwork/splatoon-3`'s notes --
second-hand, see `docs/CAPTURES/README.md` for what is confirmed and what is
still open. A room is a `matchmaking.Session` with its host as the only
player, so `GetGameSession`, `JoinGameSession` and gamesync treat it exactly
like a matched session once it exists; the differences (anyone holding its
name may read and join it, capacity is the mode's rather than the player
count, it expires from its last activity) are documented where they apply.

Nothing in the protocol removes a player. `GamesyncMemberGone` does, when
gamesync reports that a player's last watch stream on the session closed and
stayed closed (see `internal/gamesync/departures.go`) -- for every session,
since a matched session reopened by "Again!" is backfilled from its players
exactly as a room is joined. A room everybody has left is closed; a host
leaving a room others are still in is only removed, since what the game does
with such a room has not been seen.

`SyncGameSession` is a real RPC -- it is defined in the same upstream proto
set as the rest of `GameSessionService` -- but its path string does not
appear anywhere in Splatoon 3's binary, unlike all four RPCs this server does
implement. See its doc comment in `matchmaking.go` for what that does and does
not prove.

`internal/toyohr`'s README has the equivalent table for `FestService`.

## Friends and presence

**One friend graph, not two.** `friends.go` reads `internal/db`'s friendship
store directly — the same rows `internal/baas/friends.go` reads — rather than
keeping a second copy. `presence_service.go` resolves presence through
`internal/presence`, the package `internal/baas`'s own HTTP presence
endpoints were refactored onto for exactly this reason: a second
implementation of the TTL rules is how "friends were never online" happens
again, in a new place. See `docs/ROADMAP.md#2.5`.

Two things ROADMAP §2.5 calls out as easy to miss, both handled here:

- **`FriendUser.Relationship` sets `presence_deliverable` and
  `presence_receivable`** from `internal/presence.Visible`, in each
  direction — not left `false`, which would make the client neither show a
  friend's presence nor publish its own while the friend list itself still
  looks healthy.
- **`SubscribeFriendUsers` and `SubscribePresences` both stay warm**: a
  keep-alive timer resends on its own, and a fresh presence report on
  `internal/presencebus` (when `redis.url` is set — see `cmd/npln/main.go`)
  pushes an update immediately rather than waiting for the timer.

`SubscribePresences` only ever serves the caller's own presence and their
friends'; anything else named in the request is dropped rather than refusing
the whole call, the same "skip and log" reasoning
`internal/baas/friends.go`'s `listFriends` uses for a friendship naming a
deleted user.

Every authenticated call — not just PresenceService's own streams — counts as
liveness: `enter()` in `server.go` touches presence on every request, the
same as `internal/baas/router.go`'s `authenticated` middleware does for
HTTP. Without it, a console that only ever touches BAAS at login would never
look alive to `PresenceService`'s own TTL rules.

`Presence.Attributes` is what each player's own game publishes over
`KeepAlive` (`GameStatus`, `SessionId`, participant counts, ...), merged per
player and handed to their friends untouched — see
`presence_attributes.go` and `docs/CAPTURES/README.md` for the capture it
rests on. It is the only channel for in-game detail here:
`resources.proto`'s `State` enum has no `PLAYING`, so `PLAYING` is read down
to `ONLINE`.

### `KeepAlive` acks the client's pings, but throttled

The client pings on this bidi stream and resends its next ping the instant
it receives a reply to the last one — so acking every single ping
unconditionally makes the whole exchange spin as fast as the round trip
allows, not at any pace the client sets on its own. A real Nintendo server
sits far enough away that network latency alone keeps this slow; GRID0+
cannot assume that. Confirmed on real hardware on 2026-09-24: a mitmproxy
capture of GRID0+'s own console showed 101 ping/ack round trips in 2
seconds (~50Hz), ending with the console resetting every one of its
long-lived NPLN streams at once and the game showing "a communication error
has occurred" — with nothing actually disconnected.

`presenceKeepAliveAckMinInterval` (`presence_service.go`) delays each reply
until at least 50ms after the preceding one, using real wall-clock time.
It never drops a ping: the client waits for that reply before sending the
next one, so the former one-second drop window left it with only the opening
heartbeat and another hall error. Both working captures show essentially one
response per ping at roughly 20 per second. The opening reply is sent before
any ping arrives and announces the `{30s interval, 50s deadline}` contract.
See `docs/CAPTURES/npln-keepalive-ack-pacing-2026-09-24.md`.

## Messaging

`messaging.go` relays opaque payloads between two authenticated users:
`SendMessage` fans one out to any number of receivers, `SendAck`
acknowledges one back to its original sender, and `RecvMessage` streams a
caller's own mailbox — everything retained since its resume tokens, then
live as it arrives.

`MessageBody`'s `message_type` and `fields` are never read here, the same
reason `internal/baas` never parses a game's `appField`: guessing at a
game-private format is exactly what CLAUDE.md warns against for an
undocumented shape. Routing an ack needs nothing this server tracked
itself — `AckBody` already carries the original message's sender, so
`SendAck` delivers straight there.

`internal/messaging`'s mailbox is in memory, the same tradeoff
`internal/matchmaking`'s queue makes: a message undelivered when this
process restarts is one the sender's own retry resends, and it keeps this
package as protocol-agnostic as that one — it imports nothing from `proto/`,
and `messaging.go` is where a `Message` or `Ack` becomes a
`RecvMessageResponse`.

`Friends/ActivateUser` accepts `tenants/current/users/current` as the already
authenticated caller. An explicit different user is refused; the alias cannot
establish identity without a valid token.

The 2026-09-23 successful captures show `SubscribeMaintenanceSchedules`,
`SubscribeFriendUsers`, and `SubscribePresences` sending lightweight pulses
about every 20 seconds. Friend responses advertise 50 seconds; presence
heartbeats advertise 30 seconds. These values differ from the pulse cadence;
see [the capture comparison](../../docs/CAPTURES/npln-stream-liveness-2026-09-24.md).
