# The Nintendo Account link flow (`accounts.nintendo.com`)

| | |
| --- | --- |
| Host | `accounts.nintendo.com` |
| System version | 22.5.0 (2250) |
| Captured against | **Nextendo Network**, not real Nintendo — see below |
| Captured | 2026-08-25 |
| Tool | mitmproxy (reverse-proxy mode, own CA) |
| Captured by | np752952@gmail.com |

## This is not a capture of real Nintendo

Everything below was captured with the console pointed at [Nextendo
Network](https://nextendo.network) (via their own homebrew, Prelude), not real
Nintendo's servers. `accounts.nintendo.com` was redirected to Nextendo's real
IP, decrypted with mitmproxy in reverse-proxy mode using GRID0+'s own CA
(already trusted by the console via the CertStore install — no additional
cert-bypass was needed or used).

This matters for how to read the rest of this document: it shows what a
**working, real, community-run private server** does for this flow, not what
real Nintendo's does. GRID0+'s own implementation (`internal/baas/nalink.go`)
deliberately does not try to replicate either one byte-for-byte — see the
package comment there for why, and why that turns out to be fine.

## What the console was doing

Two things were captured together:

1. **A routine background session refresh.** The console's `nnAccount` module
   periodically calls `POST /connect/1.0.0/api/token` with an existing
   `refresh_token` to keep its session alive, for a local user profile
   ("doughston") that was already linked to a Nextendo account from before
   this session.
2. **A silent reauthorization**, `POST /connect/1.0.0/authorize` with
   `prompt=none` — the console asking for a fresh `id_token` without showing
   any UI, using the access token from (1).

**Not captured**: the actual first-time interactive link (typing credentials
into a login page). Repeated attempts at the "Add User → Transfer → no access
to other console → was linked to a Nintendo Account" recovery flow, on a
genuinely fresh local user, never reached the network at all — see the
session notes; that flow may not be the same code path as (1)/(2) above, or
may fail before ever making a network call in this environment. This remains
open.

## Request — the reauthorization (`POST /connect/1.0.0/authorize`)

```http
POST /connect/1.0.0/authorize HTTP/1.1
Host: accounts.nintendo.com
User-Agent: libcurl (nnAccount; 789f928b-138e-4b2f-afeb-1acae821d897; SDK 22.2.0.0; Add-on 22.2.0.0)
Content-Type: application/x-www-form-urlencoded
Authorization: Bearer <REDACTED>

response_type=code+id_token&prompt=none&client_id=f4e5f2f3e022208b&redirect_uri=nintendo%3A%2F%2Flhub.nx.sys&scope=openid+user.basic&state=<REDACTED>&nonce=+
```

## Response — the critical finding

```http
HTTP/1.1 302 Found
Location: nintendo://lhub.nx.sys#state=<REDACTED>&session_state=<REDACTED>&code=<REDACTED>&id_token=<JWT>
```

**`redirect_uri` is a custom URI scheme (`nintendo://lhub.nx.sys`), not an
HTTP redirect.** The console's system software listens for it directly; there
is no webpage on the other end. This was the one piece of this flow that
could not be reasoned about from documentation alone — the wiki lists the
endpoint and its parameters, but not what completing it actually looks like.

The `id_token`'s decoded payload:

```json
{
  "aud": "f4e5f2f3e022208b",
  "c_hash": "<REDACTED>",
  "exp": 1787686933,
  "iat": 1787685433,
  "iss": "https://accounts.nintendo.com",
  "jti": "<REDACTED>",
  "nonce": " ",
  "sub": "00000000deadbee2"
}
```

`sub` is what becomes the "Nintendo Account id" — a value clearly synthesized
by Nextendo's server (not a real Nintendo id), matching exactly the shape
`internal/baas/accounts.go`'s existing `federation` handler already expects:
an unverified `idToken`, read only for its `sub` claim via
`sjwt.ParseUnverified`.

## Request/response — the routine refresh (`POST /connect/1.0.0/api/token`)

```http
POST /connect/1.0.0/api/token HTTP/1.1
Host: accounts.nintendo.com
User-Agent: libcurl (nnAccount; 789f928b-138e-4b2f-afeb-1acae821d897; SDK 22.2.0.0; Add-on 22.2.0.0)
Content-Type: application/x-www-form-urlencoded

grant_type=refresh_token&client_id=6ffd70c434d303c8&code_verifier=<REDACTED>&refresh_token=<REDACTED>&device_authentication_token=<REDACTED>
```

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "access_token": "<JWT>",
  "expires_in": 900,
  "scope": ["openid","offline","napps","urn:oauth:init-sso","user","user.birthday","user.email","user.links","user.links[].id","user.terms","user.screenName","user.loginId"],
  "token_type": "Bearer"
}
```

Two distinct `client_id` values were observed: `6ffd70c434d303c8` for
`nnAccount`'s own background refresh/profile calls, and `f4e5f2f3e022208b`
specifically for the `lhub` (linking hub) redirect flow.

## What this changes

- [x] Confirmed the redirect mechanism GRID0+'s own stub needed to get
      right: a custom URI scheme with the token in the fragment, not an HTTP
      callback. Implemented in `internal/baas/nalink.go`.
- [x] Confirmed the `federation`-compatible id_token shape (`sub` is what
      matters, everything else is decorative). No changes needed to
      `internal/baas/accounts.go`'s existing `federation` handler.
- [x] **Settled 2026-08-27**, by a full decrypted capture of a genuinely
      successful Nextendo link (`docs/CAPTURES/raw/nextendo-working-nalink-2026-08-27.flow`):
      yes, `POST /1.0.0/users` fires automatically, immediately after the
      redirect completes, driven by the `nnAccount` SDK — no console-local
      confirmation step in between. Device account attachment is implemented
      in `accounts.go`'s `federation` handler (`attachToUserID`, verified via
      `s.idVerifier`) — see `internal/baas/nalink.go`'s package comment for
      the current state, and the "A real Nintendo capture" section below for
      what real hardware (not Nextendo) actually contacts during this flow.

## Notes

- User-Agent throughout: `libcurl (nnAccount; <uuid>; SDK 22.2.0.0; Add-on
  22.2.0.0)` — confirms these calls come from the `nnAccount` system module
  directly (`libcurl`), not the browser applet rendering a page.
- `GET /2.0.0/users/me` and `GET /api/1.0.0/users/<id>/qrcode_param` were also
  observed. `qrcode_param` was not implemented at the time this note was
  first written; it is now — see "Full call-by-call verification" below.
- Redacted: the Bearer token, all JWT signatures, `state`/`nonce`/`c_hash`,
  and the device UUID in the User-Agent.

---

## A real Nintendo capture, undecrypted (2026-08-27)

| | |
| --- | --- |
| Captured against | **real Nintendo**, real account, real email 2FA |
| Console | Stock sysMMC, no Atmosphère, no CFW of any kind |
| Tool | plain `tcpdump`, no MITM — genuinely not possible without CFW |
| Flow | System Settings → Add User → Transfer a user from another console → no access to that console → was tied to a Nintendo Account → link account |

The "not captured" gap the first half of this document left open — the actual
first-time interactive link, as opposed to a background session refresh — is
still not captured *decrypted*. It can't be: a stock console validates
certificates for real, and there is no way to add a trusted CA or disable
verification without CFW. What's captured instead is every TLS ClientHello's
SNI (sent in cleartext even over TLS) and per-connection data volume, in
order. Nothing here reveals request or response bodies, which also means
there is nothing in this section that needs redacting — no tokens, no
account details, nothing decrypted at all.

### The host list, in order, with total TCP payload per connection

```
dauth-lp1.ndas.srv.nintendo.net            19.9 KB
accounts.nintendo.com                      93.6 KB   (first contact)
cdn.accounts.nintendo.com                  75.4 KB  7.9 KB  1.45 MB  11.2 KB   (x4, page assets)
accounts.nintendo.com                     195.7 KB  5.8 KB  15.4 KB   (page interaction)
                                           -- ~45s gap: real user typing credentials + email 2FA code --
