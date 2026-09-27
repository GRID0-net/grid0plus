# internal/scsi

OLSC (Online Save Cloud): the OS-level per-game save-data backup tied to a
Nintendo Switch Online subscription. Phase 4 — see `docs/ROADMAP.md`. **Not
the same thing** as any one game's own cloud-save API — Splatoon 3's
`CloudSave.GetSaveRecord` (phase 2) is a separate, game-specific RPC on a
separate protocol.

| | |
| --- | --- |
| Reimplements | `https://storage.hac.lp1.scsi.srv.nintendo.net` (save data archives, v2) |
| | `https://permission.lp1.sata.srv.nintendo.net` (save data sets / key package) |
| Reference | [OLSC Servers](https://github.com/kinnay/NintendoClients/wiki/OLSC-Servers) |
| Needs a database | yes |
| Needs local disk | yes — `scsi.storage_dir` (raw save bytes; metadata is in Postgres) |

## Files

| File | What |
| --- | --- |
| `handler.go` | routing and every endpoint handler |
| `auth.go` | verifies the caller's BAAS access token — see below |
| `models.go` | the wire objects (`archiveView`, `componentView`) |
| `storage.go` | `BlobStore`, and `LocalDisk`, the local-disk implementation with per-owner quota enforcement |
| `errors.go` | the error envelope, and the unimplemented-endpoint logger |
| `ids.go` | id generation |

## What's confirmed versus this project's own reading

Most of this package **is** confirmed against the wiki page above, the same
kind of public source this project already leans on heavily for BAAS/dAuth/
aAuth — not a guess. Specifically:

- Both hosts, both title-derived (`010000000000003E` decompiled 2026-09-20 —
  see `internal/nintendo/hosts.go`'s own comments on these two entries).
- The full `save_data_archive` and `component_files` object field names.
- `start_upload`'s request body, `finish_upload`'s `encoded_mac` field,
  `extend_upload_timeout`'s `{"code": 2000}` response,
  `generate_key_seed_package`'s field names and the confirmed 512-character
  response length.
- `GET /api/nx/v2/save_data_archives`'s `application_id`/`device_id` (with
  `"me"`) query filters.

What is **this project's own defensible reading**, each marked
`TODO(capture-needed)` in `handler.go` at the exact function, because no
capture of real OLSC traffic exists yet:

1. **`component_files/create`, `signed_uri`, and the raw upload/download
   endpoint under `/data`.** The wiki lists these URLs with method `"?"` and
   no body shape at all. `signed_uri` almost certainly points to a
   time-limited, cryptographically signed URL on separate infrastructure in
   real Nintendo's deployment (the CloudFront/`X-Amz-Cf-*` headers in BAAS's
   own example responses are the evidence real infrastructure is in play
   here) — GRID0+ has no separate infrastructure to sign a URL for, so it
   hands back a same-server URL under its own invented `/data` path instead.
2. **`start_download`/`finish_download`.** Answered as, respectively, the
   same shape as `GET .../{id}` and a bare acknowledgement. No basis beyond
   "this is the least invented option that still lets a restore proceed."
3. **`application_id`.** `start_upload`'s documented request body has no
   title-id field, but the archive it returns has one. Read from an
   `X-SwitchNet-Application-Id` header this project's own console-side
   pieces would need to set, rather than guessing a JSON field name a real
   console might never send — see `applicationIDFrom`'s own comment.
4. **The two `generate_key_*package` endpoints — the most likely hard
   blocker in this whole feature.** `generate_key_seed_package` (SCSI) has a
   confirmed field name and response length (512 base64url characters) but
   an unconfirmed byte meaning; `generate_key_package` (SATA) has neither —
   not even a field name, just a bare URL in the wiki's own table, though a
   2026-09-20 decompile of title `010000000000003E` independently confirms
   the literal endpoint string exists. Both are answered with freshly
   random, correctly-sized opaque bytes. **If a real console
   cryptographically validates either value — plausibly by wrapping it
   around the console's own SSL device key via an HSM GRID0+ has no
   equivalent of — no shape guessed here will pass that check, and no amount
   of code review will tell you that in advance.** This is the same
   category of unknown `internal/baas/accounts.go`'s `nxAccountBlob` header
   prefix turned out to be for phase 1's Nintendo Account link — a capture
   is what actually settled that one, and a capture is what would settle
   this one too. See `docs/CAPTURES/README.md`.
5. **The error envelope shape** (`errors.go`). Mirrors BAAS's own confirmed
   shape on the theory that this is the same family of API, but no capture
   of a real SCSI/SATA error response exists to confirm it.
6. **`/api/nx/v2/permissions` and `/api/nx/v1/save_data_sets`.** Both listed
   with no method or shape. Answered permissively (an empty object, an empty
   list) — the same "always allowed" / "nothing to report" posture
   `internal/stubs`'s op2 membership stub and a fresh account's
   `CloudSave.GetSaveRecord` already use elsewhere in this project for the
   same reason: a private deployment has no legitimate case for denying its
   own operator's own consoles their own save backups.

`/api/nx/v1/customer_operations` and its `update_action_completed` sibling,
and `/api/nx/v2/component_files/{id}/update`, are **not implemented at all** —
answered `501` and logged loudly (`logUnimplemented`) rather than given an
invented shape, per `CLAUDE.md`'s ground rule against inventing plausible
Nintendo internals. Nothing about their names or neighbours gives enough to
guess at, unlike everything above.

## Identity

Every endpoint requires a BAAS access token — the same one
`internal/baas/accounts.go`'s login/federation handlers already issue,
verified in `auth.go` against the same `baas_access` signing key rather than
by importing `internal/baas`. This is the same seam
`internal/npln/auth.go`'s `exchangeIDToken` documents for its own equivalent
check: a service reads identity off a token BAAS already issued, without a
second issuance step of its own. Real OLSC traffic is system-level
account-state synchronization from the account sysmodule, which already
holds this token — there is no separate token-issuance endpoint anywhere in
OLSC's documented surface to suggest otherwise, but this is still this
project's own reading, not a confirmed one.

Every archive- and component-file-scoped handler checks ownership
(`loadOwnedArchive`, `loadOwnedComponentFile`) on every request — the same
fail-closed posture `internal/baas/router.go`'s `authenticate` documents at
length for exactly the bug this project is shaped around: an id being hard
to guess is not the same as an id being a secret.

## Storage

Metadata (`db.SaveDataArchive`, `db.ComponentFile`) lives in Postgres, next
to everything else — see `internal/db/migrations/0015_scsi_save_data_archives`.
Raw save bytes go through `BlobStore`, an interface `LocalDisk` is the only
implementation of today, per `docs/ROADMAP.md`'s phase 4 storage-backend
decision (local disk, not paid object storage). `usedBytes` sums a caller's
stored bytes across every archive on every upload for quota enforcement —
`O(archives)` per upload, which is fine at the scale a single operator's
private deployment runs at. If that ever stops being true, the fix is a
running-total column updated alongside each write rather than a
recalculation, not a different storage backend.
