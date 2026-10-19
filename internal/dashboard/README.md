# dashboard

The operator's view of GRID0+: who is online, what they are playing, and the Splatfest and Salmon Run controls.

| | |
| --- | --- |
| Stands in for | nothing; this is GRID0+'s own interface |
| Needs a database | yes |
| Needs Redis | yes, to be live (see below) |

It is plain HTML, CSS and JavaScript served from `embed.FS`, with no build step. It is the one surface in this repository
that a person looks at, so the interface matters. Two colours carry meaning and are used for nothing else: **pink is in
game** and **green is online**. A player's avatar colour is hashed from their user id (`hueForID` in `dashboard.js`) from
hues that skip the pink and green bands, so it never gets mistaken for a status. The theme (dark by default) is a
browser-local preference stored in `localStorage`; the server never hears about it.

## Set `redis.url`, or the page never updates

The dashboard is a separate process from `baas`. The in-process presence bus delivers nothing across a process boundary,
and does so silently: the page loads, the stream connects, and no presence event ever arrives. `cmd/dashboard` warns at
start-up when `redis.url` is unset, and the live indicator says "no event bus" instead of claiming to be live.

## Authentication

`dashboard.token` guards every route except the static assets. If it is unset there is no authentication at all, so anyone
who can reach the port can schedule and cancel Splatfests. It is a shared secret, not a login per person, which is the
honest minimum while there is one operator.

`/login` is a friendlier door onto the same token, not a second credential. Its form takes `dashboard.token` and, on a match,
sets an `HttpOnly` cookie (`switchnet_dashboard_session`) that `guard()` accepts like the query string or an `Authorization:
Bearer` header (`presentedToken` in `server.go`). There is no session store: the cookie carries the shared secret, so it is
exactly as strong as the token, and `POST /logout` just clears it. A bookmarked `?token=` link still works and upgrades itself
to a cookie the first time it loads.

## One page, two games

A switch at the top picks Splatoon 3 or Splatoon 2, each with its own tabs. Players, Health and Debug are about accounts and
services, so they are there for both. Each overview counts only the players whose presence names that game, and each Matches
tab asks the server for its own game's history (`/api/matches?game=`).

