# GRID0+

GRID0+ is a self-hosted stand-in for the Nintendo servers a Switch talks to
while it is online. It is meant for a small, private group of friends who play
on consoles and games they own.

With it running you get accounts and friend lists that work through the normal
Switch friends applet, a game server for Splatoon 3 (matchmaking, Salmon Run,
Splatfests, cloud save), a game server for Splatoon 2, and a web dashboard for
whoever runs the thing.

GRID0+ contains no Nintendo code, keys or game data. It signs its own tokens with
its own keys and produces nothing that Nintendo's servers would accept. It is
not a way to play games you don't own.

## What it does

| Area | What works |
| --- | --- |
| Accounts and friends | Sign-in, friend codes, friend requests, friend lists and presence ("Playing Splatoon 3") through the stock friends applet. Accounts are created through the Discord bot. |
| Splatoon 3 | Lobby, Turf War, Anarchy, private battles, Salmon Run, Splatfests with BCAT delivery, per-player cloud save, importing an existing save. |
| Splatoon 2 | Matchmaking, rankings, Splatfests and the plaza over NEX. Tested against a client library; not yet confirmed on a console. |
| Cloud save backup | The per-game save backup that normally needs a Nintendo Switch Online subscription. Not yet confirmed on a console. |
| Dashboard | Players, live matchmaking, match history, Splatfests, Splatoon 2 lobbies and rankings, and a health page. |
| Toolbox API | The update and status endpoints the GRID0+ Toolbox homebrew app uses. |

The eShop is not part of this repository.

## How it fits together

```
Switch / Splatoon  --HTTPS-->  nginx (TLS, routes by hostname)
                                  |
      +--------+--------+--------+--------+---------+
      |        |        |        |        |         |
    dauth    aauth     baas     npln    stubs      nex   ...
   (device) (game)  (accounts, (Splatoon 3) (the rest) (Splatoon 2)
                     friends)
                        |        |
                    PostgreSQL  Redis
   UDP:  natcheck (NAT type) and STUN, for peer-to-peer play
```

Postgres, Redis and nginx run in Docker using the stock images. The Go services
run as ordinary processes, as systemd units on a real server or in a terminal
while you work on them. [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) explains
what each piece does and why it is split this way.

## Getting started

You need Go 1.24 or newer, Docker with Compose, and a machine your consoles can
reach.

```sh
cp config/config.example.yaml config/config.yaml
$EDITOR config/config.yaml     # set network.public_ip and keys.device_id_secret
                               # (openssl rand -hex 32 makes a good secret)

make config-check              # validates the file and warns about risky settings
make up                        # Postgres, Redis and nginx
make gen-certs                 # a local CA and the edge certificate
make gen-nginx                 # the nginx configuration
make run                       # all Go services, in the foreground
```

In a second terminal, `make seed` walks the whole account chain over HTTP
(device token, sign-up, friend code, friend request, presence) and prints the
resulting friend list. `make seed-npln` does the same for the Splatoon 3 game
server. If both finish, everything except DNS and TLS on the console side is
working.

For a real deployment, see [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md). To point a
console at the server, see [docs/CONSOLE_SETUP.md](docs/CONSOLE_SETUP.md).
`make help` lists every make target.

## Documentation

| Document | Read it when you want to |
| --- | --- |
| [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) | install on a server, open the right ports, run it day to day |
| [docs/CONFIGURATION.md](docs/CONFIGURATION.md) | understand the settings in `config.yaml` |
| [docs/CONSOLE_SETUP.md](docs/CONSOLE_SETUP.md) | get a console talking to the server (DNS, certificates, Splatoon 3 patches) |
| [docs/SPLATFESTS.md](docs/SPLATFESTS.md) | build and run a Splatfest |
| [docs/DISCORD_BOT.md](docs/DISCORD_BOT.md) | connect the Discord bot and use its API |
| [docs/TOOLBOX.md](docs/TOOLBOX.md) | set up the Toolbox update and status service |
| [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) | work out why something isn't working |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | understand how the pieces fit and why |
| [CONTRIBUTING.md](CONTRIBUTING.md) | change the code |
| [SECURITY.md](SECURITY.md) | report a problem, or see what the server does and doesn't protect |

Each package under `internal/` has its own `README.md` describing what it does,
which Nintendo endpoints it stands in for, and any behaviour worth knowing
before you change it.

## Repository layout

```
cmd/          one directory per service, plus switchnetctl (the admin tool)
config/       configuration loading and config.example.yaml
deploy/       nginx snippets, systemd units, the keepalive helper
docs/         the documents listed above
internal/     the implementation; one package per concern, each with a README
scripts/      install, start, update, certificate and test helpers
switch/       console-side files (the BCAT patch and the cache installer)
```

## Development

```sh
make check        # gofmt, go vet and go test: what CI runs
make test-race    # the same tests with the race detector
make build        # compile every service into bin/
```

The tests need no database or Redis. To also run the integration tests, set
`TEST_DATABASE_URL` and `TEST_REDIS_URL`. See [CONTRIBUTING.md](CONTRIBUTING.md)
for the rest, including how to run one service at a time.

## Credits

Most of what is known about the Switch's online protocols comes from
[Kinnay's NintendoClients wiki](https://github.com/kinnay/NintendoClients/wiki)
and the NintendoClients library. Splatoon 3 gear, stage and weapon ids come from
[Leanny's splat3 data](https://github.com/Leanny/splat3).

## Licence

[AGPL-3.0](LICENSE).

## INKLINK player companion

The optional Afterhours player website uses existing Grid0 accounts, histories,
friends and rotations, separately from the operator dashboard.
See [setup, demo, and current data limits](docs/INKLINK.md).
