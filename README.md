<h1 align="center">GRID0+</h1>

<p align="center">
  <b>A self-hosted replacement for Nintendo's online infrastructure, for a private group of modded Switch consoles.</b>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/license-AGPL--3.0-blue" alt="License: AGPL-3.0">
  <img src="https://img.shields.io/badge/go-1.24%2B-00ADD8" alt="Go 1.24+">
  <img src="https://img.shields.io/badge/phase%201-on%20hardware-brightgreen" alt="Phase 1 running on hardware">
  <img src="https://img.shields.io/badge/phase%202-on%20hardware%2C%20hall%20blocked-yellow" alt="Phase 2 on hardware, hall blocked">
  <img src="https://img.shields.io/badge/phase%203-separate%20module%2C%20not%20started-lightgrey" alt="Phase 3">
  <img src="https://img.shields.io/badge/phase%204-code%20complete-brightgreen" alt="Phase 4 code complete">
</p>

---

## Running it

Postgres, Redis and the nginx TLS edge run in Docker (off-the-shelf images,
never rebuilt from this repo); the nine Go services run as native processes,
either in a terminal while you're working on them or as systemd units on the
box that actually serves your consoles.

```sh
cp config/config.example.yaml config/config.yaml   # then edit it
scripts/install-systemd.sh                         # once, from /opt/switchnet
scripts/start.sh                                   # start everything
scripts/update.sh                                  # pull, restart only what changed
```

`install-systemd.sh` builds every service and installs `deploy/systemd/` as
systemd units, so `systemctl restart switchnet-baas` (etc.) works and a
service that crashes gets restarted without you watching for it. `start.sh`
generates the edge certificate and the nginx configuration if they are
missing, validates the config, brings the stack up and waits until it is
actually answering — a container or unit reporting "running" is not the
same thing.

`update.sh` works out which services a change reaches by reading each binary's
Go package graph, so a change to `internal/baas` restarts only baas and a change
to `internal/db` restarts everything that imports it. `--dry-run` says what it
would do.

Iterating on the code itself, instead of running the deployed box: `make up`
(Postgres/Redis/nginx) then `make run` (every Go service, in one terminal,
foreground, Ctrl-C to stop) or `bin/switchnetctl run baas` for just one. See
[`CLAUDE.md`](CLAUDE.md) for the full loop.

The dashboard's **Health** tab asks every service directly and shows what each
one's own dependencies are doing.

## What this is

Accounts, friend codes, friend requests, friend lists and presence — including
"Playing Splatoon 3" — working through the **stock** Nintendo friends applet, on
consoles you own, against a server you run.

Four phases, in [`docs/ROADMAP.md`](docs/ROADMAP.md):

| Phase | Scope | State |
| --- | --- | --- |
| **1** | accounts, friend codes, friends, presence | **running on a real console**: sign-in, Nintendo Account linking and the friend list work; the received-requests view still shows 2124-0104 |
| **2** | NPLN game server for Splatoon 3, Splatfests, web dashboard | **running on a real console** up to the hall, where it stops with a communication error — see [`TODO.md`](TODO.md) §4.2 |
| **3** | eShop, served from local `.NSP` dumps of games you own | planned as a **separate, optional module**, not started |
| **4** | per-game cloud save backup (OLSC) | code complete on a defensible-but-unconfirmed shape, not yet tried on a console |

