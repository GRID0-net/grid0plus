# internal/stubs

A catch-all for the Nintendo endpoints GRID0+ redirects but does not
implement.

| | |
| --- | --- |
| Needs a database | no |
| Routing | by `Host` header — one process sits behind many hostnames |

## Why answer them at all

Many console services treat a *failed* request as a fault rather than as "that
feature is unavailable", and the failure lands somewhere unhelpful. The clearest
case is the connection test: a console that cannot reach it decides it is behind a
captive portal, and the user sees "this feature is not available" rather than a
network error.

Answering with a well-formed empty result is what keeps the console quiet about
things that are genuinely not part of this stack.

## What each host gets

| Host contains | Answer | Why that shape |
| --- | --- | --- |
| `ctest`, `conntest` | `ok`, plus `X-Organization: Nintendo` | the console checks both the body and the header; JSON here is a captive-portal signal |
| `.frs.` | `[]` | friend recommendations — there is nobody to recommend |
| `.five.` | `[]` | online play invitations — phase 2 |
| `.op2.` | `/v1/users/<id>/memberships`: an `items` envelope with a `default` and an `ex` entry (the shape the account sysmodule's parser accepts — see `docs/CAPTURES/README.md` item 7); every other path `{"active": true, …}` | membership verification; every registered user may play |
| `service-status` | everything up | if it were not, the console learning it here would not help |
| `receive-*.dg.`, `receive-*.er.` | bare `200`, body read and discarded | telemetry (play/error reports) — read, not null-routed; see below |
| `.dragons.` | mostly `{}`, one path is not — see below | eLicense; `aauth` accepts the token without looking at it |
| `god.…penne` | `POST /v1/accounts/<id>/notification_tokens` → `{"notification_token": "<36 hex>"}`, every other path `{}` | npns hands this token to the account sysmodule for `PUT /1.0.0/push_channels`; `{}` left the Nintendo Account link stuck one request short. See `docs/CAPTURES/README.md` item 11 |
| anything else | `{}` | well-formed JSON beats a 404 for a client whose expectations are unknown |

Empty **lists**, not `null`: a console decoding `null` into a list type fails.

### Telemetry is read and discarded, not null-routed

`receive-*.dg.srv.nintendo.net` (play reports) and `receive-*.er.srv.nintendo.net`
(error reports) used to be `ServiceBlackhole` in `internal/nintendo/hosts.go`,
reasoned as "tells Nintendo nothing either way, and this way it does not
fill our logs" — and reasoned, wrongly, that null-routing them is what
Prelude-Nro does too. Checked against Prelude's actual current source
rather than assumed: it does not. NextendoNetwork/Prelude's own commit
history (facts only, not its code) documents a real hardware measurement:
the NSO/friends applet (`0100000000000816`) waits **synchronously** for an
HTTP response from these two hosts before it loads its own main module
(`lp1.nso`). Null-routed, the connection hangs until timeout and the
applet gives up without ever reaching `lp1.nso` — no profile icon
handling, plausibly more of the applet than that, never initialises.

`h.telemetrySink` reads the request body, discards it, and answers `200`
immediately. Telemetry still never reaches real Nintendo — discarding it
here, not forwarding it, is the whole point — only the synchronous wait
the applet blocks on is satisfied.

### One `dragons` path is not a safe `{}`: `/v2/elicenses/exercise`

"Exercising" a licence is the online-play eligibility check. Answering it
with anything but 200 and a genuinely empty body — including `{}`, which is
still two bytes a truly empty response does not have — 404s the path, and the
console concludes it has no right to play online at all: the actual mechanism
behind a console reporting it "cannot use online features," not a cosmetic
mismatch. `h.dragons` (`handler.go`) answers this one path with a bare
`200` and nothing else; every other `dragons` path keeps the blanket `{}`,
since `aauth` never looks at their content either way.

Source: `NextendoNetwork/nx-dauth`, a from-scratch dauth/aauth
reimplementation tested against real hardware (PolyForm Shield 1.0.0 —
source-available, not open-source; only this fact is taken here, never its
code, same rule already applied to it elsewhere in this repository — see
`docs/CAPTURES/README.md`). Two more `dragons` paths
(`/v2/elicenses/migration_state`, `/v2/elicense_archives/publish` and
`/v2/rights/publish_device_linked_elicenses`) are answered with the specific
fields nx-dauth's own server uses rather than a bare `{}`, marked
`TODO(capture-needed)` since — unlike `elicenses/exercise` — nothing
independently confirms those exact field names against a real Nintendo
capture.

## The log is the to-do list

Every stubbed request is logged with its host, path and user agent. An endpoint
that turns up here repeatedly is one that may need implementing for real.

`stubs.log_bodies: true` also logs request bodies. Invaluable exactly once, while
working out what an unimplemented endpoint is being asked for; far too chatty to
leave on.

The nginx config generated by `switchnetctl nginx` routes **unmatched hostnames**
here through a `default_server` block, so a host that was redirected on the
console but has no server block shows up as a log line rather than getting an
answer from whichever service nginx happened to list first.

## The three `bcat-*` hosts, all answered with 304, none null-routed

All three — `bcat-list-lp1`, `bcat-data-lp1`, `bcat-topics-lp1.cdn.nintendo.net`
— answer through `h.newsTopics` (in `handler.go`): an unconditional
`304 Not Modified`, empty body. Named for its first confirmed case —
`bcat-topics-lp1`'s `GET /api/nx/v1/titles/<id>/topics`, the System news
feed a title polls right at the start of its own boot sequence — but not
specific to it any more.

A real capture against Nextendo (facts only, not its code — see
`docs/CAPTURES/raw/nextendo-second-20260923T210513Z`) shows the topics
exchange in full: the console's request carries no conditional-GET header
at all, and the real server answers `304` with an empty body regardless —
its own permanent "nothing new" answer, not ordinary cache validation.
Reproduced on GRID0+'s own hardware on 2026-09-24: `bcat-topics-lp1` was
null-routed, the console's very first boot request failed outright, and "a
communication error has occurred" appeared instantly on opening the hall,
before `IssuePrearrangedUserToken` even fires.

`bcat-list-lp1` and `bcat-data-lp1` used to be null-routed too, reasoned as
the safe choice while GRID0+ cannot answer BCAT usefully. That reasoning
turned out to be wrong: NextendoNetwork/Prelude's own commit history (facts
only, not its code) documents a real, hard failure from exactly this
choice — Splatoon 3 raised `2122-2403` (module 122 = bcat) when
`bcat-list-lp1` was unreachable, not silence. Their fix, applied to all
three hosts, is the same unconditional 304: the console reads "your cache
is current" and falls back to whatever it already has locally. This sends
no data — a player with nothing in their BCAT cache still gets nothing —
it only stops that absence from being a hard error the game surfaces to
the player.

