# internal/ugcstore

`nn.npln.ugcstore.v1.Ugcstore` — the document store Splatoon 3 keeps its own
per-player records in. Served on the NPLN gateway (`internal/npln` registers
it) and shared with `internal/toyohr`, whose `GameRecord/InitializeAttributes`
creates documents through it.

| | |
| --- | --- |
| Reimplements | `nn.npln.ugcstore.v1.Ugcstore` on the Splatoon 3 tenant host |
| Definitions | [kinnay/NPLN-Protocols](https://github.com/kinnay/NPLN-Protocols), generated into `proto/ugcstore/v1` |
| Evidence | [`docs/CAPTURES/npln-gamerecord-attributes-2026-09-23.md`](../../docs/CAPTURES/npln-gamerecord-attributes-2026-09-23.md) |
| Needs a database | yes — `ugc_documents` (migration 0018) |

## Why it exists

The game reads its GameRecord documents (`…/users/<uid>/VsUserAttribute/Data`,
`…/CoopUserAttribute/Data`, …) right after signing in and on entering the
lobby, creates missing ones through `GameRecord/InitializeAttributes`, and reads
them back. Before this package the service was not registered at all.

## The rules it keeps, and why each one

| Rule | Because |
| --- | --- |
| `GetDocument` is registered as server-streaming although the console sends one request | this matches the working S3 implementation and lets an absent read end without sending a protobuf message |
| a missing document is `NotFound`, headers and trailers in separate frames | an empty `Document`, or a single Trailers-Only frame, crashes the game (`2162-0001`) — the NPLN stream interceptor forces the opening headers |
| writes persist in Postgres | a document the game wrote and cannot read back makes it re-initialise forever |
| names must be `tenants/<tenant>/documents/<rest>` | the game's own name parser aborts on anything else, and a stored bad name is read back on every start |
| `tenants/current` and both forms of our tenant id are one document | the console uses more than one form |
| a `users/<id>/` path is writable only by that player; otherwise by its creator | identity is fail-closed |
| a commit is all or nothing | a refused operation must not leave half a batch written |

Fields are stored as the encoded `MapValue`, so value types survive exactly.

## Implemented

| RPC | |
| --- | --- |
| `GetDocument` | yes |
| `CommitDocuments` | update (with or without a field mask, with preconditions), delete, field transforms (`increment`, `maximum`, `minimum`, `set_server_value: REQUEST_TIME`); attachment operations refuse the batch |
| `RunQuery` | the captured empty-query response only; it does not yet enumerate matching stored documents (see `docs/SPLATOON3_NEXTENDO_AUDIT.md`) |
| `BulkGetDocuments`, attachments, short aliases, `IssueUploadUri` | **no** — `Unimplemented`, logged once by the gateway when a console asks |

**`RunQuery` used to be Unimplemented too, and that broke the lobby.** It is
the last RPC in the confirmed boot sequence, called right after
`Locker/SelectDocuments` — `Unimplemented` here (grpc-status 12) reproduced
on real hardware on 2026-09-24 as "a communication error has occurred,"
landing the console in the plaza instead of the hall. A real capture
(`docs/CAPTURES/raw`'s Nextendo second session, facts only) shows a query
with no matches answered by one streamed `RunQueryResponse` carrying only
`read_time`. That establishes the empty case. GRID0+ **does** persist named
documents via `CommitDocuments`, so returning zero matches when one belongs
to the requested collection is still a gap.
