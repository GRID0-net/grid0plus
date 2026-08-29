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
| `services.go` | auth and maintenance |
| `matchmaking.go` | `Matchmaker` and `GameSessionService`, over `internal/matchmaking`'s queue |
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

**A stream that goes quiet is a dead stream.** `SubscribeMaintenanceSchedules`
carries a `KeepAlive` message in its own oneof, which is the protocol saying
outright that silence is not a valid state. SwitchNet has no maintenance
windows, but it cannot express that by returning — returning closes the stream,
which the client reads as the connection failing. It sends keep-alives instead.
ROADMAP §2.5 records the same rule for the friends streams.

## Not yet implemented

Registered services answer `Unimplemented` for RPCs that have no handler, and
log. That log is the to-do list (ROADMAP §2.4).

`grep -rn 'TODO(capture-needed)' .` for the shapes that are reasoned rather than
known.

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

Resource names (`tenants/<t>/matchmakingTickets/<id>` and friends) are
`TODO(capture-needed)`. `lastSegment` parses whatever the console echoes back
leniently, including a bare id, so a wrong prefix costs a cosmetic mismatch
instead of making every `Track` and `Cancel` answer `NotFound`.