accounts.nintendo.com                      13.7 KB
api.accounts.nintendo.com                  10.9 KB
m-lp1.baas.nintendo.com                    17.3 KB
cdn-image-e0d67c...baas.nintendo.com       47.2 KB
aauth.hac.lp1.ndas.srv.nintendo.net        10.2 KB   -- mid-flow, not just at boot
m-lp1.baas.nintendo.com                    22.7 KB
god.hac.lp1.penne.srv.nintendo.net          9.9 KB   8.9 KB
cdn-image-e0d67c...baas.nintendo.com       43.8 KB
accounts.nintendo.com                      28.2 KB
capi.lp1.op2.nintendo.net                   6.0 KB
accounts.nintendo.com                      16.6 KB
api.accounts.nintendo.com                   4.3 KB
m-lp1.baas.nintendo.com                    31.3 KB
gw.hac.lp1.vermillion.srv.nintendo.net     16.5 KB   -- see below
accounts.nintendo.com                      13.7 KB
god.hac.lp1.penne.srv.nintendo.net          7.7 KB
atum.hac.lp1.d4c.nintendo.net             1.06 MB   (system update check, unrelated)
storage.hac.lp1.scsi.srv.nintendo.net       9.8 KB  12.2 KB   (cloud save, unrelated)
e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com   25.1 KB   -- the MAIN baas host, separate from m-lp1
cdn-image-e0d67c...baas.nintendo.com        0.5 KB   (final)
```

### What this settles

- **`m-lp1.baas.nintendo.com` and the main `e0d67c...baas.nintendo.com` host
  are both used**, not just `m-lp1`. GRID0+'s `nalink.go`/`federation`
  handling was built entirely around `m-lp1`. Nothing observed here proves
  the main host is required for the flow to *succeed* (it could be routine
  presence/friends-adjacent traffic that happens to fall in this window), but
  it's worth keeping in mind if `m-lp1`-only ever needs revisiting.
- **`aauth` gets contacted mid-flow**, not only during the initial device-auth
  chain at boot. GRID0+'s `aauth` service already exists and answers any
  well-formed application-token request regardless of when it's called, so
  this is very likely already handled correctly — noted here because it was a
  genuine surprise, not because anything needs to change.
- **`cdn.accounts.nintendo.com`** (page assets, ~1.5MB across 4 connections —
  by far the single biggest exchange in the flow) and **`god.hac.lp1.penne`**
  are both already accounted for in `internal/nintendo/hosts.go` (blackholed
  and stubbed respectively) from earlier findings. No change needed.
- **`gw.hac.lp1.vermillion.srv.nintendo.net` was the one real gap.** It was
  `ServiceBlackhole` (connection refused outright) on the assumption that it's
  negligible background telemetry. This capture shows it contacted squarely
  mid-flow — between the second `m-lp1.baas` call and the final
  `e0d67c...baas` call — exchanging 16.5KB, not a negligible amount. Changed
  to `ServiceStubs` in the same commit as this writeup. Its actual purpose is
  still unconfirmed — a stub only avoids an outright refusal, it does not
  claim to answer correctly.

### What is still open

**`2124-0292` itself is confirmed fixed on real hardware, 2026-09-20** — see
`internal/baas/accounts.go`'s `nxAccountBlob` and commit `99c9a61`:
`extras.self.nxAccount` needs a fixed 6-byte header (base64 `01606150`,
hex `d3 5e b4 eb 5e 74`) before the console will accept federation at all.
The paragraphs below this note describe the investigation that led there
and are kept for history; they no longer describe the current failure.

**A new symptom past that point, reported 2026-09-20 (not yet capture-confirmed):**
federation now succeeds, the console shows the confirmation screen with a
generated default avatar instead of the profile picture, and pressing
"add to console" reports "linking failed" while still committing an empty
account locally. Two things came out of a first look, before any new
capture:

- A real, separate bug: `config.go`'s `Default()` still set
  `baas.image_base_url` to the pre-`215d8af` shape
  (`.../baas.nintendo.com/images`), while `config.example.yaml` was fixed to
  drop the `/images` suffix in that same commit and never carried forward
  into `Default()`. Fixed in the commit alongside this note, plus a
  `switchnetctl check` warning for the same class of value. This only
  matters if the operator's own `config.yaml` (gitignored, not visible from
  here) either omits `image_base_url` or still carries the pre-`215d8af`
  value — unconfirmed either way without seeing that file.
- The default avatar itself may be a red herring: `ensureDefaultAvatar`
  deliberately generates one for any account with no uploaded thumbnail
  (a solid blue-gray square, `RGBA{0x4a,0x5a,0x8a}`) — a freshly-linked
  account having no picture yet is expected, not a symptom, *if* that is
  what was actually shown. Whether the console displayed that square or its
  own placeholder silhouette (the fetch never landing) is the discriminator
  and is still open.

Per `naUsersMe`'s own doc comment, the final local commit step (writing
`isNnLinked` and this profile data to System Save Data
`8000000000000010`) is one GRID0+'s server side can influence but never
observe failing — nothing here rules out a capture eventually being needed.
The cheaper first step is nginx's own access log (`docker compose logs
nginx`, already running, no MITM needed) around the failed attempt: the
documented working tail after federation is token refresh →
`GET /2.0.0/users/me` → image fetch(es) → `GET /api/1.0.0/users/{id}/qrcode_param`,
and where that sequence stops in the log names what the console rejected.

---

`2124-0292` has persisted through every fix found by comparing GRID0+'s
live traffic against the working Nextendo capture, including: the
`vermillion` gap above, a `response_type` regression that wrongly omitted
`id_token`/`session_state`, `access_token`/`refresh_token` being signed with
the wrong issuer's key, and `refresh_token` being a JWT instead of the
opaque 64-character value real captures show. Each was verified fixed via a
fresh live decrypted capture; the error is unchanged. `federation` returns a
fully-correct 200 (checked field-by-field against the working capture) and
then there is *zero* further network activity before the console shows the
error — the failure, whatever it is, happens client-side on data the console
already has.

The most recent lead, not yet tested on hardware: the working Nextendo
capture's device token carries two fields undocumented anywhere and absent
from GRID0+ — `nintendo.x5t#S256` and `nintendo.esv`. See item 9 in
[`README.md`](README.md) for the full writeup. `x5t#S256` (a real SHA-256
thumbprint of the console's own TLS client certificate) is now implemented
and verified end-to-end against the live stack. `esv` is now implemented
too — **settled 2026-09-04** by reading `NextendoNetwork/nx-dauth`'s own
source (source-available; the real, running server behind this capture): it
is `base64url(33 random bytes)`, freshly generated per token, never derived
from anything, so it never actually implicated CLAUDE.md's first ground
rule. Whether the console validates either field locally — as opposed to
their being server-side-only bookkeeping the console never reads — is still
unconfirmed; that question doesn't depend on `esv`'s derivation, only on
whether the *key being present* mattered, which GRID0+'s tokens were
previously failing to do at all. Only a hardware retest of the NA-link flow
settles it.

**`extras.self.nxAccount`'s real shape, settled 2026-09-04** — a genuine,
decrypted, working Nextendo federation response, captured and supplied
directly by the operator (a mitmproxy `.flow`, read with the real
`mitmproxy` library rather than eyeballed): a fixed **174-byte binary
structure, base64-encoded to exactly 232 characters** (174 divides evenly by
3, so no padding). That corrects an earlier, unverified guess elsewhere in
this codebase calling it "a non-base64 alphabet" — decoded, it is ordinary
base64 of opaque bytes. `internal/baas/accounts.go`'s `nxAccountBlob` now
matches this length and encoding; the interior byte layout is still unknown
and is not guessed at (see that function's own comment).

Also tested on real hardware, and directly relevant to whether `nxAccount`
is what is driving `2124-0292`: the operator tried both another real
account's literal captured bytes and unformatted random data in this field,
in separate live federation attempts. **Both still produced 2124-0292** —
the same error already shown above to survive several unrelated, capture-
verified fixes. That is consistent with the console never checking this
field's content at all, but it does not settle the question: a content
check keyed to per-request context (so that even genuine bytes copied from
a *different* request would also read as wrong) predicts the exact same
outcome. Recorded here as a real data point either way, not a conclusion —
see `nxAccountBlob`'s `TODO(capture-needed)` for what would actually settle
it.

The raw `.pcap` is not committed (nothing decrypted, but it is still real
traffic timing/metadata tied to a real account) — see
`docs/CAPTURES/raw/` and its own redaction notes in `docs/CAPTURES/README.md`.

---

## External research pass (2026-09-03) — no hardware, wiki + repo cross-check

Before this pass, `CalculateNintendoAccountAuthenticationFingerprint` and a
few related IPC names (`IOAuthProcedureForNintendoAccountLinkage`,
`IManagerForSystemService::EnsureIdTokenCacheAsync`/`LoadIdTokenCache`) had
only been checked against this repo's own captures. This pass went wider,
cloning [kinnay/NintendoClients' wiki](https://github.com/kinnay/NintendoClients/wiki)
(`.wiki.git`, since search-engine summaries of it kept paraphrasing rather
than quoting) and reading it in full, then cross-checking every claim against
`internal/baas` and `internal/jwt`.

**Confirmed against GRID0+, not just plausible:**

- `/1.0.0/federation`'s and `POST /1.0.0/users/{id}/link`'s parameter shapes
  match the wiki exactly, including the two being genuinely separate
  endpoints (login-time link vs. link-onto-an-existing-session) —
  `router.go`/`accounts.go` already implement both correctly.
- The real OAuth scope string the console requests (`openid offline napps
  urn:oauth:init-sso user user.birthday user.email user.links
  user.links[].id user.loginId user.screenName user.terms`) — `naScopeIDs` in
  `nalink.go` already has every one of these mapped.
- The BAAS id_token's real header/payload shape (`alg: RS256`, `jku` →
  `/1.0.0/certificates`, `sub`/`bs:did` as `%016x`, `aud: ed9e2f05d286f7b8`
  fixed, `typ: id_token` fixed) — matches `internal/jwt` as built.
- NPLN access tokens are `ES256`, every other token `RS256` — already the
  case (`internal/jwt/README.md`'s "ES256, and why it is a separate path").
- `m-lp1.baas.nintendo.com` (21.0.0+, mTLS-only, `nnAccount` sysmodule)
  alongside the original host (still used by the friends sysmodule) —
  already both present in `internal/nintendo/hosts.go`.
- `extras.self`'s field list is exactly `playLog` and `nxAccount` — nothing
  else. Confirms the existing `nintendoAccountId`-stripped-from-`extras.self`
  / `nintendoAccountId`-placed-in-`links.nintendoAccount.id` split in
  `models.go` is the documented shape, not a guess.

**Confirmed absent, not just unfound:** a full-text search of every page in
the cloned wiki (not a search-engine summary) turns up nothing for
`fingerprint` in any casing, anywhere. `CalculateNintendoAccountAuthentication
Fingerprint` is not a documented Nintendo internal on the largest public
Switch-server reverse-engineering reference. Its existence, inputs and output
remain an open HYPOTHESIS — this is a stronger negative result than "not
found yet," and it should not be treated as a real Nintendo protocol detail
on the strength of its name alone. The same full-text check found `2124-0292`
and `2124-3122` in none of the wiki's documented module-2124 sub-ranges
(DAuth/AAuth/BAAS/NAS/HTTP all listed exhaustively), which is consistent
with — not proof of — the existing "client-side check" reading above.

**A source that turned out not to be one:** `NextendoNetwork/Prelude-Nro`
(the homebrew used for the 2026-08-25/26/27 Nextendo captures above) bundles
a file, `Switch nnAccount Private Server Setup.txt`, that reads like a
reverse-engineering writeup. It is not one — the inline `[cite: N]` markers
throughout are the signature of an AI-generated research report, and nearly
every citation traces back to the same kinnay wiki pages already read
directly here. Worse, its "Known Client IDs" table (`75fe236362ff5f8b` for
"Nintendo Account Applet" and five others) does not appear anywhere in the
wiki page it cites as the source — compare against the real table in
`Account-Server-(Switch).md#known-client-ids`, which lists a completely
different five values (`6ffd70c434d303c8` NxAccount, `f4e5f2f3e022208b`
NxLHub, etc. — the ones already used throughout this document). Treat that
`.txt` file as unreliable for anything not independently verified elsewhere;
it added no primary information this pass and its one falsifiable table
turned out to be fabricated.

**Net effect on this repo's code: none.** Nothing found this pass contradicts
the current implementation — most of it is independent confirmation of
choices already made. The `nxAccount` byte format and
`CalculateNintendoAccountAuthenticationFingerprint` remain exactly as open as
before: unresolvable without a capture, and per CLAUDE.md's ground rule
against inventing a plausible-looking Nintendo internal, `accounts.go`'s
placeholder (`nxAccount = naID`) stays as it is rather than being replaced
with a guess dressed up by this pass's research.

---

## Full call-by-call verification against the working capture (2026-09-04)

Following the `nxAccount` fix above (same capture,
`53eb8af0-nextendoworkingnalink20260827.flow`, read with the real `mitmproxy`
library), every one of the 61 HTTP exchanges in that flow was walked
individually and compared against GRID0+'s implementation — not just the
`federation` call already covered.

**57 of 61 already matched.** 36 of those 61 are `GET
/connect/1.0.0/qr/etat?s=...` polls with `User-Agent: NintendoBrowser` — the
same UA as the login page itself, i.e. the page's own JavaScript polling for
a QR-login-on-another-device state change, not a call the `nnAccount`/`libcurl`
system module makes. GRID0+'s own login page never triggers this family of
call, so it is not a gap. The rest (`/connect/1.0.0/authorize` classic
submit, `/connect/1.0.0/api/token` both grant types across 7 repeated calls,
`/2.0.0/users/me`, `/1.0.0/application/token`, `/1.0.0/users`, `/1.0.0/login`,
`/1.0.0/users/{id}`, `/2.0.0/users/{id}/friends`) were checked field-by-field
and already matched the existing implementation.

**Four real gaps were found and fixed, all in this commit:**

1. **`PUT /1.0.0/push_channels/{uid}/{daid}`.** Previously answered a bare
   204 via the generic `acknowledge` handler. The capture shows a real
   server answers 200 with a JSON body echoing every field the console PUT
   (`service`, `deviceToken`, `deviceAttributes`) plus
   `createdAt`/`updatedAt`/`userId`/`deviceAccountId` and one key never in
   the request, always empty in the capture: `userAttributes: {}`. Called
   with the authenticated user's own access token (`bs:grt:2`, confirmed by
   decoding the JWT), matching `s.authenticated()`. Implemented in
   `internal/baas/devices.go`'s `updatePushChannel` — no persistence added,
   since nothing in GRID0+ sends push notifications yet; only the
   response shape changed.

2. **`POST /1.0.0/devices/snapshot`.** Same issue: previously a bare 204.
   The capture shows `{"deletedUserIds": [...], "persistentUserIds": [...]}`
   — the requested `userIds` split by whether each still names a real,
   non-deleted user. Called with the anonymous application-level token
   (`bs:grt:1`, confirmed by decoding the JWT), matching `s.anonymous()`.
   Implemented in `internal/baas/devices.go`'s `devicesSnapshot`, using
   `db.User.Deleted` — data the store already has, not a guess.

3. **`GET /api/1.0.0/users/{id}/qrcode_param`** (note the `/api/1.0.0/`
   prefix, distinct from `/1.0.0/` — not a typo in the capture). Previously
   unimplemented; no route existed at all. Response:
   `{"exp": <unix>, "param": "<url>", "userId": "<id>"}`. Decoded, `param` is
   a plaintext URL: `https://accounts.nintendo.com/qr_verify?sig=...
   &iat=...&aud=...&sub=...&exp=...` — `exp - iat` is exactly `604800`
   seconds (7 days) in the capture. Implemented in
   `internal/baas/nalink.go`'s `naQRCodeParam`, matching that shape and
   duration; `sig` is an honest random placeholder rather than a forged
   signature — GRID0+ does not hold Nextendo's (or Nintendo's) private
   signing key and per CLAUDE.md's ground rule 2 does not pretend to.
   Whether anything ever calls `qr_verify` back is unconfirmed — see the
   `TODO(capture-needed)` on `naQRCodeParam`.

4. **A second, previously undocumented `POST /connect/1.0.0/authorize`
   sub-flow.** Every other capture of this endpoint uses a
   username/password submit; this one instead carries
   `Authorization: Bearer <access_token>` with no username/password fields
   at all, `prompt=none`, and a *different* `client_id`
   (`48d6d1f020427ad7`, `redirect_uri=nintendo://e-license.nx.sys` —
   e-license/eShop related) than the `lhub` linking flow documented above.
   It silently re-authorizes the existing NA session for a new client
   rather than starting a new login. GRID0+'s `naAuthorizeSubmit` had no
   handling for this shape at all: a request with neither field would have
   fallen through to "Wrong username or password", which is wrong for a
   request that was never trying to supply either. Implemented as a branch
   in `naAuthorizeSubmit` that, when both fields are empty and a `Bearer`
   token is present, verifies that token via the existing `s.idVerifier`
   (the same verifier `federation` already trusts) and re-issues the
   redirect for the new `client_id` using its subject; an invalid or
   expired token now fails with a `prompt=none`-appropriate redirect
   fragment (`redirectNAOAuthError`, per the OAuth spec's own rule that
   `prompt=none` must fail through the same redirect mechanism a success
   would use, never a login page) rather than a page a console making this
   call would never render.

All four are additive: nothing about the existing username/password
authorize path, the federation handler, or any other already-verified call
changed.

---

## `nxAccount`'s interior bytes — partial structure recovered (2026-09-13)

Same capture as above (`nextendo-working-nalink-2026-08-27.flow`), reanalyzed
from scratch: every flow's raw body (not just the ones already known to carry
JSON worth parsing), including the one non-JSON, non-error body in the whole
file — flow 0, the 13,569-byte WebView login page — checked for anything
bearing on `nxAccount`.

