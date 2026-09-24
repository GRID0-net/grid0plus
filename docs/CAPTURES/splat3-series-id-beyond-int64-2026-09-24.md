# series_id exceeds int64, not just int32

Widening `series_id` to `BIGINT`/`int64` (migration 0019,
`splat3-series-id-overflow-2026-09-24.md`) was not enough. The next real
hardware attempt sent `12237957226414712540`, which is itself larger than
`int64`'s maximum (`9223372036854775807`) -- exposed by new logging added
specifically to see this
(`internal/scsi/handler.go`'s `startUpload`, decode-failure branch):

```
json: cannot unmarshal number 12237957226414712540 into Go struct field
startUploadRequest.series_id of type int64
```

Not signed 64-bit, and Postgres has no unsigned 64-bit integer type either.
`series_id` is an opaque identifier -- nothing in this project does
arithmetic on it -- so migration 0020 stores it as `TEXT`, its exact decimal
digit string, holding any value a console sends regardless of width.
`db.SaveDataArchive.SeriesID`, `scsi.archiveView.SeriesID` and
`scsi.startUploadRequest.SeriesID` are now `json.Number` (request/response
JSON) and `string` (the store), not any fixed-width integer: `json.Number`
marshals as a bare JSON number (matching a real console's own response
shape) while never losing precision or overflowing.

A wire-level test
(`TestStartUploadSeriesIDSurvivesBeyondInt64Range`,
`internal/scsi/handler_test.go`) round-trips this exact value through
`start_upload` and a follow-up `GetSaveDataArchive`, asserting the response
body contains an unquoted `"series_id":12237957226414712540` -- not a
quoted string, which would be a different, wrong shape.