Splatoon 2's state lives in `cmd/nex`, so its tabs read it over nex's admin API (`splatoon2.go`), the same way the matchmaking
panel reads npln's: `dashboard.nex_admin_url` and `nex_admin_token` here, `nex.admin_listen` and `admin_token` there. Without
them, each Splatoon 2 tab says so, instead of showing an empty list that reads as "nobody is playing". Live status is polled
every five seconds while Splatoon 2 is on screen, and the record views (Splatfests, Rankings, Plaza, Annie's orders) are read
when their tab opens and every fifteen seconds after. The proxy passes on only the sections it names, so the dashboard can't be
used to reach an admin route it doesn't know about.

Splatoon 2's Schedule tab shows no schedule on purpose. The game reads its map and Salmon Run rotations from BCAT, not from its
game server, so an editor here would look like a control and change nothing a console reads. The same goes for its Splatfest
scheduling.

## Presence is resolved, not read

The dashboard applies the same rule as `internal/baas` and `internal/npln`, through [`internal/presence`](../presence/README.md):

```
fresh report?               use it exactly, including OFFLINE
otherwise seen recently?    ONLINE, with the app fields cleared
otherwise                   OFFLINE
```

Reading `presence.state` directly would be simpler and wrong, because a console in the Home Menu reports nothing but keeps
polling, so it would look offline here while its friends see it online. `TestPresenceIsResolvedTheSameWayTheFriendsAppletSeesIt`
covers the four cases.

## The matchmaking tile distinguishes zero from no data

`/api/overview`'s `matchmaking` is null when nothing is reporting and an object when something is. On a tile, "nobody is queuing"
and "`cmd/npln` isn't running, or `redis.url` is unset in one of the two processes" look identical, and only one of them is worth
acting on. The value is a gauge (`presencebus.Stats`), published by `internal/npln` on every queue change and on a timer, and it
expires, so a stopped server reads as absent within one lifetime instead of as an empty queue forever.

Match history is different. `internal/matchmaking`'s queue is in memory, but `npln_matches` is the record that survives it. The
"Recent matches" panel reads it through `/api/matches`, straight from the database, so it works before the admin API is configured.

## Players

Clicking a row in the Players table opens a detail panel (friends, device accounts, recent matches) assembled from
`GET /api/players/{id}`. It shows the effective state **and** the console's last reported state **and** both timestamps, because
"reported OFFLINE" and "never reported" are different states and only seeing all three tells an operator which case they are in
without reading the database. Friends go through the same presence rule as everywhere else, so a friend shown as online here
agrees with what they see of each other.

## Splatfests

The fest controls fix what has one right answer, such as a start time that isn't on a two-hour boundary or a capitalised resource
name, and say so before you type. A revision that isn't 40 hex characters is refused, because there is nothing right to substitute:
it has to come from the pack's `summary.yml`. Validation errors are passed to the page verbatim, because "not on a two-hour slot
boundary" is the explanation. The procedure is in [SPLATFESTS.md](../../docs/SPLATFESTS.md).

## Salmon Run

The Salmon Run tab's dropdowns (`GET /api/coop-reference`, `coop.go`) come from `internal/toyohr/coop_ids.go`'s tables, the same ones
`switchnetctl coop stages`, `weapons` and `bosses` print, so the operator picks ids a console can actually resolve. The page filters
the stage list to match the Kind toggle, but `POST /api/coop-shifts` still runs the real `Validate` before storing anything. A shift's
status (`upcoming`, `live`, `ended`) is computed from the server's clock, not stored.

The form picks a current or upcoming Normal rotation instead of accepting arbitrary dates. A Normal override keeps that rotation's start
and end. Big Run and Team Contest start at the selected Normal rotation's start and run for 48 hours, and Normal rotations resume
afterwards. Consoles keep the schedule they loaded, so a change shows only after a game restart. Overlapping stored shifts must be removed
before scheduling an event.

Weapon dropdowns start with the two random slots, -1 (green ?) and -2 (gold ?, Grizzco weapons only). The "Special Big Run egg total"
panel (`GET /api/coop-bigrun-eggs`) shows the running total a special Big Run reads from `GlobalCounterService/GetSeason`.

**Auto-fill** (`POST /api/coop-shifts/generate`) wraps `toyohr.Service.GenerateAndSaveCoopShifts`, the generator `switchnetctl coop
generate` uses, which picks real stage, weapon and boss ids at random. It never overwrites a slot that already has a shift, however it
was put there, and there is no "regenerate everything" button.

## The Debug tab

`GET /api/debug/accounts/{id}` (`debug.go`) shows the raw `db.Presence` row next to the resolved one, and the two are expected to differ:
a console idle in the Home Menu reports nothing, so its raw state stays whatever it last was while the effective state reads `ONLINE`.
The account and device-account controls beside it (the `Deleted` toggle, token revocation, device-account delete and undelete) wrap
existing `db.Store` methods and are as reversible as those are. `Deleted` is a flag, and there is no path from this tab to
`DELETE FROM users`.

## The live table browser

Below the account lookup (`tables.go`), an editable, spreadsheet-like view over the tables BAAS and NPLN read from, polling every few
seconds while "live" is on. It reaches Postgres directly and not through `db.Store`, so it is Postgres only and answers 503 against the
in-memory store. Every value crosses the wire as text and is cast back with the column's own type, so a value that is invalid for the
column, or that a `CHECK` constraint refuses, answers with Postgres's own error. The registry in `tables.go` is the access-control
surface: no primary key, foreign-key-shaped column or password hash is ever selected or editable, whatever a request asks for.
`tables_test.go` needs a real database and is skipped without `SWITCHNET_TEST_DATABASE_URL`.

## BCAT seed endpoints

`GET /api/seed/manifest` and `GET /api/seed/file/{region}/{name}`, behind the same token, let the console-side
[BCAT installer](../../switch/bcat-installer/README.md) fetch a fest's delivery cache instead of an operator moving files by hand.

- The manifest's fest id, resource and revision come from the `summary.yml` on disk, not the fest table, because what the console will hold
  is the authority and a disagreement is worth seeing.
- Every file carries a SHA-1, which the installer checks before writing, because a truncated pack looks valid to the console's file system
  and fails hours later as `BcatInvalid`.
- Paths are checked against an allowlist (one region directory and one file name), not by rejecting `..`.
- An unbuilt seed directory answers 404, not an empty manifest, since the installer would report success having written nothing.
