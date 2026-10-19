# stubs

A catch-all for the Nintendo endpoints GRID0+ redirects but doesn't implement.

| | |
| --- | --- |
| Needs a database | no |
| Routing | by `Host` header, because one process sits behind many hostnames |

## Why answer them at all

Many console services treat a failed request as a fault instead of "that feature is
unavailable", and the failure lands somewhere unhelpful. The clearest case is the connection
test: a console that can't reach it decides it is behind a captive portal and shows "this
feature is not available" instead of a network error. Answering with a well-formed empty
result keeps the console quiet about things that aren't part of this stack.

## What each host gets

| Host contains | Answer | Why |
| --- | --- | --- |
| `ctest`, `conntest` | `ok`, plus `X-Organization: Nintendo` | the console checks both the body and the header; JSON here signals a captive portal |
| `.frs.` | `[]` | friend recommendations; there is nobody to recommend |
| `.five.` | `[]` | online play invitations |
| `.op2.` | `/v1/users/<id>/memberships`: an `items` envelope with a `default` and an `ex` entry; any other path `{"active": true, ...}` | membership checks; every registered user may play |
| `service-status` | everything up | if it weren't, hearing it here wouldn't help the console |
| `receive-*.dg.`, `receive-*.er.` | a bare `200`, body read and discarded | telemetry; see below |
| `.dragons.` | mostly `{}`; one path differs, see below | eLicense; `aauth` accepts the token without looking at it |
| `god.` penne host | `POST /v1/accounts/<id>/notification_tokens` answers `{"notification_token": "<36 hex>"}`, every other path `{}` | the push service hands this token to the account system module; without it the Nintendo Account link stops one request short |
| `bvc-*`, the browser version check | `404` for every version | see below |
| anything else | `{}` | well-formed JSON beats a 404 for a client whose expectations are unknown |

Empty **lists**, not `null`, because a console decoding `null` into a list type fails.

### Telemetry is read and discarded, not null-routed

The play-report and error-report hosts used to be null-routed. The friends applet waits
synchronously for an HTTP answer from them before it loads its own main module, so a
null route made it hang and give up. The handler reads the request body, throws it away and
answers `200` straight away. Telemetry still never reaches Nintendo; only the wait the applet
blocks on is satisfied.

### One `dragons` path isn't safe to answer with `{}`

`/v2/elicenses/exercise` is the online-play eligibility check. Anything other than `200` and a
genuinely empty body, including `{}`, makes the console conclude it has no right to play online.
That one path answers a bare `200`; every other `dragons` path keeps the blanket `{}`.

## The log is the to-do list

Every stubbed request is logged with its host, path and user agent. An endpoint that shows up
repeatedly may need a real implementation. `stubs.log_bodies: true` also logs request bodies,
which is useful for working out what an unimplemented endpoint is being asked for and far too
chatty to leave on.

The nginx config from `switchnetctl nginx` routes unmatched hostnames here through a
`default_server` block, so a host that was redirected on the console but has no block shows up
as a log line, instead of being answered by whichever service nginx listed first.

## The `bcat-*` hosts

All three (`bcat-list-lp1`, `bcat-data-lp1`, `bcat-topics-lp1.cdn.nintendo.net`) stay reachable.
Null-routing them is not safe: Splatoon 3 raises error `2122-2403` when the list host is
unreachable, and a null-routed topics host fails the console's very first boot request.

- `bcat-topics-lp1` always answers `304 Not Modified`: "nothing new".
- `bcat-list-lp1` and `bcat-data-lp1` answer `304` as well unless `bcat.seed_dir` holds a tree
  for the requested title. If it does, they serve a BCAT list and its files. See
  [`internal/bcat`](../bcat/README.md) for the layout and [SPLATFESTS.md](../../docs/SPLATFESTS.md)
  for the procedure.

`internal/bcat.Probe` is a diagnostic. With `stubs.bcat_record_dir` set, every request to a
`bcat-*` host is also written to disk, hexdumped, without changing the answer.

## The browser version check

Before opening a web applet (account linking, the eShop, Switch Online), the console sends a
request for `bvc-hac-lp1.cdn.nintendo.net/<major>-<minor>-<micro>`. Nintendo publishes a file for
every firmware version that must update: a `2xx` means "update required" and `404` means fine.
Nintendo started listing firmware 22.5.0 in October 2026, which put an update wall in front of those
applets. The stub answers `404` for every version, because the default empty object would be read as
"vulnerable" too. A GRID0+ console must never be told to update.
