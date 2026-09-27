# policy.hac.lp1.scsi's application_policy: {"policy_type":"ALL_OK"}

Found by following the user's own suggested method: identify the last
request a failing hardware attempt actually made, and diff GRID0+'s real
response to it against the same request in both preserved working Nextendo
captures.

Two consecutive 2026-09-24 hardware attempts both stopped at the same point:
`GET /api/console/v2/application_policy/<title>/<value>` on
`policy.hac.lp1.scsi.srv.nintendo.net`, called right before the working
captures go on to `storage.hac.lp1.scsi`'s `save_data_archives` (with
`application_id`) and `start_upload` -- neither of which GRID0+'s console
has ever reached.

`policy.hac.lp1.scsi.srv.nintendo.net` fell through to `route()`'s
catch-all `default: h.emptyObject(w)` -- a bare `{}`, two bytes. Decoding
both working captures' raw `.flow` files directly (`mitmproxy.io.FlowReader`,
not Nextendo's code) shows an identical, non-empty answer in both:

```
{"policy_type":"ALL_OK"}\n
```

25 bytes, trailing newline included, `Content-Length: 25` in both captures.
`internal/stubs/handler.go` now answers this host with those exact bytes
(`applicationPolicyBody`) instead of falling through to `emptyObject`. A
wire-level test
(`TestApplicationPolicyAnswerMatchesBothWorkingCaptures`,
`internal/stubs/handler_test.go`) asserts the exact body and Content-Length.

**Unconfirmed whether this is what stops the hall entirely.** It is the
most concrete, best-evidenced mismatch found at the actual point both
recent attempts stopped, decoded from real captured bytes rather than
guessed -- but GRID0+'s own connection to `storage.hac.lp1.scsi` (the
next host in the working sequence) has never been reached even once, so
there may be more than one gap between here and a working hall entry. The
next hardware test is what actually answers that.
