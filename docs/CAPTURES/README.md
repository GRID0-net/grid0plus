# Captures

Where to put packet captures from your own consoles, and what SwitchNet still
needs one for.

Every guess in the implementation is marked in the source with
`TODO(capture-needed):` and says what a capture would settle and which single
function to change. `grep -rn 'TODO(capture-needed)' internal/` is the full list.

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

**What a capture would settle:** which encoding and which key. Then the others
can be dropped.

**How to capture:** type a friend code into Add Friend on a real console.

**Related, separately guessed:** the block-request body
(`TODO(capture-needed)` at `internal/baas/friendcodes.go:185`) accepts the
same form/JSON, `userId`/`nsaId` ambiguity, for the same undocumented reason —
blocking a user on a real console would settle this one independently of the
friend-request body above, since nothing confirms the friends sysmodule uses
the same encoding for both actions.

**Also open:** the per-item shape of the inbox/outbox this request populates
(`TODO(capture-needed)` at `internal/baas/models.go:187`, `FriendRequestResponse`)
— captures 04 and 05 both returned empty item lists, so `id`/`from`/`to` are
SwitchNet's best guess at the minimum a console needs to draw the "friend
requests" screen, not a confirmed shape. A capture with a real pending request
in the list (send one from a second console and capture the first one's inbox
before accepting) would settle it.

### 3. `PATCH /2.0.0/friend_requests/<id>` — the body

**What SwitchNet does:** accepts a JSON patch on `/state`, a plain
`{"state":"ACCEPTED"}`, and `{"accept":true}`.

**How to capture:** accept a friend request on a real console.

### 4. `GET /1.0.0/users` — the friends-module lookup

The wiki lists this as a friends-module endpoint but documents neither parameters
nor response. The applet has to turn a typed friend code into a user somehow, and
this is the only endpoint that plausibly does it.

**What SwitchNet does:** accepts `?friendCode=`, `?nickname=` and `?ids=`, and
returns an array of stranger-visibility user objects. An unfiltered query is
refused, because it would otherwise enumerate every account on the server to any
authenticated console.

It is possible the code is resolved inside `POST /2.0.0/friend_requests` instead,
which SwitchNet also supports; in that case this endpoint stays as a convenience
for tooling.

### 5. `GET /2.0.0/users/<id>/relationships/<id>`

**What SwitchNet does:** returns `{"type": …}` with `NONE`, `FRIEND`,
`FRIEND_REQUEST_SENT`, `FRIEND_REQUEST_RECEIVED` or `BLOCKED`, modelled on the
console's own `nn::friends` relationship enum.

### 6. `bs:sts` — the opaque status array in BAAS tokens

Nintendo sends `[385]` in an access token and a seven-element array in a user
token. Nothing on the console appears to read them and SwitchNet reproduces the
observed values verbatim.

**What a capture would settle:** whether they encode account state. A user token
from a console whose account is banned, or has no membership, would show it.

### 7. `capi.lp1.op2.nintendo.net` — the membership response

