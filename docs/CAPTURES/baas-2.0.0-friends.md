# GET /2.0.0/users/\<id\>/friends

| | |
| --- | --- |
| Host | `e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com` |
| System version | 22.5.0 (2250) |
| Title | friends applet (system) |
| Captured | 2026-08-25 |
| Tool | `scripts/capture-baas.py` |
| Captured by | np752952@gmail.com |

## What the console was doing

The friends applet was opened. 37 friends were present on the account. The
capture was made from a PC using the console's device account credentials,
with no console involvement.

## Request

```http
GET /2.0.0/users/<REDACTED>/friends?count=300 HTTP/1.1
Host: e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com
Authorization: Bearer <REDACTED>
```

## Response

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "count": 300,
  "etag": "",
  "itemsPerPage": 37,
  "items": [
    {
      "userId": "<caller's user id>",
      "friendId": "<friend's user id>",
      "friend": {
        "id": "<friend's user id>",
        "etag": "\"<16 hex>\"",
        "nickname": "<str>",
        "thumbnailUrl": "<url>",
        "thumbnail2Url": "<url>",
        "deviceAccounts": [{"id": "<16 hex>"}],
        "permissions": {
          "personalAnalytics": true,
          "personalNotification": true,
          "friendRequestReception": true,
          "friends": "EVERYONE",
          "presence": "FRIENDS",
          "presenceUpdatedAt": <unix>,
          "personalAnalyticsUpdatedAt": <unix>,
          "personalNotificationUpdatedAt": <unix>
        },
        "extras": {
          "self": {},
          "favoriteFriends": {},
          "friends": {"playLog": "<str>"},
          "foaf": {"playLog": "<str>"},
          "everyone": {"playLog": "<str>"}
        },
        "presence": {
          "state": "OFFLINE",
          "pf": 1,
          "extras": {
            "self": {},
            "favoriteFriends": {},
            "friends": {},
            "foaf": {},
            "everyone": {}
          },
          "updatedAt": <unix>,
          "logoutAt": <unix>
        },
        "deleted": false,
        "blocksUpdatedAt": <unix>,
        "friendsUpdatedAt": <unix>,
        "thumbnailUploadedAt": <unix>,
        "nicknameUpdatedAt": <unix>,
        "createdAt": <unix>,
        "updatedAt": <unix>
      },
      "extras": {
        "self": {"route:sender": "me", "isConfirmed": true},
        "favoriteFriends": {},
        "friends": {},
        "foaf": {},
        "everyone": {}
      },
      "etag": "\"<16 hex>\"",
      "createdAt": <unix>,
      "updatedAt": <unix>,
      "channels": {
        "FRIEND_CODE": {"authorizedAt": <unix>}
      },
      "isFavorite": true,
      "friendNote": ""
    }
  ]
}
```

## Key findings

**The response is an envelope, not a bare array.** The TODO in `friends.go`
predicted either; the capture confirms the envelope form:
`{count, etag, itemsPerPage, items}`.

- `count` echoes the requested page size (300), not the actual return count.
- `itemsPerPage` is the number of items actually returned (37 here).
- `etag` is always `""` in this capture.

**The entry is a relationship object, not a user object.** The top-level keys
are `userId` (caller), `friendId` (friend), `friend` (the friend's profile),
`isFavorite`, `channels`, `extras`, `friendNote`, `etag`, `createdAt`, `updatedAt`.

**The `friend` sub-object differs from `UserResponse`:**
- `birthday`, `country` and `links` are absent.
- `nicknameUpdatedAt` is added.
- `presence.pf` is always `1` across all 37 captured entries.

**`channels` contains `{"FRIEND_CODE": {"authorizedAt": <unix>}}`** — tracks
when the friend code channel was authorized. The meaning of the timestamp is not
yet clear; see the TODO(capture-needed) in `models.go`.

**Relationship `extras`** have `self.route:sender = "me"` and
`self.isConfirmed = true`. These appear to be metadata about the relationship
from the caller's perspective. SwitchNet does not track these; they are returned
as empty scoped extras.

## What this changes

- [x] The response envelope — `listFriends` in `internal/baas/friends.go` now
      wraps the result in `CollectionEnvelope`.
- [x] `FriendResponse` redesigned as a relationship object with `userId`,
      `friendId`, `friend`, `channels`, `extras`, `friendNote`, `etag`,
      `createdAt`, `updatedAt`.
- [x] `FriendUserResponse` added for the `friend` sub-object, omitting
      `birthday`, `country`, `links`; adding `nicknameUpdatedAt`.
- [x] `PresenceResponse.Pf` added, always `1`.
- [x] `TODO(capture-needed)` removed from `internal/baas/friends.go` and
      the `FriendResponse` comment in `internal/baas/models.go`.

## Notes

`nicknameUpdatedAt` is not tracked as a separate field in `db.User`. SwitchNet
uses `UpdatedAt` as a conservative approximation until the column is added.

The `channels` entry appears on every friendship. Whether it is created at
friend-request acceptance or at some other point is not known from this capture
alone; it would need a second capture immediately after adding a new friend.
