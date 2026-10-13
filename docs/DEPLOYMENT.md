# Deployment

Getting GRID0+ onto a machine your consoles can reach.

- [Local, on one machine](#local-on-one-machine)
- [The nginx TLS edge](#the-nginx-tls-edge)
- [Addresses and ports](#addresses-and-ports)
- [Checklist: from nothing to a console with a friend](#checklist-from-nothing-to-a-console-with-a-friend)
- [Operating it](#operating-it)
- [Security](#security)

---

## Local, on one machine

Postgres, Redis and the nginx TLS edge run in Docker (off-the-shelf images,
never rebuilt from this repo). The nine Go services run natively: as
systemd units on the box that actually serves your consoles, installed
once with `scripts/install-systemd.sh`.

```sh
git clone https://github.com/n-popescu/grid0plus /opt/switchnet && cd /opt/switchnet

cp config/config.example.yaml config/config.yaml
$EDITOR config/config.yaml            # network.public_ip and keys.device_id_secret
                                       # (openssl rand -hex 32 for the latter) are the
                                       # two you must set yourself

make config-check                     # validates and warns about risky settings
make up                               # Postgres, Redis and nginx
make gen-certs                        # the local CA and the edge certificate
make gen-switch                       # switch/hosts.txt and the nginx config
scripts/install-systemd.sh            # builds every service, installs the units
sudo systemctl start switchnet.target # starts all eight
```

The clone location matters: `deploy/systemd/*.service` hardcode
`/opt/switchnet` (`WorkingDirectory=`, `ExecStart=`), since a static unit
file can't discover an arbitrary checkout path at install time.
`install-systemd.sh` refuses to run from anywhere else.

`make config-check` is worth running first. It refuses a configuration that
cannot work and warns about the ones that are valid but cause trouble — most
importantly `natcheck_secondary_ip` being unset.

Then check the chain works before involving a console:

```sh
make seed
```

That drives the real HTTP API — device token, application token, anonymous token,
register, log in, friend code, friend request, accept, presence — and prints the
resulting friend list. If it completes, everything except DNS and TLS is correct.

## The nginx TLS edge

The console connects to Nintendo hostnames on port 443 and picks the backend by
SNI. One nginx in front of the services does that routing.

```sh
make gen-nginx        # writes deploy/nginx.generated.conf
```

Do not hand-edit the result: it is generated from the same host list as the
console's hosts file and the certificate SANs, and the next regeneration loses
the edit. To add a host, add it to `internal/nintendo/hosts.go` and regenerate
all three.

Install it:

```sh
sudo mkdir -p /etc/switchnet/certs
sudo cp certs/edge.crt certs/edge.key /etc/switchnet/certs/
sudo chmod 600 /etc/switchnet/certs/edge.key
sudo cp deploy/nginx.generated.conf /etc/nginx/conf.d/switchnet.conf
sudo nginx -t && sudo systemctl reload nginx
```

Two things in the generated config are load-bearing:

**`ssl_verify_client optional_no_ca`.** The console presents its own device
certificate to dauth, and from firmware 21.0.0 to `m-lp1.baas`. It is signed by
Nintendo, so it cannot be validated here. `optional_no_ca` asks for it and passes
the fingerprint through without validating it, which is what gives each console a
stable device id. Nothing is authorised on that id — accounts are keyed on the
BAAS device account — so without this everything still works, but the dashboard's
device list becomes a single row.

**A `default_server` block.** A hostname that was redirected on the console but
has no block here would otherwise fall through to whichever block nginx saw
first, and get an answer from the wrong service. The generated config sends
unmatched hosts to the stub server, which logs them. That log is the to-do list.

## Addresses and ports

GRID0+ does not run its own DNS server or listen on port 53 anywhere; a
console finds it via a resolver you already run, configured with
`make gen-dns` (see [`switch/README.md`](../switch/README.md#part-1-redirection-with-dns)).

| Port | Protocol | Faces | What |
| --- | --- | --- | --- |
| 443 | TCP | **the consoles** | the nginx TLS edge |
| **7575** | TCP | **the consoles** | the nginx edge again, for NPLN's `gamesync` |
| **8443** | TCP | **consoles running GRID0+ Toolbox** | the nginx edge again, for `toolbox` — see below |
| 10025, 10125 | **UDP** | **the consoles** | NAT check (phase 1 titles; **not** Splatoon 3) |
| 3478 | UDP and TCP | **the consoles** | STUN/TURN, if you run one — Splatoon 3 uses ICE |
| 8081–8084 | TCP | localhost only | phase 1's services, behind nginx |
| 8085–8087 | TCP | localhost only | phase 2's: gateway, JSON front end, dashboard |
| 8088, 8089 | TCP | localhost only | gamesync; npln's matchmaking admin API (off unless configured) |
| 8090 | TCP | localhost only | `toolbox` — see below |
| 8091 | TCP | localhost only | `scsi` — phase 4's OLSC cloud save |
| 8092, 8093 | TCP | localhost only | `nex` (Splatoon 2), and its admin API (off unless configured) |
| 30100–30999 | **UDP** | **the consoles** | the NEX relay, **only if `nex.relay.enabled`** |
| 5432, 6379 | TCP | localhost only | Postgres, Redis |

The UDP rules are the ones people forget. A firewall that only opens TCP gives
you a friends list that works and games that never start a match. The same is
true of UDP 53 on whichever machine runs your resolver — block that and a
console resolves nothing at all.

`8081–8084`'s loopback confinement comes from `config.example.yaml`'s own
defaults (`dauth.listen`, `aauth.listen`, `stubs.listen` are all
`127.0.0.1:<port>`) now that nothing publishes these ports for it — change
one of those to bind-all and it is reachable from the LAN, not just this
machine, so leave them as configured unless you have a specific reason not
to. The same is true of `npln.listen`, `.rest_listen`, `.gamesync_listen` and
`.admin_listen`.

**7575 is the one that is easy to miss.** Splatoon 3 reaches `gamesync` on that
port directly rather than through the tenant host on 443, so the edge needs a
second listener and the firewall needs a second rule. Without it the game
authenticates, opens its gRPC channel, and then cannot sync the lobby.

**Note which NAT mechanism applies to which title.** Phase 1's titles and
Splatoon 2 use the Pia NAT-check pair on 10025/10125. **Splatoon 3 does not** — it uses ICE, so
`natcheck` is not involved and `npln.ice_servers` is what matters. Those
addresses have to be reachable *from the console*, not from the server; getting
them wrong gives a lobby that fills and a match that never starts, which from the
server side looks exactly like a matchmaking bug. `switchnetctl check` warns when
the list is empty, and consoles on one LAN work without it.

### Splatoon 2's process

| Service | Listens | Notes |
| --- | --- | --- |
| `nex` | 8092, and 8093 if `admin_token` is set | every NEX title on one WebSocket listener behind nginx. **Set `redis.url`** for both `natcheck` and `nex`: it is how the NAT-check responder's view of each console's router reaches the game server, and without it a console behind a router that remaps ports is handed to its peers at a port nothing listens on |

The relay (`nex.relay`) is off by default. It carries match traffic through
this machine for pairs of consoles that cannot reach each other, one UDP port
per pair from `port_base`/`port_span` (30100–30999 by default), on
`advertise_ip` (default `network.public_ip`). Open that range to the consoles
only if you enable it, and start with `allow` naming the one player who
cannot connect: `switchnetctl check` warns when it is enabled for nobody.

### Phase 2's processes

| Service | Listens | Notes |
| --- | --- | --- |
| `npln` | 8085, 8086, 8088, and 8089 if `admin_listen` is set | four listeners, one process. gamesync verifies tokens the gateway signs, so splitting it out would mean inventing a protocol between them; the admin API is served only when `npln.admin_token` is set |
| `dashboard` | 8087 | bound to `0.0.0.0`, like `baas`: it is a page for a person's browser on the LAN. **Set `dashboard.token`**, or anybody who can reach it can schedule and cancel Splatfests |

### `toolbox`: the GRID0+ Toolbox homebrew app's own API

**Step-by-step deployment checklist: [`docs/TOOLBOX_SETUP.md`](TOOLBOX_SETUP.md).**

`toolbox` (`internal/toolbox`, listens on `127.0.0.1:8090`) is not a
Nintendo endpoint and deliberately not in `internal/nintendo/hosts.go`'s
redirect list — it answers the GRID0+ Toolbox homebrew app, on its own
edge port (**8443**, `deploy/nginx.toolbox.conf`) rather than a slot in the
generated SNI map, the same reasoning `gamesync` gets its own port instead
of one. It needs no `redis.url`, no database, and no console credential:
see `internal/toolbox`'s own README for why it is unauthenticated by
design.

Set `toolbox.github_owner`, `.github_repo`, `.github_token` (a fine-grained
PAT, `Contents: Read-only`, scoped to only that one repository) and
`.public_base_url` (this service's own `https://<public IP>:8443`, needed to
build a `browser_download_url` a console can actually reach — it cannot be
inferred from the request behind nginx's TLS-terminating proxy) to enable
`/updates/latest` and `/updates/download` — the mechanism that lets
the Toolbox's release source be a private repository, if the operator wants it private, while the homebrew app
still auto-updates. Leave all four empty and `/status` still works;
`/updates/*` answers 503 instead.

**`redis.url` must be set for both**, and it is in `config.yaml`. The
dashboard is a separate process from `npln` and `baas`, and the in-process
presence bus delivers nothing across a process boundary — *silently*. Without it
the live view never updates and the matchmaking tile reports no data. Both
processes warn at start-up, and `switchnetctl check` does too.

**Set `npln.rest_record_dir` before the first console test.** The JSON front end
Splatoon 3 contacts first has undocumented request shapes; with this set, one boot
writes the whole exchange to disk, hexdumped, with credentials redacted. It costs
one line and it is the only record anyone would have.

### The two addresses

**`network.public_ip` and `network.natcheck_secondary_ip` must be different
addresses.**

Pia probes `nncs1` and `nncs2` as two separate servers, and one of the NAT-check
message types has to be answered from a different address. If both hostnames
resolve to the same IP, the console de-duplicates the probe, never sends the
second one, and NAT detection does not complete. In the deployment this was
checked against, that appeared as Mario Kart 8 error 2618-201 and looked nothing
like a NAT problem.

A second address on the same machine is enough:

```sh
# temporary, to try it
sudo ip addr add 192.168.1.51/24 dev eth0

# permanent, systemd-networkd
# /etc/systemd/network/10-eth0.network
#   [Network]
#   Address=192.168.1.50/24
#   Address=192.168.1.51/24
```

Then in `config.yaml`:

```yaml
network:
  public_ip: "192.168.1.50"
  natcheck_secondary_ip: "192.168.1.51"
natcheck:
  alternate_listen: "192.168.1.51:10025"
```

Running with one address works, but consoles report a stricter NAT type than
their router really has, and some titles refuse to matchmake on that basis.

### natcheck runs natively, and that is load-bearing

The protocol works by the console observing *which* address and port
answered. That only works cleanly because `natcheck` is a native process
talking directly on the two addresses it configures (`natcheck.primary_listen`,
`.secondary_listen`, `.alternate_listen`) — there is no NAT or userland proxy
of any kind between it and the network sitting in the way and rewriting the
reply's source address. (This used to be a real footgun when natcheck ran in
Docker: the userland proxy would make every message type look like it came
from the same place, and NAT classification would silently come out wrong in
a way that looked like a router problem. Running natively removes the
failure mode entirely rather than working around it.)

## Checklist: from nothing to a console with a friend

Do these in order. Each rules out everything before it, which is what makes
debugging on hardware tractable.

- [ ] **1. The services answer locally.**
      ```sh
      curl -fsS localhost:8081/keys              | jq .keys[0].kid   # dauth
      curl -fsS localhost:8082/keys              | jq .keys[0].kid   # aauth
      curl -fsS localhost:8083/1.0.0/certificates | jq .keys[0].kid  # baas id
      curl -fsS -H 'Host: ctest.cdn.nintendo.net' localhost:8084/     # -> ok
      ```
      Three *different* key ids. Two the same means two issuers are sharing a
      key.

- [ ] **2. The chain works.** `make seed` completes and prints a friend list with
      a `PLAYING` entry carrying a title id.

- [ ] **3. TLS answers on 443, with the right certificate.** From another machine
      on the console's network:
      ```sh
      curl -vk --resolve dauth-lp1.ndas.srv.nintendo.net:443:<YOUR_IP> \
           https://dauth-lp1.ndas.srv.nintendo.net/keys
      ```
      A completed handshake and a JSON key set. Check the certificate came from
      *your* server: `openssl s_client -connect <YOUR_IP>:443 -servername
      dauth-lp1.ndas.srv.nintendo.net | openssl x509 -noout -issuer` should say
      `SwitchNet Local CA`.

- [ ] **4. Every redirected host resolves to a backend.** For each hostname in
      `switch/hosts.txt`, curl it through `--resolve` and confirm you do not land
      on the stub server's unmatched log. `make gen-nginx` covers them all, so
      this is a check that nginx reloaded, not that the config is right.

- [ ] **4b. DNS resolves, and resolves the right way.** From a machine on the
      console's network:
      ```sh
      dig @<YOUR_IP> dauth-lp1.ndas.srv.nintendo.net A     +short   # -> <YOUR_IP>
      dig @<YOUR_IP> nncs2-lp1.n.n.srv.nintendo.net  A     +short   # -> the SECOND address
      dig @<YOUR_IP> dauth-lp1.ndas.srv.nintendo.net AAAA           # -> NOERROR, no answer
      dig @<YOUR_IP> atumn.hac.lp1.d4c.nintendo.net  A     +short   # -> 0.0.0.0
      dig @<YOUR_IP> example.com                     A     +short   # -> a real address
      ```
      The AAAA and `atumn` checks are the ones people skip, and both are
      misconfigurations that bite later rather than immediately. `atumn`
      resolves to `0.0.0.0` (null-routed), not real Nintendo: this
      deployment's emuMMC is permanently and exclusively GRID0+'s, so
      there is no "switching back to real Nintendo" for the system-update
      hosts to stay passthrough for — see `docs/ROADMAP.md`'s note on this.

- [ ] **5. NAT check answers on UDP.**
      ```sh
      # 16 bytes: message type 1, then three zero fields
      printf '\x00\x00\x00\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00' \
        | timeout 2 nc -u <YOUR_IP> 10025 | xxd
      ```
      16 bytes back, with your source port in bytes 4–7.

- [ ] **6. The console side.** Follow [`../switch/README.md`](../switch/README.md):
      hosts file on both `sysmmc.txt` and `emummc.txt`, `enable_dns_mitm`,
      the `disable_ca_verification` patch **matching your firmware**, and the
      clock set.

- [ ] **7. `baas: registered user` in the log.** DNS, TLS, routing and the whole
      auth chain are correct at that point.

- [ ] **8. Two consoles, a friend code, a request, an accept.** Both should show
      the other online.

- [ ] **9. Launch a game on one.** The other shows "Playing &lt;game&gt;", and
      the log shows `presence reported … state=PLAYING title=…`.

- [ ] **10. Decide on registration.** `baas.allow_registration: false` stops
      both the Discord bot's `/register` and a new console's own placeholder
      account, so a new console cannot join until it is turned back on. With
      the bot, the usual gate is the Discord server itself (and the bot's
      `register_role_id`); turn this off only to freeze the group.

People get their accounts from the Discord bot
([`n-popescu/grid0plus-discord`](https://github.com/n-popescu/grid0plus-discord)):
set `baas.bot_api` (a loopback `listen` and a token from `openssl rand -hex 32`)
and install the bot on the same host, as its README describes. There is no
registration web page any more.

## Operating it

**Back up the key directory.** `certs/keys` holds four RSA keys. Losing them
invalidates every token every console is holding — recoverable, since consoles
re-authenticate, but it also means every already-issued id token stops verifying.
Once it is backed up, set `keys.generate_if_missing: false` so a lost volume
fails loudly instead of silently minting a new identity for the network.

**Back up the database.** It is the accounts, the friend graph and the friend
codes. `pg_dump` on a schedule.

```sh
docker compose exec postgres pg_dump -U switchnet switchnet | gzip > backup.sql.gz
```

**Migrations** run on startup when `database.auto_migrate` is on. The files are
also golang-migrate compatible if you prefer to run them yourself:

```sh
migrate -path internal/db/migrations -database "$SWITCHNET_DATABASE_URL" up
```

**Logs.** `make logs` follows Postgres/Redis/nginx. The Go services log to
journald: `journalctl -u switchnet-baas -f` for one, `journalctl -u
'switchnet-*' -f` for all eight together. `log.level: debug` logs every
NAT-check datagram, which is a lot, and is the right setting while bringing
a console up. `stubs.log_bodies` logs what unimplemented endpoints are being
asked for — invaluable exactly once, far too chatty to leave on.

**Upgrades.** `scripts/update.sh`, from `/opt/switchnet`: pulls, works out
which services the change actually reaches (by reading each binary's Go
package graph, not by guessing from directory names), runs `make check` —
the replacement for what a Docker image build's `go vet`/`go test` step used
to gate on, now that there is no image build in the loop at all — builds
only what changed, and restarts only those systemd units. `--dry-run` says
what it would do without doing it; `--all` forces every service regardless.

### systemd

`scripts/install-systemd.sh` (from `/opt/switchnet`, once) creates a
dedicated `switchnet` system user, builds every service, and installs
`deploy/systemd/switchnet-*.service` plus a grouping `switchnet.target`.

```sh
sudo systemctl start switchnet.target     # start all eight
sudo systemctl status switchnet.target    # is everything up
sudo systemctl restart switchnet-baas     # restart just one
journalctl -u switchnet-baas -f           # its logs
```

Each unit restarts on its own on a crash (`Restart=on-failure`, uncapped --
see the comment on `switchnet-baas.service` for why the usual "5 failures in
10s" throttle is deliberately disabled) without taking any other service
down with it, the same per-service isolation the old Docker setup's
separate containers gave for free.

### The box must not look idle

If this deployment runs on Oracle's Always Free tier, the box has a deadline
nobody wrote a ticket for: Oracle reclaims an instance that looks unused, and
it defines unused as **all three** of the 95th percentile of CPU utilization,
network utilization and (on A1 shapes) memory utilization being below the
threshold across a **seven-day** window -- 20% in today's documentation, 10%
in the older notices. Nine Go services waiting for a console that has not
booted yet is idle in exactly that sense.

`deploy/keepalive` exists for it: one stdlib-only Python file, installed and
enabled by `scripts/install-systemd.sh`, that holds CPU utilization at 45% by
sampling `/proc/stat` for the whole machine and making up only the shortfall.
It restarts on any exit, comes up at boot, yields to everything else
(`Nice=19`, `CPUWeight=1`), and does nothing at all on a host that is not a
reclaiming cloud instance. Full details, including how to check it and turn
it off, are in [`deploy/keepalive/README.md`](../deploy/keepalive/README.md).

**The console's own CPU graph in the cloud console is the reading that
decides.** Check that, not just the status file, after changing anything.

## Security

This is a private server for a group of friends. It is not hardened for the open
internet, and a few properties are worth being explicit about.

**Anybody who can reach port 443 can register an account** while
`baas.allow_registration` is true. Turn it off once your group is registered. On
a VPS, also firewall 443 and 10025/10125 to your friends' addresses.

**There is no rate limiting.** A friend-code lookup is a cheap indexed query, but
somebody who can reach the server could enumerate the 10^12 code space given
enough time. Registration being off closes the interesting half of that; nginx's
`limit_req` closes the rest if you need it.

**Device tokens are not authenticated.** Anybody who can reach dauth can get one.
That is inherent — the real check is a Nintendo-signed client certificate, which
cannot be verified here — and it is why nothing is authorised on the device id.

**Never expose Postgres or Redis.** The compose file binds them to 127.0.0.1.
There is no authentication on Redis and the Postgres password is a development
default.

**dauth/aauth/stubs stay loopback-only by configuration, not by a container
port mapping.** `config.example.yaml`'s defaults already bind them to
`127.0.0.1`; changing one of those `listen` values to bind-all puts it on
the LAN with nothing in front of it. nginx is the only thing that is meant
to reach them.

**`config/config.yaml`, `certs/` and `switch/hosts.txt` are gitignored.** They
contain your addresses and your keys. Check `git status` before committing if you
have edited anything under those paths.
