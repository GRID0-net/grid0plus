# ugcstore

`nn.npln.ugcstore.v1.Ugcstore`: the document store Splatoon 3 keeps its per-player records in.
It is served on the NPLN gateway (`internal/npln` registers it) and shared with
[`internal/toyohr`](../toyohr/README.md), whose `GameRecord/InitializeAttributes` creates
documents through it.

| | |
| --- | --- |
| Stands in for | `nn.npln.ugcstore.v1.Ugcstore` on the Splatoon 3 tenant host |
| Definitions | [kinnay/NPLN-Protocols](https://github.com/kinnay/NPLN-Protocols), generated into `proto/ugcstore/v1` |
| Needs a database | yes (`ugc_documents`, migration 0018) |

## Why it exists

The game reads its GameRecord documents (`.../users/<uid>/VsUserAttribute/Data`,
`.../CoopUserAttribute/Data` and so on) right after signing in and on entering the lobby,
creates missing ones through `GameRecord/InitializeAttributes`, and reads them back. Without
this service the game can't get past the lobby.

## Rules it keeps, and why

| Rule | Because |
| --- | --- |
| `GetDocument` is registered as server-streaming although the console sends one request | it lets an absent read end without sending a protobuf message |
| a missing document is `NotFound`, with headers and trailers in separate frames and no `content-length: 0` | nginx drops the status trailers when that header is present, so the console would see `UNKNOWN` instead of `NotFound`; an empty `Document` or a Trailers-Only response also crashes the game (`2162-0001`) |
| writes persist in Postgres | a document the game wrote and can't read back makes it re-initialise forever |
| names must be `tenants/<tenant>/documents/<rest>` | the game's own name parser aborts on anything else, and a stored bad name is read back on every start |
| `tenants/current` and both forms of the tenant id are one document | the console uses more than one form |
| a `users/<id>/` path is writable only by that player, and otherwise by its creator | identity fails closed |
| a commit is all or nothing | a refused operation mustn't leave half a batch written |

Fields are stored as the encoded `MapValue`, so value types survive exactly.

## Implemented

| RPC | |
| --- | --- |
| `GetDocument` | yes |
| `CommitDocuments` | update (with or without a field mask, with preconditions), delete, field transforms (`increment`, `maximum`, `minimum`, `set_server_value: REQUEST_TIME`), and `SetAttachment`, `ImportAttachment` and `UnsetAttachment` |
| `RunQuery` | lists the documents directly in the requested collections under `parent`, newest name first. With no match it answers with a single message carrying only `read_time`. The in-game battle history is this query over `VsResults`. |
| `IssueUploadUri`, `IssueAttachmentUri`, `BulkIssueAttachmentUri` | yes, see below |
| `BulkGetDocuments`, short aliases | no. The game carries them but never calls them. They answer `Unimplemented`, logged once if anything does. |

`nn.npln.ugcstore.v1.Screening` (`screening.go`) is registered beside it. `CreateReport` logs
a player's report of a post and echoes it back (nothing is reviewed on a private server), and
its `IssueUploadUri` is the same signed upload as Ugcstore's, for a report with a screenshot.

**Results the server writes.** The console only reads these, so the server writes them:
each player's `VsResults/<battle>` after a versus battle (`RecordVsResults`), `CoopResults/<job>`
after a Salmon Run job (`RecordCoopResults`), and Salmon Run's point cards after each job
(`RecordCoopPointCards`). Without the point cards the Bonus Meter never moves.

**Salmon Run grades.** `ApplyCoopGrades` updates `CoopUserAttribute/Data` before the final
verdict is published. A clear adds 20. A failure in wave one, two or three deducts 20, 10 or
zero. A real reported teammate disconnect halves a survivor's deduction, and deliberately empty
player slots don't. The grade changes and a private retry marker commit atomically, so retries
and late peer reports can't credit the same job twice. Private jobs and Eggstra Work don't use
this grade rule.

`coopranks.go` moves the same grade outside online jobs. `CreditLANCoopGrade` applies a LAN job a mod accepted through the Discord bot, once per job (the same retry marker), without touching the Salmometer. `RollCoopRotation` is the rotation reset: the title is kept and its points go back to 40, or at Eggsecutive VP to 200/300/400 with the stage's bronze/silver/gold badge. It changes nothing when the stored `shift_id` is already the new one, because the game sends `coop_shift` several times per rotation. Every Eggsecutive VP result also raises that shift's best in the private `grid0/coopBest/<uid>` document; together with the point cards' `best_grade_point`, that is what a stage's badge is read from.

**Attachments** (`attachments.go`). The bytes behind a document (a drawing, a locker photo, a
replay) never travel over gRPC. `IssueUploadUri` hands out a signed URL on
`gw.hac.lp1.vermillion.srv.nintendo.net` under `/grid0/ugc/`, which the console uploads to
using Google Cloud Storage's resumable protocol. A commit then names the upload, and
`IssueAttachmentUri` signs a download URL for other consoles. The bytes live in `ugc_blobs`,
one attachment per document, up to 8 MB each, and uploads nobody attached are purged after a
day.