**Correction to the working assumption up to this point:** it had been said,
here and to the operator, that a second capture from a different account was
the only way to get comparative data on this field. That was wrong. The field
appears **five times in this one capture**, not once — the earlier pass only
ever opened the `federation` response (flow 42) and stopped. Rereading every
flow's raw bytes for the literal string `"nxAccount"` (regex over the body,
not JSON-path walking, so it can't miss an occurrence in a differently-shaped
response) finds it in:

| flow | call | value |
| --- | --- | --- |
| 42 | `POST /1.0.0/federation` | A |
| 45 | `POST /1.0.0/login` | A |
| 55 | `POST /1.0.0/login` | A |
| 47 | `GET /1.0.0/users/{self id}` | B |
| 58 | `GET /1.0.0/users/{self id}` | B |

All five are the same account, all within the same ~7-second window of the
capture. Decoded, there are exactly **two distinct 174-byte values**, not
five — the three login-shaped calls (`federation`, `login` × 2) share one
value byte-for-byte; the two profile-read calls (`GET /users/{id}`) share a
different one. That is real differential data for a single account, obtained
without a second capture, and it settles more than the earlier brute-force
search could:

**Stable across both values** (so: not per-account content, and not
login-vs-profile context either — same in both):
- offsets `0–5` (6 bytes)
- offset `12` alone (1 byte), sitting inside an otherwise-varying span —
  checked explicitly since it looked like it might be noise; it is not, it
  reads `0x10` in both values
