# gamesync

NPLN's document and watch service: the live lobby state Splatoon 3 shares between the consoles in
a session. It is shaped like Firestore: named documents in collections, with field masks, field
transforms, preconditions, transactions and a bidirectional watch stream.

| | |
| --- | --- |
| Stands in for | `nn.npln.gamesync.v1.Gamesync` (`gamesync.npln.nintendo.net`) |
| Reference | [NPLN Servers](https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers) |
| Definitions | [kinnay/NPLN-Protocols](https://github.com/kinnay/NPLN-Protocols), `latest/proto/gamesync/v1/` |
| Needs a database | no |

## Its own port, the same process

Splatoon 3 reaches gamesync directly on port **7575** instead of through the tenant host, so it is a
second gRPC listener rather than another service on the gateway. It still runs inside `cmd/npln`,
because it verifies the matchmaking id tokens the gateway signs, and splitting the two would mean
inventing a protocol to pass a key around.

## The store is in memory

A gamesync document is live lobby state: who is ready, which weapon somebody picked, how far a
countdown has got. It is worth nothing once the lobby ends, and nothing across a restart, because
the consoles that owned it have already given up. This is the same reasoning as
[`internal/matchmaking`](../matchmaking/README.md)'s queue. Save data is a different service (the
`CloudSave` service in [`internal/toyohr`](../toyohr/README.md)) and a different decision.

## How a console gets in

```
gateway:   a session token per player      ES256, issuer "gss", one-hour lifetime
gamesync:  IssueToken                      verifies it and returns an opaque session handle
```

- **The id token is verified, not just read.** A console can't name itself; the signature is what
  binds the name to the caller.
- **The issuer keeps the credentials apart.** An NPLN access token and a session token are both
  ES256, signed by the same key. `iss` separates them: a session token says `gss` and carries the
  `gamesync` object the console reads, while an access token carries the tenant URL as its issuer
  and has no such block.
- **The token's shape is the console's, not ours.** It arrives inside the ticket
  (`MatchedUserSession.matchmaking_id_token`) and the console takes `gsid`, `usid`, `uid` and `tid`
  out of it before it calls `IssueToken` at all. The field names are a client-facing contract; see
  `issueSessionToken` in `internal/npln/auth.go`.
- **The gateway only issues id tokens for the caller's own user.** The protocol names this case,
  `INCLUDE_ID_TOKEN_FOR_SAME_CLIENT`. A request naming somebody else is refused instead of
  filtered, because a console that asked for two tokens and got one would carry on with the peer it
  has and fail somewhere that says nothing about the call.

Session handles are random, not signed, because they live in this process's own map and there is
nothing for a signature to buy. A refresh token is **single use**: the old session is replaced, so a
leaked one stops working as soon as the real console refreshes.

## Rematches and observed phases

Public rematches reuse a gamesync session, not a waiting matchmaking ticket. An explicit `__gs/m`
reopen (`cp` and `ebf` true) starts a 20-second window for Versus as well as Coop. Versus keeps the
first deadline, and an additional Coop caller restarts it. Repeated writes from the same caller
don't extend it, and a client lock cancels the old callback. Directly created private rooms are
excluded.

The dashboard reads committed session events through `SessionPhase`: kickoff, each player's profile,
results, and the session reopening or locking afterwards. Receiving every profile is labelled
"ready", which isn't proof that play began. No match duration or presence timeout drives these
transitions.

## Writes are atomic

`apply` runs two passes: check every precondition against the current tree, then mutate. A batch that
fails changes nothing. A half-applied batch would leave the client's next read showing a state
neither side intended, with no way to tell which half landed, and the client is a game whose lobby
state has to agree across eight consoles. Two consequences:

- an unsupported field transform is rejected in the precondition pass, not when it is reached,
  because discovering it halfway would break the guarantee;
- the whole tree is under one lock, held across a batch. Per-document locking would be faster and
  would let a reader see half a batch.

## Preconditions and transactions

`Precondition.update_time` is optimistic concurrency. Two consoles updating the same lobby document
both send the version they read, and the second loses with `FailedPrecondition`. Without it the later
write silently wins and one console's view of the lobby is quietly discarded.

A committed transaction can't be committed again, so a client retrying a commit it believes failed
doesn't double a field it incremented. A rollback is idempotent: a client that lost its connection
mid-transaction retries it, and failing that leaves the client believing state is still pending.

## Field semantics

- **A masked field the client didn't send is a deletion.** That is what a field mask means, and
  reading it as "leave alone" would make clearing a field impossible. `MergeDocument` passes the
  names it did send as its own mask, which is what makes a merge leave everything else alone.
