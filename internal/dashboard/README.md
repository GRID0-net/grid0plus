# internal/dashboard

The operator's view of GRID0+: who is online, what they are playing, and the
Splatfest and Salmon Run controls.

| | |
| --- | --- |
| Reimplements | nothing — this is GRID0+'s own interface, not a Nintendo one |
| Needs a database | yes |
| Needs Redis | yes, to be live. See below. |

## The one surface a person looks at

Everything else in this repository answers a Switch. This answers the operator,
which is why it is the one place where the interface itself matters.

Two colours carry meaning and are used for nothing else: **pink is in game**,
**green is online**. Everything decorative stays grey, so a splash of colour on
the page always means somebody is actually there.

A player's avatar colour is the one exception, and it is deliberate: it is
hashed from their user id (`hueForID` in `dashboard.js`), stable for the
whole session, and picked from hues that skip the pink/green bands
specifically so it never gets confused for a status. It exists so a player
becomes a colour the operator recognises at a glance, not another line of
text — the small status dot at the avatar's corner is what still carries the
actual state.

The theme (dark by default, light on request) is a **viewer-local**
preference, not a server one: the toggle in the header writes
`data-theme` on `<html>` and remembers it in `localStorage`, falling back to
the OS preference when nothing has been chosen. It has nothing to do with
`dashboard.token` and nothing is sent to the server about it.

## Set `redis.url`, or the page never updates

The dashboard is a **separate process** from `baas`. `internal/presencebus`'s
in-process implementation delivers nothing across a process boundary, and it
does so **silently** — the page loads, the stream connects, and no presence
event ever arrives.

`cmd/dashboard` warns loudly at start-up when `redis.url` is unset, and the
live pill in the corner says "no event bus" rather than claiming to be live.
Both exist because the silent version of this wastes an afternoon.

## Presence is resolved here, not read