Phases 1, 2 and 4 are what this repository contains today. Phase 4 has not
been tried on a console yet. It stays in this repository too,
unlike phase 3 — see
[`docs/ROADMAP.md`](docs/ROADMAP.md#phase-4--cloud-save-backup-olsc) and
[`internal/scsi/README.md`](internal/scsi/README.md).

Phase 3 is deliberately **not** going into this repository. It's a different
kind of system — real game dumps, licensing/rights bookkeeping, a storefront
web app — with its own legal-exposure profile, and bundling it here would make
every phase 1/2 user carry that weight whether they want the eShop or not. It's
planned as a separate module a deployment can add later, not core scope. See
[`docs/ROADMAP.md`](docs/ROADMAP.md#phase-3--the-eshop-a-separate-module) for
what that boundary means in practice.

```
┌──────────────┐  HTTPS, Nintendo hostnames   ┌──────────────────────────────┐
│  Switch      │ ───────────────────────────► │  nginx, TLS + SNI routing    │
│  system,     │  redirected by Atmosphère's  └──────────────┬───────────────┘
│  Splatoon 3  │  hosts file or your resolver                │
└──────┬───────┘        ┌─────────────┬──────────────┬───────┴──────┬──────────────┐
       │                ▼             ▼              ▼              ▼              ▼
       │         ┌────────────┐ ┌───────────┐ ┌─────────────┐ ┌──────────┐ ┌────────────┐
       │         │ dauth      │ │ baas      │ │ npln        │ │ scsi     │ │ stubs      │
       │         │ aauth      │ │ accounts  │ │ NPLN gRPC,  │ │ OLSC     │ │ everything │
       │         │ device/app │ │ friends   │ │ toyohr,     │ │ save     │ │ else, incl.│
       │         │ tokens     │ │ presence  │ │ ugcstore,   │ │ backup   │ │ BCAT 304s  │
       │         └────────────┘ └─────┬─────┘ │ nplnrest,   │ └──────────┘ └────────────┘
       │                              │       │ gamesync    │
       │                              │       └──────┬──────┘
       │                        ┌─────┴──────────────┴─┐        ┌───────────────────────┐
       │                        ▼                      ▼        │ dashboard  (operator) │
       │                 ┌────────────┐         ┌────────────┐  │ toolbox    (the .nro) │
       │                 │ PostgreSQL │         │ Redis      │  └───────────────────────┘
       │                 └────────────┘         └────────────┘
       │  UDP
       └───────────────► natcheck (NAT type), STUN (latency) — for peer-to-peer play
```

## Scope, and what this is not

This is for a private group of consenting friends running consoles they own and
games they own.

**GRID0+ ships no Nintendo code, keys or data, and reproduces none.** It signs
its own tokens with its own keys. It cannot and does not produce anything real
Nintendo servers would accept.

It also does not verify Nintendo's credentials, and that is deliberate: device
certificates, game tickets and gamecard challenges are accepted without being
looked at, because verifying them would require Nintendo key material and because
on a private network of consoles you own there is nobody to prove ownership to.
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md#what-is-deliberately-not-verified)
has the full table of what is and is not checked, and why.

It is not a way to play unowned games, and not a way to get anything online
against Nintendo's own servers.

## Quickstart

```sh
cp config/config.example.yaml config/config.yaml
$EDITOR config/config.yaml               # set network.public_ip and keys.device_id_secret
                                          #   (openssl rand -hex 32 for the latter)

make config-check                        # validate, and warn about risky settings
make up                                  # Postgres, Redis and nginx
make gen-certs                           # local CA and edge certificate
make gen-nginx                           # the TLS edge configuration
make run &                               # the nine Go services, natively
make seed                                # exercise the whole chain over HTTP
```

`make seed` walks the real API — device token, application token, anonymous
token, register, log in, friend code, friend request, accept, presence — and
prints the resulting friend list. If it completes, everything except DNS and TLS
is working.

`make seed-phase2` does the same for the game server: it registers two accounts
through BAAS, exchanges the id tokens BAAS hands out for NPLN sessions, queues
both players, and checks that they are told about **the same** session — then
drives `gamesync` and reads back the announced Splatfest. Two players told about
different sessions is a lobby that never fills, and it looks fine from either
side alone, which is why that is the assertion.

Then: [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) for the nginx edge and the
address/port checklist, and [`switch/README.md`](switch/README.md) for pointing a
console at it.

`make help` lists every target.

Phase 2 adds one command worth knowing about:

```sh
switchnetctl fest build -assets ./fest-assets -out ./seed -enable
```

That builds a Splatfest's BCAT packs, derives the revision from their bytes,
writes the seed folder for the consoles, generates the pack keys, and registers
the fest the schedule announces — in one step, because those four values appear
in two places each and any path where a human types one of them twice eventually
produces `BcatInvalid`, which the player sees as a generic communication error
naming nothing.

## Redeploying an existing install

The nine Go services run as native processes (via systemd in production, via
`make run`/`switchnetctl run` when iterating) — Docker is only for Postgres,
Redis and the nginx edge, none of which this repo builds. On the always-on
box, install the units once:

```sh
scripts/install-systemd.sh       # from /opt/switchnet: creates the switchnet
                                  # user, installs deploy/systemd/*.service,
                                  # enables switchnet.target
```

Day-to-day operation from then on is usually "pull and restart", not the full
bring-up:

```sh
scripts/start.sh                 # (re)start everything -- same preflight as
                                  # make up (cert/nginx-config generation, a
                                  # config validation pass), then
                                  # `docker compose up -d` for postgres/redis/
                                  # nginx and `systemctl start switchnet.target`
                                  # for the Go services, then waits on baas's
                                  # /healthz before printing next steps
scripts/update.sh                # pull, then restart only what the change reaches
scripts/update.sh --dry-run      # say what would restart, change nothing
scripts/update.sh --all          # restart every service regardless
```

`update.sh` reads each service's own Go package graph (`go list -deps`), not
directory names, so a change to `internal/db` restarts every service that
imports it and a change to `internal/baas` restarts only `baas`. It refuses to
run anywhere but an `/opt/switchnet` checkout (systemd's unit files hardcode
that path) and refuses with uncommitted changes in the tree. Before
restarting anything it runs `make check` — the replacement for the old
Dockerfile's `go vet && go test` image-build gate, since there is no image
build left in the loop to stop a broken commit from reaching
`systemctl restart` (which would otherwise just crash-loop it, because the
units set `Restart=on-failure`). nginx gets a config reload rather than a
restart, since it serves generated configuration and static files and a
reload does not drop in-flight connections. `journalctl -u switchnet-<svc> -n
50 --no-pager` is how you read a native service's logs; `docker compose logs
--tail=50 nginx` (or `postgres`/`redis`) for the three Dockerized pieces.

## Contributing, or picking this up as an agent

[`CLAUDE.md`](CLAUDE.md) is the orientation document: the two real hardware bugs
this codebase is shaped around, the things that will waste your time, and where
the guesses are. Read it before changing anything under `internal/baas`.

## How a console is pointed at it

Three separate problems, and most guides conflate them. Full detail, and the
alternatives, in [`switch/README.md`](switch/README.md).

**What this deployment actually runs:** the GRID0+ Toolbox homebrew
(the `toolbox/` folder of
[`net-GRID0/cfw-GRID0plus`](https://github.com/net-GRID0/cfw-GRID0plus), private), on an emuMMC that only ever talks to GRID0+. One menu entry
writes Atmosphère's `hosts/{sysmmc,emummc}.txt` (generated from
`internal/nintendo/hosts.go`), installs the certificate-verification IPS
patches and Splatoon 3's own patches, and reboots. "Default" mode restores
the previous hosts folder.

**Redirection.** Either Atmosphère's DNS-MITM hosts file, as above, or no SD
card change at all: add the generated `dnsmasq`/`unbound` configuration
(`make gen-dns`) to a resolver you already run and hand it out over DHCP.

**Certificate trust** has to happen on the console: it asks for
`dauth-lp1.ndas.srv.nintendo.net`, whose certificate is signed by a private
Nintendo CA. The Toolbox uses the public `disable_ca_verification` and
`disable_browser_ca_verification` patches, which are matched on the firmware
build id and fail silently when it does not match. `switch/README.md`
documents a narrower route — extending the console's own CA store as a
LayeredFS data file — whose one unconfirmed step is written down there.

**Splatoon 3 is a third problem.** It links its own TLS stack and pins its
own certificates, so neither of the above touches it. The Toolbox ships
patches for v11.3.0 (certificate pin, gRPC peer and verify options); how they
were found is in cfw-GRID0plus's `toolbox/docs/game-specific-tls-pinning.md`. For a
different build, `make patch-splatoon3` builds the upstream patch from your
own dump:

```sh
make patch-splatoon3 NSO=/path/to/main            # into Ryujinx's mods directory
make patch-splatoon3 NSO=/path/to/main SD=/mnt/sd # onto an Atmosphère SD card
```

Every one of these patches is matched on a build id, so a firmware or title
update silently stops it applying.

## The one setting people get wrong

**`nncs1` and `nncs2` must be two different IP addresses.** Pia probes them as
separate servers and one NAT-check message type has to be answered from a
different address. Point both at one IP and the console de-duplicates the probe,
never finishes NAT detection, and matchmaking finds matches that will not start.
Set `network.natcheck_secondary_ip`; `make config-check` warns if you have not.

## What phase 1 implements

| Feature | Endpoint |
| --- | --- |
| Device tokens, all API versions v1–v8 | `POST /v{1..8}/device_auth_token`, `/challenge`, `/edge_token` |
| Application tokens, v1–v5 | `POST /v{1..5}/application_auth_token`, `/challenge`, `GET /v1/time` |
| Anonymous access token | `POST /1.0.0/application/token` |
| Register, log in, federate | `POST /1.0.0/users`, `/login`, `/federation` |
| Profile: nickname, country, birthday, privacy | `PATCH /1.0.0/users/<id>` |
| Friend codes, with the 24-hour cooldown | `POST /1.0.0/users/<id>/generate_code` |
| Friend requests: send, accept, deny, cancel | `POST /2.0.0/friend_requests`, `PATCH …/<id>` |
| Inbox and outbox | `GET /2.0.0/users/<id>/friend_requests/{inbox,outbox}` |
| Friend list, favourites, unfriend | `GET`/`PATCH`/`DELETE /2.0.0/users/<id>/friends…` |
| Relationships | `GET /2.0.0/users/<id>/relationships/<id>` |
| Presence, with title id | `PATCH /1.0.0/users/<id>/device_accounts/<id>` |
| Blocking | `GET`/`POST`/`DELETE /1.0.0/users/<id>/blocks…` |
| Profile pictures | `POST /1.0.0/image_upload` |
| JWK sets, for game servers | `GET /1.0.0/certificates`, `/internal_certificates` |
| NAT check | UDP 10025 / 10125, message types 1–5 and 101–103 |
| Stubs | ctest, frs, five, op2, dragons, penne, npns, znc, service-status |

## What phase 2 implements

Running on a real console up to Splatoon 3's hall — see
[`docs/ROADMAP.md`](docs/ROADMAP.md#phase-2--splatoon-3-npln--dashboard) and
[`TODO.md`](TODO.md) for exactly what within each row below is real versus
honestly-empty.

| Feature | Where |
| --- | --- |
| The JSON front end (device init, login tickets, frontline) | `cmd/npln`'s second listener, `internal/nplnrest` |
| NPLN auth, friends, presence, messaging | `internal/npln` |
| Matchmaking, sessions, ICE server allocation | `internal/matchmaking`, `internal/npln/matchmaking.go` |
| Match history | `npln_matches`, read by the dashboard |
| `gamesync` (Firestore-shaped document/watch service, on its own port) | `internal/gamesync` |
| Splatfests, map/mode + Salmon Run schedules, per-player cloud save, the rest of `toyohr` | `internal/toyohr` |
| Splatoon 3's per-player document store (GameRecord and friends) | `internal/ugcstore` |
| Latency measurement (STUN), before matchmaking | `internal/stunresponder` |
| BCAT pack building (serving it natively is [`TODO.md`](TODO.md) §2.4) | `internal/bcat` |
| Dashboard: players, matchmaking, Splatfests, match history, Debug tab | `internal/dashboard`, `cmd/dashboard` |

## What Splatoon 2 support implements

Splatoon 2 is a NEX title: the stack Nintendo's games used before NPLN,
PRUDP over a TLS WebSocket. `cmd/nex` serves it, built from the public
protocol documentation (Kinnay's wiki and NintendoClients), Pretendo's
AGPL servers for semantics, and the measured behaviour NextendoNetwork
published about Splatoon 2 (facts only; none of its code). Exercised end to
end with Kinnay's NintendoClients against the running services — login,
secure connection, two consoles matched into one lobby with their
notifications — **not yet tried on a console**. See
[`internal/splatoon2/README.md`](internal/splatoon2/README.md) for every
method and [`docs/CAPTURES/README.md`](docs/CAPTURES/README.md#splatoon-2-nex)
for the shapes that are still readings.

| Feature | Where |
| --- | --- |
| PRUDP-Lite over WebSocket, RMC, NEX types, Kerberos tickets | `internal/nex` |
| Login with a GRID0+ id token, fail-closed (the PID *is* the user id) | `internal/rendezvous/auth.go`, `internal/identity` |
| Matchmaking: auto-matchmake, browse, create/join, friend-join, host and owner migration, notifications | `internal/rendezvous` |
| NAT traversal: probes, NAT-aware matching, the NAT-check bridge, an optional per-pair relay | `internal/rendezvous`, `internal/natobs` |
| Ranking (standard and Splatoon 2's league, Splatfest, X Power, event methods) | `internal/rendezvous/ranking.go`, `internal/splatoon2/ranking.go` |
| DataStore: match results, Annie's gear orders, Splatfest plaza posts, play logs | `internal/splatoon2/datastore.go` |
| Match history beside Splatoon 3's; presence while playing | `internal/splatoon2`, `internal/rendezvous/presence.go` |
| Dashboard: its own side of the page -- overview, lobbies, matches and results, Splatfests, league rankings, plaza moderation, Annie's orders, calls and relay | `internal/dashboard`, the Splatoon 2 switch |

## What phase 4 implements

OLSC (Online Save Cloud) — the OS-level per-game save-data backup tied to a
Nintendo Switch Online subscription, **not** the same thing as Splatoon 3's
own `CloudSave` save record above. Not yet touched by a real console, and
built against public documentation rather than a capture — see
[`internal/scsi/README.md`](internal/scsi/README.md) for exactly which
endpoints are confirmed versus this project's own defensible reading of an
undocumented one, and `docs/CAPTURES/README.md`'s Phase 4 section for what is
still open.

| Feature | Endpoint |
| --- | --- |
| List / get a save data archive | `GET /api/nx/v2/save_data_archives`, `.../<id>` |
| Start / finish a backup upload | `POST .../start_upload`, `.../<id>/finish_upload` |
| Upload / finish one component file | this project's own `PUT .../component_files/<id>/data`, `POST .../finish_upload` |
| Restore (start/finish download) | `GET .../<id>/start_download`, `POST .../<id>/finish_download` |
| Delete an archive | `POST .../<id>/delete` |
| Key material for a restore | `POST .../generate_key_seed_package`, `permission.lp1.sata…/generate_key_package` |
| Local-disk storage, with a per-user quota | `internal/scsi/storage.go` |

## Repository layout

```
config/          configuration loading, and config.example.yaml
cmd/             one directory per binary, plus switchnetctl
internal/
  nintendo/      the Nintendo host list — one source of truth for the
                 dnsmasq/unbound generators, the nginx map, the certificate
                 SANs and the hosts file
  jwt/           token issuance, verification and JWK sets
  db/            schema, the Store contract, PostgreSQL and in-memory stores
  dauth/         device authentication
  aauth/         application authentication
  baas/          accounts, friends, presence, blocks, images (phase 1)
  scsi/          OLSC cloud save backup, local-disk storage (phase 4)
  presencebus/   presence pub/sub, in-process or Redis
  presence/      the shared TTL/visibility rules BAAS and NPLN presence both read
  stubs/         the catch-all
  natcheck/      the NAT-check UDP responder
  service/       shared startup: logging, signals, graceful shutdown

  # phase 2 — Splatoon 3 (NPLN); see docs/ROADMAP.md#phase-2
  npln/          the NPLN gRPC gateway: auth, friends, presence, messaging,
                 matchmaking
  nplnrest/      the JSON front end (vermillion, penne, frontline) the game
                 talks to before it opens the gRPC channel
  matchmaking/   the match queue and session formation behind npln's gRPC
  gamesync/      nn.npln.gamesync.v1.Gamesync, a Firestore-shaped document/
                 watch service -- its own port, its own listener
  toyohr/        Splatoon 3's own services: Splatfests, Schedule (incl. a
                 real Salmon Run rotation), CloudSave, and the rest
  ugcstore/      Splatoon 3's per-player document store (GameRecord etc.)
  stunresponder/ STUN Binding answers for the game's latency measurement
  splatdiag/     opt-in one-account comparison against private records
                 (the records themselves are never in the repository)
  messaging/     per-user mailbox, behind npln's Messaging
  bcat/          builds and encrypts the BCAT packs custom Splatfest content
                 ships in
  wirelog/       request transcripts, for the protocols nobody has documented
  health/        /healthz for every service, read by the dashboard
  toolbox/       the public API the GRID0+ Toolbox .nro asks for updates
                 and server status
  dashboard/     the operator's web UI: players, matchmaking, Splatfests,
                 match history, Splatoon 2, a Debug tab

  # Splatoon 2 (NEX); see internal/splatoon2/README.md
  nex/           PRUDP-Lite, the WebSocket transport, RMC, NEX types, Kerberos
  rendezvous/    a NEX game server: login, secure connection, matchmaking,
                 NAT traversal, notifications, Utility, Ranking, the relay
  splatoon2/     Splatoon 2's own methods, DataStore, and its operator API
  identity/      the id-token check NEX and NPLN logins share
  natobs/        natcheck's NAT observations, shared with nex through Redis
switch/          console-side setup and the generated hosts file
scripts/         start.sh, update.sh, install-systemd.sh, gen-certs.sh,
                 seed-dev-data.sh, seed-phase2.sh, smoke-npln/,
                 capture-baas.py, build-certstore.py
docs/            roadmap, architecture, deployment, packet capture, captures
```

Every package under `internal/` has a doc comment saying which Nintendo endpoint
it reimplements and linking the wiki page it was built from.

## Development

Iterating on one service — a `go build`, not a full rebuild — while the still-
Dockerized Postgres/Redis keep running:

```sh
make up                     # Postgres, Redis and nginx, if not already up
bin/switchnetctl run baas   # or: dauth, aauth, npln, dashboard, ...
```

`make run` (no args) does the same for all nine at once, in the foreground,
with each service's output prefixed by name; Ctrl-C stops all of them. Unlike
the systemd units used in production, this never restarts a crashed child —
you want to see a crash immediately while coding, not have it silently come
back.

Before sending anything, run the whole verification suite:

```sh
make check        # gofmt, go vet, go test -- what CI runs
make test-race    # the same tests, with the race detector -- the servers are
                   #   concurrent by nature, so a data race is the more likely
                   #   kind of bug in this codebase than a plain logic error
make test-cover   # coverage per package
make build        # compile every service into bin/
```

Tests run with no database and no Redis: the handler tests use an in-memory store
and finish in seconds. To also run the integration tests:

```sh
make test TEST_DATABASE_URL="postgres://…" TEST_REDIS_URL="redis://…"
```

Both store implementations run the *same* contract test, so the in-memory one the
handler tests use cannot drift from the PostgreSQL one that runs in production.

None of the above starts a process or touches the network — they're static
checks against the source. To confirm the whole thing actually *runs*, end to
end (CLAUDE.md's "run it, do not just test it": several real bugs here were
invisible to a green test suite and obvious within thirty seconds of starting
the processes):

```sh
make up            # Postgres, Redis and nginx
make run &         # the nine Go services, natively
make seed          # exercises phase 1 over the real API -- device token,
                    #   application token, anonymous token, register, log in,
                    #   friend code, friend request, accept, presence -- and
                    #   prints the resulting friend list
make seed-phase2    # exercises phase 2 -- registers two accounts, exchanges
                    #   their BAAS id tokens for NPLN sessions, queues both,
                    #   checks they're told about the *same* match, then
                    #   drives gamesync and reads back the announced Splatfest
```

Then check that the generated artefacts still generate (a hostname added
anywhere other than `internal/nintendo/hosts.go` produces a failure with no
useful error, so this is worth re-running after any change that touches
hostnames):

```sh
SWITCHNET_PUBLIC_IP=192.168.1.50 SWITCHNET_NATCHECK_SECONDARY_IP=192.168.1.51 \
  go run ./cmd/switchnetctl hosts | head
```

And that nothing that should not be tracked is:

```sh
git ls-files | grep -E '\.(pem|key|crt)$|^certs/|config/config\.yaml'   # must be empty
```

`make help` lists every target, including `gen-certs`/`gen-nginx`/`gen-dns`/
`gen-hosts` (the generated artefacts above, run individually),
`config-check` (validate `config.yaml` and warn about risky settings), and
`install-systemd` (production only — see
[Redeploying an existing install](#redeploying-an-existing-install)).

### Where the guesses are

The friends `2.0.0` endpoints are not documented anywhere and no public
implementation exists to compare against. Every guessed request or response shape
carries a `TODO(capture-needed):` comment saying what a capture would settle and
which single function to change:

```sh
grep -rn 'TODO(capture-needed)' internal/
```

[`docs/CAPTURES/README.md`](docs/CAPTURES/README.md) lists them in priority order.

### Settling them

Nothing here is captured from Nintendo *through the console*: this
deployment's console never runs Atmosphère against Nintendo's servers, so a
capture of Nintendo from it is a tcpdump at most. Shapes are settled by
reverse engineering, live GDB on the emuMMC, captures of the console talking
to GRID0+, and facts other projects have published.

For BAAS specifically, `scripts/capture-baas.py` answers most of them in one command. It talks to
Nintendo's real BAAS from a PC using your console's credentials, so **the console
is never involved** — and in particular never runs with certificate verification
disabled while talking to Nintendo, which is the main risk in the usual
mitmproxy-against-the-console approach.

```sh
python3 scripts/capture-baas.py --parse-save /path/to/su/baas/xxxxx.dat
python3 scripts/capture-baas.py --keys prod.keys --prodinfo PRODINFO \
    --device-account-id <16 hex> --password "<40 chars>" --country NL
```

[`docs/PACKET_CAPTURE.md`](docs/PACKET_CAPTURE.md) covers that, the console-MITM
route and its ban risk, and NPLN's gRPC capture for phase 2.

## Credits

Protocol knowledge comes almost entirely from
[Kinnay's NintendoClients wiki](https://github.com/kinnay/NintendoClients/wiki)
and the `NintendoClients` library itself, which is where the real presence request
shape came from. The Atmosphère host list and the certificate-trust approach come
from [Nextendo's Prelude-Nro](https://github.com/NextendoNetwork/Prelude-Nro).
Much of phase 2's Splatoon 3 behaviour is checked against facts recorded by
[NextendoNetwork/splatoon-3](https://github.com/NextendoNetwork/splatoon-3) —
facts only, never its PolyForm Shield-licensed code. Gear, stage and weapon
ids come from [Leanny/splat3](https://github.com/Leanny/splat3)'s public data
dump.

Both the identity design and the presence design in this repository are shaped by
a write-up of two real failures on real hardware — every console acting as one
account, and friends who were never online — documented in
[`n-popescu/splatoon-3`](https://github.com/n-popescu/splatoon-3)'s
`docs/FRIENDS.md`. GRID0+ is the component that write-up was asking for, and
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md#identity-and-the-bug-this-design-exists-to-avoid)
explains what was done differently.

## Licence

[AGPL-3.0](LICENSE), matching Prelude-Nro.
