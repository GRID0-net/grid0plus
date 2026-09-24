# series_id overflowed Postgres INTEGER on every real start_upload

The first genuine server-side error this investigation found, rather than
silence: after the `/api/console/v3/start_upload` route fix, a real hardware
attempt reached it and the console's own request failed with

```
scsi: create archive: failed to encode args[7]: unable to encode
1463435653829973917 into binary format for int4 (OID 23):
1463435653829973917 is greater than maximum value for int4
```

`series_id` was created `INTEGER` in migration 0015 (guessed against the
wiki's page, which gives no type). A real console's `start_upload` sends a
genuine 64-bit value -- `1463435653829973917` on real hardware 2026-09-24,
and separately `4502276665849307917` in a preserved working Nextendo
capture's own `start_upload` request body -- both far outside `INTEGER`'s
range. Every real start_upload has been failing at the database layer since
phase 4 was built; this was never exercised until the `/api/console/v3/`
routing fix (see `splat3-start-upload-console-v3-2026-09-24.md`) let a
console's request actually reach this code path for the first time.

Migration 0019 widens the column to `BIGINT` (a safe, lossless ALTER on
Postgres). `db.SaveDataArchive.SeriesID`, `scsi.archiveView.SeriesID` and
`scsi.startUploadRequest.SeriesID` are now `int64`, not `int` -- Go's own
`int` is 64-bit on every platform this project targets, so this was never a
Go-side truncation risk, only a database-column one, but the wider type
documents the real range at every layer that touches it. The store contract
test (`internal/db/store_test.go`'s `testSaveDataArchives`, which runs
against both Memory and Postgres) now creates an archive with the same
large `SeriesID` a real console sends, so a regression here fails in CI
against Postgres, not just silently in production.
