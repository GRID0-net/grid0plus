# start_upload is also under /api/console/v3/, and carries application_id in its body

Two consecutive 2026-09-24 hardware attempts, following the `application_policy`
fix, both progressed further and reached
`POST /api/console/v3/save_data_archives/start_upload` -- and both got
`logUnimplemented`'s 404, the exact retry-loop-causing shape this project
already documented for `/api/console/v3/save_data_archives` (GET) and
`.../notification_tokens` on 2026-09-23. This project's own OLSC surface had
a route for `start_upload` only under `/api/nx/v2/`.

Both preserved working Nextendo hall captures' raw `.flow` files (decoded
directly with `mitmproxy.io.FlowReader`, not Nextendo's code) confirm the
real request:

```
POST /api/console/v3/save_data_archives/start_upload
{"application_id":"0100c2500fc20000","datatype":0,"data_size":67764224,
 "saved_at_as_unixtime":...,"launch_required_version":42,"series_id":...,
 "encoded_digest":"...","has_thumbnail":false,"desc":null,"auto_backup":false,
 "acd_index":0}
```

This also settles an open `TODO(capture-needed)`: whether `application_id`
is a request-body field. It is -- `internal/scsi/handler.go`'s own comment
previously reasoned the opposite (that the wiki's documented body has no
such field, so it must come from request context instead) and read an
invented `X-SwitchNet-Application-Id` header. That header and its comment
are now gone; `startUploadRequest.ApplicationID` reads the real field.

`Routes` now registers `POST /api/console/v3/save_data_archives/start_upload`
and its `{id}` form against the same `startUpload` handler as the `/api/nx/v2/`
routes, per the existing comment's own instruction ("add its route here, to
the same handler, the moment one is [confirmed]"). Two new tests:
`TestStartUploadReachableUnderConsoleV3Path` (the v3 route itself) and the
existing `startUploadRequest` callers updated to set `ApplicationID` in the
body rather than the removed header.

**Whether this actually unblocks the hall is unconfirmed** -- the next
hardware test is what answers that. `acd_index` (a field neither the wiki
nor this project's `startUploadRequest` names) is a new, still-open
observation from the same capture, not yet acted on.
