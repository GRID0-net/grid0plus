# Captures

Where to put packet captures from your own consoles, and what SwitchNet still
needs one for.

Every guess in the implementation is marked in the source with
`TODO(capture-needed):` and says what a capture would settle and which single
function to change. `grep -rn 'TODO(capture-needed)' internal/` is the full list.

Static analysis has now settled several of the same questions without a
capture — see
[splat3-auth-client-trace-2026-09-23.md](splat3-auth-client-trace-2026-09-23.md)
for the Auth stub dispatch and the open hall-error question after UDP latency
probes began receiving replies; see
[account-sysmodule-decompile.md](account-sysmodule-decompile.md) for the
`account` sysmodule: the console's own validation of `nxAccount` and the error
codes it raises, the response parsers behind each BAAS call, and the complete
list of endpoints it can construct (eight of which SwitchNet does not serve).
[friends-sysmodule-decompile.md](friends-sysmodule-decompile.md) does the same
for the client of this file's own endpoints: it confirmed the friend list, the
presence extras, the game-play log and the friend-request state spellings as
already right, found **four** responses shaped so the console could not read them
(all fixed — items 2, 4 and 5 below), and leaves three things open.

## Fastest way to answer most of this

```sh
python3 scripts/capture-baas.py \
    --keys prod.keys --prodinfo PRODINFO \
    --device-account-id <16 hex> --password "<40 chars>" --country NL
```

That talks to Nintendo's real BAAS from a PC using your console's credentials,
and covers items 1, 3, 4 and 5 below in one run. **The console is not involved**,
so it never runs with certificate verification disabled while talking to Nintendo
— see [`../PACKET_CAPTURE.md`](../PACKET_CAPTURE.md) for why that matters.

Add a friend on the console first, or the friend list and the relationship
endpoint have nothing to show.

## What is still guessed

Ordered by how much a capture would help.

### 1. `GET /2.0.0/users/<id>/friends` — ~~the response shape~~ **CAPTURED**

See [baas-2.0.0-friends.md](baas-2.0.0-friends.md).

**What the capture settled:** the response is a `{count, etag, itemsPerPage,
items}` envelope (not a bare array). Each item is a relationship object with
`userId`, `friendId`, `friend` (a subset of the user object), `isFavorite`,
`channels`, `extras`, `friendNote`, `etag`, `createdAt`, `updatedAt`. The
`friend` sub-object omits `birthday`, `country` and `links` and adds
`nicknameUpdatedAt`. `presence.pf` is always `1`.

**What SwitchNet does now:** returns `CollectionEnvelope[FriendResponse]` with
`FriendResponse` as the confirmed relationship object.

**Confirmed field-by-field by the 2026-09-22 friends-sysmodule decompile**
(see [friends-sysmodule-decompile.md](friends-sysmodule-decompile.md)): the
entry parser reads `friendId`, `friend.nickname`, `friend.thumbnailUrl`,
`friend.presence.state`, `friend.presence.extras.friends.appField` and
`appInfo:*`, `friendNote`, `createdAt` — all of which SwitchNet already sends.
The one thing in that family it reads and SwitchNet does not send is
`extras.self.route:*`; see that document's "What is still open".

**Still open within this item:** `models.go`'s `Channels` field
(`TODO(capture-needed)` at `internal/baas/models.go:175`) — the envelope shape
is settled, but what triggers a `FRIEND_CODE` entry inside it and what its
`authorizedAt` timestamp represents are not. SwitchNet always sends an empty
map, which is a defensible default rather than a confirmed one.

### 2. `POST /2.0.0/friend_requests` — the request body

**What is known:** nothing documented. The applet can add a friend two ways —
typing a friend code, and picking somebody the console already knows about.

**What SwitchNet does:** accepts form encoding and JSON, keyed on `friendCode`,
`userId` or `nsaId`. Accepting all of them is cheaper than guessing wrong: a
refused request during bring-up is a dead end with nothing to go on.

**Settled by decompile, 2026-09-22:** the encoding is JSON. The request builder
for this exact endpoint sends `Content-Type: application/json` and nothing
form-encoded, so the form branch is dead weight rather than a second real shape.
What that pass could not read: the body's keys — the builder function formats
only the URL and headers, and the small writer that assembles the body was not
isolated.

**What a capture would settle:** which key (`friendCode`, `userId`, `nsaId`).
Then the others can be dropped, along with the form encoding.

**How to capture:** type a friend code into Add Friend on a real console.

**Related, separately guessed:** the block-request *body*
(`TODO(capture-needed)` in `internal/baas/friendcodes.go`) accepts the same
form/JSON, `userId`/`nsaId` ambiguity, for the same undocumented reason —
blocking a user on a real console would settle this one independently of the
friend-request body above, since nothing confirms the friends sysmodule uses
the same encoding for both actions. The block *list* response, which used to be
in the same state, was settled by decompile instead: it is an envelope of
`targetUserId`/`targetUser` entries, fixed in `2e77714`.

**The inbox/outbox per-item shape, settled 2026-09-22.** Captures 04 and 05 both
returned empty item lists, so this was a guess for months — and a wrong one. The
inbox parser (`0x1cf640`) and outbox parser (`0x1cfe20`) read `id`, `senderId`,
`receiverId`, `sender.nickname`, `sender.thumbnailUrl`, `receiver.nickname`,
`receiver.thumbnailUrl`, `state`, `createdAt`, `channels[0]`,
`extras.receiver.read` and the `extras.sender`/`senderAndReceiver` `route:*`
blocks. `from`/`to` are read by nothing. Fixed in `2456156`.

**Still open within it:** `extras.receiver.read` is the recipient's unread flag,
and SwitchNet keeps no read state for a request, so it sends none. Whether the
client reads an absent key as unread or as read needs the same capture this item
always needed — a real pending request sitting in an inbox — and it is now the
only thing that capture would still settle for this endpoint.

### 3. `PATCH /2.0.0/friend_requests/<id>` — the body

**What SwitchNet does:** accepts a JSON patch on `/state`, a plain
`{"state":"..."}`, and `{"accept":true}`.

**Partially settled by decompile, not a capture:** a 2026-09-20 decompile of
the operator's own console's friends sysmodule found the real state enum --
`PENDING`, `AUTHORIZED`, `REJECTED`, `CANCELED`, `EXPIRED` -- and this was a
real, shipped bug until fixed the same day: SwitchNet's own pre-capture guess
used `ACCEPTED`/`DENIED`/`CANCELLED`, which a real console's PATCH would
never have matched, and accepting or declining a friend request would have
failed outright. `requestStateFromWire`/`requestStateToWire` in
`internal/baas/friends.go` now translate both directions; the old guessed
spelling is kept working as an input alias. What a decompile cannot settle:
whether the body shape itself (JSON patch array vs. plain object) is right,
or whether `EXPIRED` is ever something a client sends rather than only reads.

**How to capture:** accept a friend request on a real console.

### 4. `GET /1.0.0/users` — the friends-module lookup ~~the query parameter~~ **the friend-code case settled by decompile**

The wiki lists this as a friends-module endpoint but documents neither parameters
nor response.

**Settled, not a capture:** a 2026-09-20 decompile of the friends sysmodule
found the exact request it builds for a friend-code lookup: a MongoDB-style
filter, `?filter.links.friendCode.id.$in=<code>`, not the plain `friendCode=`
SwitchNet originally guessed -- and the response's JSONPath constants
(`$.links.friendCode.id`, `$.links.friendCode.regenerableAt`) confirm the
response shape needed no correction. Fixed in `internal/baas/accounts.go`;
`?friendCode=` is kept as a working alias.

**Still open:** whether `nickname`/`ids` go through the same `filter.*`
convention rather than their own plain query keys -- the decompile only
found evidence for the friend-code case.

**Settled by the same 2026-09-22 decompile:** the id form *does* use the filter
convention, and the response is an envelope. `filter.id.$in=` carries
comma-separated 16-hex-digit ids (the builder de-duplicates its list before
formatting it), and the parsers either side of that builder read `$.items[*]`,
`$.items[*].id`, `.nickname`, `.thumbnailUrl` and `.extras.*.playLog`. SwitchNet
answered the id form with 400 and the response with a bare array, so neither
half of that call worked. Fixed in `369011a`.

**What SwitchNet does:** accepts `?filter.links.friendCode.id.$in=` (real),
`?filter.id.$in=` (real), plus `?friendCode=` and `?ids=` as this server's own
aliases, and `?nickname=` (still guessed as a plain key). Returns an envelope of
stranger-visibility user objects. An unfiltered query is refused, because it
would otherwise enumerate every account on the server to any authenticated
console.

It is possible the code is resolved inside `POST /2.0.0/friend_requests` instead,
which SwitchNet also supports; in that case this endpoint stays as a convenience
for tooling.

### 5. `GET /2.0.0/users/<id>/relationships/<id>`

**Settled, not a capture, 2026-09-22.** The friends sysmodule builds its
response object for this endpoint with a vtable (`0x258558`) that registers
exactly two callbacks: one reading `$.isFriend` and `$.isBlocking` as booleans,
and one reading `$.sentFriendRequestIds[0]` as a third. There is no `type` field
on the wire. SwitchNet sent only its own invented fields, so every relationship
read as the all-false default -- a friend looked like a stranger. The three real
keys are now sent; see
[friends-sysmodule-decompile.md](friends-sysmodule-decompile.md).

**What SwitchNet does:** returns the three flags above, plus `{"type": …}` with
`NONE`, `FRIEND`,

