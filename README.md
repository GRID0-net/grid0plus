<h1 align="center">SwitchNet</h1>

<p align="center">
  <b>A self-hosted replacement for Nintendo's online infrastructure, for a private group of modded Switch consoles.</b>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/license-AGPL--3.0-blue" alt="License: AGPL-3.0">
  <img src="https://img.shields.io/badge/go-1.24%2B-00ADD8" alt="Go 1.24+">
  <img src="https://img.shields.io/badge/phase%201-code%20complete-brightgreen" alt="Phase 1 code complete">
  <img src="https://img.shields.io/badge/phase%202-not%20started-lightgrey" alt="Phase 2">
  <img src="https://img.shields.io/badge/phase%203-not%20started-lightgrey" alt="Phase 3">
</p>

---

## Running it

```sh
cp config/config.example.yaml config/config.yaml   # then edit it
scripts/start.sh                                   # start everything
scripts/update.sh                                  # pull, restart only what changed
```

`start.sh` generates the edge certificate and the nginx configuration if they
are missing, validates the config, brings the stack up and waits until it is
actually answering — `docker compose ps` saying "running" is not the same thing.

`update.sh` works out which services a change reaches by reading each binary's
Go package graph, so a change to `internal/baas` restarts only baas and a change
to `internal/db` restarts everything that imports it. `--dry-run` says what it
would do.

The dashboard's **Health** tab asks every service directly and shows what each
one's own dependencies are doing.

## What this is

Accounts, friend codes, friend requests, friend lists and presence — including
"Playing Splatoon 3" — working through the **stock** Nintendo friends applet, on
consoles you own, against a server you run.

Three phases, in [`docs/ROADMAP.md`](docs/ROADMAP.md):

| Phase | Scope | State |
| --- | --- | --- |
| **1** | accounts, friend codes, friends, presence | code complete, awaiting hardware verification |
| **2** | NPLN game server for Splatoon 3, web dashboard | not started |
| **3** | eShop, served from local `.NSP` dumps of games you own | not started |

Phase 1 is what this repository contains today.

```
┌──────────────┐   HTTPS, Nintendo hostnames    ┌─────────────────────────────┐
│  Switch      │ ─────────────────────────────► │  nginx, TLS + SNI routing   │
│  Home Menu   │   redirected by SwitchNet's     └────────────┬────────────────┘
│  friends     │   DNS -- nothing on the SD card              │
└──────────────┘                          ┌───────────────────┼─────────────────┐
                                          ▼                   ▼                 ▼
                                  ┌───────────────┐   ┌───────────────┐  ┌──────────────┐
                                  │ dauth         │   │ baas          │  │ stubs        │
                                  │ device tokens │   │ accounts      │  │ ctest, frs,  │
                                  ├───────────────┤   │ friends       │  │ op2, penne…  │
                                  │ aauth         │   │ presence      │  └──────────────┘
                                  │ app tokens    │   └───────┬───────┘
                                  └───────────────┘           │
                                                    ┌─────────┴─────────┐
                                                    ▼                   ▼
                                             ┌────────────┐      ┌────────────┐
                                             │ PostgreSQL │      │ Redis      │
                                             │ accounts,  │      │ presence   │
                                             │ friends    │      │ events     │
                                             └────────────┘      └────────────┘

                                  ┌──────────────────────────────────────────┐
                                  │ dns (UDP/TCP 53)                         │
                                  │ answers Nintendo's hostnames with your   │
                                  │ address, forwards everything else        │
                                  ├──────────────────────────────────────────┤
                                  │ natcheck (UDP)                           │
                                  │ so the consoles can find each other for  │
                                  │ peer-to-peer play                        │
                                  └──────────────────────────────────────────┘
```

## Scope, and what this is not

This is for a private group of consenting friends running consoles they own and
games they own.

**SwitchNet ships no Nintendo code, keys or data, and reproduces none.** It signs
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
$EDITOR config/config.yaml               # set network.public_ip

echo "SWITCHNET_DEVICE_ID_SECRET=$(openssl rand -hex 32)" > .env