- offsets `22–25` (4 bytes, all zero in both)
- offsets `30–61` (32 bytes), which includes a 4-byte marker at `46–49`:
  `01 00 01 01`, identical in both values
- offsets `66–71` (6 bytes, all zero in both)
- offsets `78–173` (96 bytes, all zero in both) — the zero tail already
  known from the single-sample pass, confirmed to be genuinely fixed and not
  an artifact of the one account sampled before

**Varies between login-shaped and profile-read responses, same account:**
- offsets `6–11` and `13–21` (6 + 9 = 15 bytes, split around the stable byte
  at 12)
- offsets `26–29` (4 bytes) — this is where an earlier pass floated a
  timestamp hypothesis (a big-endian read landing ~512s from
  `nicknameUpdatedAt`). **That hypothesis is retracted.** A field that moves
  between two responses issued seconds apart, for the same account, in the
  same session, cannot be a slowly-changing timestamp; whatever it is, it is
  keyed to something that differs between a login call and a profile read,
  which a last-modified time is not.
- offsets `62–65` and `72–77` (4 + 6 = 10 bytes) — populated
  (`01 28 28 28` / `01 9f 27 d3 15 1e`) only on the three login-shaped
  responses; zero on both profile-read responses. This is the cleanest
  signal in the whole blob: whatever these ten bytes encode, the server only
  computes them when issuing a login/federation result, not when just
  reading a profile back.

