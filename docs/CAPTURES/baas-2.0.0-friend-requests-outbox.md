# GET /2.0.0/users/\<id\>/friend_requests/outbox

| | |
| --- | --- |
| Host | `e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com` |
| System version | 22.5.0 (2250) |
| Title | friends applet (system) |
| Captured | 2026-08-25 |
| Tool | `scripts/capture-baas.py` |
| Captured by | np752952@gmail.com |

## What the console was doing

Friends applet opened. No pending outgoing friend requests existed at the time
of capture.

## Request

```http
GET /2.0.0/users/<REDACTED>/friend_requests/outbox HTTP/1.1
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

Identical envelope shape to the inbox: `{count, etag, itemsPerPage, items}`.
`count` is 50 (default page size, no `count` parameter was sent). Items is empty.

The per-item shape is still unconfirmed.

## What this changes

Covered by the same `listRequests` change as the inbox capture.

## Notes

See [baas-2.0.0-friend-requests-inbox.md](baas-2.0.0-friend-requests-inbox.md)
for the shared notes on the page-size default.
