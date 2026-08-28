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

## Set `redis.url`, or the page never updates

The dashboard is a **separate process** from `baas`. `internal/presencebus`'s
in-process implementation delivers nothing across a process boundary, and it
does so **silently** — the page loads, the stream connects, and no presence
event ever arrives.

`cmd/dashboard` warns loudly at start-up when `redis.url` is unset, and the
live pill in the corner says "no event bus" rather than claiming to be live.
Both exist because the silent version of this wastes an afternoon.

## Presence is resolved here, not read

The dashboard applies **the same rule** `internal/baas` does:

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

## The player page puts the three presence timestamps side by side

`/api/players/{id}` is the page an operator opens when somebody says "it is not
working for me", and the useful answer is almost always a *combination* of facts:
a reported `OFFLINE` with a recent `last_seen_at`, a device account that was
reassigned, a friend list with one entry.

So it shows the effective state **and** the console's own last reported state
**and** both timestamps. "Reported OFFLINE" and "never reported" are different
states — conflating them is what makes a friend list permanently offline — and
only showing all three lets an operator see which case they are in without
reading the database.

Friends are resolved with the same presence rule as everywhere else, favourites
first. A friend list here that disagreed with the console's would send the
operator hunting for a bug that is only in this page.

## Fest controls normalise what they safely can

A misaligned start time and a capitalised resource name are **fixed**: there is
exactly one right answer for each, and the form says so before you type. A
revision that is not 40 hex characters is **refused**, because there is no right
answer to substitute — it has to come from the pack's `summary.yml`.

Validation errors are passed through to the page verbatim rather than flattened
into "invalid", because "not on a two-hour slot boundary" *is* the explanation.

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
is the honest minimum rather than a pretend identity system. See
[`TODO.md`](../../TODO.md) §3.3.

## No build step

Plain HTML, CSS and JavaScript, served from `embed.FS`. This repository ships Go
and generated protobuf; adding an npm toolchain for one page would mean a second
build system for the smallest artefact in the tree.
