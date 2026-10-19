# The Discord bot and its API

People get a GRID0+ account from a Discord bot, which lives in its own repository:
[n-popescu/grid0plus-discord](https://github.com/n-popescu/grid0plus-discord). A person
runs `/register` in your community's Discord and receives a login and a generated
password to type into the Switch's "Link Nintendo Account" screen. There is no
registration web page.

This page covers the server's half: the bot API the bot calls. The bot's own commands and
install steps are in its README.

## Turning it on

Add a `bot_api` block under `baas` in `config/config.yaml`:

```yaml
baas:
  bot_api:
    listen: "127.0.0.1:8096"      # loopback only; the bot runs on the same machine
    token: "<openssl rand -hex 32>"
    splatoon3_save_table: ""      # optional, see "Importing a Splatoon 3 save"
```

An empty `listen` turns the API off. The same token goes into the bot's own
configuration. Registration through the bot honours `baas.allow_registration`, so turn that
off to freeze the group.

## How requests are authorised

The API listens on its own address, never the console-facing one. Every request must
carry `Authorization: Bearer <token>`. The person is named by Discord user id in the
`X-Discord-User-ID` header. The server trusts that header only because the token proves
the request came from the bot, which takes the id from Discord's own interaction payload
and never from anything a person typed.

Identity still fails closed: a Discord id the server doesn't know gets a 404, never a
fallback account.

One Discord account can own several GRID0+ accounts. The first one it creates is its
main account, which any request that names no login acts on. The others are reached by
naming their login. A login that belongs to somebody else and a login that doesn't exist
get the same answer, so nobody can find out whether a login exists by asking.

Errors have one shape, `{"code": "...", "message": "..."}`. The bot switches on `code`;
`message` is for logs.

## Endpoints

All paths start with `/bot/v1`.

| Method and path | What it does |
| --- | --- |
| `POST /register` | Creates an account for a Discord user. Optionally with a chosen login, for an extra account. |
| `POST /link` | Connects an account made before the bot to a Discord user. |
| `POST /reset-password` | Issues a new password for one of the caller's accounts. |
| `GET /accounts` | Lists the caller's accounts. |
| `GET /me` | The caller's profile: login, friend code, linked consoles, status. |
| `PUT /avatar` | Sets the profile picture. |
| `GET /friends`, `DELETE /friends/{userId}` | Lists friends and removes one. |
| `GET /friend-requests`, `POST /friend-requests` | Lists pending requests, and sends one. |
| `POST /friend-requests/{id}/{action}` | Accepts, declines or cancels a request. |
| `GET /users/by-discord/{discordId}` | Looks up the account for a Discord user. |
| `GET /online` | Who is online right now, leaving out people whose presence is set to "only me". |
| `GET /status` | Health of each service, and how many people are on. |
| `POST /splatoon3/save/wipe` | Archives and deletes the caller's Splatoon 3 cloud save. |
| `POST /splatoon3/save/import` | Replaces the caller's Splatoon 3 cloud save with an uploaded `save.dat`. |

Passwords are never logged. The server stores only a bcrypt hash, so nobody can read one
back, the admin included.

## Splatoon 3 saves

Splatoon 3 keeps part of a player's progress in a record on the server (rank, money,
unlocked gear, Splatfest history) and part only on the console. On every boot the console
takes the server's copy over its own. That has two consequences for someone who changes
the save on their console:

- The console uploads only what changes, so a different save on the console is merged into
  the old server record, producing a mix of two saves.
- An edited save has no effect on the server-side fields, because the server's copy wins.

**Wiping** deletes the server record so the console starts fresh from its own save. The
record is archived under `data/splat3-save-backups` first, and if the archive
fails the wipe is refused, so a save is never lost this way. The player should close the
game before wiping.

**Importing** replaces the server record, whole, with the server-side half of a
`save.dat` the player uploads. It replaces rather than merges, because merging would bring
back fields from the old save. The previous record is archived first. The local half of
the save is never sent to the server and is ignored.

Importing needs the game's save key table, which comes from the game and is not in this
repository. Put the 128-byte file somewhere on the server and set
`baas.bot_api.splatoon3_save_table` to its path. If it is empty or unreadable the import
stays off and everything else keeps working. A save that contains fields the importer
doesn't know is refused rather than imported with those fields silently dropped.
See [internal/splat3save](../internal/splat3save/README.md).
