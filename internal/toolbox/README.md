# internal/toolbox

The public API for the GRID0+ Toolbox homebrew app
([`n-popescu/grid0plus-toolbox`](https://github.com/n-popescu/grid0plus-toolbox)):
whether a newer build exists, and a coarse status of the other GRID0+
services.

| | |
| --- | --- |
| Needs a database | no |
| Reimplements a Nintendo endpoint | no — this is not part of the console-facing protocol at all |
| Credential it holds | a fine-grained GitHub PAT, scoped to `grid0plus-toolbox`'s contents, read-only |
| Credential the caller needs | none |

## Why this exists, and why it is its own service

`grid0plus-toolbox` (the homebrew app's own source) is a private repository, so
an unauthenticated `GET /repos/.../releases/latest` from a console gets a
404. The two ways around that are: give the console a GitHub token, or give
it a service that holds the token instead. The first means every console
carries a real, extractable credential — anyone who runs `strings` on the
`.nro` has it, permanently, until every distributed copy is somehow revoked
and replaced. This package is the second option: the console asks *this*
service, which asks GitHub, and the token never leaves the box.

It is a separate service (`cmd/toolbox`) rather than a route added to
`stubs` or `dashboard` for three separate reasons:

- **It is not a Nintendo endpoint.** `stubs` answers hostnames
  `internal/nintendo/hosts.go` redirects; this app is not on that list and
  does not need DNS redirection at all — it already knows the server's
  address (the same one the app's own hosts screen writes) and reaches this
  service directly, on its own port (`deploy/nginx.toolbox.conf`, the same
  pattern `gamesync` uses for port 7575 rather than a slot in the SNI map).
- **It must never require dashboard's token.** `internal/dashboard`'s
  `/api/health` answers the same question `/status` here does, in more
  detail, behind the shared operator token — the right amount of detail for
  the operator, and exactly the amount that would leak internal error text
  (a database host, a connection error) to whichever console asks. `/status`
  here answers only `ok` / `degraded` / `down` / `unknown`, on purpose, with
  no token at all: an unauthenticated caller earns no more than that.
- **It is the one place the GitHub token needs to exist.** Nothing else in
  GRID0+ talks to GitHub. Giving that credential its own service, rather
  than folding it into one that already does several other things, keeps the
  one thing that must never reach a console physically separate from
  everything that already does.

## Endpoints

| Path | Answers |
| --- | --- |
| `GET /status` | `{"services":[{"name":"baas","status":"ok"}, ...]}` — see below |
| `GET /updates/latest` | `{"tag_name", "browser_download_url", "size"}`, shaped exactly like GitHub's own release JSON |
| `GET /updates/latest/download` | streams the latest release's `.nro`, for a caller that does not know the asset id |
| `GET /updates/download?asset=<id>` | streams that release's `.nro` asset |

### `/status`

Each named service (`dauth`, `aauth`, `baas`, `stubs`, `npln`'s admin
listener, `dashboard`) is asked its own `/healthz` and reduced to one word:
`ok`, `degraded`, `down`, or `unknown` if it answered but not with a health
report. `dns` and `natcheck` are UDP — there is no `/healthz` to ask — so
they are reported `unknown` with a reason, never guessed at. This is the
same honesty rule `internal/dashboard/servicehealth.go` applies to its own,
more detailed probe: a status that says "unknown, and here is why" is worth
more than a colour this package never earned.

### `/updates/latest` and `/updates/download`

`/updates/latest/download` is the same body as `/updates/download`, reached
without the caller having to walk `/updates/latest` first and hold on to a
numeric asset id. The updater shipped before this endpoint knew only the
`browser_download_url` that `/updates/latest` hands back, so a console that
wants the newest `.nro` outright — a first install, or a recovery where its
stored URL is stale — otherwise has two round trips and a piece of
GitHub-shaped state to get wrong. It resolves the asset through the same
release fetch/cache and the same `nroAsset` check as everything else here, so
a release with several `.nro` files is a `502` rather than whichever one
GitHub happened to list first.

Its response differs from the legacy path in one deliberate way: it sets
`Content-Disposition: attachment; filename="switchnet.nro"`. That filename is
part of the stable URL the toolbox's own updater was built against, so
changing it would strand installed copies. `/updates/download?asset=<id>`
keeps its old headers untouched, because something may already be parsing
them.

`source/update.c` in `grid0plus-toolbox` already parses exactly three keys out
of a GitHub release response — `tag_name`, `browser_download_url`, `size` —
tolerant of GitHub's own formatting. `/updates/latest` answers those same
three keys, so the client needed no parsing changes, only a different host
to ask. `browser_download_url` is rewritten to point back at this service's
own `/updates/download`: a console must never see, and cannot use, the
token that made the answer possible. The release's asset list is expected to
carry exactly one `.nro`; if it carries several or none, that is reported as
a `502` rather than guessed at.

That rewritten URL is built from `toolbox.public_base_url`
(`config.Toolbox.PublicBaseURL`), never from the incoming request. It looks
like it should be derivable from `r.TLS`/`r.Host` — this service *does* know
its own hostname and whether the connection is TLS — but behind the real
`deploy/nginx.toolbox.conf` neither is true: nginx terminates TLS itself and
proxies to this service over plain HTTP, so `r.TLS` is always `nil` here,
and `proxy_set_header Host $host;` forwards the hostname without the port
(`$host` never carries one). Both mistakes together used to produce
`http://host/updates/download?...` — wrong scheme, missing `:8443` — a URL
nothing listens on. `public_base_url` is required, alongside the three
GitHub fields, for `/updates/*` to answer anything but `503`.

`release_cache_ttl_seconds` bounds how often GitHub is actually asked:
several consoles checking within the same window cost one request, not one
per console.

## What this deliberately does not do

- **No authentication on the console's side, anywhere.** The whole point is
  that a homebrew binary distributed to consoles can reach this safely with
  no credential to protect. If GRID0+ ever needs to restrict *which*
  consoles can check for updates, that has to be a different mechanism —
  this package is not it.
- **No error detail in `/status`.** See "why this exists" above.
- **No mirroring or caching of the `.nro` itself.** `/updates/download` and
  `/updates/latest/download` proxy GitHub live, per request; the latter reuses
  only the *release metadata* cache, never the binary. At the traffic level one GRID0+
  deployment sees, holding a second copy of the binary on disk (and keeping
  it in sync with whatever GitHub currently has tagged latest) is
  complexity this does not need yet.
