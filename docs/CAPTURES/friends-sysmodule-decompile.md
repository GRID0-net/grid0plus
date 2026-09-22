# The `friends` sysmodule, decompiled (2026-09-22)

Static analysis, not a packet capture, but it belongs here for the same reason as
[account-sysmodule-decompile.md](account-sysmodule-decompile.md): it settled
several of `internal/baas`'s remaining guesses, and two of them turned out to be
real defects rather than open questions.

**What the input was.** The operator's own console's `friends` sysmodule, from a
firmware 22.5.0 package, decrypted with the operator's own `prod.keys` and
analyzed headlessly with Ghidra. Nothing derived from it is committed — no
binary, no decrypted segment, no Nintendo-issued material. What follows is
findings about behaviour and wire shape.

Addresses are Ghidra's, which map to this repo's `FUN_71000XXXXX` naming as
`address + 0x70FFF00000`.

## The one structural fact worth knowing first

This sysmodule's HTTP response handling is a set of **incremental JSON key
callbacks**: for each key/value the JSON parser walks past, the callback asks
whether the current path is one it cares about, and if so reads the value into a
slot. Two consequences that are easy to get wrong when reasoning from a capture
alone:

- A key the callback does not know about is simply skipped. **Absence is almost
  always tolerated** — this is why the repo's missing-field TODOs keep turning
  out not to break anything.
- An **array is matched by its own marker path**, `$.items[*]` for the element
  and `$.items[]` for the end. A response with the right fields but no `items`
  wrapper is not "missing a field" — nothing inside it is ever visited. That is
  the defect class below.

## Two defects it found, both fixed

Both were the same mistake: a bare top-level array where this client can only
read an `{items: [...]}` envelope.

**`GET /1.0.0/users/<id>/blocks`.** The request builder at `0x1b4c60` formats
that exact endpoint, and the parser it feeds (`0x1b50e0`, via `0x1b4fe0`) reads
`$.items[*]`, `$.items[*].targetUserId`, `.targetUser.nickname`,
`.targetUser.thumbnailUrl`, `.extras.self.reason`, `.extras.self.route:*` and
`.createdAt`. It never reads a `user` key, and the envelope is not optional.
SwitchNet sent `[{"user": ...}]`. Fixed in `2e77714`.

`targetUserId` is parsed as a **string of hex digits** (the callback guards the
field with a type check that its `nickname`/`appId`/`reason` fields share, and
then runs a hex accumulator over the text, tolerating a `0x` prefix and bailing
at 60 bits). SwitchNet's 16-hex-digit ids fit with room to spare — the same
format the mirror image of this runs into in URL paths, `%016llx`.

**`GET /1.0.0/users`.** The id-lookup builder (`0x1d40b0`, and its twin at
`0x1d57d0`) de-duplicates its id list and then formats
`https://%s/1.0.0/users?filter.id.$in=` followed by `%016llx` per id with a `,`
between them. SwitchNet understood only the friend-code filter, so an id lookup
hit the "refuse an unfiltered query" guard and came back **400**. The response
parsers beside that builder (`0x1d4880`, `0x1d4d20`) read `$.items[*].id`,
`.nickname`, `.thumbnailUrl`, `.extras.*.playLog` — an envelope again, where
SwitchNet sent a bare array. Both fixed in `369011a`.

## What it confirms is already right

Worth recording, because each of these was a guess that has now been checked
against the client that consumes it:

| SwitchNet | Client evidence |
| --- | --- |
| `{count, etag, itemsPerPage, items}` on every list endpoint | `$.items[*]` / `$.items[]` markers in each list parser |
| `friendId`, `friend.nickname`, `friend.thumbnailUrl`, `friendNote`, `createdAt` | read verbatim by `0x1ac660`, the friend-list entry parser |
| `presence.state`, `presence.logoutAt`, `presence.updatedAt`, `presence.pf` | `0x1ac510`; `pf` is read, and a 2026-09-13 capture has it always `1` |
| `presence.extras.friends.appField` / `appInfo:appId` / `appInfo:presenceGroupId` | `0x1ac660`, and `0x1a7670` for the notification payload |
| `extras.*.playLog`, seeded `""` on all five scopes by `db.EmptyExtras` | `0x1bec20` (`$.extras.*.playLog`, `$.extras.self.playLog`, `.friends.`, `.favoriteFriends.`, `.everyone.`) and `0x1ac660` |
| `links.friendCode.id`, `links.friendCode.regenerableAt` | `0x1bec20` |
| `permissions.friendRequestReception`, `.presence` | `0x1bec20`, alongside the five patch paths `presence.go` already handles |
| `state` spelled `AUTHORIZED` / `REJECTED` / `CANCELED` | the friend-request parsers, `0x1cf640` / `0x1cfe20` |
| `PATCH` bodies as JSON Patch | `0x1d0710` and `0x1b7620` both set `application/json-patch+json` |
| `POST /2.0.0/friend_requests` as `application/json` | `0x1c1420`; the form encoding `wire.go` also accepts is unused by the real client |
| `type=NX` on `generate_code` | `0x1ba5c0` sends it |
| `filter.links.friendCode.id.$in` | `0x1d5600` |

Two earlier readings of mine that this pass **corrected**, recorded so they are
not re-derived: `playLog` is already emitted (it is not a gap), and the
app-catalog host `c-<n>.accounts.nintendo.com` is already null-routed by
`Resolve`'s `nintendoOwnedDomains` fallback — neither needed a change.

## What is still open

**`extras.self.route:*` is emitted nowhere.** The friend-list entry parser reads
eight keys the block parser also reads five of:

```
$.items[*].extras.self.route:name              $.items[*].extras.self.route:appInfo:acdIndex
$.items[*].extras.self.route:name:language     $.items[*].extras.self.route:appInfo:presenceGroupId
$.items[*].extras.self.route:appInfo:appId     $.items[*].extras.self.route:candidate:catalogId
$.items[*].extras.self.route:nnid:miiName      $.items[*].extras.self.route:nnid:miiImageUrlParam
```

SwitchNet has no `route:` key at all. Because absence is tolerated, this is not
a proven break — it is a proven *unread* field, and the one place in this
sysmodule where the client has data SwitchNet never supplies. Inventing values
for it would be exactly what `CLAUDE.md` rule 6 forbids, so it stays a finding:
a capture of a real friend list would show what a populated `route` contains, and
until then nothing should be written into it.

**`extras.self.reason` on a block entry.** The block parser reads it and routes
it through the same string-to-enum helper its other enums use. SwitchNet sends
none. What the values are needs a capture.

**The friend-request POST body.** `POST /2.0.0/friend_requests` is confirmed to
be `application/json` (`0x1c1420`) but that function only builds the URL and
headers — the body is assembled by a small writer elsewhere that this pass did
not isolate. `docs/CAPTURES/README.md`'s item 2 is unchanged by this.

**`$.permissions.*` on a friend.** `0x1bec20` reads `permissions.presence` and
`permissions.friendRequestReception` from the user record. Whether the applet
reads the *friend's* permissions out of `FriendUserResponse` for its own
visibility display (which SwitchNet does send, via `scopedExtras`) was not
traced.

## Endpoint inventory

30 URL formats are constructible by this sysmodule. Every one is served by
`internal/baas`, on the base `e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com`
(the modern host `m-lp1.baas.nintendo.com` appears for account-level calls, as
`README.md` records). The list is reproducible — it is every
`https://%s/...`-shaped literal referenced by code in the module — and its value
is that adding a SwitchNet route or renaming a query parameter can be checked
against it directly.
