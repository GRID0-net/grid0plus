# Architecture

SwitchNet replaces the Nintendo servers a Switch talks to before and during
online play, for a private group of consoles the operator owns.

Phase 1 — this document's subject — is the account and friends layer: everything
needed for the **stock** Nintendo friends applet to work.
[`ROADMAP.md`](ROADMAP.md) covers phase 2 (the NPLN game server for Splatoon 3,
and a dashboard) and phase 3 (the eShop, served from local `.NSP` dumps).

## Contents

- [The three protocol layers](#the-three-protocol-layers)
- [The auth chain](#the-auth-chain)
- [Services](#services)
- [Identity, and the bug this design exists to avoid](#identity-and-the-bug-this-design-exists-to-avoid)
- [Presence, and the other bug](#presence-and-the-other-bug)
- [Data model](#data-model)
- [What is deliberately not verified](#what-is-deliberately-not-verified)
- [Where the knowledge came from](#where-the-knowledge-came-from)

---

## The three protocol layers

A Switch does not have "an online service". It has three, and they are unrelated
to each other:

| Layer | What it does | Protocol | Phase |
| --- | --- | --- | --- |
| Accounts and friends | login, friend list, presence, friend codes | REST over HTTPS | 1 |
| Game servers | matchmaking, lobbies, rankings | NEX/PRUDP (older titles) or NPLN/gRPC (newer) | 2 |
| Gameplay | the actual in-match packets | Pia, peer-to-peer UDP | — |
| Store and content | catalogue, purchases, downloads | REST over HTTPS, plus a web storefront | 3 |

**Gameplay is always peer-to-peer.** Once a match starts the consoles talk
directly to each other and SwitchNet is not in the path. This is why the NAT
check server matters out of proportion to its size: it is what lets the consoles
find each other at all, and it is also why a fast server does nothing for in-match
latency.

## The auth chain

Every online feature goes through this before it can do anything. Each step's
output is the next step's input, which is why a wrong claim in one place fails
several steps later with an unrelated-looking error.

```
 console boots
      │
      ▼
┌───────────────────────────────┐
│ 1. dauth                      │  POST /v{1..8}/device_auth_token
│    "I am a Switch"            │
│                               │  in   client_id, system_version, challenge, mac
│    internal/dauth             │  out  device_token (JWT, RS256)
└──────────────┬────────────────┘
               │  device_token
               ▼
┌───────────────────────────────┐
│ 2. aauth                      │  POST /v{1..5}/application_auth_token
│    "I own this game"          │
│                               │  in   device_token, application_id, ticket
│    internal/aauth             │  out  app_token (JWT, RS256)
└──────────────┬────────────────┘
               │  app_token (optional from here on)
               ▼
┌───────────────────────────────────────────────────────────┐
│ 3. baas                                                   │
│    accounts, friends, presence                            │
│                                                           │
│    POST /1.0.0/application/token   device_token → anon    │
│    POST /1.0.0/users               register               │
│    POST /1.0.0/login               device account → user  │
│    ... the friends API                                    │
│                                                           │
│    internal/baas                                          │
│    out  user access_token  (this API)                     │
│         id_token           (for game servers)             │
└──────────────┬────────────────────────────────────────────┘
               │  id_token
               ▼
┌───────────────────────────────┐
│ 4. game server (phase 2)      │  verifies the id token against
│    NPLN for Splatoon 3        │  /1.0.0/certificates
└──────────────┬────────────────┘
               │  peer addresses
               ▼
┌───────────────────────────────┐
│ 5. Pia, console ↔ console     │  not through any server
│    NAT traversal via natcheck │
└───────────────────────────────┘
```

The three JWT hops are not decoration. The id token is how a game server learns
*which player* is connecting without trusting anything the game says, and it is
verified against a public key set the game server fetches itself. That is the
seam between phase 1 and phase 2, and it is the reason the BAAS access key and
the BAAS id key are separate: an id token handed to a game server must not be
usable as an access token against the player's own account.

## Services

Each is a separate process. They are separate because that is how they are
deployed — an operator can restart the BAAS server without dropping a console's
dauth request — and because it keeps their dependencies honest.

| Service | Port | Reimplements | Needs a database |
| --- | --- | --- | --- |
| `dauth` | 8081 | `dauth-lp1.ndas.srv.nintendo.net`, `dcert-…` | no |
| `aauth` | 8082 | `aauth-lp1.ndas.srv.nintendo.net`, `acert-…` | no |
| `baas` | 8083 | `*.baas.nintendo.com` | yes |
| `stubs` | 8084 | ctest, frs, five, op2, dragons, penne, npns, znc, … | no |
| `natcheck` | 10025/10125 UDP | `nncs{1,2}-lp1.n.n.srv.nintendo.net` | no |

`dauth` deliberately has no database connection. It is the first thing a console
talks to, so keeping it free of a database dependency means a database outage
still lets a console get far enough to see a useful error rather than a timeout.

Everything HTTP sits behind one nginx TLS edge that routes by SNI.
`switchnetctl nginx` generates that configuration from the same host list that
generates the console's hosts file, so the two cannot disagree.

## Identity, and the bug this design exists to avoid

**A request is authorised by exactly one thing: an access token SwitchNet signed,
whose subject is a user that exists and whose device account still belongs to
that user.** There is no default account, no "last console that logged in", and
no lookup that falls back to a global.

That constraint is not theoretical caution. A production deployment of this same
stack — the Switch-facing account server that SwitchNet replaces — resolved a
console by its device account id and, when the lookup missed, fell back to a
process-wide variable holding the last account that had authenticated. The
consequences, from that project's own write-up:

- every console acted as one person;
- they all sent friend requests as him;
- they all displayed **his** friend code;
- regenerating the friend code changed nothing, because the code was never the
  identity.

A console caches its device account in system save `8000000000000010` and
presents it forever, so a console whose credential the server does not recognise
is a completely normal situation — and it is exactly when that fallback fired.

What SwitchNet does instead:

- `POST /1.0.0/login` looks up the device account, compares the password in
  constant time, and returns `invalid_device_account` on any miss. There is no
  other outcome.
- Every authenticated request re-reads the device account and checks it still
  belongs to the token's subject. A token stays cryptographically valid until it
  expires, so without this a deleted console would keep full access for hours.
- Every user-scoped endpoint refuses a caller who is not the user in the path.
- A friend request where sender and recipient are the same is refused. With a
  broken identity layer, "add a friend" becomes a self-request surprisingly
  often, and the resulting graph is very hard to read back.

`internal/baas/accounts_test.go` has a test per bullet. They are the most
important tests in the repository.

## Presence, and the other bug

The same write-up reported that friends were "never online". Two causes, and
SwitchNet's read path is shaped by both.

**A reported state expires.** The console reports `PLAYING` when a game starts
and does not report again on a timer. Without a TTL, a console switched off
mid-game shows as playing forever.

**Request traffic is proof of life.** The console reports *nothing* while it sits
in the Home Menu — which is exactly where players look at each other's status —
but it keeps polling its friend list. Treating that traffic as an `ONLINE`
heartbeat is what makes an idle console visible at all.

So the read path takes the strongest fresh signal:

```
fresh report?          →  use it, exactly as reported
                          (including OFFLINE: a console reporting that is
                           signing out, and the heartbeat must not override it)
otherwise, seen recently?  →  ONLINE, with the app fields cleared
otherwise                  →  OFFLINE
```

Two details that are easy to get wrong and were both found by running it:

- **"never reported" is not "reported OFFLINE".** A new account's presence row
  has a NULL `reported_at`. Defaulting it to `now()` makes a brand new user look
  like it just reported OFFLINE, which suppresses the heartbeat and reproduces
  the original bug.
- **The app fields have to be cleared** whenever the effective state is not
  `PLAYING`. A console that closes a game reports `ONLINE` without clearing them,
  so a friend list would keep showing "playing X" for a game that is no longer
  running.

Presence changes are also published to `internal/presencebus`, so phase 2's
dashboard and NPLN presence service consume events rather than polling. Publish
runs on the request path, so it drops rather than blocks: an event that misses the
dashboard is cosmetic, a friend-list request that hangs is not.

### How the console reports presence

Not through an endpoint you would guess. It JSON-patches the *device account*:

```http
PATCH /1.0.0/users/<user-id>/device_accounts/<device-account-id>
Content-Type: application/json-patch+json

[{"op":"replace","path":"/presence/state","value":"PLAYING"},
 {"op":"add","path":"/presence/extras/friends/appField","value":"{…}"},
 {"op":"add","path":"/presence/extras/friends/appInfo:appId","value":"0100c2500fc20000"},
 {"op":"add","path":"/presence/extras/friends/appInfo:acdIndex","value":0},
 {"op":"add","path":"/presence/extras/friends/appInfo:presenceGroupId","value":"0100c2500fc20000"}]
```

Confirmed against `kinnay/NintendoClients`, `nintendo/switch/baas.py`,
`BAASClient.update_presence`.

`appInfo:appId` is the field that makes "Playing Splatoon 3" appear. The patch is
**merged** into the existing block rather than replacing it, because the console
sends only what changed — a later report that moves only `appField` must not drop
the app id.

`appField` is a JSON-encoded string the game chooses. It is stored and returned
byte for byte; parsing it would mean guessing at each game's private format.

## Data model

`internal/db/migrations/`. Three decisions worth explaining:

**Friendships are two rows, one per direction.** A single row with an ordered
pair is smaller but wrong: `favorite` is a per-direction property. A may mark B
as a favourite without B doing the same, and the console shows each side its own
flag. The two rows are always written together in one transaction.

**Presence keeps `reported_at` and `last_seen_at` apart.** See above. Conflating
them is the whole bug.

**Blocks are one-directional, but checked both ways.** Only the blocker sees the
block, but friend requests and presence are suppressed in both directions —
otherwise the blocker could still reach the person they blocked.

The `Store` interface in `internal/db/store.go` is deliberately free of business
logic: it reads and writes rows, and `internal/baas` decides what they mean. That
split is what lets the handler tests run against an in-memory store with no
database, in milliseconds, in CI. Both implementations run the *same* contract
test, so they cannot drift — and that test has already caught one real bug where
an empty id filter matched every user.

## What is deliberately not verified

SwitchNet does not reproduce any Nintendo secret and does not validate any
Nintendo-issued credential. Specifically:

| Thing | What Nintendo does | What SwitchNet does | Why |
| --- | --- | --- | --- |
| Device certificate | requires a Nintendo-signed client certificate | reads its fingerprint if nginx passes one, ignores its validity | verifying it needs Nintendo's CA |
| dauth challenge | MAC keyed on the console's master key | issues and echoes it, never checks the MAC | checking it needs Nintendo key material |
| Game ticket / gamecard certificate | proves the game is owned | accepted, never looked at | on a private network of consoles you own there is nobody to prove ownership to |
| Gamecard challenge | answered by the cartridge | issued, never checked | same |
| NSO membership | checked against Nintendo | every registered user is entitled to play | there is no subscription |

None of this produces a credential Nintendo's own servers would accept, and none
of it is a way to get an unowned game online anywhere but on the operator's own
network. The bypasses exist because the operator trusts their own consoles, not
because the server forges anything.

The one thing SwitchNet *does* verify rigorously is its own signatures. A device
token presented to aauth, or an access token presented to BAAS, must verify
against the key SwitchNet generated, with the right key id, unexpired, and with
the right token type. `alg: none`, a token signed by a different issuer, and a
token with an edited claim are all rejected, and there is a test for each.

## Where the knowledge came from

Protocol details are from Kinnay's NintendoClients wiki, which each package's doc
comment links to for its own endpoint:

- [DAuth](https://github.com/kinnay/NintendoClients/wiki/DAuth-Server)
- [AAuth](https://github.com/kinnay/NintendoClients/wiki/AAuth-Server)
- [BAAS](https://github.com/kinnay/NintendoClients/wiki/BAAS-Server)
- [Switch tokens](https://github.com/kinnay/NintendoClients/wiki/Switch-Tokens)
- [NAT check](https://github.com/kinnay/NintendoClients/wiki/NAT-Check-Server)
- [Server list](https://github.com/kinnay/NintendoClients/wiki/Server-List)
- [NPLN](https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers) (phase 2)
- [eShop Applet](https://github.com/kinnay/NintendoClients/wiki/eShop-Applet-(Switch)),
  [Atum](https://github.com/kinnay/NintendoClients/wiki/Atum-Server),
  [Dragons](https://github.com/kinnay/NintendoClients/wiki/Dragons-Servers),
  [Tagaya](https://github.com/kinnay/NintendoClients/wiki/Tagaya-Server-(Switch)) (phase 3)

The presence request shape came from `kinnay/NintendoClients`,
`nintendo/switch/baas.py`. The Atmosphère host list and the cert-trust mechanism
came from [Nextendo's Prelude-Nro](https://github.com/NextendoNetwork/Prelude-Nro),
which is AGPL and running in production.

**The friends 2.0.0 endpoints are not documented anywhere, and no public
implementation exists to compare against.** Their request and response shapes are
reasoned from the surrounding API and carry `TODO(capture-needed):` comments
saying what a capture would settle and which single function to change. See
[`CAPTURES/README.md`](CAPTURES/README.md).
