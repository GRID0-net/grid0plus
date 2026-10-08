# internal/gamesync

NPLN's document and watch service — the **live lobby state** Splatoon 3 shares
between the consoles in a session. Firestore-shaped: named documents in
collections, with field masks, field transforms, preconditions, transactions and
a bidirectional watch stream.

| | |
| --- | --- |
| Reimplements | `nn.npln.gamesync.v1.Gamesync` (`gamesync.npln.nintendo.net`) |
| Reference | [NPLN Servers](https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers) |
| Definitions | [kinnay/NPLN-Protocols](https://github.com/kinnay/NPLN-Protocols) `latest/proto/gamesync/v1/` |
| Needs a database | no |

## Its own port, the same process

Splatoon 3 reaches gamesync directly on **7575** rather than through the tenant
host, so it is a second gRPC listener rather than another service on the gateway.
It still runs inside `cmd/npln`, because it verifies the matchmaking id tokens
the gateway signs — splitting the two would mean inventing a protocol between
them to pass a key around.

## The store is in memory, deliberately

A gamesync document is live lobby state: who is ready, which weapon somebody
picked, how far a countdown has got. It is worth nothing once the lobby ends and
worth nothing across a restart — the consoles that owned it have already given
up. Same reasoning as `internal/matchmaking`'s queue.

**Save data is a different service** (`toyohr`'s `CloudSave`) and would be a
different decision.

## How a console gets in

```
gateway: a session token per player   ES256, iss "gss", 1-hour TTL
gamesync: IssueToken                  verifies it, returns an opaque session handle
```

Four things about that:

- **the id token is verified, not read.** A console cannot name itself; the
  signature is what binds the name to the caller.
- **the issuer is what keeps the credentials apart.** An NPLN access token and a
  session token are both ES256 signed by the same key. `iss` separates them: a
  session token says `gss` and carries the `gamesync` object the console reads,
  where an access token — which acts on the account API — carries the tenant URL
  as its issuer and no such block.
- **the shape is the console's, not ours.** The token arrives inside the ticket
  (`MatchedUserSession.matchmaking_id_token`) and the console takes
  `gsid`/`usid`/`uid`/`tid` out of it before it calls `IssueToken` at all, so the
  field names are a client-facing contract — see
  `docs/CAPTURES/npln-session-token-2026-09-23.md` for the capture that settles
  them, and `internal/npln/auth.go`'s `issueSessionToken` for the shape itself.
- **the gateway issues id tokens for the caller's own user only.** The protocol
  names this case itself, `INCLUDE_ID_TOKEN_FOR_SAME_CLIENT`. A request naming
  somebody else is refused rather than filtered: a console that asked for two
  tokens and got one would proceed with the peer it has and fail somewhere that
  says nothing about the call.

Session handles are random rather than signed, because they live in this
process's own map and there is nothing for a signature to buy. A refresh token is
**single-use** — the old session is replaced, so a leaked one stops working the
moment the legitimate console refreshes.

## Rematches and observed phases

Public rematches reuse a Gamesync session, not a waiting matchmaking ticket.
An explicit `__gs/m` reopen (`cp` and `ebf` true) starts a 20-second window
for Versus as well as Coop. Versus keeps the first deadline; an additional
Coop caller restarts it. Repeated writes from the same caller do not extend it,
and a client lock cancels the old callback. Directly created private rooms are
excluded. The earlier `bfmin=0` experiment has been removed; the captured
value remains 1. The timer alone did not resolve the earlier hardware rejoin
failure, so this behavior still needs testing with Toolbox 0.8.1's additional
lobby minimum-player comparisons.

The dashboard reads committed session events through `SessionPhase`: kickoff,
each player's profile, results, and subsequent session reopening/locking.
Receiving every profile is labelled "ready", not proof that gameplay began.
No match duration or presence timeout drives these transitions.

## Writes are atomic, and that is the point

`apply` runs two passes: check every precondition against the current tree, then
mutate. A batch that fails changes nothing.

This matters more than it sounds. A half-applied batch leaves the client's next
read showing a state neither side intended, **with no way to tell which half
landed** — and the client is a game whose lobby state has to agree across eight
consoles.

Two consequences:

- an unsupported field transform is rejected in the *precondition* pass, not
  when it is reached. Discovering it halfway through would break the guarantee.
- the whole tree is under one lock, held across a batch. Per-document locking
  would be faster and would let a reader observe half a batch.

## Preconditions are what make a transaction worth having

`Precondition.update_time` is optimistic concurrency: two consoles updating the
same lobby document both send the version they read, and the second one loses
with `FailedPrecondition`. Without it the later write silently wins and one
console's view of the lobby is quietly discarded — which is the class of bug that
shows up as "the lobby disagreed with itself" and is close to impossible to
reproduce.

A committed transaction cannot be committed again. A client retrying a commit it
believes failed must not double a field it incremented. A *rollback*, by
contrast, is idempotent: a client that lost its connection mid-transaction
retries it, and failing that leaves the client believing state is still pending.

## Field semantics worth knowing

**A masked field the client did not send is a deletion.** That is what a field
mask means, and reading it as "leave alone" makes clearing a field impossible.
`MergeDocument` therefore passes the field names it *did* send as its own mask,
which is what makes a merge leave everything else alone.

**Integers stay integers.** `Increment` on an integer field returns an integer.
A client that increments an integer and reads back a double has had its type
changed underneath it, and the value it writes next may not fit the field it came
from.

**Incrementing an absent field creates it.** There is no separate "create field"
step in this protocol, so the alternative is losing the first increment.

**Masks are top-level only.** A nested path would need structural pruning inside
a `MapValue`, and getting that subtly wrong drops a field rather than failing. It
errs towards sending too much, which is the harmless direction — and the limit is
stated here rather than looking like an oversight.

## The watch stream

`KeepUserSession` is bidirectional and **never closed from this side** while the
console holds it — the same rule `SubscribeMaintenanceSchedules` and
`internal/nplnrest`'s frontline connection follow.

- **all sends go from one goroutine.** gRPC allows one concurrent `Send` per
  stream; the reader loop sends echoes while document changes arrive from the
  store, and two senders would be a race the transport does not protect against.
- **a target is acknowledged before any change for it arrives** (`LISTED`), so
  the client can tell "nothing has changed" from "my target never took effect".
- **an echo comes back verbatim.** It is the client's own liveness check, so
  altering the payload defeats the only thing it is for.
- **a collection target fires for direct children only**, matching
  `ListDocuments`, or the client receives changes for documents it has never
  seen.
- **a send that would block is dropped.** A console that stopped reading must not
  stall a write for every other console in the lobby.

Splatoon 3 also watches `docs/@@RefereeResult` as a direct-child collection.
Its `@@RefereeRequest` write is a match-start barrier, and its
`@@RefereeReport/.../User/...` write is a results-screen barrier. A working
match capture shows a result document with the same battle suffix changing from
`status=0` to `status=1`. The service constructs those two verdicts from the
committed request and report; storing the writes without publishing the result
leaves the game waiting even though matchmaking already succeeded. Results are
scoped to the authenticated game session on both reads and watches.

## Pia inboxes: `docs/__pgn/All/__stu/<usid>`

Before a match, the consoles exchange Pia packets through gamesync. Each user
session watches **its own** `__stu/<usid>` document, which is its *inbox*. A peer
writes a packet into the *recipient's* document and stamps `suid`/`susid`/
`suscid`/`sussid` with its own identity. The server delivers that write to the
recipient as written and adds nothing.

Do not relabel. An earlier version read each peer's document as that peer's
outbox and renamed it onto the caller's inbox. A peer's inbox holds the
caller's *own* outgoing packets, so each console got its own packets back
stamped "from the peer". From then on the joiner addressed its packets to
itself, and the private room's "ready" step ended in a communication error.

Peer state is retained per authenticated sender within a game session, so a
late or reconnected watch receives each sender's latest committed inbox state.
The initial watch snapshot uses backpressure; live notification overflow ends
the stream explicitly rather than silently losing peer updates. Tokens continue
to determine identity and room scope, including transaction ownership.

Versus final verdicts are projected for each recipient: a player remains at the
kickoff status until their own personal result arrives. The shared accumulated
verdict remains unchanged; Salmon Run uses its existing result shape.

## Not implemented, and why

| RPC | Why |
| --- | --- |
| deferment operations | a batch of writes to apply later, on a trigger nothing here models. Refused rather than dropped: a client told "done" would wait for an effect that never arrives |
| global-value transforms | they read and write a namespace shared across documents. A no-op would have the client store a value it later reads back as absent |
| paging on `ListDocuments` | the whole store is one lobby's live state — tens of documents. A page token would name the only page. The absence is visible here rather than hidden behind an empty `next_page_token` |