## The BCAT probe: a deliberate detour, not the default any more

`internal/bcat.Probe` (built there, still wired into this package's
`Handler`) is an **instrument, not a service** — a diagnostic tool an
operator reaches for on purpose, not what these hosts answer with day to
day since the 304 fix above shipped.

GRID0+ cannot serve real BCAT delivery-cache content. The `list`
response body is undocumented, and whether the console verifies the
delivery container's RSA-2048 signature has never been established —
switchbrew documents the *field* and says nothing about a verification
step. A console running GRID0+ already has sigpatches, so the check may
well be bypassed; nobody has looked.

The probe settles the cheap half of that. Reached, it logs each request —
method, path, headers, a hexdump of the body — and replies `200` with an
**empty body**, deliberately different from the 304 these hosts answer
with normally: anything else would be an invented protocol shape, and what
the console does next is itself the finding. Setting `stubs.bcat_record_dir`
also writes a hexdumped transcript to disk via `internal/wirelog`; leaving
it empty still logs.

Reaching it needs a temporary detour — the routed handler answers first,
so the probe has to be called directly (`h.bcat.ServeHTTP`, see
`handler_test.go`'s own probe tests) rather than through a hosts-file flip
the way it used to be:

```sh
# regenerate the real hosts file (304 for all three bcat-* hosts) once done
switchnetctl hosts > /path/to/sd/atmosphere/hosts/sysmmc.txt   # and emummc.txt
```

Write up whatever the console did under `docs/CAPTURES/`; either answer is
worth having. See `TODO.md` §2.4 for the open question the probe still
serves — whether GRID0+ could serve real delivery-cache content, not
just "nothing changed."