`FRIEND_REQUEST_SENT`, `FRIEND_REQUEST_RECEIVED` or `BLOCKED`, modelled on the
console's own `nn::friends` relationship enum.

### 6. `bs:sts` — the opaque status array in BAAS tokens

Nintendo sends `[385]` in an access token and a seven-element array in a user
token. Nothing on the console appears to read them and SwitchNet reproduces the
observed values verbatim.

**What a capture would settle:** whether they encode account state. A user token
from a console whose account is banned, or has no membership, would show it.

### 7. `capi.lp1.op2.nintendo.net` — the membership response — **`/v1/users/<id>/memberships` settled by decompile**

`GET /v1/users/<na id>/memberships` is asked, by the account sysmodule, on
every friend-list refresh and after a Nintendo Account link — the
"console never asks" note that used to be here was wrong. Its shape is
read out of the account sysmodule's own parser (fw 22.5.0,
`FUN_7100113d90`/`FUN_7100113ed0`/`FUN_7100113c00`/`FUN_7100114070`, judged by
`FUN_7100113e10`):

```json
{"items":[
  {"user_id":"<na id>","context":"default","membership":{"active":true}},
  {"user_id":"<na id>","context":"ex","membership":{"active":true}}]}
```

Every item needs all three fields; `user_id` must be the URL's id as 16 hex
digits; an answer without both a `default` and an `ex` item is `0x18647c`
(2124-3122), and an active `ex` over an inactive `default` is `0x18667c`.
The old stub answered `{"active": true, "expiresAt": …}` everywhere, which
failed every one of those checks. Implemented in `internal/stubs`
(`Handler.membership`).

**Still open:** every *other* op2 path keeps the old guessed answer. A
2026-09-20 decompile of `nim.nso` shows this host's error-code convention
(`500/op2_error`, `503/op2_maintenance`) and a distinct path,
`/v1/my/membership/free_trial_availability`; neither has been seen asked.

### 8. `accounts.nintendo.com` — the Nintendo Account link flow ~~the redirect mechanism~~ **CAPTURED**

See [nintendo-account-link.md](nintendo-account-link.md).

**What the captures settled:** the redirect target on a successful link is a
custom URI scheme (`nintendo://lhub.nx.sys`), not an HTTP callback (captured
against Nextendo Network, not real Nintendo). `POST /1.0.0/users` does fire
automatically once the console has the token, driven by the `nnAccount` SDK,
with no console-local confirmation step first (confirmed 2026-08-27 by a full
decrypted capture of a genuinely successful Nextendo link). A 2026-08-27
`tcpdump` of a real Nintendo Account import on stock hardware (no CFW, no
MITM possible) additionally showed the real host list for this flow —
including `gw.hac.lp1.vermillion.srv.nintendo.net` mid-flow, previously
null-routed on an unverified assumption and now stubbed instead.

**What is still open:** whether real Nintendo's response *content* for this
flow differs from what SwitchNet (modeled on the Nextendo capture) currently
sends — undecryptable without either a working private server that redirects
real Nintendo Account holders, or CFW-level cert trust on a console signed in
with a real account, neither of which was available for the 2026-08-27
real-hardware capture. Two narrower sub-details within the same flow:
`ac:grt` (`jwt.Claims.ACGrantType`, `TODO(capture-needed)` at
`internal/jwt/issue.go:206` and `internal/baas/nalink.go:368`) has only ever
been observed as `4`, for this exact grant — what other values map to is
unknown; and `accounts.go`'s `nxAccountBlob` produces the `nxAccount` opaque
blob, now shape-correct and checked against the console's own consumer — see
[account-sysmodule-decompile.md](account-sysmodule-decompile.md), which
settled its length, encoding, payload skeleton and the exact check the console
applies to it. The earlier reading here, that two real-hardware tests "suggest
but do not prove the console never checks" it, is superseded: the console does
check it, narrowly — the base64 must decode to exactly 168 bytes with a
non-zero byte in the first 16 — and a failure is 2124-0292. What remains open
in that field is what its *values* mean, not whether they are examined. A 2026-09-03 pass
cross-checked this whole flow against a full clone of the
kinnay/NintendoClients wiki (not just search-engine summaries of it) and
against `NextendoNetwork/Prelude-Nro`'s bundled write-up — see
[nintendo-account-link.md](nintendo-account-link.md)'s "External research
pass" section: it independently confirms most of SwitchNet's existing
choices, confirms `CalculateNintendoAccountAuthenticationFingerprint` is
undocumented anywhere public (not merely unfound), and flags that
Prelude-Nro's write-up is an AI-generated secondary summary with at least
one fabricated detail — not a source to trust on its own.