- **Integers stay integers.** `Increment` on an integer returns an integer. A client that increments
  an integer and reads back a double has had its type changed underneath it.
- **Incrementing an absent field creates it.** There is no separate "create field" step in this
  protocol, so the alternative is losing the first increment.
- **Masks are top level only.** A nested path would need structural pruning inside a `MapValue`, and
  getting that subtly wrong drops a field instead of failing. This errs towards sending too much,
  which is the harmless direction.

## The watch stream

`KeepUserSession` is bidirectional and is never closed from this side while the console holds it.

Its closing is, however, the only sign a console gives that it has left a
game session. When a user session's last stream closes and none reopens
within `memberGoneGrace` (10 s), `OnMemberGone` tells internal/npln, which
takes the player out of the session; see `departures.go` for the grid0 logs
that set the number.

A Salmon Run "Again!" window (`again_queue.go`) is bracketed by
`OnAgainOpen` and `OnAgainLocked`, so internal/npln can fill the reopened
session from the queue in between. `AgainJoined` restarts the window for each
arrival, the way each new player restarts the queue's own grace period, and
locks the session at once when it is full.

- **All sends come from one goroutine.** gRPC allows one concurrent `Send` per stream, and the reader
  loop sends echoes while document changes arrive from the store.
- **A target is acknowledged before any change for it arrives** (`LISTED`), so the client can tell
  "nothing has changed" from "my target never took effect".
- **An echo comes back verbatim.** It is the client's own liveness check.
- **A collection target fires for direct children only**, matching `ListDocuments`.
- **A send that would block is dropped.** A console that stopped reading mustn't stall a write for
  every other console in the lobby.

Splatoon 3 also watches `docs/@@RefereeResult` as a direct-child collection. Its `@@RefereeRequest`
write is a match-start barrier and its `@@RefereeReport/.../User/...` write is a results-screen
barrier. The result document with the same battle suffix moves through `status=-1` (request
accepted), `status=0` (the recipient has submitted their profile) and `status=1` (results). Startup
readiness is projected per authenticated user session, so another player's profile can't skip the
early phase for a late watcher. The service builds those verdicts from the committed request and
report; storing the writes without publishing the result leaves the game waiting even though
matchmaking succeeded. Results are scoped to the authenticated game session on reads and watches.

## Pia inboxes: `docs/__pgn/All/__stu/<usid>`

Before a match the consoles exchange Pia packets through gamesync. Each user session watches its own
`__stu/<usid>` document, which is its inbox. A peer writes a packet into the recipient's document and
stamps `suid`, `susid`, `suscid` and `sussid` with its own identity. The server delivers that write
to the recipient as written and adds nothing.

**Don't relabel.** An earlier version read each peer's document as that peer's outbox and renamed it
onto the caller's inbox. A peer's inbox holds the caller's own outgoing packets, so each console got
its own packets back stamped "from the peer", and the private room's "ready" step ended in a
communication error.

Peer state is kept per authenticated sender within a game session, so a late or reconnected watch
receives each sender's latest committed inbox state. The initial watch snapshot uses backpressure, and
a live notification overflow ends the stream explicitly instead of silently losing peer updates.
Tokens continue to decide identity and room scope, including transaction ownership.

Versus final verdicts are projected per recipient: a player stays at the kickoff status until their own
personal result arrives. The shared accumulated verdict is unchanged, and Salmon Run uses its existing
result shape.

## Not implemented

| RPC | Why |
| --- | --- |
| deferment operations | a batch of writes to apply later, on a trigger nothing here models. They are refused rather than dropped, because a client told "done" would wait for an effect that never arrives |
| global-value transforms | they read and write a namespace shared across documents. A no-op would have the client store a value it later reads back as absent |
| paging on `ListDocuments` | the whole store is one lobby's live state, tens of documents, so a page token would only name the one page |
| `CreateRound` | its message has fields whose meaning is unknown, and answering would mean inventing values |

## Lobby-control logging

`gamesync: lobby control write` lines (every console write to `docs/__gs/*` and `docs/SessionInfo/*`)
are off by default, because a busy Splatfest writes them several times per player per match. Turn them
on without restarting npln by creating a flag file, and off by removing it. npln notices within five
seconds and logs `gamesync: lobby control logging enabled=...`.

```sh
sudo -u switchnet touch /opt/switchnet/data/gamesync-lobby-log   # on
sudo rm /opt/switchnet/data/gamesync-lobby-log                   # off
```

`SWITCHNET_NPLN_LOBBY_LOG_FLAG` moves the file.
