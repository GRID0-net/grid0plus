# Architecture

GRID0+ replaces the Nintendo servers a Switch talks to before and during online
play, for a private group of consoles that the operator owns. This page explains
what the pieces are and why they are shaped the way they are.

- [The layers of a Switch's online life](#the-layers-of-a-switchs-online-life)
- [The authentication chain](#the-authentication-chain)
- [Services](#services)
- [Redirection and trust](#redirection-and-trust)
- [Identity](#identity)
- [Presence](#presence)
- [Data model](#data-model)
- [What is not verified](#what-is-not-verified)
- [References](#references)

## The layers of a Switch's online life

A Switch doesn't have one online service. It has several, and they don't depend on
each other.

| Layer | What it does | Protocol |
| --- | --- | --- |
| Accounts and friends | sign-in, friend list, presence, friend codes | REST over HTTPS |
| Game servers | matchmaking, lobbies, rankings | NEX/PRUDP (older titles) or NPLN/gRPC (newer ones) |
| Gameplay | the packets during a match | peer-to-peer UDP |
| Store | catalogue, purchases, downloads | REST over HTTPS (not part of GRID0+) |

Gameplay is always peer to peer. Once a match starts, the consoles talk directly to
each other and GRID0+ is not in the path. That is why the NAT check server matters
out of proportion to its size: it is how the consoles find each other. It is also
why a faster server does nothing for in-match latency.

## The authentication chain

Every online feature goes through this first. Each step's output is the next step's
input, so a wrong claim in one place tends to fail several steps later with an
unrelated-looking error.

```
console boots
    |
    v
1. dauth    "I am a Switch"       POST /v{1..8}/device_auth_token
             in: client id, system version, challenge     out: device token (JWT)
    |
    v
2. aauth    "I own this game"     POST /v{1..5}/application_auth_token
             in: device token, application id, ticket     out: application token (JWT)
    |
    v
3. baas     accounts, friends, presence
             POST /1.0.0/application/token   device token -> anonymous token
             POST /1.0.0/users               register
             POST /1.0.0/login               device account -> user
             out: an access token (for this API) and an id token (for game servers)
    |
    v
4. game server (NPLN for Splatoon 3, NEX for Splatoon 2)
             verifies the id token against /1.0.0/certificates
    |
    v
5. Pia, console to console, with NAT traversal helped by natcheck
```

The id token is how a game server learns which player is connecting without
trusting anything the game says. It is verified against a public key set the game
server fetches itself. That is also the reason BAAS has separate keys for access
tokens and id tokens: an id token handed to a game server must never work as an
access token against the player's own account.

## Services

Each is its own process. They are separate because that is how they are deployed
(you can restart BAAS without dropping a console's device-auth request) and
because it keeps each one's dependencies honest.

| Service | Default port | Stands in for | Database |
| --- | --- | --- | --- |
| `dauth` | 8081 | `dauth-lp1.ndas.srv.nintendo.net` and the matching JWK host | no |
| `aauth` | 8082 | `aauth-lp1.ndas.srv.nintendo.net` and the matching JWK host | no |
| `baas` | 8083 | the BAAS hosts: accounts, friends, presence | yes |
| `stubs` | 8084 | everything else a console contacts (telemetry, news, BCAT, ...) | no |
| `npln` | 8085, 8086, 8088 | Splatoon 3's game server, JSON front end and `gamesync` | yes |
| `dashboard` | 8087 | nothing; it is the operator's web page | yes |
| `toolbox` | 8090 | nothing; it serves the Toolbox homebrew app | no |
| `scsi` | 8091 | the cloud save backup hosts | yes |
| `nex` | 8092 | Splatoon 2's game server | yes |
| `natcheck` | UDP 10025, 10125, 3478 | the NAT check hosts, plus STUN | no |

Everything HTTP sits behind one nginx edge that routes on the hostname the console
asked for. `switchnetctl nginx` generates that configuration from the same host list
that drives the DNS configuration, so the two can't disagree.

`dauth` has no database connection on purpose. It is the first thing a console
talks to, so a database outage should still let a console get far enough to see a
useful error rather than a timeout.

`nex` serves every NEX title from one listener. nginx passes the WebSocket upgrade
through and the host header picks the title. Its login is the same BAAS id token
that NPLN accepts, checked by the same code (`internal/identity`), and a player's
NEX id is their GRID0+ user id, so there is no second identity to keep in step.

## Redirection and trust

These are two different problems, and most guides mix them up.

**Redirection** means making Nintendo's hostnames resolve to your server. It
doesn't need the console at all. A Switch uses whatever DNS its network gives it, so
`switchnetctl dnsmasq` and `switchnetctl unbound` write configuration for a resolver
you already run (a router, Pi-hole, Unbound). Hand that resolver out over DHCP and
the console needs no configuration whatsoever.

**Certificate trust** does need the console. It asks for hosts like
`dauth-lp1.ndas.srv.nintendo.net`, whose certificate is signed by a private
Nintendo CA, and no public authority will issue a certificate for a Nintendo
hostname. So the console has to be told to accept yours. Splatoon 3 is a third case:
it carries its own TLS stack and pins its own certificates, so it needs its own
patches. [CONSOLE_SETUP.md](CONSOLE_SETUP.md) covers all three.

One detail that bites later if you miss it: the generated resolver configuration
answers `AAAA` queries for redirected names with "no data" rather than forwarding
them. If it forwarded, a console on an IPv6 network would sometimes resolve
Nintendo's real address and bypass GRID0+, intermittently, which is close to
undiagnosable from the console side.

## Identity

A request is authorised by exactly one thing: an access token GRID0+ signed, whose
subject is a user that exists and whose device account still belongs to that user.
There is no default account, no "last console that logged in", and no lookup that
falls back to a global.

This matters because of how the server GRID0+ replaces failed. It looked a console
up by its device account id and, when the lookup missed, fell back to a
process-wide variable holding the last account that had authenticated. A console
caches its device account forever, so misses are completely normal, and that was
exactly when the fallback fired. The result was every console acting as one person,
sending friend requests as that person and displaying that person's friend code.
Regenerating the code changed nothing, because the code was never the identity.

What GRID0+ does instead:

- `POST /1.0.0/login` looks up the device account, compares the password in
  constant time and returns `invalid_device_account` on any miss. There is no other
  outcome.
- Every authenticated request re-reads the device account and checks it still
  belongs to the token's subject. A token stays cryptographically valid until it
  expires, so without this a deleted console would keep full access for hours.
- Every user-scoped endpoint refuses a caller who isn't the user in the path.
- A friend request from a user to themselves is refused. With a broken identity
  layer, "add a friend" turns into a self-request surprisingly often.

`internal/baas/accounts_test.go` has a test for each. They are the most important
tests in the repository.

## Presence

The same server also had friends who were never online, for two reasons, and the
read path is shaped by both.

A reported state has to expire. The console reports "playing" when a game starts
and doesn't repeat it on a timer, so without a time limit a console switched off
mid-game would show as playing forever.

And ordinary requests count as proof of life. A console reports nothing while it
sits in the Home Menu, which is where players look at each other, but it keeps
polling its friend list. Treating that traffic as an "online" heartbeat is what
makes an idle console visible at all.

So the read path takes the strongest fresh signal:

```
fresh report?               use it as reported (including "offline": a console
                            reporting that is signing out)
otherwise, seen recently?   online, with the app fields cleared
otherwise                   offline
```

Two details that are easy to get wrong:

- "Never reported" is not "reported offline". A new account's `reported_at` is NULL.
  Defaulting it to the current time makes a brand-new user look as if it just went
  offline, which suppresses the heartbeat.
- The app fields must be cleared whenever the effective state isn't "playing". A
  console that closes a game reports "online" without clearing them, so a friend
  list would keep saying "playing X" for a game that has stopped.

The console reports presence by JSON-patching its device account:

```http
PATCH /1.0.0/users/<user-id>/device_accounts/<device-account-id>
Content-Type: application/json-patch+json

[{"op":"replace","path":"/presence/state","value":"PLAYING"},
 {"op":"add","path":"/presence/extras/friends/appInfo:appId","value":"0100c2500fc20000"}, ...]
```

`appInfo:appId` is what makes "Playing Splatoon 3" appear. The patch is merged into
the stored block rather than replacing it, because the console only sends what
changed. `appField` is a JSON string chosen by the game; it is stored and returned
byte for byte, since parsing it would mean guessing each game's private format.

Presence changes are published on `internal/presencebus` so the dashboard and the
NPLN presence service consume events instead of polling. Publishing happens on the
request path, so it drops an event rather than blocking: a missed dashboard update is
cosmetic, a hung friend-list request is not.

## Data model

Migrations are in `internal/db/migrations/`. Three decisions are worth knowing.

**Friendships are two rows, one per direction.** `favorite` is a per-direction
property: A can mark B as a favourite without B doing the same, and the console
shows each side its own flag. Both rows are always written in one transaction.

**Presence keeps `reported_at` and `last_seen_at` apart.** See above. Conflating
them is the whole bug.

**Blocks are one-directional but checked both ways.** Only the blocker sees the
block, but friend requests and presence are suppressed in both directions, or the
blocker could still reach the person they blocked.

The `Store` interface in `internal/db/store.go` holds no business logic. It reads
and writes rows, and `internal/baas` decides what they mean. That split is what lets
the handler tests run against an in-memory store with no database. Both
implementations run the same contract test, which has already caught a real bug
where an empty id filter matched every user.

## What is not verified

GRID0+ doesn't reproduce any Nintendo secret and doesn't validate any
Nintendo-issued credential.

| Thing | What Nintendo does | What GRID0+ does | Why |
| --- | --- | --- | --- |
| Device certificate | requires a Nintendo-signed client certificate | reads its fingerprint if nginx passes it on, ignores its validity | checking it needs Nintendo's CA |
| dauth challenge | a MAC keyed on the console's master key | issues and echoes it, never checks the MAC | checking it needs Nintendo key material |
| Game ticket, gamecard certificate | proves the game is owned | accepted without a look | on a private network of consoles you own there is nobody to prove ownership to |
| Gamecard challenge | answered by the cartridge | issued, never checked | same |
| Switch Online membership | checked against Nintendo | every registered user may play | there is no subscription |

None of this produces a credential Nintendo's servers would accept, and none of it
gets an unowned game online anywhere except on the operator's own network. The
bypasses exist because the operator trusts their own consoles, not because the
server forges anything.

What GRID0+ does verify strictly is its own signatures. A device token shown to
aauth, or an access token shown to BAAS, must verify against the key GRID0+ made,
with the right key id and token type, and be unexpired.

## References

Protocol details come from Kinnay's NintendoClients wiki, which each package README
links for its own endpoint:
[DAuth](https://github.com/kinnay/NintendoClients/wiki/DAuth-Server),
[AAuth](https://github.com/kinnay/NintendoClients/wiki/AAuth-Server),
[BAAS](https://github.com/kinnay/NintendoClients/wiki/BAAS-Server),
[Switch tokens](https://github.com/kinnay/NintendoClients/wiki/Switch-Tokens),
[NAT check](https://github.com/kinnay/NintendoClients/wiki/NAT-Check-Server),
[NPLN](https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers) and the
[server list](https://github.com/kinnay/NintendoClients/wiki/Server-List).
The presence request shape comes from the NintendoClients library's
`nintendo/switch/baas.py`.

The 2.0.0 friends endpoints are not documented anywhere, so their request and
response shapes are reasoned from the rest of the API. They are marked
`UNCONFIRMED:` in the source.
