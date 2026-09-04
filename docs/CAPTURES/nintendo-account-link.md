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
IP, decrypted with mitmproxy in reverse-proxy mode using SwitchNet's own CA
(already trusted by the console via the CertStore install — no additional
cert-bypass was needed or used).

This matters for how to read the rest of this document: it shows what a
**working, real, community-run private server** does for this flow, not what
real Nintendo's does. SwitchNet's own implementation (`internal/baas/nalink.go`)
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

- [x] Confirmed the redirect mechanism SwitchNet's own stub needed to get
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
  are both used**, not just `m-lp1`. SwitchNet's `nalink.go`/`federation`
  handling was built entirely around `m-lp1`. Nothing observed here proves
  the main host is required for the flow to *succeed* (it could be routine
  presence/friends-adjacent traffic that happens to fall in this window), but
  it's worth keeping in mind if `m-lp1`-only ever needs revisiting.
- **`aauth` gets contacted mid-flow**, not only during the initial device-auth
  chain at boot. SwitchNet's `aauth` service already exists and answers any
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

`2124-0292` has persisted through every fix found by comparing SwitchNet's
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
from SwitchNet — `nintendo.x5t#S256` and `nintendo.esv`. See item 9 in
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
whether the *key being present* mattered, which SwitchNet's tokens were
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

**Confirmed against SwitchNet, not just plausible:**

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
individually and compared against SwitchNet's implementation — not just the
`federation` call already covered.

**57 of 61 already matched.** 36 of those 61 are `GET
/connect/1.0.0/qr/etat?s=...` polls with `User-Agent: NintendoBrowser` — the
same UA as the login page itself, i.e. the page's own JavaScript polling for
a QR-login-on-another-device state change, not a call the `nnAccount`/`libcurl`
system module makes. SwitchNet's own login page never triggers this family of
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
   since nothing in SwitchNet sends push notifications yet; only the
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
   signature — SwitchNet does not hold Nextendo's (or Nintendo's) private
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
   rather than starting a new login. SwitchNet's `naAuthorizeSubmit` had no
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
