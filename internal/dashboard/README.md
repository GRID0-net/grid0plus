# internal/dashboard

The operator's view of SwitchNet: who is online, what they are playing, and the
Splatfest and Salmon Run controls.

| | |
| --- | --- |
| Reimplements | nothing — this is SwitchNet's own interface, not a Nintendo one |
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

## A player row opens onto stores that already exist

Clicking a row in the Players table opens a detail panel -- friends, device
accounts, recent matches -- assembled entirely from `GET /api/players/{id}`
(`handlePlayerDetail` in `server.go`) rather than a new history table.
Friends are resolved through the same `internal/presence` rule everything
else on this page uses, so a friend shown here as online is never out of
step with what they see of each other. There is no stored presence
*history*, deliberately: SwitchNet keeps one current presence row per user
(see "Presence is resolved here, not read", above), so what the panel shows
for "last seen" is that same row, not a log. Matches are filtered
client-side from the match history the Matchmaking tab already fetches,
rather than a second server-side query.

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

## Authentication is a shared token, and that is deliberate

`dashboard.token` guards every route except the static assets. Unset, there is
no authentication at all.

Nothing in phase 1 authenticates a **person** — every credential in the stack
belongs to a console — so there is no account to check against. A shared token
is the honest minimum rather than a pretend identity system. See
[`TODO.md`](../../TODO.md) §3.3.

## No build step

Plain HTML, CSS and JavaScript, served from `embed.FS`. This repository ships Go
and generated protobuf; adding an npm toolchain for one page would mean a second
build system for the smallest artefact in the tree.