Put together, the blob looks like: a ~6-byte fixed header, ~24 bytes of
opaque content that changes with request context rather than time, a fixed
`01 00 01 01`-anchored structural region, a login-only ~10-byte suffix, and a
96-byte zero pad — not a hash of anything, and not the near-uniform noise
GRID0+'s current placeholder produces (see below).

**This also explains, rather than merely extends, the earlier negative
result.** The prior pass tried every scalar value visible anywhere in the
capture (identifiers, timestamps, every raw secret and JWT claim once the
account's deletion made that safe to include, ~340,000 value/transform and
HMAC combinations total) against the blob and found nothing. That search
could only ever have found the varying region if it happened to equal
something else already present in plaintext. Now that the region is known to
change between two calls carrying *no new client-supplied data* (same
Authorization token, same account, same session), it is server-side state —
something like a per-issuance nonce, a request-context tag, or a
server-held counter — not a deterministic function of anything the client
sends or the operator's capture could show. The negative result was correct;
it was answering a question ("is this derivable from visible inputs?") whose
answer is now known to be no, rather than just unproven.

**Flow 0 — the only actual "code" in the capture — is a dead end for this
question, and worth saying so explicitly** per the instruction to check it.
It is the WebView login page: French-commented HTML/CSS plus one inline
`<script>` block implementing the QR-login state machine (poll
`/connect/1.0.0/qr/etat`, animate between "show QR" / "scanned, enter these
four digits" / "confirm on console" states, `/connect/1.0.0/qr/nouveau` on
expiry). It contains no reference to `nxAccount`, no hashing, no encoding
logic of any kind — it is UI code for an entirely different part of the
login flow (device-to-device QR pairing) and has nothing to do with how the
BAAS response field is built server-side, which happens after this page has
already done its job. The only other non-JSON body in the capture (flow 10,
a 30-byte plaintext `502` error) was already known and is unrelated. There is
no other "code" in this capture to check.