**What SwitchNet does now:** `internal/baas/nalink.go` implements
`GET/POST /connect/1.0.0/authorize` and `POST /connect/1.0.0/api/token` as a
custom login page (checking a person's own SwitchNet login credentials, from
`internal/baas/website.go`'s self-registration), not a copy of anyone's real
OAuth flow. See [nintendo-account-link.md](nintendo-account-link.md) for the
full writeup of both captures.

**A later capture runs the whole flow start to finish** —
[`nextendo-login-flow-2026-08-26.md`](nextendo-login-flow-2026-08-26.md) — and
it answers two things the endpoint-by-endpoint ones could not. First, the eight
unserved `accounts.nintendo.com` paths raised above as a suspected cause of the
post-federation failure are **not called at all** by a link that succeeds:
`term_agreement_token`, `term_confirm`, `transfer_na_tokens*`,
`analytics/device/opt_in` and `%s/profile` appear nowhere in it. Second, every
endpoint the flow does visit — `application/token`, `users`, `federation`,
`login`, `push_channels`, `GET users/{id}`, `qrcode_param`, `devices/snapshot`,
`friends` — is already routed, at the same paths and methods SwitchNet already
serves.

### 9. `nintendo.x5t#S256` and `nintendo.esv` — undocumented device-token fields

Not on the [Kinnay wiki's device-information
page](https://github.com/kinnay/NintendoClients/wiki/Switch-Tokens#device-information).
The 2026-08-27 working Nextendo capture's device token (issued by dauth,
carried as `assertion`/`device_authentication_token` into both
`/1.0.0/application/token` and `accounts.nintendo.com/connect/1.0.0/api/token`)
has a `nintendo` block with two fields SwitchNet did not emit:

- `x5t#S256` — a 43-character unpadded base64url string, the shape of a
  SHA-256 digest. RFC 7517 §4.9 names this exact key for an X.509 certificate
  thumbprint. It is present only in the device token's `DeviceInfo` block
  (dauth) and echoed unchanged into `/1.0.0/application/token`'s access token
  — never recomputed per call, so it is a static per-console value, not a
  per-connection one.
- `esv` — same base64url shape, present in *both* the device token (dauth)
  and the app token (aauth)'s `nintendo` blocks. Meaning originally unknown —
  **settled 2026-09-04, see below.**

**What SwitchNet does now:** `x5t#S256` is implemented — see
`jwt.DeviceInfo.X5tS256` and `dauth.Handler.x5tS256` in
`internal/dauth/handler.go`. nginx forwards the console's already-untrusted
TLS client certificate (`$ssl_client_escaped_cert`, a new
`X-SwitchNet-Client-Cert-PEM` header) and dauth hashes it — a real SHA-256
digest of data already being passed through unverified, not new trust placed
in anything Nintendo-signed. Verified end-to-end on 2026-08-27 against the
live stack with a real TLS client certificate: the claim matches
`openssl x509 -fingerprint -sha256` exactly, on both the device token and the
`application/token` access token it is echoed into.

`esv` is now implemented too. **Settled 2026-09-04**, without a new capture:
`NextendoNetwork/nx-dauth` — the real, running dauth/aauth server behind the
2026-08-27 capture — is source-available (PolyForm Shield 1.0.0), and its
token minting is exactly `"esv": randB64(33)`, i.e.
`base64.RawURLEncoding.EncodeToString(33 random bytes)`, generated fresh on
every single token and never derived from the certificate, serial, device
id, or anything else. It never touched CLAUDE.md's first ground rule to
begin with — it is opaque padding a real server invents per call, not a hash
or MAC of Nintendo key material. The "echoed unchanged" observation above was
SwitchNet's own `/1.0.0/application/token` copying a device token's
`nintendo` block forward (which it still correctly does), not evidence that
`esv` is cached or device-static at the minting layer. Implemented as
`jwt.NewESV`, called once per token minted (see its own comment for why not
once per device) in `dauth.Handler.sign`, `aauth`'s application-token path,
and echoed forward in `/1.0.0/application/token` alongside `x5t#S256`.

**What is still open:** whether the console validates either field locally at
all — that question is unaffected by `esv`'s derivation being settled, since
a real server can emit arbitrary opaque bytes for it regardless of whether
the console reads them back. JWTs are opaque to the party that did not sign
them; there is no direct evidence the Switch OS parses its own device
token's `nintendo` block and compares it against something it independently
knows, as opposed to these being Nintendo/Nextendo server-side bookkeeping
fields the console never reads. This was investigated because `2124-0292`
(see [nintendo-account-link.md](nintendo-account-link.md)) happens with zero
network activity after `federation` returns a fully-correct 200, which is
consistent with — but not proof of — a client-side check on data the console
already has by that point. What was a real, actionable gap regardless of
that open question: SwitchNet's tokens omitted the `esv` key entirely rather
than sending a (harmless) present value, and a console checking only for the
key's presence would have read that omission as malformed. Only a real
hardware retest of the NA-link flow settles whether that omission was
contributing to `2124-0292`.

**What a capture would settle:** a decrypted capture of the *real Nintendo*
NA-link flow (not Nextendo's) showing whether `x5t#S256`/`esv` are present
there too, and — more usefully — any evidence in the Switch's own applet
logs or behavior of it rejecting a token specifically because one of these
fields was wrong or missing, as opposed to a generic module-2124 failure.
Neither field needs a *new* capture just to keep matching a working private
server, though: reading `nx-dauth`'s own source, as above, already settles
that question whenever a private server (Nextendo's own, or one like it)
publishes its implementation.

---

### 10. `dragons.*` — the eLicense/online-eligibility flow — ~~answered blindly~~ **one path settled, two more informed**

`internal/stubs`, the catch-all for hosts SwitchNet redirects but does not
implement for real, answered every path under
`dragons.hac.lp1.dragons.nintendo.net` with the same bare `{}`. Reading the
whole of `NextendoNetwork/nx-dauth`'s `main.go` (single file, 353 lines;
PolyForm Shield 1.0.0, so only the facts below are taken, never its code —
same rule already applied to it in item 9) rather than only the two claims
it had already settled there turned up a service SwitchNet had never looked
at closely: `dragons` is not just an eLicense token endpoint, it is where a
console checks whether it has the *right* to play online at all.

**Settled, 2026-09-05, and now implemented** (`internal/stubs/handler.go`,
`Handler.dragons`): `POST /v2/elicenses/exercise` — "exercising" a licence,
i.e. the online-play eligibility check — must answer `200` with a genuinely
**empty body**, not `{}`. nx-dauth's own operational comment is explicit
about the failure mode: answered any other way, the path 404s, and the
console concludes it has no right to play online — described there as *the*
blocker behind a console reporting it cannot use online features, not a
cosmetic mismatch. This was not previously distinguished from every other
`dragons` path in this repository at all.

**Informed but not independently confirmed, implemented with
`TODO(capture-needed)`:** `/v2/elicenses/migration_state` (answers
`{"state": "COMPLETED", "migration_state": "NONE"}`) and
`/v2/elicense_archives/publish` /
`/v2/rights/publish_device_linked_elicenses` (both answer
`{"elicense_archives": []}`). nx-dauth answers all three with exactly these
field names, but — unlike `elicenses/exercise` — its own source carries no
comment tying a specific one to an observed console failure, so this is a
better-shaped guess (the field the console is presumably looking for,
rather than an object with none of them) and not evidence at the same
strength.

**A new lead, not acted on:** nx-dauth serves `POST /v2/penne_id` from
`dragons.hac.lp1.dragons.nintendo.net`, answering
`{"id": "nxd<uuid>", "password": "<24 random bytes, base64url>"}` — with
no comment claiming this was measured against a real console either. Item
1.3 in `TODO.md` and `docs/QUESTIONS.md` record "how a console obtains a
Penne id and password" as unresolved, tied to
`god.hac.lp1.penne.srv.nintendo.net`'s `/v1/immigrate`/`/v1/penne_ids` (the
wiki's own guess at where it lives, unconfirmed past the path). If this
lead is right, the host and path guessed at were wrong, not just the
shape — worth checking before spending a capture on `god`. Not implemented
here: weaker evidence than `elicenses/exercise`, and CLAUDE.md's rule against
inventing a plausible-looking Nintendo internal cuts harder for an endpoint
this repository does not yet reach at all than for one it already answers
badly.

**Also observed, recorded rather than acted on:** the same file's
`application_auth_token` minting includes two more fields SwitchNet's own
`aauth` does not emit — `tbn` (16 random bytes) and `tbh` (32 random
bytes) — with no comment anywhere in nx-dauth explaining what they mean;
even its own author appears to be filling an observed-but-not-understood
field with random bytes, the same treatment `esv` originally got before
item 9 above settled its derivation. Weaker evidence than item 9's `esv`/
`x5t#S256` (those were cross-checked against a real captured Nintendo
token; these are not shown to be), so left unimplemented rather than
copied blind — a real capture of an `application_auth_token` would settle
whether they are present at all.

### 11. `god.hac.lp1.penne` — `POST /v1/accounts/<id>/notification_tokens` — **shape documented, one detail open**

The last request every hardware Nintendo Account link attempt makes. It is
sent by npns, not the account sysmodule: decompiling `account` (fw 22.5.0)
shows the link ends in `FUN_71000c8450`, which logs in, asks npns over IPC
for a notification token (`FUN_710015b5d0`), and only then sends
`PUT /1.0.0/push_channels/<user>/<device>` with it as `deviceToken`. The
generic stub answered `{}`, so npns had nothing to return and the PUT — the
very next request in the working 2026-08-27 capture — never happened.

Now answered by `internal/stubs` (`Handler.notificationToken`) with
`{"notification_token": "<36 hex digits>"}`, the shape on Kinnay's
[Penne page](https://github.com/kinnay/NintendoClients/wiki/Penne-Servers#post-v1accountsidnotification_tokens),
and the length of the one real value on record (a working server's
`push_channels` body: `"deviceToken":"0009c95be1ac9b2c1e9d9d9fc8bf3302c7f5"`).

**Still open:** whether the leading bytes (`0009…`) are structure npns
checks. SwitchNet's token is a hash, so it has no such prefix. A capture of
this exact response from a working server would settle it; if npns rejects
ours, the push_channels PUT will still be missing and this is the first
place to look.

---

## Phase 2 — Splatoon 3 (NPLN)

In progress (see [`../ROADMAP.md`](../ROADMAP.md#phase-2--splatoon-3-npln--dashboard)),
and already has a growing list of its own `TODO(capture-needed)` markers --
this section was written when the phase genuinely had none yet and was not
kept in step as more accumulated (an earlier version of this paragraph
named a specific count; it had already drifted from `grep -rn
'TODO(capture-needed)' internal/`'s real total by the time this sentence was
written, which is exactly the kind of drift this file exists to prevent, so
this version deliberately names no number to keep stale).

### What is already guessed

- **Squad/delegated matchmaking tickets**
  (`internal/npln/matchmaking.go:42`, `CreateMatchmakingTicket`): whether
  Splatoon 3 ever sends more than one `UserDefinition` in a ticket. NPLN
  supports one console queuing on behalf of a party; if the game uses it for a
  squad, everyone but the leader is currently dropped. A capture of a squad
  entering matchmaking together would settle it.
- **Whether Splatoon 3 ever calls `QueryGameSessions`, and for what**
  (`internal/npln/matchmaking.go`, `GameSessionService.QueryGameSessions`):
  answered with an empty page rather than left `Unimplemented` -- see "What
  `NextendoNetwork`'s source settled, 2026-09-16" below for why -- but nothing
  confirms Splatoon 3 calls this RPC at all, or what a private-battle room
  browser (the plausible candidate, since ticket-based matchmaking never needs
  one) would expect back if it does.
- **Whether Splatoon 3 dials `GameSession.Port` at all**
  (`internal/npln/matchmaking.go:394` area, `SyncGameSession`): gameplay is
  peer-to-peer over ICE, and a client that connects entirely through
  negotiated ICE candidates may never read this field. Persisting and
  returning whatever the host publishes is the defensible reading either
  way -- a field the client ignores costs nothing -- but nothing confirms a
  real client actually uses it. A capture of a match connecting would settle
  it.
- **Matchmaking/session resource-name shapes**
  (`internal/npln/matchmaking.go:320`, `ticketProto`, and `:448`,
  `lastSegment`): every NPLN resource name is a path, and SwitchNet follows
  the same AIP-style convention the rest of the definitions do, but nothing
  confirms what Splatoon 3 actually receives or how strictly it parses it.
  `lastSegment` deliberately parses leniently so a wrong prefix costs a
  cosmetic mismatch rather than a broken `Track`/`Cancel`.
- **The matched-session security token** — **ANSWERED 2026-09-23, and it is
  required**: see [npln-session-token-2026-09-23.md](npln-session-token-2026-09-23.md).
  The console reads `gsid`/`usid`/`uid`/`tid` out of the token's `gamesync`
  object to open its session, so a match that hands out none leaves the console
  opening TCP+TLS+h2 and never sending a request — `2321-4992` on
  `KeepUserSession`, with nothing in the server's log. SwitchNet mints the
  measured shape now (`internal/npln/auth.go`, `issueSessionToken`). Still open,
  and only a console of our own can settle it: whether one accepts a token
  SwitchNet signed. Two fields also go out on defaults rather than evidence —
  `team` (Splatfest teams are announced but not assigned) and `attr`/`ltcy`.
- **`Auth.RefreshToken`**
  (`internal/npln/services.go:72`, `IssueToken`): whether Splatoon 3 ever
  calls it at all, and what it expects back. SwitchNet issues no refresh
  token and relies on the game re-authenticating from its BAAS id token on
  reconnect instead -- a path already served, so this is a defensible
  default rather than a fabricated response, but unconfirmed.
- **The NPLN user resource-name prefix**
  (`internal/npln/services.go:96`, `userResourceName`): follows the
  `tenants/<t>/users/<u>`-shaped convention every other NPLN name uses, but
  no capture confirms Splatoon 3 receives or parses this specific one.
- **What `Friends.ActivateUser` actually changes server-side**
  (`internal/npln/friends.go:51`): SwitchNet has no separate activation state
  to flip -- presence already resolves fresh on every read -- so this
  acknowledges the call and does nothing else. If a capture shows the console
  waiting on some effect this does not produce, that effect is what is
  missing here.
- **`FriendUser`/`BlockingUser` resource-name shapes**
  (`internal/npln/friends.go:287`, `friendUserProto`): same AIP-style
  `.../friendUsers/<id>` and `.../blockingUsers/<id>` convention as
  matchmaking's resource names, and the same caveat -- `lastSegment` parses
  leniently, so a wrong prefix costs a cosmetic mismatch rather than breaking
  a call.
- **Presence resource-name shape**
  (`internal/npln/presence_service.go:231`, `presenceUserID`): follows the
  same `.../users/<u>/presence` singleton convention `resources.proto`'s
  `Presence.name` field implies, parsed leniently for the same reason as the
  matchmaking and friends names above.
- **`Schedule`'s Vs map/mode rotation content**
  (`internal/toyohr/schedule.go`, `SelectVsParams`/`SelectVsSchedules`/
  `SelectSeasonSchedules`/`SelectLeagueSchedules`): answered as "nothing
  scheduled" rather than invented. This is Nintendo's own game design data
  (which stages, which mode), not a shape SwitchNet can reason its way to the
  way a resource-name convention can -- there is genuinely no source for it
  short of a capture or a dump. A capture of a real server's response for
  each of the four RPCs would settle what a populated one looks like.
  `SelectCoopSchedules`, the fifth `Schedule` RPC, is no longer on this list:
  its wire shape was never the actual blocker (the proto already carries it,
  independently corroborated -- see `internal/toyohr/README.md`), so
  `coop_ids.go` and `coop_schedule.go` give it the operator-authored table
  this entry used to only suggest as a hypothetical for Vs.
- **Whether `Messaging.RecvMessage`'s empty resume token replays the whole
  retained mailbox**
  (`internal/npln/messaging.go`, `RecvMessage`): the defensible reading, since
  this is the invite mailbox opened early in boot and a pending invite sent
  while the console was off has to still be there -- but nothing confirms it
  against a real client. Getting it backwards either loses an invite
  silently or replays a stale one on every reconnect.
- **~~Vermillion's response bodies~~ CAPTURED, 2026-09-21.**
  NextendoNetwork's own public Splatoon 3 NPLN server documents real-console
  capture evidence for most of this host -- `internal/nplnrest.go`'s
  `vermillion` now implements every confirmed path
  (`/v1/devices/initialize`, `/v1/devices/vermillion-device-id`,
  `/v1/devices/penne-id`, `/v1/accounts/config`) with the real shapes and
  values, plus the two bugs their capture caught that would otherwise
  silently strand a console at the ink loading screen: the device-id
  response key must be camelCase (`vermillionDeviceId`), and its
  base64-decoded value must not contain `/` or `+` (Splatoon 3 uses it as a
  literal path segment and hard-crashes on either). `/v1/accounts/<other>`,
  `/v1/acds/search` and `/v1/nearness-checks` remain genuinely unconfirmed --
  no capture of these exact bodies exists anywhere, including theirs -- and
  are answered with a well-formed empty envelope rather than a guessed one.
  See `internal/nplnrest.go`'s package doc comment for the full citation and
  the code/facts boundary (PolyForm Shield licensed; facts taken, never
  their code, same as nx-dauth).
- **~~How a console obtains a Penne id and password~~ PARTIALLY SETTLED,
  2026-09-21.** The same source's real-console capture shows the password
  never appears in any server response at all -- the console generates both
  the penne id and its password itself, then registers them on two hosts
  this project already redirects (`POST beach.hac.lp1.eshop.nintendo.net
  /v1/devices/<id>/penne_id/register` and `PUT dragons.hac.lp1.dragons
  .nintendo.net/v2/penne_id`, both `{"penne_id":"..."}`) before attempting
  `POST .../login_tickets`. Registration failing on either host is what
  left their own server's console looping device initialisation forever,
  never reaching a real penne login. Both hosts' existing generic
  empty-object stub answer already matches their own (independently
  reached, still capture-unconfirmed for the exact response body) answer
  for this specific path -- see `internal/nintendo/hosts.go`'s `beach`
  entry and `internal/stubs/handler.go`'s `dragons` default case. Still
  open: `god.hac.lp1.penne.srv.nintendo.net`'s own two plausible endpoints,
  not served by this project at all.
- **Vermillion and penne's request bodies, and login_tickets'
  ~~real values~~ CONFIRMED, 2026-09-21.** The same source's capture of a
  working login pinned `persistent_connection_params_simple` completely --
  `wait_sec: 3600`, `rtt_max: 1000`, `wowl_timeout: 200`, `ignore_rst: true`,
  plus two nested `awake`/`sleep` objects this project's earlier guess had
  no field for at all -- and the exact top-level response shape
  (`expires_at`, `frontline_fqdn`, `issued_at`, `ticket`), now implemented
  in `internal/nplnrest.go`'s `penneLoginTickets`/`penneFrontlines`. Still
  open: whether the console validates the ticket's own shape (the value
  itself is still an opaque placeholder, since nothing here can construct
  or validate a real Nintendo-signed one).
- **Later Frontline messages and acknowledgements**
  (`internal/nplnrest/frontline_frame.go`, `frontlineOpeningFrame`): the
  [successful hall capture](nextendo-hall-bootstrap-2026-09-23.md) establishes
  the opening frame and that request EOF must leave the response open. These
  are implemented. The semantic name of discriminator 12, handling of later
  client messages, and any later server cadence remain unconfirmed.
- **`FestEntry`'s resource-name shape**
  (`internal/toyohr/entries.go`, `festEntryProto`): follows the same
  `tenants/<t>/fests/<ID>/...` convention `GetFestDecryptionKey` already
  uses, extended with an `entries/<userID>` suffix -- but nothing confirms
  what `CreateFestEntry`'s response actually names a new entry, or whether
  the console parses this field at all rather than only checking the RPC
  succeeded.
- **Whether an all-`is_valid: false` `FestResult` reads as "not concluded"**
  (`internal/toyohr/entries.go`, `GetFestResult`): SwitchNet has no real
  battle-outcome data (`npln_fest_entries` records who picked which team,
  not who won, and `GameRecord` is unimplemented), so every sub-result
  reports `IsValid: false` rather than a fabricated ratio. Whether the
  console reads that as "the fest has not concluded yet" (leaving it
  running, the same as before this RPC existed) or as a valid but empty
  outcome is unconfirmed either way.
- ~~**Whether Gamesync is reached through the tenant channel or a separate
  port**~~ — resolved: its own port. `gamesync.npln.nintendo.net` not ending
  in `srv.nintendo.net`, unlike every other NPLN host, turned out to be
  exactly the deliberate signal it looked like: `internal/nintendo/hosts.go`
  classifies it `ServiceGamesync`, a distinct service on its own port (7575),
  and `internal/gamesync` is a complete, self-contained gRPC service rather
  than one registered on `internal/npln`'s gateway. See `docs/QUESTIONS.md`.
- **`FieldTransform`'s `LoadGlobalValue`/`StoreGlobalValue`/
  `ClearGlobalValue`, and `DefermentOperation`** (`internal/gamesync/
  gamesync.go`): no Firestore equivalent and no public documentation of what
  a gamesync "global value" is scoped to, what loading one that was never
  stored should return, or what a "deferment" defers or until when.
- **`Round`'s fields** (`proto/gamesync/v1/resources.pb.go`): named `Unk1`
  through `Unk4` in this project's own proto reversal, still uninterpreted.
  `CreateRound` no longer needs a capture to be safe, though: it echoes back
  the caller's own `Round` rather than guessing at them server-side, the same
  answer a real, hardware-tested NPLN reimplementation
  (`NextendoNetwork/splatoon-3`) gives. A capture would only matter if
  something here ever needed to construct a `Round` itself.

### What `NextendoNetwork`'s source settled, 2026-09-05

`NextendoNetwork/nextendo-account` and `NextendoNetwork/splatoon-3` (real
NPLN/account server reimplementations the operator supplied, tested against
real hardware per their own README; PolyForm Shield 1.0.0-licensed, so
everything below is a fact taken from reading them, never any of their code
-- same rule already applied to `nx-dauth` above) settled or bore on several
items above and elsewhere in this repository:

- **The two-frame gRPC response shape — settled, and now implemented**
  (`internal/npln/server.go`, `sendNPLNHeader`). Real Nintendo NPLN gateways
  answer every unary call as two separate HTTP/2 HEADERS frames (headers,
  then trailers), never grpc-go's default "Trailers-Only" single frame for a
  handler that returns with nothing already sent. Their commit history
  documents a real console crashing (`2162-0001`, `nn.npln.Worker` aborting)
  specifically on the single-frame shape for an otherwise-correct empty or
  `NotFound` response -- which is what essentially every `Unimplemented`/
  `NotFound` answer in `internal/toyohr`'s backlog (§1.4) and
  `internal/gamesync` was almost certainly sending before this fix, untested
  against hardware. `TestSendNPLNHeaderSplitsHeadersFromTrailers` locks the
  wire shape down at the frame level.
- **`CreateRound` — settled, and now implemented** (`internal/gamesync`):
  see the `Round` entry above.
- **The Splatfest asset-naming question — corroborated, not yet certain**:
  see `docs/QUESTIONS.md`'s "Does the game key Splatfest assets on the
  announced fest id" entry.
- **Never echo `tenants/current/users/current` (or any `current` alias) back
  in a response** -- checked against SwitchNet, not an issue found: their
  matchmaking notes measured a real client failing to find its own session
  when a response echoed the unresolved `current` alias instead of a
  concrete `tenants/<t>/users/<uid>`. `grep -rn 'tenants/current\|users/
  current' internal/` finds nothing in this repository; every resource name
  SwitchNet returns is already built from a resolved caller/user id.
- **A `Document`-shaped response must never carry an empty `name` field --
  checked against SwitchNet, not an issue found**: their measurement (twice,
  on two different documents) is that a client SDK receiving a message-less
  success constructs a default object whose name is empty, and the
  resource-name parser then aborts the game. `internal/gamesync.Service`'s
  own `GetDocument` (service.go:149) already answers a missing document with
  a `codes.NotFound` error rather than an empty-named `Document`, so this
  does not apply here -- recorded so it stays checked if that method ever
  changes shape.
- **`GameRecord`'s document-naming and empty-response rules -- recorded for
  whenever CLAUDE.md's "no defensible default" reasoning against
  implementing `GameRecord` (see `internal/toyohr/backlog.go`) is revisited,
  not acted on now**, because none of it supplies the one thing still
  missing (defensible X Power/rating seed *values*):
  - A create-shaped RPC's response document name is load-bearing: the client
    immediately re-reads exactly that document afterward, so an empty or
    guessed name (their own first attempt used the request's `parent`
    field, `tenants/current/users/current` -- not a valid document name)
    causes an infinite create-read loop rather than a graceful miss. A
    pure existence-check RPC (plain `GetDocument`) does not have this
    problem -- the same "no message" answer that breaks a create's
    read-back is exactly what a real hall capture shows Nintendo doing for
    an ordinary absent document.
  - An account's `CoopUserAttribute`/`VsUserAttribute`-equivalent documents
    must answer consistently with whether that account has ever played
    (correlated with save-record state), never independently defaulted --
    answering both as absent for an account that also has a non-empty saved
    game is a state that does not exist on real Nintendo and was measured
    crashing the client.
  - A per-period record (their `PointCardRegular`) is legitimately absent
    until that period has actually been played; that is real game-state
    semantics; a real client was measured never calling the equivalent of
    `InitializeAttributes` at all once an account has a save.
  - The real captured `NotFound` for an absent ugcstore-family document
    (`Canola`/`Locker`/`CoopScenario`/`Replay` in this repository's own
    terms) carries a `grpc-status-details-bin` detail of type
    `nn.npln.errdetails.NError` with three fields: 1 (16 random bytes,
    hex-encoded), 19 (8 random bytes, hex-encoded), 6 (varint `0`). Both
    hex fields are per-request markers regenerated on every response, not
    values to persist. Not added to this repository's own `IkasuDocument`/
    `ResolveCoopScenarioCode`/`ResolveReplayCode` NotFound answers here: all
    of those are one-shot code lookups against codes nothing ever
    registers, not the specific create-then-immediately-reread loop the
    detail was measured fixing, so there is no evidence yet that it matters
    for them specifically -- recorded rather than spent speculatively,
    per CLAUDE.md's rule against inventing a plausible-looking Nintendo
    internal.

### What `NextendoNetwork/splatoon-3`'s newer commits settled, 2026-09-16

Two commits landed on `NextendoNetwork/splatoon-3` since the 2026-09-05 pass
above (still PolyForm Shield 1.0.0 -- facts only, same rule as every entry
above):

- **`GameSessionService.QueryGameSessions` must not be left
  `Unimplemented` — settled, and now implemented**
  (`internal/npln/matchmaking.go`, `gameSessionService.QueryGameSessions`).
  Their commit message documents a real console error (`2321-4224`) from a
  different NPLN title (Nintendo Classics, "Nintendo 64") whose online lobby
  would not open while this RPC answered `Unimplemented`; returning an empty
  `QueryGameSessionsResponse{}` instead fixed it. SwitchNet's own
  `GameSessionService` embedded `UnimplementedGameSessionServiceServer` and
  never overrode this method, so any caller — regardless of title — got the
  same hard failure. Whether Splatoon 3 itself ever calls this RPC is still
  unconfirmed (ticket-based matchmaking, which is Splatoon 3's whole flow,
  never needs it), so this is the same class of fix as item 10's
  `elicenses/exercise` above: a defensible default for an RPC this server was
  already answering badly, not a confirmed Splatoon 3 shape. See the
  `TODO(capture-needed)` this leaves behind.
- **A strict full-nominal-count matchmaking threshold can leave a real queue
  permanently unfilled — checked against SwitchNet, not an issue found.**
  Their commit measured this directly: requiring the full nominal player
  count (8, for Turf War) produced 28 tickets, 12 cancellations and zero
  matches formed over a night of real testing, fixed by relaxing the
  threshold down to no less than half the nominal count as tickets wait.
  SwitchNet was never exposed to this failure mode in the first place:
  `matchmaking.DefaultConfig.PlayersPerMatch` is 2, not a title's real mode
  size, specifically because "a number that lets two friends actually start
  a match is more useful than one that is faithful and idle" (see
  `internal/matchmaking/queue.go`'s own `DefaultConfig` comment and TODO.md
  §5.3) — and an operator who does raise a mode's size at runtime has
  `Queue.ForceMatch`, a manual escape hatch that forms a match from
  whoever is waiting rather than an automatic time-based relaxation. Recorded
  here because the empirical failure mode is worth knowing, not because it
  changed anything: SwitchNet's design choice already avoids it by a
  different route.

### Presence attributes, settled from NextendoNetwork's capture notes

Two entries used to sit on the open list above -- whether Splatoon 3 reports
game-specific presence over `PresenceService.KeepAlive`'s `UpdatePresence`,
and which `Presence.Attributes` keys a real client reads. Both are answered by
a real Nintendo capture that `NextendoNetwork/splatoon-3` records in its own
comments (2026-08-15; facts only, PolyForm Shield, none of its code used):

- The game does publish over `KeepAlive`: a friend's presence carries
  thirteen attributes. `GameStatus=1` with an empty `SessionId` is "just
  online"; `GameStatus=2` with a non-empty `SessionId` is "in a room", which
  is what makes "Join" appear, and that `SessionId` is the game session the
  joiner then passes to `JoinGameSession`. `CurrentParticipants` and
  `MaxParticipants` follow the room; `PlayerName`, `GameMode`, `Udemae`,
  `UsePassword` and `FestTeam` are among the rest.
- Updates are partial: a player opening a room sent thirteen keys, then four
  (the room opening), then one (the participant count). Replacing rather than
  merging erased `GameStatus`/`SessionId` two seconds after the room opened.
- An already-online friend opening a room changes attributes, not state, so a
  stream that only pushes on state changes never shows the room.

Implemented in `internal/npln/presence_attributes.go`: attributes are merged
per player, passed through untouched in `presenceProto`, pushed to open
`SubscribePresences` streams on change, and cleared when the player's last
`KeepAlive` stream closes. They are kept out of `db.Presence`, which stays
BAAS's alone. Second-hand -- their capture, not ours -- so a SwitchNet capture
of a friend opening a room is still worth taking to confirm.

### Private rooms (`2321-4224`), from NextendoNetwork's capture notes

Opening a private battle or Salmon Run room answered `Unimplemented` on
`CreateGameSessionCreationTicket`, which Splatoon 3 shows as `2321-4224`. The
flow is now implemented in `internal/npln/rooms.go`, on what
`NextendoNetwork/splatoon-3` records from a real Nintendo capture of it
(facts only, PolyForm Shield, none of its code used). Settled there:

- `CreateGameSessionCreationTicket` answers PENDING, with the concrete
  tenant in `matchmaking_config` and a `GameSession` holding only the
  request's properties.
- `TrackGameSessionCreationTicket` sends one message, already SUCCEEDED, and
  then ends the stream. Measured: held open, the game waited about twenty
  seconds and cancelled; closed, it dialled its session server within a
  second. The opposite of `TrackMatchmakingTicket`, deliberately.
- The SUCCEEDED `GameSession` is marked `is_public` even for a private room,
  carries no `user_sessions` (`GetGameSession` is where a real server lists
  them), and keeps the client's own properties with `_BaseConfigName` and
  `_AliasSuffix` added. Replacing the client's properties made the game
  abandon the room with `CreateSessionFailed`.
- The host's `MatchedUserSession` needs the same `gss` session token as a
  matched player, and the concrete user name rather than `current`.
- Salmon Run's room size, 4, is in the capture.

Still open, each a `TODO(capture-needed)` in `rooms.go`:

- The size of every other room mode. Private battle's 10 is NextendoNetwork's
  reading; the squad a player opens to bring friends into Turf War is set to
  4 from its description alone. The config name of each room mode is logged
  the first time it is seen.
- The room code's format. Their short-alias capture is a different title's
  (Mario Party Jamboree), and on Splatoon 3 their displayed code relies on a
  console-side patch SwitchNet does not ship. Friends joining from the friend
  list use the room's session id, not a code, so this is not on that path.
- The status a real server answers when a room is full.
- The room's password lock indicator: NextendoNetwork injects an undocumented
  `GameSession` field for it, number unconfirmed. Not ported. The password
  itself is checked on join.

A SwitchNet capture of a friend opening a room and another joining it from
the friend list would confirm all of it, and settle the presence attributes
above at the same time.

When it is further along, these are the captures that will unblock the rest.
NPLN is gRPC over HTTP/2, so this needs `grpc-dump` or `proxide`, not
mitmproxy, and it does need the console.

| Capture | Why |
| --- | --- |
| game boot, from the very first vermillion request through the NPLN auth handshake | confirms whether vermillion is actually contacted (and needed) for Splatoon 3 specifically, penne's real request bodies and this server's guessed response fields, and what the tenant expects in `authorization`/`uid` metadata once the gRPC channel opens |
| a frontline connection, bytes in both directions after the HTTP response headers | later client-message handling, acknowledgements, and server cadence beyond the captured opening frame |
| entering the lobby | `Schedule`'s four Vs/season RPCs (currently answered empty; `SelectCoopSchedules` is no longer on this list -- see above), the first things the lobby draws |
| a matchmaking request, and the session response | the station info each client needs for its Pia/ICE handshake |
| the in-game friends list | `Friends.ListFriendUsers` and `PresenceService`, and specifically whether `presence_deliverable` / `presence_receivable` are set |
| an invite sent while the receiving console is off, then a boot | `Messaging.RecvMessage`'s empty-resume-token behaviour, and its resource-name shapes |
| post-match results upload | `GameRecord` |
| picking a team during a running fest, then the fest concluding | `CreateFestEntry`'s response shape and `GetFestResult`'s real (non-`is_valid:false`) content |
| any real `WriteDocuments`/`CommitTransaction` call carrying a `DefermentOperation` or a `LoadGlobalValue`/`StoreGlobalValue`/`ClearGlobalValue` transform | what either feature is for, which nothing public documents |

[kinnay/NPLN-Protocols](https://github.com/kinnay/NPLN-Protocols) also ships an
IPS patch generator that disables TLS verification *inside Splatoon 3
specifically* — narrower than the system-wide patch, and what
[`switch/README.md` part 3](../../switch/README.md#part-3-splatoon-3-pins-its-own-certificates)
documents. Regenerate it after every title update: it is gated on the game
version and stops applying silently.

## Phase 3 — the eShop

The eShop commerce API is the **least documented thing in this whole project**.
The wiki has one endpoint name for ECS and nothing else, so captures here are not
a refinement — they are the only way in.

Ranked by how blocking they are:

| Capture | What it settles |
| --- | --- |
| **the eShop applet's first requests after opening** | which host the operator's firmware actually uses: `bugyo.hac.lp1.eshop.nintendo.net/ashigaru/` or the newer `hac.lp1.savanna.srv.nintendo.net`. Building the storefront against the wrong one wastes all of it |
| **a purchase, end to end** | the ECS/IAS commerce calls, in order, with bodies. This is the core of 3.2 and there is currently nothing to work from |
| **`dragons` rights calls around an install** | `rights/available_elicenses`, `rights/publish_elicenses`, `elicenses/exercise` — the request and response bodies |
| **`atum` requests during a download** | confirms the path scheme and the `X-Nintendo-DenebEdgeToken` header against a real download, and shows whether range requests are used |
| **`tagaya` version list** | one response is enough; the shape is documented but a real one confirms it |
| the wallet/balance screen | how the balance is reported, and in what currency shape |
| redownload from the purchase history | whether it differs from a first purchase |

The eShop is a **website**, so its own requests to the commerce API are ordinary
`fetch`/XHR from the page. That means once the storefront is ours, we define most
of that API — the captures matter for the parts the *system* drives, which is
rights and content, not the storefront.

Note that `bugyo` requires the "SwitchShop" client certificate, so a proxy in
front of it needs to pass the client cert through or the connection is refused
before it becomes a request.

---

## Phase 4 — cloud save (OLSC)

`internal/scsi` implements storage.hac.lp1.scsi.srv.nintendo.net and
permission.lp1.sata.srv.nintendo.net against Kinnay's wiki page for OLSC —
most of the shape is confirmed there, the same kind of source this project
already leans on for BAAS/dAuth/aAuth. What is not confirmed is marked
`TODO(capture-needed)` at the exact function in `internal/scsi/handler.go`;
see `internal/scsi/README.md` for the full list. Ranked by how blocking each
is:

| Capture | What it settles |
| --- | --- |
| **a real save backup, start to finish** | whether a console cryptographically validates `generate_key_seed_package`'s response before it will accept a restore — the single most likely hard blocker in this whole feature, the same category of unknown `nxAccountBlob`'s header prefix turned out to be for phase 1's account linking. If it does, no shape guessed here will pass, and the real wrapping format needs to come from this capture |
| the same capture | `generate_key_package` (`permission.lp1.sata.srv.nintendo.net`) — not even a confirmed field name exists for this one, only the bare endpoint string (independently confirmed by decompile) |
| the same capture | `component_files/create`'s request body and `signed_uri`'s response shape — both listed on the wiki with method `"?"` and no fields at all |
| the same capture | whether `start_upload` really has no `application_id` field, or whether this project's own `X-SwitchNet-Application-Id` header workaround (see `applicationIDFrom`) is masking a real field name |
| a restore on a console that already has a real backup | `start_download`/`finish_download`'s actual shape, currently answered as "the same as GET .../{id}" and "a bare acknowledgement" respectively with no evidence either way |
| any real SCSI/SATA error response | confirms or replaces `internal/scsi/errors.go`'s BAAS-shaped guess |

This needs a console that reaches a real, unmodified Nintendo server (or
Nextendo's own working reference deployment) with cloud save actually
enabled on the account — the same posture as every other phase's captures,
see [`../PACKET_CAPTURE.md`](../PACKET_CAPTURE.md).

---

### What real hardware and `NextendoNetwork/splatoon-3`'s source settled, 2026-09-24

A full hall bootstrap finally succeeded on SwitchNet's own hardware after this
session's earlier fixes (STUN, SCSI auth, the Frontline opening frame, the
`tenants/current/users/current` alias). Two more real gaps surfaced right in
the resulting call sequence, both fixed the same session:

- **`Schedule.SelectLeagueSchedules` and `Schedule.SelectVsParams` had no
  handler at all — settled, and now implemented**
  (`internal/toyohr/schedule.go`). Both were embedded via
  `UnimplementedScheduleServer` only, despite this project's own README and
  ROADMAP already documenting all four `Schedule` RPCs as "answered empty" —
  a stale claim two of the four never matched. The console called both,
  back to back, immediately before the game crashed at the Splatfest region
  prompt. Now answered the same "nothing scheduled, not invented" way as the
  other two.
- **`FestService.SelectFestSchedule` must never answer with an empty
  response, even when no fest is configured — settled, and now fixed.**
  `NextendoNetwork/splatoon-3`'s own comment (facts only, not its code)
  documents measuring this directly against two real Nintendo captures: a
  real server always answers with a non-empty schedule (the current fest, or
  the last one, never nothing), and an empty one crashes Splatoon 3 about
  two seconds later (`2162-0001` in `nn.npln.Worker`), right after the game
  loads `FestRegion`/`Fest`/`FestRecord` from its own save and asks for the
  matching schedule. SwitchNet's own handler answered a genuinely empty
  `SelectFestScheduleResponse{}` whenever no operator fest was configured —
  exactly the shape their measurement says crashes the game — and this
  reproduced on SwitchNet's own hardware the same day: the Splatfest region
  prompt appeared (reading those same save keys) and the game crashed under
  a second later. Fixed with `noFestSentinel`, a fest of SwitchNet's own
  (no team names, no banners, no real BCAT content, started at the Unix
  epoch so it can never be "announced" in the sense that would trigger a
  decryption-key fetch) sent in place of an empty response.

Two more instances of the same crash family surfaced immediately after,
each requiring its own real capture to reproduce -- the earlier VM-side
loopback capture technique (`docs/PACKET_CAPTURE.md`) decrypted every gRPC
exchange without console-side setup:

- **`CloudSave.GetSaveRecord` answering `NotFound` — settled, and now
  fixed.** `NextendoNetwork/splatoon-3`'s own `cloudsave_nextendo.go`
  documents testing three behaviors on real hardware for a player with no
  save record: `NotFound` makes the game create nothing and loop on a
  communication error; a genuinely empty success makes it stop dead — no
  `CreateSaveRecord`, no `FestSchedule`, no documents; only a real,
  structurally-complete record made everything resume. SwitchNet answered
  `NotFound`, reasoned as correct rather than a stub. A VM-side loopback
  capture confirmed the console went silent immediately after this RPC on
  SwitchNet's own hardware. Fixed by answering a real `SaveRecord` for the
  caller with an empty-but-present `SaveData` `MapValue`, never seeded with
  Nextendo's own captured save content since that belongs to a real
  stranger's account.
- **`Schedule.SelectVsParams` answering with `params` unset — settled, and
  now fixed.** `NextendoNetwork/splatoon-3` replays a real captured
  `VsParams` payload rather than answering empty, and separately found the
  payload's own two timestamps must ride the same clock shift as everything
  else or the client "cannot tie the current rotation to its parameters"
  and stops advancing. A second VM-side loopback capture, taken after the
  `GetSaveRecord` fix, showed the full confirmed reference sequence firing
  correctly through this RPC and then going silent before ever calling
  `PresenceService/SubscribePresences` — the next call in the confirmed
  sequence. Fixed by answering a real, non-nil `VsParams` timestamped off
  the current clock; `Attributes`, the one field whose real content is
  genuinely undocumented, stays an honestly empty `MapValue`
  (`TODO(capture-needed)`, in `internal/toyohr/schedule.go`).

A third VM-side loopback capture, taken after the `SelectVsParams` fix,
showed the console making it one RPC further into the confirmed sequence
each time before going silent at the exact same crash address
(`[574284b4] + 0x17574c`, byte-identical registers and full stack trace
across three separate crash reports) — pointing at a single generic abort
in the client's resource-path parser rather than three unrelated bugs:

- **`UserScreening.GetViolation` answering a genuinely empty message —
  settled, and now fixed.** Upstream's own proto marks
  `GetViolationResponse`'s fields `[UNKNOWN]`; SwitchNet answered a truly
  empty message, reasoned as unambiguous since "no violation" and "here is
  your violation" are the same bytes with no fields to tell them apart.
  `NextendoNetwork/splatoon-3` measured (facts only, not its code) that
  Splatoon 3's own client parses this response's own resource name and
  hard-aborts in its resource-path parser when it is empty — the crash
  return address they name for this sits a few hundred bytes from the one
  reproduced on SwitchNet's own hardware, inside the same client module.
  The upstream `.proto` now carries the confirmed `name` field (field 1,
  string) and was regenerated via `scripts/gen-proto.sh`; the handler
  always resolves it to `tenants/<t>/users/<uid>/violations/current`.

**Later correction, 2026-09-24:** The named `/violations/current` reply
prevents the empty-name crash but tells the game the player is sanctioned.
The current working Nextendo server handles "no violation" as a two-frame
NotFound: it sends response headers before the status trailers. Its source
records a fresh player being blocked from the hall by the named form.
SwitchNet now uses the absent-resource form, covered by a gRPC framing test.
The exact Nintendo trailer status was not visible in the capture format, so
this is a reference-implementation behavior, not a direct Nintendo-wire claim.

A fourth VM-side loopback capture, taken after the `GetViolation` fix,
showed the console making it further still — every RPC through
`Schedule/SelectVsParams` firing cleanly, `PresenceService/KeepAlive`
open and answering — before crashing at a DIFFERENT return address inside
Splatoon 3's own module ([28c4287a] + 0x7d3094 this time, not the
+0x7d5ec4 `GetViolation` reproduced) — confirming a fourth, distinct
instance of the same generic-parser bug rather than a regression. This one
was run down with a real Ghidra decompile of the game's own `main` (NSO
build id `28c4287a...`, this project's own hardware dump, already
extracted from earlier RE work under `switchnet-re/`), not a guess from the
crash address alone:

- **`CoopSchedule.Name` is missing a `targets/<target>` path segment —
  settled by decompile, and now fixed.** Disassembling
  `[28c4287a] + 0x7d3094`'s caller found a resource-name parser
  (`FUN_71007d2a70`) that requires the literal shape
  `tenants/<t>/targets/<target>/coopSchedules/<rest>` — a hard `memcmp`
  against `"targets"` and `"coopSchedules"` as the third and fifth path
  segments — and its caller hard-aborts when the parse fails.
  `coopScheduleProto`'s old name, `tenants/<t>/coopSchedules/<id>`, omits
  the `targets/<target>` segment entirely. This reproduced on real hardware
  with four real coop shifts seeded (an empty Salmon Run rotation never
  reaches this code path at all, which is why earlier attempts with no
  shifts configured never hit it). `<target>` is opaque to the parser — it
  is extracted and stored, never compared against a literal — so it now
  carries the shift's own `Kind` as a stable SwitchNet placeholder, not
  reconstructed Nintendo data.

A fifth VM-side loopback capture -- and, once those pcaps had aged out of the
scratch directory, a fifth real crash report -- was reproduced with the
coop shifts deliberately deleted (ruling out the fix above) and still hit
the exact same generic abort, at yet another `[28c4287a]` return address:

- **`VsParams.Name` needed the same `targets/<target>` segment
  `CoopSchedule.Name` did — settled by decompile, and now fixed.** The
  crash's `ReturnAddress[04]` this time was `[28c4287a] + 0x7d2434`, inside
  a wrapper (`FUN_71007d2034`) around a second resource-name parser
  (`FUN_71007d1e10`) in the same source region, checking for the literal
  segment `"vsParams"` after `targets/<target>/` exactly the way the coop
  one checks for `"coopSchedules"`. `SelectVsParams`'s own fix earlier in
  this file answered `tenants/<t>/vsParams/<id>` — missing that segment —
  and reproduced this crash on real hardware. Unlike `CoopSchedule`,
  `<target>` here is not invented: `SelectVsParamsRequest` already carries a
  `Target` field the client itself sends, echoed back into the response's
  `Name` rather than guessed.

A sixth VM-side loopback capture, taken after that fix, confirmed it worked:
`CloudSave/GetSaveRecord` and `Schedule/SelectVsParams` both answered
`grpc-status: 0` on the wire, and the console went one step further still,
reaching `PresenceService/SubscribePresences` for the first time all
session. It then called `CloudSave/WriteSaveRecord` twice, got
`grpc-status: 12` (`Unimplemented`) both times, and went silent for good
shortly after -- and a sixth crash report showed the identical
`[28c4287a] + 0x7d2434` return address once more, this time with
`SelectVsParams` itself proven innocent by the capture, at the time this
was written. That turned out to be the wrong read of "innocent" -- see the
next entry.

- **`CloudSave.WriteSaveRecord` answering `Unimplemented` — settled, and
  now fixed.** NextendoNetwork/splatoon-3's own proto comment (facts only,
  not its code) documents the real shape: the client sends an
  `evt=Validate`-style `WriteSaveRecord` naming exactly the keys missing
  from a blank record (`CloudRandomSet`, `HaveGearClothesMap`,
  `HaveGearHeadMap`, `HaveGearShoesMap`) as soon as it sees one, i.e.
  immediately after `GetSaveRecord`'s honestly-blank answer.
  `WriteSaveRecordResponse` carries exactly one field, a timestamp, per
  both this project's own proto and NextendoNetwork/splatoon-3's comment —
  so acknowledging costs nothing this server does not already have, and
  nothing is stored, the same "no game dump, no save data" boundary
  `GetSaveRecord` draws.

A seventh and eighth crash report, deployed on top of the `WriteSaveRecord`
fix, still hit the identical `[28c4287a] + 0x7d2434` address -- proving the
`WriteSaveRecord` theory above wrong. A seventh capture explained why:
`SelectVsParamsRequest`'s `Target` field is not a bare id, it is a full
alias path, `tenants/current/targets/default` -- the same `tenants/current/...`
convention `GameRecord/InitializeAttributes` and `Friends/ActivateUser`
already resolve elsewhere in this codebase. The `VsParams.Name` fix above
echoed `Target` back *whole*, building
`tenants/<t>/targets/tenants/current/targets/default/vsParams/<id>` — two
nested `targets` segments, which fails the parser's segment-position check
identically to a missing one. Fixed by taking only `Target`'s own last path
segment (`schedule.go`'s existing `lastSegment` helper) rather than the
field verbatim.

With that fix deployed, the console stopped crashing entirely and reached
the plaza -- but "a communication error has occurred" (no error code)
still appeared on entering the lobby, landing in the plaza instead of
looping back to the hall. A ninth VM-side loopback capture, and a
cross-check against two real Nextendo captures already sitting under
`docs/CAPTURES/raw/` from earlier in this project (PolyForm Shield-licensed
Nextendo source and its own real-hardware captures -- facts only, no code
ever taken), pinned it down:

- **`Ugcstore.RunQuery` answering `Unimplemented` — settled, and now
  fixed.** It is the last RPC in the confirmed boot sequence, called right
  after `Locker/SelectDocuments`, and was the only one of the whole
  sequence still genuinely unanswered (`Canola`/`Locker.SelectDocuments`
  were confirmed fine on the wire, `grpc-status: 0`). Both the VM-side
  capture and a real Nextendo capture of a live account (`docs/CAPTURES/raw`'s
  second session) show what a real server answers when a query matches
  nothing: exactly one streamed `RunQueryResponse` carrying only
  `read_time`. SwitchNet keeps no document collections a `StructuredQuery`
  could actually match against, so "zero matches" is this server's honest
  answer, not a placeholder -- `Unimplemented` (grpc-status 12) was the
  actual bug, confirmed independently by Nextendo's own comment
  (`ugcstore_runquery.go`) using the identical shape for the same case.

With `RunQuery` fixed, the console reliably completed the full boot
sequence with no crash and no error, but a *new* "a communication error has
occurred" still appeared some seconds after the hall finished loading, with
nginx's own logs showing four to six `client canceled stream` events fired
in the same instant across every one of the console's long-lived NPLN
streams (`RecvMessage`, `KeepAlive`, `SubscribeMaintenanceSchedules`,
`SubscribeFriendUsers`, `SubscribePresences`), immediately followed by one
raw HTTP 400 on a reopened stream — a proxy-layer rejection, not a gRPC
error, happening before the request ever reached SwitchNet's own Go
backend.

A dedicated mitmproxy capture against SwitchNet itself (the operator's own
console, redirected the same way the two Nextendo captures were, but at
the SwitchNet VM's own IP instead — `docs/CAPTURES/raw/switchnet-hall-20260924T071622Z`)
settled the mechanism directly, at the console's own TLS layer rather than
nginx's summarized logs:

- **`PresenceService.KeepAlive` acking every ping unconditionally — settled,
  and now fixed.** The client resends its next ping the instant it receives
  a reply to the last one; acking immediately, with no real network latency
  between console and server to naturally pace it, makes the exchange spin
  as fast as the round trip allows. The capture measured it directly: 101
  ping/ack round trips in 2 seconds (~50Hz), ending in the exact instant of
  the four-to-six-stream reset burst and the "communication error." Fixed
  initially with a one-second ack drop window. That workaround was itself
  wrong: it discarded the one response the client was waiting for. The
  current implementation delays each reply by at least 50ms without dropping
  any; see `npln-keepalive-ack-pacing-2026-09-24.md`. NextendoNetwork/splatoon-3 measured the same
  underlying mechanism (facts only, not its code): their own comment ties a
  worsened version of this exact loop directly to a faster transport, not a
  fixed client-side interval, matching a real Nintendo server's distance
  self-limiting a behavior SwitchNet cannot assume away.

With the `KeepAlive` throttle deployed, the reset burst stopped: a full wire
trace of the next attempt (07:26:05–07:28:07) showed every request
answered `200`, including the reconnect, `blocks`, `save_data_archives` and
`frontlines`/`penne` calls after it. But the operator reported the exact
same "a communication error has occurred," now appearing *instantly* on
opening the hall — before `IssuePrearrangedUserToken` even fires. That
timing pointed at the very first request of the whole boot sequence,
confirmed already decoded in this same second Nextendo capture:

- **`bcat-topics-lp1.cdn.nintendo.net` (the System news feed) was
  null-routed — settled, and now fixed.** Despite the `bcat-` name, this
  host is not part of the unconfirmed BCAT delivery-cache experiment its
  two siblings (`bcat-list-lp1`, `bcat-data-lp1`) still are — it is a
  separate, already-decoded exchange in `nextendo-second`'s own
  `stream-events.jsonl`: the console's request carries no conditional-GET
  header at all, and the real server answers `304 Not Modified` with an
  empty body regardless, its own permanent "nothing new" answer. SwitchNet
  had this host `ServiceBlackhole`d, so the console's very first request of
  its own boot sequence failed outright. Flipped to `ServiceStubs` with a
  dedicated handler (`internal/stubs.newsTopics`) that always answers `304`
  — checked ahead of the generic `bcat-` prefix case, which would otherwise
  have answered a blanket `200` instead. This is also, independently, why
  the operator never once saw the in-game news screen on SwitchNet.

That fix alone did not clear the error: the missing `TLS: true` flag on the
new host entry silently excluded it from the certificate's SAN list and
nginx's routing table generation, and separately, switchnet-nro's own
bundled hosts file still null-routed the same host client-side -- deploying
to the VM never touches what is already written to a console's SD card.
Both were fixed and a new switchnet-nro release shipped (v0.5.1), but the
operator still hit the identical error afterward. Asked to check how
NextendoNetwork/Prelude routes the same hosts (facts only, not its code)
rather than keep guessing from SwitchNet's own side alone, this turned up
the real remaining gap:

- **`bcat-list-lp1` and `bcat-data-lp1` were also null-routed, and that is
  a real, documented hard failure, not a safe default — settled, and now
  fixed.** NextendoNetwork/Prelude's own commit history documents Splatoon 3
  raising `2122-2403` (module 122 = bcat) when `bcat-list-lp1` was
  unreachable -- a hard error, not silence, the same class of bug as
  `bcat-topics-lp1` above. Their fix, applied to all three `bcat-*` hosts
  uniformly, is the identical unconditional `304 Not Modified` this project
  had already confirmed independently for topics alone. `internal/stubs.newsTopics`
  now answers all three; `internal/bcat.Probe` (the TODO.md §2.4 experiment
  about serving real delivery-cache content) is reached directly by an
  operator on purpose now, not through routing any `bcat-*` host answers
  with day to day. The now-vestigial `switchnetctl hosts -bcat-probe` flag,
  which only ever affected `ServiceBlackhole` hosts, was removed rather
  than left silently doing nothing.

Asked directly to check whether SwitchNet's own hosts file is right by
diffing against Prelude's *entire* current hosts generator, not just the
sections already touched tonight, rather than trust SwitchNet's own
internal cross-checks alone. Two more findings, one real bug and one
scope note:

- **`receive-*.dg/er.srv.nintendo.net` (telemetry) were null-routed on a
  wrong assumption — settled, and now fixed.** SwitchNet's own comment
  claimed "null-routing them is what Prelude-Nro does too" -- checked
  against Prelude's actual current source rather than trusted, and it does
  not. NextendoNetwork/Prelude's own commit history documents a real
  hardware measurement: the NSO/friends applet (`0100000000000816`) waits
  *synchronously* for an HTTP response from these two hosts before loading
  its own main module (`lp1.nso`) -- null-routed, the connection hangs
  until timeout and the applet never gets there, no profile icon handling
  and plausibly more of the applet than that. Fixed with
  `internal/stubs.telemetrySink`: read the body, discard it, answer `200`
  immediately. Telemetry still never reaches real Nintendo.
- **Prelude's `d4c` (system update) hosts are deliberately *not*
  null-routed, unlike SwitchNet's** -- checked, and this is not a bug to
  copy: Prelude's own comment says why ("`nim` stocke un flag persistant"),
  the identical reasoning `internal/nintendo/hosts.go` already gives for
  the opposite choice on this deployment. Prelude's console moves between
  Nintendo mode and Nextendo mode; this deployment's emuMMC is permanently
  and exclusively SwitchNet's (see CLAUDE.md), so the "switch back that
  will never happen" the persistent flag would otherwise corrupt cannot
  occur here. Confirmed intentional, not copied.

---

A later comparison of both working hall captures found one more response
mismatch: `SelectVsParams` carried a stable, quoted server ETag while every
request omitted one. SwitchNet had echoed the empty request ETag. See
[`npln-vsparams-etag-2026-09-24.md`](npln-vsparams-etag-2026-09-24.md).
Both captures also show the client's current time strictly inside the
returned `VsParams` timestamp window, while SwitchNet had returned equal
endpoints; see [`npln-vsparams-window-2026-09-24.md`](npln-vsparams-window-2026-09-24.md).

## Documenting a capture

Add one file per endpoint under `docs/CAPTURES/`, named after the path:

```
docs/CAPTURES/baas-2.0.0-friends.md
docs/CAPTURES/baas-2.0.0-friend_requests.md
docs/CAPTURES/dauth-v7-device_auth_token.md
```

Use the template below. Then reconcile the implementation against it and delete
the matching `TODO(capture-needed)` comment in the same commit, so the source and
these notes cannot disagree.

### Template

````markdown
# <METHOD> <path>

| | |
| --- | --- |
| Host | `e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com` |
| System version | 17.0.1 |
| Title | Splatoon 3 `0100c2500fc20000`, or "system" |
| Captured | 2026-08-25 |
| Tool | mitmproxy 10.x |
| Captured by | <you> |

## What the console was doing

One or two sentences. This is the most useful part of the file: a response
makes sense only in the context of what the console had just done. "Opened the
friends applet with one friend already added, who was in a game."

## Request

```http
POST /2.0.0/friend_requests HTTP/1.1
Host: e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com
User-Agent: libcurl (nnFriends; …)
Accept: */*
Authorization: Bearer <REDACTED>
Content-Type: application/x-www-form-urlencoded
Content-Length: 20

friendCode=…
```

## Response

```http
HTTP/1.1 201 Created
Content-Type: application/json
Content-Length: …

{…}
```

## What this changes

- [ ] Matches the implementation — no change needed.
- [ ] Differs: <what, and which function was changed>
- [ ] Removed the `TODO(capture-needed)` in `internal/baas/<file>.go`.

## Notes

Anything odd. A stray leading `&` in the body, a header in an unexpected order,
a field that is a string where a number was expected. These details are the
reason to write the file down rather than just fixing the code.
````

### Redact these

A capture contains real credentials. Before committing:

| Redact | Why |
| --- | --- |
| Bearer tokens, `device_auth_token`, `application_auth_token`, `idToken` | live credentials for your account until they expire |
| `password` in a `/1.0.0/login` or `/1.0.0/users` response | the device account password, which is the console's permanent credential |
| The console serial number, in `nintendo.sn` | identifies your hardware |
| `cert` and `cert_key` | your game ticket, tied to your console |
| Friend codes, if you would rather not publish them | — |

Keep the *shape*: `"password": "<40 chars>"` is as useful as the real value and
carries none of the risk. A JWT's header and payload are worth keeping in decoded
form, since that is what the implementation is compared against; the signature is
not.

## Setting up a capture

The mechanics are in [`../PACKET_CAPTURE.md`](../PACKET_CAPTURE.md).

## 2026-09-24 boot crash: the Versus schedule resource name

A crash at 13:25:01 UTC (`main` build `28C4287A…`, `nn.npln.Worker`,
`ReturnAddress[04] = main+0x7d2a64`) followed successful `SelectVsSchedules`
and `SelectCoopSchedules` calls on the VM. Disassembly shows `main+0x7d2a60`
calling `AbortImpl` when its call to the parser at `main+0x7d2440`
returns false. That parser requires the literal `tenants` / `targets` /
`vsSchedules` segments. The crash address immediately precedes the separate
CoopSchedule parser at `main+0x7d2a70`; proximity alone had misleadingly
implicated Salmon Run.

SwitchNet sent `tenants/<t>/vsSchedules/<id>`, omitting the target segment.
Both preserved, successful Nextendo captures show a request for
`tenants/current/targets/default` and responses named
`tenants/<t>/targets/default/vsSchedules/<id>`. They also show
`targets/default/coopSchedules/<id>`, while SwitchNet used the shift kind
in the target position. Both response builders now use the last segment of
the requested Target; a wire-serialization regression test checks the names.
No captured schedule content or player identifiers were copied into source.
The fix needs a fresh hardware boot test; a crash at the same generic abort
address would require another wire comparison, not an assumption that the
Versus name remains wrong.
