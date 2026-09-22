# The Nextendo login/link flow, end to end (captured 2026-08-26)

| | |
| --- | --- |
| Hosts | `accounts.nintendo.com`, `api.accounts.nintendo.com`, `m-lp1.baas.nintendo.com` |
| System version | unknown; `nnAccount` SDK 22.2.0, `LoginApplet` `NintendoBrowser/5.1.0.54290` |
| Title | system (`nnAccount` / the login applet) |
| Captured | 2026-08-26 |
| Tool | mitmproxy, decrypted |
| Captured by | the operator, of [Nextendo Network](https://nextendo.network)'s own server |

Second Nextendo capture in this directory, and the first one that runs a whole
Nintendo Account link from the login page to the friend list. That matters more
than any single field below: the previous captures each covered one endpoint,
so nothing before this showed **which endpoints the flow actually visits, in
what order, and which of them it never touches**.

## What the console was doing

Linking a Nextendo account to a real console, from the login applet's Nintendo
Account page through to the friends list. This is the flow `2124-0292` and
`2124-3122` live in, and the one
[`nintendo-account-link.md`](nintendo-account-link.md) was built from.

## The whole sequence, in order

```
GET  accounts.nintendo.com/connect/1.0.0/authorize?…     200  the server's own login page
GET  accounts.nintendo.com/connect/1.0.0/qr/etat?s=…    200  the page polls this; not required by the console
POST accounts.nintendo.com/connect/1.0.0/authorize       302  Location: nintendo://account.nx.sys#
                                                              state=…&session_state=…&code=…&id_token=…
POST accounts.nintendo.com/connect/1.0.0/api/token       200  grant_type=authorization_code
GET  api.accounts.nintendo.com/2.0.0/users/me            200  the Nintendo Account object
POST m-lp1.baas.nintendo.com/1.0.0/application/token     200  grantType=public_client, assertion=<device token>
POST m-lp1.baas.nintendo.com/1.0.0/users                 201  brand-new, empty BAAS user
POST m-lp1.baas.nintendo.com/1.0.0/federation            200  idToken=<NA id_token>, naCountry=FR
POST accounts.nintendo.com/connect/1.0.0/api/token       200  refresh_token
GET  api.accounts.nintendo.com/2.0.0/users/me            200
POST m-lp1.baas.nintendo.com/1.0.0/login                 200  appAuthNToken, isPersistent=false
PUT  m-lp1.baas.nintendo.com/1.0.0/push_channels/<user>/<device_account>  200
GET  m-lp1.baas.nintendo.com/1.0.0/users/<user>          200
GET  accounts.nintendo.com/api/1.0.0/users/<na_id>/qrcode_param           200
POST m-lp1.baas.nintendo.com/1.0.0/devices/snapshot      200
GET  m-lp1.baas.nintendo.com/2.0.0/users/<user>/friends?count=300         200
```

**Three things this settles by itself, without any field-level reading:**

- **The eight unserved `accounts.nintendo.com` paths are not part of this
  flow.** `term_agreement_token`, `term_confirm`, `transfer_na_tokens*`,
  `analytics/device/opt_in` and `%s/profile` — the gap list
  [`nintendo-account-link.md`](nintendo-account-link.md) raised as a suspected
  cause of the post-federation failure — appear **nowhere** in a link that
  succeeds. They are not what a first link calls.
- **`qrcode_param` is called late, after the second `login`, not during
  authorization** — it is a separate step the console takes once it has a
  session, not part of the redirect handshake.
- **Every endpoint above is already routed** by `internal/baas/router.go`, at
  the same paths and methods. Nothing in this flow is a 404 from SwitchNet.

## The `nxAccount` field, from a second account

The field appears five times, in the same five places the 2026-09-13 diff
found, for a **different account on a different server build**:

| Blob | Seen in | bytes 6-21 | 22-25 | 26-29 | 62-65 | 72-77 |
| --- | --- | --- | --- | --- | --- | --- |
| login-shaped | `federation`, `login` (×2) | data | **zero** | data | data | data |
| profile-shaped | `GET /1.0.0/users/<id>` (×2) | data | **zero** | data | zero | zero |

Both are 174 bytes, decode to exactly 174 from 232 characters, start
`d3 5e b4 eb 5e 74`, carry `01 00 01 01` at 46-49, and are zero at 30-45,
50-61, 66-71 and 78-173. Every range the 2026-09-13 diff called *fixed in
both* is byte-identical here, `0x10` at offset 12 included — and, the point
of the capture, **22-25 reads zero in both of this account's values**, which
is what promoted that range from "one account's profile record" to protocol
structure. `nxAccountBlob` reproduced it in the same commit.

The login-only pair is reconfirmed at the same two offsets with entirely
different values, so it stays what the code already calls it: account state in
the copied profile region, not structure, and not reproduced.

**The one thing that is new and not yet explained:** the two values differ for
the *same account* depending on which endpoint serves them. Whatever selects
the head bytes is per-response, not per-account — so a console that validated
them against a derivation of its own account would be validating something a
working server does not itself keep stable.

## What this changes

- [x] Matches the implementation, with one exception — `nxAccountBlob` wrote
  hash bytes across 6-29, contradicting the 22-25 range both accounts agree on.
  Fixed in the same commit, along with `0x10` at offset 12.
- [x] No new `TODO(capture-needed)`; this capture closes a sub-question of the
  existing one rather than opening one.
- [ ] **Still open:** what the head bytes mean, and why they differ per
  response. Neither question is answerable from wire data alone.

## Notes

- Credentials are redacted: the login form's `password`, every Bearer token, the
  device account password, the `nintendo.sn` serial, the refresh/session tokens
  and the account ids are all replaced by shape-preserving placeholders. The
  blob bytes in the table above are described by range, not reproduced — the
  values belong to one real account.
- The QR flow in the login page (`/connect/1.0.0/qr/etat`, `/qr/nouveau`,
  `/qr/refuse`) is Nextendo's own page talking to itself. The console never
  calls those; they are in the capture only because the page's JavaScript does.
- The `302` from `POST /connect/1.0.0/authorize` carries `state`,
  `session_state`, `code` **and** `id_token` in the fragment. That combination
  is what `naAuthorizeSubmit` already builds.
- `/1.0.0/application/token` answers with `accessToken`/`expiresIn`/`tokenType`
  only — no `idToken`, unlike `federation` and `login`, which carry both.