**Net effect on `internal/baas/accounts.go`'s `nxAccountBlob`:** the length,
encoding, fixed header, `01 00 01 01` marker, and 96-byte zero tail are now
confirmed structure, not guesses, across two independent samples of the same
account. The two varying regions (client-context-independent but
call-context-dependent, and login-only) are not reproducible from anything
GRID0+ has available when building this field — there is no
"per-issuance nonce" GRID0+ is tracking, and inventing one would be
exactly the "plausible-looking Nintendo internal" CLAUDE.md's ground rule 6
says not to do. Whether to update the placeholder to reproduce the *now-known*
fixed structure (header/marker/zero-tail) while still leaving the varying
regions as opaque per-account filler, as it does today, is a decision left to
the operator rather than made here — see `nxAccountBlob`'s own comment and
`TODO(capture-needed)`.

A capture from a **second, different account** is still valuable and not
superseded by this — it would separate what's fixed *per account* (this pass
already shows offsets `0–5`/`22–25`/`30–61`/`66–71`/`78–173` are fixed
*within* one account across five calls) from what's fixed *across all
accounts* (unknown; every byte checked so far could still be per-account
constant rather than protocol-constant, since only one account has been
observed). It is a refinement of the picture above, not the only way to make
further progress on it, which is what was said to the operator before this
pass — that was inaccurate and is corrected here.

