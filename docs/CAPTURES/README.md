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

### 2. `POST /2.0.0/friend_requests` — the request body

**What is known:** nothing documented. The applet can add a friend two ways —
typing a friend code, and picking somebody the console already knows about.

**What SwitchNet does:** accepts form encoding and JSON, keyed on `friendCode`,
`userId` or `nsaId`. Accepting all of them is cheaper than guessing wrong: a
refused request during bring-up is a dead end with nothing to go on.

**What a capture would settle:** which encoding and which key. Then the others
can be dropped.

**How to capture:** type a friend code into Add Friend on a real console.

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

**Implemented, and nothing in it has touched a console.** Every phase-2 test runs
against a Go client, which was deliberate — it let the work proceed while the
certificate-pinning question was open — but it means no shape here is confirmed
against hardware.

NPLN is gRPC over HTTP/2, so the gRPC half needs `grpc-dump` or `proxide` rather
than mitmproxy, and it does need the console. The JSON half needs neither: see
the first row below.

`switch/README.md`'s
[Verify Splatoon 3, in order](../../switch/README.md#verify-splatoon-3-in-order)
is the console-side sequence, and its first step is the cheapest capture in this
whole document.

### The JSON front end — settle it with one config line, not a proxy

`internal/nplnrest` writes its own transcript. Set `npln.rest_record_dir`, put a
console through one boot, and every request and response lands on disk hexdumped,
in order, with credentials redacted. **No MITM, no patched proxy, no risk.**

That one boot settles three of the four items below at once. It is worth doing
before anything else on hardware.

| Unknown | What the transcript settles | Where it changes |
| --- | --- | --- |
| `gw.hac.lp1.vermillion…` bodies | the device-initialisation exchange. A 2026-08-27 tcpdump showed 16.5 KB exchanged here mid-flow, so it expects real answers | `internal/nplnrest`, `vermillion` |
| `val.hac.lp1.penne…` bodies | the login ticket, and **the field name the frontline FQDN comes back under** — the one value that must be right, because a name the console cannot find leaves it with no frontline at all. SwitchNet currently sends it under four plausible spellings; a capture collapses that to one | `internal/nplnrest`, `penne` |
| `fro-N…` frame format | what the console sends and expects on the persistent connection. SwitchNet holds it open and sends **nothing**, because writing invented bytes into unknown framing is worse than silence | `internal/nplnrest`, `frontline` |

### The gRPC half — needs a proxy and the console

| Capture | What it settles | Where it changes |
| --- | --- | --- |
| `IssueToken`'s response, or any resource name | **the resource-name shape, all of it at once.** Every NPLN resource name is built in one file for exactly this reason: one capture retires the whole set | `internal/npln/names.go` |
| a session outliving its access token | whether Splatoon 3 ever calls `RefreshToken`. SwitchNet returns no refresh token, because the game re-authenticates from its BAAS id token on reconnect — a path that is already served | `internal/npln/services.go` |
| a matchmaking request through to a session | the mode names the game puts in `matchmaking_config`, and the station info each client needs for its ICE handshake. **The mode names are also produced by a log line** — see `npln.mode_players` | `npln.mode_players`, and `AllocateIceServerSet` |
| entering the lobby | `Schedule`'s stage and rule identifiers, and the co-op shift fields — stage, boss, four weapons, reward. All Splatoon 3 internal numbers, which is why they are operator config rather than a table in the source | `npln.rotation`, and `SelectCoopSchedules` |
| the in-game friends list | `Friends.ListFriendUsers` and `PresenceService`, and specifically whether `presence_deliverable` / `presence_receivable` are set. If they are false the client shows no presence and publishes none — a friend list that looks healthy while everybody is permanently offline | not yet implemented |
| a `gamesync` `CreateRound` call | what a `Round` is. Four of its five fields are `Unk1`..`Unk4` in the decompiled definitions, so it is left unimplemented rather than guessed | `internal/gamesync`, `CreateRound` |
| post-match results upload | `GameRecord` — **and with it the battle ratios a Splatfest verdict needs.** Until then `GetFestResult` reports the vote (measured) and marks the battle ratios not valid (not measured) | `internal/toyohr` |

### Splatfests and BCAT

| Capture | What it settles | Where it changes |
| --- | --- | --- |
| `GetFestDecryptionKey`'s response for a fest whose packs are known | **which key field opens which pack.** A seed folder has five packs and the message has six key fields, so at least one field has no pack; the three team fields are certain and `notice`/`start`/`result` are not | `internal/bcat`, `PackForKeyField` |
| the console's requests to the three `bcat-*` hosts | whether serving BCAT is possible at all, and what `/api/nx/v1/list/<name>` is asked for. **The server for this is written and waiting** — flip the hosts to `ServiceStubs` and set `stubs.bcat_record_dir` | `internal/bcat`, `Probe` |
| a from-scratch fest whose internal asset names match its own id | whether the game keys assets on the announced fest id or only on the pack it is handed. `bcat.substitute_fest_id` makes both readings reachable without a code change | `internal/bcat`, `Naming.Apply` |
| the tricolour stage numbering | which stages are valid, and what an invalid one does | `internal/toyohr` |

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
