# GET /2.0.0/users/\<id\>/friend_requests/inbox

| | |
| --- | --- |
| Host | `e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com` |
| System version | 22.5.0 (2250) |
| Title | friends applet (system) |
| Captured | 2026-08-25 |
| Tool | `scripts/capture-baas.py` |
| Captured by | np752952@gmail.com |

## What the console was doing

Friends applet opened. No pending incoming friend requests existed at the time
of capture.

## Request

```http
GET /2.0.0/users/<REDACTED>/friend_requests/inbox HTTP/1.1
Host: e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com
Authorization: Bearer <REDACTED>
```

## Response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "count": 50,
  "etag": "",
  "itemsPerPage": 0,
  "items": []
}
```

## Key findings

**The response is the same `{count, etag, itemsPerPage, items}` envelope as the
friend list.** `count` is 50 (the default page size when no `count` parameter
was sent). `etag` is `""`.

**The per-item shape is not yet confirmed** — the inbox was empty. The
`FriendRequestResponse` type and its TODO(capture-needed) remain unchanged.

## What this changes

- [x] `listRequests` in `internal/baas/friends.go` now wraps the response in
      `CollectionEnvelope` with `defaultFriendRequestCount = 50`.

## Notes

The default page size (50) is inferred from the fact that no `count` parameter
was sent and the response contains `"count": 50`. A separate capture with an
explicit `count` would confirm that `count` echoes the parameter rather than
always being 50.