## The header offset was wrong, and hardware finally moved past 2124-0292 (2026-09-21)

Two things happened the same night. In order:

**First, a transcription bug found.** The 2026-09-20 decompile writeup above
this section (in `accounts.go`'s comment, not this file) recorded the
console's own header check as `*(long *)(param_1 + 0x21) != 0x3035313630363130`
— offset **0x21 (33)**, 8 bytes, ASCII `"01606150"`. What actually shipped in
`nxAccountBlob` was 6 unrelated non-ASCII bytes at offset **0**. Neither the
offset, the byte count, nor the content matched what the same comment's own
math already said. It shipped like that for a full day without anyone
re-deriving the header from the stored constant and comparing.

**Second, real hardware got past `2124-0292` for the first time** (via an
unrelated fix, not this one), then immediately hit a new, different failure:
a generic "account couldn't be linked" dialog, no error code, appearing only
*after* every single server request in the sequence — including
`federation` — had already returned success. Re-examining the account
sysmodule to chase this new failure is what caught the transcription bug:
tracing `FUN_71000f0ee0`, the function that *builds* the structure the
header check validates, shows it writing a NUL-terminated string (believed
to be `naID` itself — see the caveat below) at offset 0, then the confirmed
8-byte header immediately after at offset **0x21**, then continuing the rest
of the structure at offset **0x29** (33 + 8 — the decompiled code computes
this sum directly, it is not an assumption here). This is the same offset
the original gate-check comment already named; two independent decompile
passes, on two different nights, agree.

**This reframes the "offsets 0-5 fixed" finding from the 2026-09-13 capture
analysis above** — not as evidence for a 6-byte header at offset 0 (that
interpretation is now known wrong), but as exactly what a per-account,
NUL-terminated ID string would look like sampled from five calls that all
belonged to the same one account: trivially identical across all five, for
a completely mundane reason. It does not, on its own, confirm the leading
string really is `naID` and not some other field of the account-cache
record `FUN_71000f0ee0` reads from — that identification is still a
decompile-shape inference, not a proven one.

**Fixed in `nxAccountBlob` this same night**: the header is now 8 correct
ASCII bytes at the correct offset 0x21, preceded by `naID` (NUL-padded to
33 bytes) instead of appended after a wrong 6-byte header at offset 0. The
96-byte zero tail (offsets 78-173, confirmed by the 2026-09-13 capture
above) is now actually zero instead of continuing the SHA-256 stretch over
it — a second, independent bug this same pass caught: the confirmed-zero
region was being silently overwritten by hash bytes the whole time.

**What is still open, unchanged in kind from before**: everything from
offset 0x29 (41) to 77 (37 bytes) remains an unconfirmed SHA-256-derived
placeholder, repositioned but not newly justified. Whether the leading
string is really `naID`. And — completely untouched by tonight's fix —
`FUN_71000faed0`, the *other* function the original gate runs immediately
before the header check, whose `-noanalysis` decompile still only recovers
an unresolved register value and has not been meaningfully examined. If the
console is still rejecting the link after this fix reaches hardware, that
function is the next place to look, not another guess at this blob's
remaining unconfirmed bytes.

## The offset-0x21 fix regressed 2124-0292 on hardware; reverted same night (2026-09-21, continued)

The fix described immediately above this section was deployed and directly
regressed federation on real hardware within the same test session — 2124-0292
came back, having been confirmed gone with the header at offset 0 minutes
earlier. Reverted byte-for-byte to the working configuration (header at
offset 0, original 6-byte content). See `internal/baas/accounts.go`'s
`nxAccountBlob` comment for the reverted state and the "do not retry without
fresh hardware confirmation" warning now attached to it. A second retry
immediately after the revert confirmed 2124-0292 stays gone — this isn't a
fluke, the revert genuinely holds.

**Directly re-verified against the raw capture, not just the old writeup's
summary** (`docs/CAPTURES/raw/nextendo-working-nalink-2026-08-27.flow`,
read with mitmproxy's own `FlowReader`): the header bytes really are `d3 5e
b4 eb 5e 74` — exactly what shipped both before and after tonight's
regression-and-revert. This also settles where the `"01606150"` ASCII
theory went wrong: that string exists in `account_main.nso`'s `.rodata`
(confirmed, still there) but is unrelated to this field — a coincidence,
not evidence. Searched for the *real* header bytes (`d35eb4eb5e74`) as a
literal sequence anywhere in `account_main.nso`'s decompressed memory:
**zero hits.** Whatever validates this header, it is not a hardcoded
byte-literal comparison anywhere in the binary — ruling out the two most
tractable hypotheses in one night. The check exists (changing these bytes
demonstrably changes behavior), but finding it statically needs a different
approach than searching for a constant.

**New structural detail from the direct capture comparison**, beyond what
the earlier 2026-09-13 writeup already established: `profile`'s last
non-zero byte is at offset 49; `login`'s is at offset 77. Only 29 of 174
bytes differ between the two values for the same account. The real blob is
overwhelmingly zero padding around a small structured region — the opposite
shape from GRID0+'s placeholder, which fills the *entire* remainder with
dense SHA-256 output. This was already implied by the old "96-byte zero
tail" finding but is worth stating plainly: GRID0+'s placeholder isn't
just missing a few confirmed fields, its general shape (dense pseudorandom
bytes vs. mostly-empty structured record) doesn't resemble the real thing
at all past the header and length.

## A new, independent lead: `$.isNnLinked` (2026-09-21)

While chasing the current "account couldn't be linked" dialog (which
appears with zero further network activity after a fully successful HTTP
sequence, including `federation` — see the 2124-0292-hardware-confirmed
entry earlier in this file), searched `account_main.nso` for the literal
string `isNnLinked`, since `naUsersMe`'s own doc comment (`nalink.go`)
already names it as part of "the final local commit step... System Save
Data `8000000000000010`" that this server can influence but never observe
failing.

Found two occurrences, both as `$.isNnLinked` — a JSONPath expression, not
a plain field name — sitting inside one large table of many distinct,
NUL-separated path strings in `.rodata` (confirmed by reading raw context
around the hits: dozens of unrelated JSONPath/JSON-Pointer expressions for
other fields sit immediately before and after it in memory, back to back).
**GRID0+ never sends a field called `isNnLinked` anywhere** (checked:
zero occurrences of `isNnLinked`/`IsNnLinked` in the whole Go codebase
outside of this one doc-comment mention).

**Not yet confirmed, and this is exactly where this lead currently stops**:
whether the console evaluates `$.isNnLinked` against a *server response*
(which would make this directly actionable — add the field) or against its
*own local save data* when later checking link status (which would make it
irrelevant to the failure happening during the link itself). Tried to trace
which function actually evaluates this specific path by finding code
cross-references to it and to the ADRP-page it lives on; both approaches
failed to narrow it down -- the containing page is a shared string pool
referenced by 236+ different functions throughout the binary, and
`-noanalysis` mode's xref database has nothing recorded for the exact
string address (likely because the load is computed via ADRP+ADD without
the Reference analyzer's pass having run over it specifically). Full
analysis is available in the project (`account_main.gpr`) and might resolve
this with more targeted work — following the JSONPath *evaluator* function
itself (a single generic function almost certainly used for every path in
this table, rather than one direct reference per string) would be a more
tractable next angle than more xref-hunting on individual string addresses.

**What this means practically**: don't add an `isNnLinked` field to any
GRID0+ response speculatively based on this alone — the causality isn't
established, and CLAUDE.md's rule against inventing plausible-looking
Nintendo internals applies exactly as much to "add a field because its name
sounds relevant" as to guessing byte values. This is a lead worth a capture
or a deeper decompile pass, not a fix to deploy yet.
