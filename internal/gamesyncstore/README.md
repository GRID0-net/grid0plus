# internal/gamesyncstore

The document store behind `internal/gamesync`, which implements
`nn.npln.gamesync.v1.Gamesync` -- see
[the Kinnay wiki's NPLN Servers page](https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers)
for where Gamesync sits among NPLN's services. This package holds no gRPC or
Nintendo-specific logic of its own; it is a Firestore-shaped key/value store
(get/list/query, update/merge/transform/delete, coarse transactions, and a
change feed for watch streams) that works directly in terms of the generated
proto types (`proto/gamesync/v1.Document`, `proto/common.Value`) rather than
a private representation.

In memory, like `internal/matchmaking` and `internal/messaging`: a document
here is worth nothing once the process restarts, because the console that
wrote it re-syncs the moment it reconnects. There is no persistence story for
Gamesync documents yet -- see TODO.md §1.5 before building one, since nothing
here has established what, if anything, needs to survive a restart.

## What is real and what is simplified

Every RPC's wire shape comes straight from the generated proto, which is
exact -- there is nothing to guess at there. Two pieces of *semantics*,
though, are not documented anywhere this project could find, and are
implemented as a defensible simplification rather than a guess at what
Nintendo's own server does:

- **Transactions are atomic but not isolated.** `CommitTransaction` checks
  every operation's precondition against the store's current state, applies
  all of them if every precondition holds, and applies none if any fails --
  so a commit cannot half-land. It does **not** take a snapshot at
  `BeginTransaction` and detect a conflicting write that happened in between;
  two transactions open at once can each see the other's writes. Building
  real snapshot isolation without a capture to check the retry/conflict
  behaviour against would be solving a concurrency problem this deployment
  does not have, for a handful of consoles sharing one lobby's documents.
- **`FieldTransform`'s `LoadGlobalValue`/`StoreGlobalValue`/`ClearGlobalValue`
  have no Firestore equivalent and no public documentation.** They are
  answered as a no-op null result rather than rejecting the whole write --
  see `applyTransform`'s `TODO(capture-needed)`.

`DefermentOperation` is accepted at the RPC layer (`internal/gamesync`) and
discarded entirely; nothing found documents what a "deferment" is for, and
this package does not carry state for a feature it cannot implement.

## Resource names

A document's name is an opaque path string (e.g.
`tenants/<t>/games/<id>/players/<uid>`) -- this package never validates or
interprets it beyond splitting on `/` for `List` and `CollectionIDs`. What
the real path shapes look like for Splatoon 3's lobby/match documents is
itself undocumented; see TODO.md §1.5.

## Watching for changes

`Subscribe` returns every `Change` from that point on. A slow subscriber's
channel has room for `changeBuffer` pending changes before new ones are
dropped for it rather than blocking every writer -- `internal/gamesync`'s
`KeepUserSession` handler resends a fresh `Document` as `EXIST` whenever a
target is (re-)registered, so a client that fell behind converges the next
time it re-lists a target, the same way a Firestore client resyncs after a
`RESET`.
