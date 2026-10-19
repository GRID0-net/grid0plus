# baas

Accounts, friend codes, friend requests, friend lists, presence, blocks and profile
pictures. This is the server the stock Switch friends applet talks to, and the only place
in GRID0+ where identity is decided.

| | |
| --- | --- |
| Stands in for | `e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com`, `m-lp1.baas.nintendo.com` (accounts, firmware 21.0.0 and later) and the `cdn-image-...baas.nintendo.com` host (profile pictures) |
| Reference | [BAAS Server](https://github.com/kinnay/NintendoClients/wiki/BAAS-Server), [Switch Tokens](https://github.com/kinnay/NintendoClients/wiki/Switch-Tokens#baas-access-tokens) |
| Needs a database | yes |

## Files

| File | What |
| --- | --- |
| `router.go` | routing, the authentication middleware and the access log |
| `accounts.go` | `application/token`, `users`, `login`, `federation`, lookups, and the username/password `POST /login` |
| `devices.go` | `push_channels` and `devices/snapshot` |
| `friends.go` | friend requests, the friend list, favourites and relationships |
| `friendcodes.go` | friend code generation with its cooldown, and blocks |
| `presence.go` | the presence report, the time-limit rules and profile patches |
| `images.go` | profile picture upload and serving |
| `certificates.go` | the two JWK set endpoints |
| `errors.go` | Nintendo's error response shape |
| `models.go` | the wire objects and visibility scoping |
| `nalink.go` | the Nintendo Account link flow: GRID0+'s own login page, not a copy of Nintendo's |
| `people.go` | creating a person's account (login, password hash, friend code), independent of any console |
| `botapi.go`, `botapi_save.go` | the Discord bot's API; see [DISCORD_BOT.md](../../docs/DISCORD_BOT.md) |
| `botfest.go` | the bot API's Splatfest side: the vote post (in-game or Discord-only fest), first-pick-final votes, LAN games players submit and a mod accepts or refuses, standings no earlier than the game shows them, and `GET /bot/v1/fest-events`, the stream that gives a player the team role the moment they pick in game. See [The Discord bot's fests](#the-discord-bots-fests) |
| `provision.go` | device account id and password shape and validation |
| `ratelimit.go` | the sliding-window limiter on login attempts |
| `wire.go` | form and JSON body decoding shared by every handler |

## Two things to read before changing anything here

### Identity fails closed

A request is authorised by exactly one thing: an access token this server signed, whose
subject is a user that exists and whose device account still belongs to that user. There is
no default account, no "last console that logged in", and no lookup that falls back to a
global. [ARCHITECTURE.md](../../docs/ARCHITECTURE.md#identity) explains the failure this
prevents.

The tests in `accounts_test.go` named `TestLoginRefuses...`,
`TestATokenWhoseSubjectDoesNotOwnItsDeviceAccountIsRefused` and
`TestOneUserCannotReadOrChangeAnother` hold that property. They are the most important tests
in the repository.

`POST /login` (username and password) is a second, independent identity check, not a
fallback. It exists for a caller with no device account to present, such as an emulator's own
account client, and it resolves identity on its own: `GetUserByLoginUsername` plus a bcrypt
comparison against `LoginPasswordHash`. Once that succeeds it hands off to `issueLoginTokens`,
the same function `completeLogin` uses, so the token shape is one piece of code.
`deviceAccountForLogin` finds the person's own oldest device account or makes one; see its
comment for why that is still fail-closed.

### Presence takes the strongest fresh signal

```
fresh report from the console?   use it exactly, including OFFLINE
otherwise, seen recently?        ONLINE, app fields cleared
otherwise                        OFFLINE
```

Both halves matter. Without the expiry, a console switched off mid-game shows as playing
forever. Without the heartbeat, a console idling in the Home Menu, which reports nothing,
reads as offline to all its friends. `presence.go` has the long version, including the two
traps: "never reported" isn't "reported OFFLINE", and the app fields must be cleared whenever
the effective state isn't `PLAYING`.

## How the console reports presence

Not through an endpoint you would guess. It JSON-patches the device account:

```http
PATCH /1.0.0/users/<uid>/device_accounts/<daid>
Content-Type: application/json-patch+json

[{"op":"replace","path":"/presence/state","value":"PLAYING"},
 {"op":"add","path":"/presence/extras/friends/appInfo:appId","value":"0100c2500fc20000"}, ...]
```

`appInfo:appId` is what makes "Playing Splatoon 3" appear in the applet.

## Unconfirmed shapes

The `2.0.0` friends endpoints aren't documented anywhere and no public implementation exists
to compare against. Every guessed request or response shape carries an `UNCONFIRMED:` comment
that says what would settle it and which single function to change:

```sh
grep -rn 'UNCONFIRMED' .
```

The friend list response shape matters most, because getting it wrong leaves the applet empty
with nothing in the log.

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
- **One fest at a time.** `POST /bot/v1/fests` answers `fest_exists` while any
  fest post is still up, results included, until a mod deletes it.
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

## The Discord bot's Salmon Run jobs

`botsalmon.go`. LAN players earn the same Salmon Run rank as online, so
whatever they reach over LAN is what the game shows when they play online.

- **One rank.** A LAN job moves `CoopUserAttribute.total_grade_point`, the
  document online jobs move (`internal/ugcstore/coopranks.go`). Its total is
  title x 100 + points below Eggsecutive VP, and 800 + points from there.
- **A job is set up by the server.** `POST /bot/v1/salmon/jobs` with two to
  four players answers the current regular rotation's stage and the hazard
  level: the players' average total (capped at 1665) x 0.2%, rounded to the
  nearest 5%, at most 333%. Above 200% a stock game cannot follow; unless the
  request says the host is `modded`, the job is played at 200% and a clear is
  worth +10 instead of +20.
- **Every player must have an account**, or the whole request is refused.
- **A mod decides.** A player reports a clear, or the wave lost in, and
  whether a teammate disconnected (which halves a loss, as in game). Nothing
  moves until a mod accepts. The credit is once per job and player, so two
  Accepts move a rank once.
- **A job belongs to its rotation.** Reporting or accepting after that
  rotation ended is refused: the reset has moved everyone's points since.
- **Rotation reset and badges** are toyohr's (`coop_rank.go`), applied when
  the game opens a rotation and before a LAN job reads a rank.

