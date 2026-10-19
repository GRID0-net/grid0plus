# toolbox

The public API for the GRID0+ Toolbox homebrew app ([GRID0-net/GRID0plus-cfw](https://github.com/GRID0-net/GRID0plus-cfw), `toolbox/`). It tells the app
whether a newer build exists and gives a coarse status of the other GRID0+ services. The setup steps are in [TOOLBOX.md](../../docs/TOOLBOX.md).

| | |
| --- | --- |
| Needs a database | no |
| Stands in for a Nintendo endpoint | no, this isn't part of the console-facing protocol |
| Credential it holds | a fine-grained GitHub token, scoped to the Toolbox repository's contents, read-only |
| Credential the caller needs | none |

## Why it is its own service

If the app's repository is private, an unauthenticated `GET /repos/.../releases/latest` from a console gets a 404. The two ways around that are to give the
console a GitHub token or to give it a service that holds the token. The first puts a real, extractable credential in every console: anyone who runs `strings`
on the `.nro` has it, permanently, until every distributed copy is replaced. This service is the second option. The console asks it, it asks GitHub, and the token
never leaves the server.

It is a separate service (`cmd/toolbox`) instead of a route in `stubs` or `dashboard` for three reasons:

- **It isn't a Nintendo endpoint.** `stubs` answers hostnames `internal/nintendo/hosts.go` redirects, and this app isn't on that list and needs no DNS redirection.
  It already knows the server's address and reaches this service directly on its own port (`deploy/nginx.toolbox.conf`), the same pattern `gamesync` uses for 7575.
- **It must never need the dashboard's token.** The dashboard's `/api/health` answers the same question as `/status` here, in more detail and behind the shared
  token. That is the right amount of detail for the operator and exactly the amount that would leak internal error text (a database host, a connection error) to
  whichever console asks. `/status` here answers only `ok`, `degraded`, `down` or `unknown`, with no token.
- **It is the one place the GitHub token needs to exist.** Nothing else in GRID0+ talks to GitHub, so giving that credential its own service keeps the one thing that
  must never reach a console physically separate from everything else.

## Endpoints

| Path | Answers |
| --- | --- |
| `GET /status` | `{"services":[{"name":"baas","status":"ok"}, ...]}` |
| `GET /updates/latest` | `{"tag_name", "browser_download_url", "size"}`, shaped like GitHub's own release JSON |
| `GET /updates/latest/download` | streams the latest release's `.nro`, for a caller that doesn't know the asset id |
| `GET /updates/download?asset=<id>` | streams that release's `.nro` asset |

### `/status`

Each named service (`dauth`, `aauth`, `baas`, `stubs`, `npln`'s admin listener and `dashboard`) is asked its own `/healthz` and reduced to one word: `ok`, `degraded`,
`down`, or `unknown` if it answered but not with a health report. DNS and `natcheck` are UDP, so there is no `/healthz` to ask and they are reported `unknown` with a
reason instead of guessed at. A status that says "unknown, and here is why" is worth more than a colour nothing earned.

### `/updates/latest` and `/updates/download`

`/updates/latest` answers the three keys the app's updater reads from a GitHub release (`tag_name`, `browser_download_url`, `size`), so the app only needed a different host
to ask. `browser_download_url` is rewritten to point back at this service's own `/updates/download`, so a console never sees, and can't use, the token that made the answer
possible. A release is expected to carry exactly one `.nro`. If it carries several or none, the answer is a `502` rather than a guess.

`/updates/latest/download` is the same body as `/updates/download`, reached without walking `/updates/latest` first and holding on to a numeric asset id. It is for a console that
wants the newest `.nro` outright, such as a first install or a recovery with a stale stored URL. It uses the same release lookup and cache, so a release with several `.nro` files is a
`502` and not whichever one GitHub listed first. It sets `Content-Disposition: attachment; filename="switchnet.nro"`, a filename the app's updater was built against, so changing it
would strand installed copies. `/updates/download?asset=<id>` keeps its original headers untouched.

The rewritten URL is built from `toolbox.public_base_url`, never from the incoming request. It looks derivable, but behind the real nginx block neither `r.TLS` nor the port is
available: nginx terminates TLS and proxies plain HTTP, so `r.TLS` is always nil, and `proxy_set_header Host $host` forwards the hostname without the port. Together those produced
`http://host/updates/download?...`, with the wrong scheme and no `:8443`. `public_base_url` is required, alongside the three GitHub settings, for `/updates/*` to answer anything but `503`.
`release_cache_ttl_seconds` bounds how often GitHub is actually asked, so several consoles checking within one window cost one request.

## What it deliberately doesn't do

- **No authentication on the console's side.** The point is that a homebrew binary distributed to consoles can reach this safely with no credential to protect. If you ever need to
  restrict which consoles can check for updates, that needs a different mechanism.
- **No error detail in `/status`**, for the reason above.
- **No mirroring of the `.nro`.** Downloads proxy GitHub live, per request, and only the release metadata is cached. At the traffic a private deployment sees, a second copy on disk would
  be complexity it doesn't need.

## The patch mirror

`GET /grid0plus/patches/manifest.json` and `GET /grid0plus/patches/patches.zip`
answer the newest `patches-<serial>` release of `toolbox.patches_repo`
(`GRID0-net/GRID0plus-cfw`), the same two files the Toolbox otherwise
downloads from GitHub. The Toolbox asks here only when GitHub cannot be
reached; see the cfw repo's `patches/README.md` for the release format.

`RunPatchMirror` asks GitHub every `toolbox.patches_refresh_seconds` (600)
for a newer release. The repository is public, so no token is sent: the
mirror does not depend on `github_token`. A new set replaces the served one
only after every file its manifest lists is found in `patches.zip` with the
listed SHA-256; a release that fails keeps the previous set in service, and
the failure is logged. The last good set is written to `toolbox.patches_dir`
(`data/toolbox-patches/<tag>/`, with `current` naming it) and loaded again at
startup, so a restart during a GitHub outage, the one case the mirror is for,
still has something to serve. Until a first set exists the endpoints answer
503.

Integrity is end to end: the Toolbox checks every file against the
manifest's SHA-256 itself, so this copy is a convenience, not something the
console has to trust.
