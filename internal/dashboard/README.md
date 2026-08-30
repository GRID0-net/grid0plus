# internal/dashboard

The operator's view of SwitchNet: who is online, what they are playing, and the
Splatfest controls.

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

## Fest controls normalise what they safely can

A misaligned start time and a capitalised resource name are **fixed**: there is
exactly one right answer for each, and the form says so before you type. A
revision that is not 40 hex characters is **refused**, because there is no right
answer to substitute — it has to come from the pack's `summary.yml`.

Validation errors are passed through to the page verbatim rather than flattened
into "invalid", because "not on a two-hour slot boundary" *is* the explanation.

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
