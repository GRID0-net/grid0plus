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
production server SwitchNet replaces resolved a console by a cached credential
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

`IssueToken` is where the two halves of SwitchNet meet. The game presents the
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
game reacts to the code, so SwitchNet matches it; the log line on this side says
what was actually wrong.

## Matchmaking: the console lives on the stream

`TrackMatchmakingTicket` is how a console learns a match was formed. There is no
polling path, so three things follow:

- **the current state is sent immediately** on attaching, or a stream that
  arrives after its ticket already matched waits for an event that has happened;
- **the stream ends when the ticket is terminal**, because it cannot change
  again and holding it open leaves the console waiting on nothing;
- **both players are named on the ticket** rather than left for a follow-up
  call, since the ticket stream is the only thing the console is certainly
  reading at that moment.

Two refusals worth knowing about. A ticket carrying **user delegation tokens**
is refused rather than served for one player: honouring an unverified delegation
would let a console queue as anybody, and quietly queueing one of two players
leaves the console waiting for somebody who was never in the queue. And a
**session is invisible to anybody not in it** — `GetGameSession` answers
`NotFound`, not `PermissionDenied`, because who is playing with whom is not
public on a server whose point is a private group.

`CreateAcceptance` is left unimplemented deliberately: acceptance is the flow
where a formed match waits for each player to agree, SwitchNet never puts a
ticket into that state, and answering would mean inventing a response for a flow
that cannot occur.

**A stream that goes quiet is a dead stream.** `SubscribeMaintenanceSchedules`
carries a `KeepAlive` message in its own oneof, which is the protocol saying
outright that silence is not a valid state. SwitchNet has no maintenance
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
- `nn.npln.globalcounter.v1.GlobalCounterService` and
  `nn.npln.leaderboard.v1.LeaderboardService` -- both confirmed real and
  linked, both left entirely `Unimplemented` (now at least registered, so a
  real call is logged). Each is a substantial subsystem — achievement-style
  counters/holders, and leaderboard scores/charts/categories — with no
  capture or documentation to build real behaviour from; implementing a
  method from the proto shape alone would be inventing state, not reporting
  it. TODO(capture-needed): what Splatoon 3 actually asks either for.

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

`GameSessionService` (15 real RPCs, 7 implemented):

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
| `CancelGameSessionCreationTicket` | **no** |
| `CreateGameSessionCreationTicket` | **no** |
| `CreateGameSessionShortAlias` | **no** |
| `GetGameSessionShortAlias` | **no** |
| `IssueMatchmakingIdToken` | **no** |
| `IssueUserDelegationToken` | **no** |
| `ListLatencyMeasurementServers` | **no** |
| `TrackGameSessionCreationTicket` | **no** |

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
`GetGameSessionShortAlias` / `CreateGameSessionShortAlias` read as a
private-room-by-code flow parallel to ticket-based matchmaking -- the same
feature `QueryGameSessions`'s doc comment already guesses at -- but that is a
reading of the names, not a confirmed shape, and implementing any of them
from a guess would be the mistake CLAUDE.md warns against.

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

`Presence.Attributes` (a `map<string, nn.npln.common.Value>` in
`resources.proto`) is left empty: nothing confirms the keys a real client
reads, and `resources.proto`'s own `State` enum has no `PLAYING` — `PLAYING`
is read down to `ONLINE` here, on the theory that BAAS's richer detail (title
id, app field) is what the friends applet reads separately rather than
through this service. Both are `TODO(capture-needed)`.

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
