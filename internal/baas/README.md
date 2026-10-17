# internal/baas

Accounts, friend codes, friend requests, friend lists, presence, blocks and
profile pictures. **This is the server the stock Nintendo friends applet talks
to**, and the only place in GRID0+ where identity is decided.

| | |
| --- | --- |
| Reimplements | `https://e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com` |
| | `https://m-lp1.baas.nintendo.com` (accounts, 21.0.0 and later) |
| | `https://cdn-image-e0d67c…baas.nintendo.com` (profile pictures) |
| Reference | [BAAS Server](https://github.com/kinnay/NintendoClients/wiki/BAAS-Server) |
| | [Switch Tokens](https://github.com/kinnay/NintendoClients/wiki/Switch-Tokens#baas-access-tokens) |
| Needs a database | yes |

## Files

| File | What |
| --- | --- |
| `router.go` | routing, the authentication middleware, the access log |
| `accounts.go` | `application/token`, `users`, `login`, `federation`, lookups, and `POST /login` (username/password, not Nintendo-shaped) |
| `devices.go` | `push_channels`, `devices/snapshot` — real response shapes, not `acknowledge` |
| `friends.go` | friend requests, friend list, favourites, relationships |
| `friendcodes.go` | code generation with the cooldown, and blocks |
| `presence.go` | the presence report, the TTL rules, profile patches |
| `images.go` | profile picture upload and serving |
| `certificates.go` | the two JWK set endpoints |
| `errors.go` | Nintendo's error response shape |
| `models.go` | the wire objects, and visibility scoping |
| `nalink.go` | the Nintendo Account link flow (`accounts.nintendo.com`) — GRID0+'s own login page, not a copy of anyone's real OAuth |
| `people.go` | creating a person's account (login, password hash, friend code), independent of any console |
| `botapi.go` | the Discord bot's account API (`baas.bot_api`): the only way to create a person's account, plus friends, avatars, who is online and status. Loopback only, shared token, caller named by Discord user id. One Discord account owns several GRID0+ accounts — the first is its main account, the rest are named by login, and a password reset reaches only the caller's own; see [`n-popescu/grid0plus-discord`](https://github.com/n-popescu/grid0plus-discord) |
| `botfest.go` | the bot API's Splatfest side: the vote post (in-game or Discord-only fest), first-pick-final votes, LAN games players submit and a mod accepts or refuses, standings no earlier than the game shows them, and `GET /bot/v1/fest-events`, the stream that gives a player the team role the moment they pick in game. See [The Discord bot's fests](#the-discord-bots-fests) |
| `provision.go` | device account id/password shape and validation, shared by registration and login |
| `ratelimit.go` | the sliding-window limiter over login attempts |
| `wire.go` | form/JSON body decoding shared by every handler |

## Two things to read before changing anything here

### Identity is fail-closed

A request is authorised by exactly one thing: an access token this server signed,
whose subject is a user that exists **and** whose device account still belongs to
that user. There is no default account, no "last console that logged in", and no
lookup that falls back to a global.

That is not caution for its own sake. The production Switch-facing account server
this replaces resolved a console by device account id and fell back to a
process-wide variable when the lookup missed. Every console then acted as one
person, they all sent friend requests as him, they all showed **his** friend code,
and regenerating the code changed nothing — because the code was never the
identity.

The tests in `accounts_test.go` named `TestLoginRefuses…`,
`TestATokenWhoseSubjectDoesNotOwnItsDeviceAccountIsRefused` and
`TestOneUserCannotReadOrChangeAnother` are the ones that hold that property. They
are the most important tests in the repository.

**`POST /login` (username/password) is a second, independent identity check,
not a fallback bolted onto the one above.** It exists for a caller with no
device account to present at all — an emulator's own account client, which
has no console-issued credential and no way to get one, unlike a real
console going through dauth/aauth — and it resolves identity entirely on its
own: `GetUserByLoginUsername` plus a `bcrypt` compare against
`LoginPasswordHash`, both already used by `nalink.go`'s login page. Once that
succeeds, it hands off to `issueLoginTokens` — the same function
`completeLogin` uses once *its* check succeeds — so the token shape is one
piece of code, not two that could drift. `deviceAccountForLogin` finds or
mints the device account those tokens name; see its own doc comment for why
"finds the person's own oldest one, or makes one" is still fail-closed and
not the process-wide-variable bug above wearing a new shape: the id and
password below are never involved, so there is nothing for this path to
guess about them.

### Presence takes the strongest fresh signal

```
fresh report from the console?  →  use it exactly, including OFFLINE
otherwise, seen recently?       →  ONLINE, app fields cleared
otherwise                       →  OFFLINE
```

Both halves are load-bearing. Without the expiry, a console switched off
mid-game shows as playing forever. Without the heartbeat, a console idling in the
Home Menu — which reports nothing, and is exactly where players look at each
other's status — reads as offline to all its friends.

`presence.go` has the long version, including the two subtleties that were only
found by running it: "never reported" is not "reported OFFLINE", and the app
fields have to be cleared whenever the effective state is not `PLAYING`.

## How the console reports presence

Not through an endpoint you would guess. It JSON-patches the **device account**:

```http
PATCH /1.0.0/users/<uid>/device_accounts/<daid>
Content-Type: application/json-patch+json

[{"op":"replace","path":"/presence/state","value":"PLAYING"},
 {"op":"add","path":"/presence/extras/friends/appInfo:appId","value":"0100c2500fc20000"},
 …]
```

Shape confirmed against `kinnay/NintendoClients`, `nintendo/switch/baas.py`,
`BAASClient.update_presence`. `appInfo:appId` is the field that makes
"Playing Splatoon 3" appear in the applet.

## Where the guesses are

The `2.0.0` friends endpoints are **not documented anywhere** and no public
implementation exists to compare against. Every guessed shape carries a
`TODO(capture-needed):` comment saying what a capture would settle and which
single function to change:

```sh
grep -rn 'TODO(capture-needed)' .
```

Priority order and capture instructions are in
[`docs/CAPTURES/README.md`](../../docs/CAPTURES/README.md). The friend list
response shape is first, because getting it wrong leaves the applet empty with
nothing in the log.

The bot's authenticated `GET /bot/v1/presence-count` returns idle `online`,
`playing`, and their `total`, using the shared TTL resolver and `/online`'s
SELF exclusion. It counts accounts once, not device connections. Two bulk
reads avoid health probes and per-user friend-code lookups during activity
refreshes; expired reports and deleted users do not inflate the result.

## The Discord bot's fests

`botfest.go`. The Splatfest Discord server ran its fests over LAN, with a bot
counting games; this lets those players, on consoles without CFW reaching
GRID0 over its ZeroTier relay, count in the same fests as GRID0+'s online ones.

- **Every bot fest is a fest row.** Votes, battles and results live in the
  console's own tables and go through toyohr's scoring, so the bot and the game
  cannot disagree. A Discord-only fest (`LANF-` id) is a row with no pack that
  is never enabled; every console-facing path skips disabled fests, and the
  dashboard refuses to enable one.
- **Every fest has its announcement text and three teams**, Alpha, Bravo and
  Charlie, each with a name, an emoji and a Discord role.
- **The first pick is final**, from the vote post as from the game
  (`Store.CreateFestEntry`, see `internal/toyohr/README.md`).
- **The game takes its team from the save, not the entry.** A Discord vote
  reaches the game because toyohr writes the entry's team into the save's
  `Fest.VoteTeam` whenever the save is read or written for that fest
  (`syncFestTeam`); the player sees it after restarting the game.
- **A LAN game counts like an online win** once a mod accepts it: a battle
  `lan-<submission>` in mode `lan_regular`, `lan_challenge` or `lan_tricolor`
  (the submission's `kind`: open, pro or tricolor), which toyohr's Open, Pro
  and Tricolor categories count. A Tricolor game is refused before the second
  half, and must have the game's lineup: the halftime leader defends with up
  to four players, each other team attacks with up to two. Every player must have an account and a team; a player's missing
  paint is this fest's online average for their side, or the fest's own
  default before anyone played online. Accepting and recording the battle is
  one transaction, and a submission can be settled once.
- **Standings are never earlier than the game's**: nothing during the first
  half, the halftime ranking from the second half, the result once out
  (`toyohr.FestStandings`).
- **In-game votes reach the bot as they happen**, without polling: npln
  publishes each new entry on the Redis channel `<redis.channel>.fest`, and the
  bot holds `GET /bot/v1/fest-events` open. Pub/sub drops what nobody heard, so
  the bot reads `GET /bot/v1/fests/{id}/entries` once per reconnect. Without
  `redis.url` the stream answers 503. Roles follow a Discord account's main
  account only, so one person with two accounts cannot hold two team roles.