Stubbed with `{"active": true, "expiresAt": …}`. With
`aauth.online_play_policy: FREE` (the default) the console never asks, which is
why this has not blocked anything.

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
blob, now shape-correct (174 bytes, base64-encoded — settled 2026-09-04 from
a real captured federation response the operator supplied directly) but
still not a claim about its interior content, which two real-hardware tests
(another account's literal captured bytes; unformatted random data) suggest
but do not prove the console never checks — see
[nintendo-account-link.md](nintendo-account-link.md)'s "What is still open"
for the detail and why that test does not fully settle it. A 2026-09-03 pass
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
- **The matched-session security token**
  (`internal/npln/matchmaking.go:394`, `matchedUserSessions`): whether
  Splatoon 3 refuses to start a match when this is absent. If it does, it
  needs to become a real token signed with the same key `IssueToken` uses.
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
- **`PresenceService.KeepAlive`'s `UpdatePresence` field**
  (`internal/npln/presence_service.go:43`): whether Splatoon 3 ever reports
  richer, game-specific presence over this RPC rather than only through
  BAAS's `PATCH .../device_accounts/<id>`. SwitchNet does not persist it --
  giving the one presence store a second writer is the same class of bug as
  the second friend graph ROADMAP §2.5 warns against -- but every message on
  the stream still counts as liveness.
- **Presence resource-name shape**
  (`internal/npln/presence_service.go:231`, `presenceUserID`): follows the
  same `.../users/<u>/presence` singleton convention `resources.proto`'s
  `Presence.name` field implies, parsed leniently for the same reason as the
  matchmaking and friends names above.
- **`Presence.Attributes`' keys**
  (`internal/npln/presence_service.go:245`, `presenceProto`): left empty.
  `resources.proto` carries a `map<string, nn.npln.common.Value>` here, and
  its own `State` enum has no `PLAYING`, which suggests title-specific detail
  (SwitchNet's BAAS-side `AppField`, in spirit) belongs here instead --
  but no capture shows what keys a real client reads, and a guessed key that
  silently never matches is worse than an empty map that visibly does
  nothing.
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
- **Vermillion's two response bodies**
  (`internal/nplnrest/server.go`, `getAccountsConfig`/`getAccountsDevices`):
  the wiki names both endpoints but gives no field table for either, unlike
  every one of Penne's -- both answer with the most honestly-empty JSON
  this server can claim (`{}`, `{"devices":[]}`), not a guessed real shape.
  Whether Splatoon 3's boot needs anything from vermillion at all is a
  separate, recorded question -- see `docs/QUESTIONS.md`.
- **How a console obtains a Penne id and password**
  (`internal/nplnrest/server.go`, `postLoginTickets`): accepted and logged
  without verification (CLAUDE.md's second ground rule), because nothing
  documents the provisioning step that would create one --
  `god.hac.lp1.penne.srv.nintendo.net`'s two plausible endpoints have an
  unconfirmed HTTP method on the wiki, and `god` is not served by this
  package at all.
- **A frontline connection's wire framing**
  (`internal/nplnrest/server.go`, `serveFrontline`): held open, nothing
  sent down it -- confirmed safe (closing it reads as a failure, per a real
  error-code capture already in `internal/nintendo/hosts.go`), but what a
  real server sends once the connection is open is undocumented anywhere
  this pass could find.
- **`FestEntry`'s resource-name shape**
  (`internal/toyohr/fest_entries.go`, `festEntryProto`): follows the same
  `tenants/<t>/fests/<ID>/...` convention `GetFestDecryptionKey` already
  uses, extended with an `entries/<userID>` suffix -- but nothing confirms
  what `CreateFestEntry`'s response actually names a new entry, or whether
  the console parses this field at all rather than only checking the RPC
  succeeded.
- **Whether an all-`is_valid: false` `FestResult` reads as "not concluded"**
  (`internal/toyohr/fest_entries.go`, `GetFestResult`): SwitchNet has no real
  battle-outcome data (`npln_fest_entries` records who picked which team,
  not who won, and `GameRecord` is unimplemented), so every sub-result
  reports `IsValid: false` rather than a fabricated ratio. Whether the
  console reads that as "the fest has not concluded yet" (leaving it
  running, the same as before this RPC existed) or as a valid but empty
  outcome is unconfirmed either way.
- **Whether Gamesync is reached through the tenant channel or a separate
  port** (`internal/gamesync/README.md`): `gamesync.npln.nintendo.net` is
  classified `ServiceNPLN` (grouped with the tenant host) in
  `internal/nintendo/hosts.go`, but it is the only NPLN hostname that does
  not end in `srv.nintendo.net` -- the only thing that reads as a
  deliberate signal, and consistent with more than one explanation. See
  `docs/QUESTIONS.md`.
- **`FieldTransform`'s `LoadGlobalValue`/`StoreGlobalValue`/
  `ClearGlobalValue`** (`internal/gamesyncstore/store.go`, `applyTransform`):
  no Firestore equivalent and no public documentation of what a gamesync
  "global value" is scoped to, or what loading one that was never stored
  should return. Answered as a no-op null result.
- **`DefermentOperation`** (`internal/gamesync/service.go`,
  `logDeferments`): accepted on `WriteDocuments` and `CommitTransaction`,
  logged, and never executed -- nothing found documents what a "deferment"
  defers, or until when.
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

When it is further along, these are the captures that will unblock the rest.
NPLN is gRPC over HTTP/2, so this needs `grpc-dump` or `proxide`, not
mitmproxy, and it does need the console.

| Capture | Why |
| --- | --- |
| game boot, from the very first vermillion request through the NPLN auth handshake | confirms whether vermillion is actually contacted (and needed) for Splatoon 3 specifically, penne's real request bodies and this server's guessed response fields, and what the tenant expects in `authorization`/`uid` metadata once the gRPC channel opens |
| a frontline connection, bytes on the wire after the HTTP response headers | the one thing `internal/nplnrest`'s `serveFrontline` could not implement at all: what, if anything, is sent down an open frontline connection |
| entering the lobby | `Schedule`'s five RPCs (currently answered empty) and `LobbyMessaging`, the first things the lobby draws |
| a matchmaking request, and the session response | the station info each client needs for its Pia/ICE handshake |
| the in-game friends list | `Friends.ListFriendUsers` and `PresenceService`, and specifically whether `presence_deliverable` / `presence_receivable` are set |
| an invite sent while the receiving console is off, then a boot | `Messaging.RecvMessage`'s empty-resume-token behaviour, and its resource-name shapes |
| post-match results upload | `GameRecord` |
| picking a team during a running fest, then the fest concluding | `CreateFestEntry`'s response shape and `GetFestResult`'s real (non-`is_valid:false`) content |
| the DNS/TLS handshake for `gamesync.npln.nintendo.net` specifically, or a `netstat`/firewall log from the console while Gamesync is in use | whether it is really reached through the tenant channel (as `internal/gamesync` currently assumes) or dials a separate address/port |
| any real `WriteDocuments`/`CommitTransaction` call carrying a `DefermentOperation` or a `LoadGlobalValue`/`StoreGlobalValue`/`ClearGlobalValue` transform | what either feature is for, which nothing public documents |

[kinnay/NPLN-Protocols](https://github.com/kinnay/NPLN-Protocols) also ships an
IPS patch generator that disables TLS verification *inside Splatoon 3
specifically* — useful because the game verifies on top of the system, and it is
narrower than the system-wide patch.

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