The dashboard applies **the same rule** `internal/baas` and `internal/npln`'s
`PresenceService` do, through `internal/presence` — a shared package rather
than a third copy, after the copy that used to live here directly was found
to have quietly drifted (see that package's own README):

```
fresh report?            use it exactly, including OFFLINE
otherwise seen recently?  ONLINE, with the app fields cleared
otherwise                 OFFLINE
```

Reading `presence.state` directly would be simpler and wrong: a console sitting
in the Home Menu reports nothing but keeps polling, so it would show as offline
here while its friends see it online. An operator who sees the dashboard and the
friends applet disagree goes hunting for a bug that is only in the dashboard.

`TestPresenceIsResolvedTheSameWayTheFriendsAppletSeesIt` covers all four cases,
including the one that is easy to miss — a console reporting `OFFLINE` is
signing out, and the heartbeat must not override it.

## The matchmaking tile distinguishes zero from no data

`/api/overview`'s `matchmaking` is **null** when nothing is reporting and an
object when something is. That distinction is the whole reason the tile was left
unwired rather than showing a zero: on a tile, "nobody is queuing" and
"`cmd/npln` is not running, or `redis.url` is unset in one of the two processes"
look identical, and only one of them is worth acting on.

The seam is a **gauge**, not an event — `presencebus.Stats`, published by
`internal/npln` on every queue change and on a timer, read on demand here. A
gauge because a freshly-loaded page needs the current value, and an event stream
only carries changes: a page opened between two players queuing would otherwise
show nothing.

It **expires**. A running server with an idle queue keeps republishing "still
nobody", so a server that stops reads as absent within one TTL rather than as an
empty queue forever.

## Match history outlives the live queue

`internal/matchmaking`'s queue is in memory — a formed session ages out
(`Sweep`) or vanishes on restart, same as an unmatched ticket. `npln_matches`
is the record that survives it: `internal/npln/matchmaking.go` and
`admin.go` each call `Server.recordMatch` the moment a match forms, whether
it filled on its own or an operator force-started it, and the Matchmaking
tab's "Recent matches" panel reads it back through `/api/matches`.

It is deliberately **not** gated behind `npln.admin_*` the way the live
queue panels above it are: it reads straight from the database this process
already has open, so it works even before the admin API is configured — the
one part of the Matchmaking tab that does not need `cmd/npln` to be
reachable at all.

## One page, two games

The page is split by game: a switch at the top picks Splatoon 3 or
Splatoon 2, and each has its own tabs. Players, Health and Debug are about
accounts and services rather than a game, so they are there for both. What
is per game is per game all the way down: each overview counts only the
players whose presence names that game, and each Matches tab asks the
server for its own game's history (`/api/matches?game=`) rather than
filtering one list, which a busy game would otherwise crowd the other out
of.

Splatoon 2's state lives in `cmd/nex`, so its tabs read it over nex's admin
API (`splatoon2.go`), exactly as the matchmaking panel reads npln's:
`dashboard.nex_admin_url`/`nex_admin_token` here, `nex.admin_listen`/
`admin_token` there. The admin client is the same one; only the 401 message
differs, so a mismatched token names the two settings to compare. With it
not set up, every Splatoon 2 tab but Schedule gives way to one explanation,
rather than each showing an empty list that reads as "nobody is playing".

Its live status (connected consoles, lobbies, calls, relay) is polled every
five seconds, and only while Splatoon 2 is the game on screen; its most
useful part is the list of methods consoles are calling, which is how a
method this server answers wrongly shows up while someone is watching a
console fail. The record views -- Splatfests, Rankings, Plaza, Annie's
orders -- walk a whole kind of record each, so each is read when its tab
opens and every fifteen seconds while it stays open. The proxy passes on
only the sections it names (`splatoon2Sections`), so the dashboard cannot
be used to reach an admin route it does not know about.

**Splatoon 2's Schedule tab shows no schedule, deliberately.** The game
reads its map and Salmon Run rotations from BCAT, not from its game server,
and GRID0+ answers BCAT with 304 for every title. An editor here would
look like a control and change nothing a console reads; the tab says so
instead. Its Splatfests tab has no scheduling form for the same reason:
a Splatfest reaches Splatoon 2 through BCAT too.

## A player row opens onto stores that already exist

Clicking a row in the Players table opens a detail panel -- friends, device
accounts, recent matches -- assembled entirely from `GET /api/players/{id}`
(`handlePlayerDetail` in `server.go`) rather than a new history table.
Friends are resolved through the same `internal/presence` rule everything
else on this page uses, so a friend shown here as online is never out of
step with what they see of each other, favourites first. A friend list here
that disagreed with the console's would send the operator hunting for a bug
that is only in this page.

The panel is also the page an operator opens when somebody says "it is not
working for me", and the useful answer is almost always a *combination* of
facts: a reported `OFFLINE` with a recent `last_seen_at`, a device account
that was reassigned, a friend list with one entry. So it shows the effective
state **and** the console's own last reported state **and** both timestamps.
"Reported OFFLINE" and "never reported" are different states — conflating
them is what makes a friend list permanently offline — and only showing all
three lets an operator see which case they are in without reading the
database. There is no stored presence *history* beyond that, deliberately:
GRID0+ keeps one current presence row per user (see "Presence is resolved
here, not read", above), so what the panel shows for "last seen" is that same
row, not a log. Matches are filtered client-side from the match history the
Matchmaking tab already fetches, rather than a second server-side query.

## Fest controls normalise what they safely can

A misaligned start time and a capitalised resource name are **fixed**: there is
exactly one right answer for each, and the form says so before you type. A
revision that is not 40 hex characters is **refused**, because there is no right
answer to substitute — it has to come from the pack's `summary.yml`.

Validation errors are passed through to the page verbatim rather than flattened
into "invalid", because "not on a two-hour slot boundary" *is* the explanation.

## Salmon Run controls offer only real ids, rather than validating them after the fact

The "Salmon Run" tab's dropdowns (`GET /api/coop-reference`, `coop.go`) are
populated from `internal/toyohr/coop_ids.go`'s own tables -- the same ones
`switchnetctl coop stages`/`weapons`/`bosses` print -- so the operator picks
from what a console can actually resolve rather than typing an id and
finding out it was wrong from a validation error. The one thing the page
still enforces client-side is filtering the stage list to match the Kind
toggle (Big Run stages only for Big Run, normal-rotation stages otherwise):
that mirrors `CoopShift.Validate`'s own check, not a replacement for it --
`POST /api/coop-shifts` still runs the real `Validate` before storing
anything, the same as fest creation does.

A scheduled shift's status (`upcoming`/`live`/`ended`) is computed the same
way `festRow`'s `Phase` is: from the server's clock against the shift's own
`Start`/`End`, not tracked as separate state that could drift from them.

The scheduling form selects a current or upcoming Normal rotation rather than
accepting arbitrary dates. A Normal override keeps that rotation's start and
end, including when only a little time remains. Big Run and Team Contest start
at the selected Normal rotation's start -- the current one included, so an
event can be tested straight away -- and run for 48 hours; the server derives
the end, then resumes Normal rotations at the event's end. Consoles keep the
schedule they loaded, so a change to the running schedule only shows after a
game restart. Overlapping stored shifts must be removed explicitly before
scheduling an event.

The weapon dropdowns start with the two random slots, -1 (green ?) and -2
(gold ?, Grizzco weapons only), both confirmed on hardware; a shift with one
lists the whole Grizzco pool in its KumaWeapons. The "Special Big Run egg
total" panel (`GET /api/coop-bigrun-eggs`) shows the running total a special
Big Run reads through `GlobalCounterService/GetSeason`.

## "Auto-fill" is additive, not authoritative

The Salmon Run tab's "Auto-fill" panel (`POST /api/coop-shifts/generate`,
`handleGenerateCoopShifts`) is a thin wrapper over
`toyohr.Service.GenerateAndSaveCoopShifts` -- the same generator
`switchnetctl coop generate` uses, picking real stage/weapon/boss ids at
random rather than a discovered Nintendo algorithm (see
`internal/toyohr/README.md`'s note on `coop_generate.go` for why that
distinction matters). The one property worth knowing before clicking it
twice: it never overwrites a slot that already has a shift, whichever form
put it there. Schedule one slot by hand in the panel above first, generate
around it, and that hand-scheduled shift is never at risk from a later
generate call over the same range -- there is no "regenerate everything"
button, on purpose.

## The Debug tab shows raw state next to resolved state, on purpose

`GET /api/debug/accounts/{id}` (`debug.go`) is the one view on this page that
does **not** go through `internal/presence.Resolver` alone -- it shows the
raw `db.Presence` row next to the resolved one. The two are expected to
disagree: a console idle in the Home Menu reports nothing (`RawPresence`
stays whatever it last was, `ReportedAt` empty) while still touching
presence on every poll, so `EffectivePresence` reads `ONLINE`. That gap is
exactly the shape of both bugs this codebase is built around (see
CLAUDE.md), so the Debug tab is where an operator goes to see it directly
instead of inferring it from the Players tab looking wrong.

The account and device-account controls next to it (`Deleted` toggle, token
revocation, device-account delete/undelete) all wrap existing `db.Store`
methods with no new state of their own -- see `db.Stats` and
`db.Store.ListAllDeviceAccounts` (added for this tab; `ListDeviceAccounts`
excludes deleted rows, which would make undeleting one impossible to find).
Every write here is exactly as reversible as the underlying store method
already is: `Deleted` is a flag, not a row removal, and there is no path
from this tab to `DELETE FROM users`.

## The live table browser (`tables.go`) is a different kind of control

Below the account lookup, an Excel-shaped view over every table BAAS and
NPLN read from, driven entirely by `tables.go`'s own registry rather than
`db.Store`: real cells, editable in place, polling every few seconds while
"live" is on. It does **not** go through a `db.Store` method at all --
deliberately, and Postgres-only, for the same reason `db.Memory` exists
purely for the fast handler-test suite rather than as a deployment target
(see `tables.go`'s package comment for the full reasoning). `s.postgres()`
type-asserts the configured store down to `*db.Postgres` and reaches its raw
pool; against `db.Memory` the endpoints answer 503 rather than pretending to
work.

Every value crosses the wire as text, both ways: a `SELECT` casts every
column with `::text`, and an edit is cast back with the column's own
Postgres type (`$1::jsonb`, `$1::text[]`, ...). That is what lets this stay
one generic handler instead of one per column type, and it is also the
safety net -- a value that is not valid syntax for the column's type, or
that a `CHECK` constraint refuses, answers with Postgres's own error message
rather than something this file would have to duplicate. The registry is
the actual access-control surface: no primary key, foreign-key-shaped
column (`host_user_id`, `player_ids`, ...), or password-hash column is ever
selected or editable, no matter what a request asks for -- see the
column-by-column reasoning in `tables.go`'s own comment.

`tables_test.go` needs a real database and is skipped without
`SWITCHNET_TEST_DATABASE_URL`, same as `internal/db`'s own PostgreSQL
contract test -- and deliberately exercises `npln_matches`, not `users`, so
it cannot be caught mid-test by that contract test's own `TRUNCATE ...
CASCADE` when `go test` runs the two packages concurrently against the same
database.
## It serves the BCAT seed to the console-side installer

`GET /api/seed/manifest` and `GET /api/seed/file/{region}/{name}`, behind the same
token as everything else. They exist so that
[`switch/bcat-installer/`](../../switch/bcat-installer/) can fetch a fest's
delivery cache instead of an operator moving files with JKSV — which works and
does not survive the second fest.

Three decisions in there:

- **the manifest's fest id, resource and revision come from the `summary.yml` on
  disk**, not from the fest table. What the console will actually hold is the
  authority, and a disagreement between the two is exactly the thing worth
  seeing rather than papering over.
- **every file carries a SHA-1.** The installer verifies before writing, because
  a truncated pack is indistinguishable from a valid one to the console's
  filesystem and the failure surfaces hours later as `BcatInvalid`.
- **the path is validated against an allowlist** — one region directory and one
  filename — rather than by rejecting `..`. A rule that enumerates what is
  allowed cannot be defeated by an encoding nobody thought of.

An unbuilt seed directory answers 404 rather than an empty manifest: an empty
manifest would have the installer report success having written nothing, and the
operator would go looking at the console.

## Authentication is a shared token, and that is deliberate

`dashboard.token` guards every route except the static assets. Unset, there is
no authentication at all.

Nothing in phase 1 authenticates a **person** — every credential in the stack
belongs to a console — so there is no account to check against. A shared token
is the honest minimum rather than a pretend identity system.

A real answer probably arrives with phase 3's Nintendo Account work, which
introduces the first human-facing login. **Do not build a bespoke user system for
one page before then** — it would be thrown away. Recorded in
[`docs/QUESTIONS.md`](../../docs/QUESTIONS.md).

**`/login` is a friendlier door onto the same token, not a second credential.**
Before it existed, using the dashboard meant either bookmarking a URL with
`?token=...` in it or hand-configuring an `Authorization: Bearer` header —
workable for a script, awkward for a person. `/login`'s form takes the same
`dashboard.token` and, on a match, sets an `HttpOnly` cookie
(`switchnet_dashboard_session`) that `guard()`/`guardPage()` accept exactly
like the query string or the header — see `presentedToken` in `server.go`.
There is no session store behind it: the cookie just carries the one shared
secret a fourth way, so it stays exactly as strong (or as absent, with the
token unset) as the token already was, and logging out
(`POST /logout`) is nothing more than clearing that cookie. A bookmarked
`?token=` link still works on its own and, as a courtesy, upgrades itself to
a cookie the first time it loads (`handleIndex`), so switching to `/login`
is never required for an existing setup to keep working.

`guardPage` exists only because `guard`'s plain 401 is the right answer for a
failed `fetch()` and the wrong one for a browser navigating to `/` by hand —
the operator needs `/login`, not an error page.

## No build step

Plain HTML, CSS and JavaScript, served from `embed.FS`. This repository ships Go
and generated protobuf; adding an npm toolchain for one page would mean a second
build system for the smallest artefact in the tree.
