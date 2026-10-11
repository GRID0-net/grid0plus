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
| a missing document is `NotFound`, headers and trailers in separate frames without `content-length: 0` | nginx drops the status trailers when that header is present, so the console sees `UNKNOWN` instead of `NotFound`; an empty `Document` or Trailers-Only response also crashes the game (`2162-0001`) |
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
| `CommitDocuments` | update (with or without a field mask, with preconditions), delete, field transforms (`increment`, `maximum`, `minimum`, `set_server_value: REQUEST_TIME`), and `SetAttachment` / `ImportAttachment` / `UnsetAttachment` |
| `RunQuery` | lists the documents directly in the requested collections under `parent`, newest name first; with no match, the captured one-message `read_time` answer. The in-game battle history is this query over `VsResults`, which the server writes after each match (`RecordVsResults`) |

The server also writes two kinds of `services/GameRecord` documents the console only reads: each player's `VsResults/<battle>` after a versus battle (`RecordVsResults`) and `CoopResults/<job>` after a Salmon Run job (`RecordCoopResults`), each row carrying the player's `player_info` from the battle's `@@Profile` documents, and Salmon Run's point cards, `PointCardTotal/Data` and `PointCardRegular/<shift_id>`, after each job (`RecordCoopPointCards`). Without the point cards the Bonus Meter never moves.

`ApplyCoopGrades` updates `CoopUserAttribute/Data` before the final referee verdict is published. A clear adds 20; failures in waves one, two and three deduct 20, 10 and zero. A real reported teammate disconnect halves a survivor's deduction; deliberately empty player slots do not. Each player's before/after grade is retained in the verdict, history and shift card. Attribute changes and the private `grid0/coopGradeCredits` retry marker commit atomically, so retries and late peer reports cannot credit the same job twice. Private jobs and Eggstra Work do not use this ordinary grade rule.
| `IssueUploadUri`, `IssueAttachmentUri`, `BulkIssueAttachmentUri` | yes — see below |
| `BulkGetDocuments`, short aliases | **no** — `Unimplemented`, logged once by the gateway when a console asks |

**Attachments** (`attachments.go`): the bytes behind a document (a drawing, a
locker photo, a replay) never travel over gRPC. `IssueUploadUri` hands out a
signed URL on `gw.hac.lp1.vermillion.srv.nintendo.net` under `/grid0/ugc/`,
which the console uploads to with Google Cloud Storage's resumable protocol
(the game's own uploader, found in its main); a commit then names the upload,
and `IssueAttachmentUri` signs a download URL for other consoles. The bytes
live in `ugc_blobs`, one attachment per document, 8 MB each; uploads nobody
attached are purged after a day. Before this, posting a drawing or a locker
stopped at `IssueUploadUri`, so nothing was ever published.

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
