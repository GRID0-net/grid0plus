# internal/nplnrest

Splatoon 3's JSON front end: the ordinary HTTPS/JSON calls the game makes
to vermillion and penne before it ever opens the NPLN gRPC channel
`internal/npln` serves.

| | |
| --- | --- |
| Reimplements | `val`/`gw` of [Penne](https://github.com/kinnay/NintendoClients/wiki/Penne-Servers) and [Vermillion](https://github.com/kinnay/NintendoClients/wiki/Vermillion-Server) |
| Served by | `cmd/npln`, on `npln.rest_listen` -- a second listener in the same process as the gRPC gateway |
| Needs a database | no |

## One handler, told apart by Host

`internal/nintendo/hosts.go` routes every `ServiceNPLNRest` hostname --
vermillion, penne's `val`, and the whole `fro-N` frontline family -- to the
**same** nginx upstream, because none of them need the per-host TLS
interop flags the rest of the stack sometimes does. So `Handler()` is one
`http.ServeMux`, and the `Host` header is what tells a vermillion request
from a penne one. `firstFrontlineHost`/`frontlineHosts` read the frontline
family straight from `hosts.go` rather than naming a `fro-N` host here
too -- the one-place-hostnames-live rule CLAUDE.md states for the whole
project, not just the DNS/nginx/certificate generators.

## What is actually confirmed, and what is a placeholder

Penne's `val` endpoints (`GET /v1/frontlines`, `POST /v1/login_tickets`)
are the well-documented half of this package: the wiki gives a full field
table for both, including the literal `persistent_connection_params_simple`
values (`wait_sec: 3600`, `ignore_rst: true`, and so on) -- those are
copied verbatim, not derived.

Vermillion's two endpoints are not. The wiki names
`GET /v1/accounts/config` and `GET /v1/accounts/devices` but documents no
response fields for either, unlike every one of Penne's. Both answer with
the most honestly-empty shape this server can claim (`{}`, and
`{"devices":[]}`) rather than a guessed one — see each handler's own
`TODO(capture-needed)`. Every other Vermillion endpoint the wiki lists
(`notify`, `nearness-checks`, `vphym-borrowers`, device replace and so on)
is left unanswered (404): they are write/action endpoints with real
side-effect semantics this package is not willing to guess at.

**Whether Splatoon 3's boot sequence needs anything from vermillion at
all is itself unconfirmed** — see `docs/QUESTIONS.md`. The wiki describes
vermillion as the *virtual game card* system (device-transfer licensing),
a general Nintendo Switch feature with nothing Splatoon-specific about it,
not a Splatoon 3 "device init" step. `internal/nintendo/hosts.go`'s
existing classification calls it exactly that, from evidence this pass
could not re-derive. Both readings are recorded; neither is asserted as
settled.

## `postLoginTickets` accepts an id it cannot verify

The same rule `internal/dauth` and `internal/aauth` already apply to every
other Nintendo-issued credential (CLAUDE.md's second ground rule): a Penne
id and password are not something SwitchNet issued, so they are read and
logged, never checked against anything. What is genuinely unknown is how a
console gets one in the first place — the wiki marks the two endpoints
that plausibly create one (`god`'s `POST /v1/immigrate` and
`POST /v1/penne_ids`) with an unconfirmed HTTP method, and `god` is not
served by this package at all (`hosts.go` still classifies it
`ServiceStubs`).

## Frontline is held open, not answered

`fro-N.hac.lp1.penne.srv.nintendo.net` is the long-lived connection
`hosts.go`'s own comment already establishes, from a real error-code
capture, that closing reads as a failure to the console
(`2307-2103` on Splatoon 2, `2321-*` on Splatoon 3). Nothing publicly
documents what it carries once open — not even whether it is a plain long
HTTP response, a WebSocket upgrade, or something else. `serveFrontline`
does the one thing confirmed safe: accept the connection and hold it open
until the client goes away, rather than closing it or inventing a message
format to send down it.