make config-check                        # validate, and warn about risky settings
make up                                  # Postgres, Redis, DNS and the services
make gen-certs                           # local CA and edge certificate
make gen-nginx                           # the TLS edge configuration
make seed                                # exercise the whole chain over HTTP
```

`make seed` walks the real API — device token, application token, anonymous
token, register, log in, friend code, friend request, accept, presence — and
prints the resulting friend list. If it completes, everything except DNS and TLS
is working.

Then: [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) for the nginx edge and the
address/port checklist, and [`switch/README.md`](switch/README.md) for pointing a
console at it.

`make help` lists every target.

## Contributing, or picking this up as an agent

[`CLAUDE.md`](CLAUDE.md) is the orientation document: the two real hardware bugs
this codebase is shaped around, the things that will waste your time, and where
the guesses are. Read it before changing anything under `internal/baas`.

## How a console is pointed at it

Two separate problems, with very different answers — and most guides conflate them:

**Redirection happens off the console.** A Switch resolves hostnames through
whatever DNS its network gives it, in stock firmware. So there is no hosts file,
no `enable_dns_mitm`, and nothing on the SD card for this half. Either point your
router's DHCP at SwitchNet's `dns` service — in which case the console needs *no
configuration at all* — set DNS manually in the console's own network settings, or
add the generated `dnsmasq`/`unbound` configuration to a resolver you already run.

**Certificate trust does need the console**, and there is no way around it: the
console asks for `dauth-lp1.ndas.srv.nintendo.net`, whose certificate is signed by
a private Nintendo CA. The usual answer is an Atmosphère `disable_ca_verification`
exefs patch, which is gated on the firmware build id, fails *silently* when it
does not match, and turns off certificate validation system-wide. SwitchNet
documents a better route — extending the console's own CA store, which is a
LayeredFS data file rather than a code patch — with an honest note about the part
of it that is not yet confirmed. See
[`switch/README.md`](switch/README.md#part-2-certificate-trust).

**Splatoon 3 is a third problem**, and neither of the above touches it. It carries
its own TLS stack and talks over a raw socket, never calling the console's `ssl`
service — so it does not consult the CA store, and it is unaffected by anything
you install for the two problems above. It needs a patch to its own executable,
which has to be built against your own dump because the address it patches is
found by searching that dump:

```sh
make patch-splatoon3 NSO=/path/to/main            # into Ryujinx's mods directory
make patch-splatoon3 NSO=/path/to/main SD=/mnt/sd # onto an Atmosphère SD card
```

Ryujinx will dump the NSO for you: right-click the title, Extract Data, ExeFS.
The patch *disables* verification rather than adding trust, so it applies to every
server the game talks to; and it is matched on the build id, so a title update
silently stops it applying and the fix is to re-run the above.

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
| DNS redirection | UDP/TCP 53, with AAAA NODATA for redirected names |

## Repository layout

```
config/          configuration loading, and config.example.yaml
cmd/             one directory per binary, plus switchnetctl
internal/
  nintendo/      the Nintendo host list — one source of truth for DNS, the
                 nginx map, the certificate SANs and the hosts file
  dns/           the DNS server: redirect Nintendo, forward everything else
  jwt/           token issuance, verification and JWK sets
  db/            schema, the Store contract, PostgreSQL and in-memory stores
  dauth/         device authentication
  aauth/         application authentication
  baas/          accounts, friends, presence, blocks, images
  presencebus/   presence pub/sub, in-process or Redis
  stubs/         the catch-all
  natcheck/      the NAT-check UDP responder
  service/       shared startup: logging, signals, graceful shutdown
switch/          console-side setup and the generated hosts file
scripts/         gen-certs.sh, seed-dev-data.sh, capture-baas.py,
                 build-certstore.py
docs/            roadmap, architecture, deployment, packet capture, captures
```

Every package under `internal/` has a doc comment saying which Nintendo endpoint
it reimplements and linking the wiki page it was built from.

## Development

```sh
make check        # gofmt, go vet, go test -- what CI runs
make test-race    # with the race detector
make test-cover   # coverage per package
```

Tests run with no database and no Redis: the handler tests use an in-memory store
and finish in seconds. To also run the integration tests:

```sh
make test TEST_DATABASE_URL="postgres://…" TEST_REDIS_URL="redis://…"
```

Both store implementations run the *same* contract test, so the in-memory one the
handler tests use cannot drift from the PostgreSQL one that runs in production.

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

`scripts/capture-baas.py` answers most of them in one command. It talks to
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

Both the identity design and the presence design in this repository are shaped by
a write-up of two real failures on real hardware — every console acting as one
account, and friends who were never online — documented in
[`n-popescu/splatoon-3`](https://github.com/n-popescu/splatoon-3)'s
`docs/FRIENDS.md`. SwitchNet is the component that write-up was asking for, and
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md#identity-and-the-bug-this-design-exists-to-avoid)
explains what was done differently.

## Licence

[AGPL-3.0](LICENSE), matching Prelude-Nro.
