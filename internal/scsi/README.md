# scsi

OLSC (Online Save Cloud): the system-level, per-game save backup that normally comes with a Nintendo
Switch Online subscription. It is **not** the same thing as a game's own cloud save, such as Splatoon 3's
`CloudSave` service in [`internal/toyohr`](../toyohr/README.md), which is a separate protocol.

| | |
| --- | --- |
| Stands in for | `storage.hac.lp1.scsi.srv.nintendo.net` (save data archives, v2) and `permission.lp1.sata.srv.nintendo.net` (save data sets and key package) |
| Reference | [OLSC Servers](https://github.com/kinnay/NintendoClients/wiki/OLSC-Servers) |
| Needs a database | yes |
| Needs local disk | yes: `scsi.storage_dir` holds the raw save bytes, and metadata is in Postgres |

**Status.** It isn't confirmed on a console yet, and parts of it are the most defensible reading of an
undocumented protocol; see below.

## Files

| File | What |
| --- | --- |
| `handler.go` | routing and every endpoint handler |
| `auth.go` | verifies the caller's BAAS access token |
| `models.go` | the wire objects (`archiveView`, `componentView`) |
| `storage.go` | `BlobStore`, and `LocalDisk`, the local-disk implementation with per-owner quota enforcement |
| `errors.go` | the error envelope and the unimplemented-endpoint logger |
| `ids.go` | id generation |

## What is confirmed and what is a reading

Most of the package follows the public wiki page above: both hosts, the full `save_data_archive` and
`component_files` field names, `start_upload`'s request body, `finish_upload`'s `encoded_mac` field,
`extend_upload_timeout`'s `{"code": 2000}` answer, `generate_key_seed_package`'s field names and its
512-character response, and `GET /api/nx/v2/save_data_archives` with its `application_id` and
`device_id` (`"me"`) filters.

These are GRID0+'s own defensible readings, each marked `UNCONFIRMED:` in `handler.go`:

1. **`component_files/create`, `signed_uri`, and the raw upload and download endpoint under `/data`.**
   The wiki lists the URLs but no body shape. Real Nintendo probably hands back a time-limited signed URL
   on separate infrastructure. GRID0+ has none, so it returns a same-server URL under its own `/data` path.
2. **`start_download` and `finish_download`.** Answered with the same shape as `GET .../{id}` and a bare
   acknowledgement, the least invented option that still lets a restore proceed.
3. **`application_id`.** `start_upload`'s request has no title id but the archive it returns does. It is
   read from an `X-SwitchNet-Application-Id` header, instead of guessing a JSON field name.
4. **The two `generate_key_*package` endpoints.** These are the most likely reason a real console would
   still refuse a restore. Both are answered with fresh random bytes of the right size. If a console
   validates either value cryptographically, no shape guessed here will pass.
5. **The error envelope.** It mirrors BAAS's, on the theory that this is the same family of API.
6. **`/api/nx/v2/permissions` and `/api/nx/v1/save_data_sets`.** Answered permissively (an empty object
   and an empty list). A private deployment has no reason to deny its own operator's consoles their own
   save backups.

`/api/nx/v1/customer_operations`, its `update_action_completed` sibling and
`/api/nx/v2/component_files/{id}/update` are not implemented. They answer `501` and are logged loudly
rather than given an invented shape.

## Identity

Every endpoint needs a BAAS access token, the same one `internal/baas` issues at login. `auth.go`
verifies it against the same `baas_access` signing key without importing `internal/baas`, the same seam
`internal/npln/auth.go` uses.

Every archive and component-file handler checks ownership on every request (`loadOwnedArchive`,
`loadOwnedComponentFile`). A hard-to-guess id isn't a secret.

## Storage

Metadata (`db.SaveDataArchive`, `db.ComponentFile`) lives in Postgres. Raw save bytes go through
`BlobStore`, an interface that `LocalDisk` implements today. Local disk is the default because it adds no
cost beyond the machine you already run. `usedBytes` sums a caller's stored bytes across every archive on
every upload to enforce `scsi.per_user_quota_bytes`. That is O(archives) per upload, which is fine for a
private deployment. If it stops being fine, the fix is a running total updated with each write, not a
different storage backend.
