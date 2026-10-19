# Deployment

How to put GRID0+ on a machine your consoles can reach and keep it running.

- [Installing](#installing)
- [The nginx edge](#the-nginx-edge)
- [Addresses and ports](#addresses-and-ports)
- [The two addresses](#the-two-addresses)
- [Checklist: from nothing to a console with a friend](#checklist-from-nothing-to-a-console-with-a-friend)
- [Running it](#running-it)

## Installing

Postgres, Redis and nginx run in Docker, using the stock images. The Go services
run as native processes: systemd units on the machine that serves your consoles.

```sh
git clone https://github.com/n-popescu/grid0plus /opt/switchnet && cd /opt/switchnet

cp config/config.example.yaml config/config.yaml
$EDITOR config/config.yaml            # network.public_ip and keys.device_id_secret are the
                                      # two you must set (openssl rand -hex 32 for the secret)

make config-check                     # refuses a config that can't work, warns about risky ones
make up                               # Postgres, Redis and nginx
make gen-certs                        # a local CA and the edge certificate
make gen-switch                       # the nginx config, DNS config and hosts file
scripts/install-systemd.sh            # builds every service and installs the units
sudo systemctl start switchnet.target
```

Clone to `/opt/switchnet`. The systemd units name that path, because a static unit
file can't discover where it was cloned, and `install-systemd.sh` refuses to run from
anywhere else.

`make config-check` is worth running first. The setting it most often catches is
`network.natcheck_secondary_ip` being unset (see [the two addresses](#the-two-addresses)).

Before involving a console, check the chain works:

```sh
make seed
```

It drives the HTTP API (device token, application token, sign-up, login, friend code,
friend request, accept, presence) and prints the resulting friend list. If it
completes, everything except DNS and TLS is correct.

## The nginx edge

The console connects to Nintendo hostnames on port 443, and one nginx in front of
the services picks the backend by hostname.

```sh
make gen-nginx        # writes deploy/nginx.generated.conf
```

Don't edit the result by hand. It is generated from the same host list as the DNS
configuration and the certificate names, and the next regeneration would lose your
edit. To add a host, add it to `internal/nintendo/hosts.go` and regenerate.

Install it:

```sh
sudo mkdir -p /etc/switchnet/certs
sudo cp certs/edge.crt certs/edge.key /etc/switchnet/certs/
sudo chmod 600 /etc/switchnet/certs/edge.key
sudo cp deploy/nginx.generated.conf /etc/nginx/conf.d/switchnet.conf
sudo nginx -t && sudo systemctl reload nginx
```

Two things in the generated config matter:

**`ssl_verify_client optional_no_ca`.** The console presents its own device
certificate to dauth (and to the BAAS host from firmware 21.0.0). Nintendo signed it,
so it can't be validated here. This setting asks for it and passes the fingerprint
through without validating it, which gives each console a stable device id. Nothing
is authorised on that id, so without it everything still works, but the dashboard's
device list collapses to a single row.

**A `default_server` block.** A hostname that was redirected on the console but has
no block here would otherwise fall to whichever block nginx saw first and get an
answer from the wrong service. The generated config sends unmatched hosts to the stub
service, which logs them. That log is a useful to-do list.

## Addresses and ports

GRID0+ doesn't run its own DNS server. A console finds it through a resolver you
already run, configured with `make gen-dns` (see [CONSOLE_SETUP.md](CONSOLE_SETUP.md)).

| Port | Protocol | Reachable by | What |
| --- | --- | --- | --- |
| 443 | TCP | consoles | the nginx edge |
| 7575 | TCP | consoles | the nginx edge again, for Splatoon 3's `gamesync` |
| 8443 | TCP | consoles running the Toolbox | the nginx edge again, for the `toolbox` service |
| 10025, 10125 | UDP | consoles | NAT check, used by most titles but **not** Splatoon 3 |
| 3478 | UDP and TCP | consoles | STUN/TURN, if you run one; Splatoon 3 uses ICE |
| 30100-30999 | UDP | consoles | the Splatoon 2 relay, **only if** `nex.relay.enabled` |
| 8081-8084 | TCP | localhost | dauth, aauth, baas, stubs, behind nginx |
| 8085-8089 | TCP | localhost | npln (gateway, JSON front end, gamesync, optional admin API) and the dashboard |
| 8090 | TCP | localhost | toolbox |
| 8091 | TCP | localhost | scsi |
| 8092, 8093 | TCP | localhost | nex and its optional admin API |
| 5432, 6379 | TCP | localhost | Postgres and Redis |

The UDP rules are the ones people forget. A firewall that opens only TCP gives you a
friends list that works and games that never start a match. The same goes for UDP 53
on whichever machine runs your resolver.

The localhost-only services are confined by their default `listen` settings
(`127.0.0.1:<port>`). If you change one to bind all addresses, it becomes reachable
from your network with nothing in front of it, so leave them alone unless you have a
reason.

**7575 is easy to miss.** Splatoon 3 reaches `gamesync` on that port directly rather
than through the host on 443, so the edge needs a second listener and your firewall a
second rule. Without it the game signs in, opens its gRPC channel and then can't sync
the lobby.

**Which NAT mechanism applies to which title.** Most titles, including Splatoon 2,
use the NAT-check pair on 10025 and 10125. Splatoon 3 uses ICE instead, so `natcheck`
isn't involved and `npln.ice_servers` is what matters. Those addresses must be
reachable from the console, not just from the server. Wrong ones give a lobby that
fills and a match that never starts, which looks exactly like a matchmaking bug from
the server side. `switchnetctl check` warns when the list is empty, and consoles on
one LAN work without it.

### Notes on specific services

**npln** runs four listeners in one process. `gamesync` verifies tokens the gateway
signs, so splitting it into its own process would mean inventing a protocol between
them. The admin API only runs when `npln.admin_token` is set.

**dashboard** binds to all addresses, since it is a page for a browser on your
network. Set `dashboard.token`, or anyone who can reach it can schedule and cancel
Splatfests.

**redis.url** must be set for the dashboard, `npln`, `baas`, `natcheck` and `nex`.
The in-process presence bus delivers nothing across a process boundary, silently.
Without Redis the dashboard's live view never updates and the matchmaking tile shows
no data, and a console behind a router that remaps ports is handed to its peers at a
port nothing listens on. The processes warn at start-up, and so does
`switchnetctl check`.

**npln.rest_record_dir** is worth setting before you try a new console. The JSON
front end Splatoon 3 contacts first has request shapes nobody has published; with
this set, one boot writes the whole exchange to disk, hexdumped, with credentials
redacted.

**The Splatoon 2 relay** (`nex.relay`) is off by default. It carries match traffic
through this machine for pairs of consoles that can't reach each other, using one UDP
port per pair from `port_base` and `port_span`, on `advertise_ip` (default
`network.public_ip`). Open that range to the consoles only if you enable it, and
start with `allow` naming the one player who can't connect.

**The toolbox service** has its own page, [TOOLBOX.md](TOOLBOX.md).

## The two addresses

`network.public_ip` and `network.natcheck_secondary_ip` must be different addresses.

Consoles probe `nncs1` and `nncs2` as two separate servers, and one NAT-check
message type has to be answered from a different address. If both names resolve to
the same IP, the console de-duplicates the probe, never sends the second one, and NAT
detection doesn't complete. In practice this showed up as Mario Kart 8 error
2618-201 and looked nothing like a NAT problem.

A second address on the same machine is enough:

```sh
# temporary, to try it
sudo ip addr add 192.168.1.51/24 dev eth0

# permanent, with systemd-networkd, in /etc/systemd/network/10-eth0.network:
#   [Network]
#   Address=192.168.1.50/24
#   Address=192.168.1.51/24
```

```yaml
network:
  public_ip: "192.168.1.50"
  natcheck_secondary_ip: "192.168.1.51"
natcheck:
  alternate_listen: "192.168.1.51:10025"
```

Running with one address does work, but consoles then report a stricter NAT type than
their router really has, and some titles refuse to matchmake because of it.

`natcheck` has to run natively rather than behind Docker's port forwarding. The
protocol works by the console observing which address and port answered, and a
userland proxy rewrites the reply's source address, so every message looks like it
came from the same place and the NAT type comes out wrong.

## Checklist: from nothing to a console with a friend

Do these in order. Each one rules out everything before it, which makes debugging
on real hardware manageable.

1. **The services answer locally.**
   ```sh
   curl -fsS localhost:8081/keys               | jq .keys[0].kid   # dauth
   curl -fsS localhost:8082/keys               | jq .keys[0].kid   # aauth
   curl -fsS localhost:8083/1.0.0/certificates | jq .keys[0].kid   # baas id
   curl -fsS -H 'Host: ctest.cdn.nintendo.net' localhost:8084/     # prints ok
   ```
   You should see three different key ids. Two the same means two issuers share a key.

2. **The chain works.** `make seed` completes and prints a friend list with a
   "playing" entry carrying a title id.

3. **TLS answers on 443 with the right certificate.** From another machine on the
   console's network:
   ```sh
   curl -vk --resolve dauth-lp1.ndas.srv.nintendo.net:443:<YOUR_IP> \
        https://dauth-lp1.ndas.srv.nintendo.net/keys
   openssl s_client -connect <YOUR_IP>:443 -servername dauth-lp1.ndas.srv.nintendo.net \
        </dev/null 2>/dev/null | openssl x509 -noout -issuer
   ```
   You want a completed handshake, a JSON key set, and an issuer that is your own local
   CA.

4. **Every redirected host reaches a backend.** For each name in `switch/hosts.txt`,
   curl it with `--resolve` and check you don't land on the stub service's
   "unmatched host" log.

5. **DNS resolves the right way.** From a machine on the console's network:
   ```sh
   dig @<YOUR_IP> dauth-lp1.ndas.srv.nintendo.net A    +short   # your address
   dig @<YOUR_IP> nncs2-lp1.n.n.srv.nintendo.net   A    +short   # the SECOND address
   dig @<YOUR_IP> dauth-lp1.ndas.srv.nintendo.net AAAA          # NOERROR, no answer
   dig @<YOUR_IP> atumn.hac.lp1.d4c.nintendo.net   A    +short   # 0.0.0.0
   dig @<YOUR_IP> example.com                      A    +short   # a real address
   ```
   The AAAA and `atumn` lines are the ones people skip. Both are mistakes that bite
   later rather than immediately.

6. **NAT check answers on UDP.**
   ```sh
   printf '\x00\x00\x00\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00' \
     | timeout 2 nc -u <YOUR_IP> 10025 | xxd
   ```
   Sixteen bytes come back, with your source port in bytes 4 to 7.

7. **The console side.** Follow [CONSOLE_SETUP.md](CONSOLE_SETUP.md): DNS, the
   certificate-trust patches that match your firmware, and a correct clock.

8. **`baas: registered user` appears in the log.** DNS, TLS, routing and the whole
   authentication chain are right at that point.

9. **Two consoles, a friend code, a request, an accept.** Each should show the other
   online.

10. **Launch a game on one.** The other shows "Playing <game>", and the log shows
    `presence reported ... state=PLAYING title=...`.

11. **Decide about registration.** `baas.allow_registration: false` stops both the bot's
    `/register` and a new console's own placeholder account, so nobody new can join
    until you turn it back on. With the bot, the Discord server itself is usually the
    gate.

People get their accounts from the Discord bot
([`GRID0-net/grid0plus-discord`](https://github.com/GRID0-net/grid0plus-discord)). Set `baas.bot_api` and install the
bot on the same machine. [DISCORD_BOT.md](DISCORD_BOT.md) has the details. There is no
registration web page.

## Running it

**Back up the key directory.** `certs/keys` holds the signing keys. Losing them
invalidates every token a console holds. Consoles recover by authenticating again, but
every id token already issued stops verifying. Once it is backed up, set
`keys.generate_if_missing: false` so a lost volume fails loudly instead of quietly
creating a new identity for the network.

**Back up the database.** It holds the accounts, the friend graph and the friend
codes.

```sh
docker compose exec postgres pg_dump -U switchnet switchnet | gzip > backup.sql.gz
```

**Migrations** run on start-up when `database.auto_migrate` is on. The files are also
golang-migrate compatible if you prefer to run them yourself:

```sh
migrate -path internal/db/migrations -database "$SWITCHNET_DATABASE_URL" up
```

**Logs.** `make logs` follows Postgres, Redis and nginx. The Go services log to
journald: `journalctl -u switchnet-baas -f` for one, `journalctl -u 'switchnet-*' -f`
for all of them. `log.level: debug` logs every NAT-check datagram, which is a lot but
the right setting while you bring a console up. `stubs.log_bodies` logs what the
unimplemented endpoints are being asked for; useful once, far too chatty to leave on.

**Updating.** `scripts/update.sh`, from `/opt/switchnet`, pulls, works out which
services the change reaches (by reading each binary's Go package graph, not by
guessing from directory names), runs `make check`, builds only what changed and
restarts only those units. `--dry-run` shows what it would do, and `--all` restarts
everything.

**systemd.** `scripts/install-systemd.sh` creates a `switchnet` system user, builds
every service and installs `deploy/systemd/switchnet-*.service` plus a
`switchnet.target` that groups them.

```sh
sudo systemctl start switchnet.target     # start everything
sudo systemctl status switchnet.target    # is it all up
sudo systemctl restart switchnet-baas     # restart one service
journalctl -u switchnet-baas -f           # its logs
```

Each unit restarts itself after a crash without taking the others down. The usual
"five failures in ten seconds" throttle is turned off on purpose; see the comment in
`switchnet-baas.service`.

**Cloud instances that reclaim idle machines.** Some free-tier hosts (Oracle's, for
one) reclaim an instance that looks unused. A server waiting for a console that
hasn't booted yet looks exactly like that. `deploy/keepalive` is a small helper that
`install-systemd.sh` installs, which keeps CPU use at a modest level and does nothing
on a host that isn't a reclaiming cloud instance. See
[deploy/keepalive/README.md](../deploy/keepalive/README.md).

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

## Repeated connections from distant players

Generated console-facing HTTP and gRPC TLS blocks use bounded shared session
caches (1 MiB per backend/client-certificate policy, ten-minute expiry).
Stateful TLS resumption reduces repeat-handshake overhead without enabling
TLS early data or changing device-certificate policy. The nginx edge must
be regenerated and reloaded for this setting to apply. Check actual reuse:

```sh
sudo python3 - <<'PYPROBE'
import socket, ssl
context = ssl.create_default_context(cafile="/opt/switchnet/certs/edge.crt")
context.verify_flags |= ssl.VERIFY_X509_PARTIAL_CHAIN
context.minimum_version = context.maximum_version = ssl.TLSVersion.TLSv1_2
session = None
for attempt in range(3):
    with socket.create_connection(("127.0.0.1", 443), timeout=5) as sock:
        with context.wrap_socket(sock,
                server_hostname="t-dce9377b-lp1.lp1.t.npln.srv.nintendo.net",
                session=session) as tls:
            print(attempt, "reused:", tls.session_reused)
            session = tls.session
            try:
                tls.unwrap().close()
            except ssl.SSLError:
                pass  # nginx may close without replying to close_notify
PYPROBE
```

The first connection should report False and subsequent ones True. A graceful
TLS shutdown matters: an abruptly closed probe can invalidate cached sessions
and misleadingly report full handshakes on every reconnect. This improves
reconnection/setup overhead; gameplay RTT still depends on the peers' routes
and any TURN relay. gRPC's automatic flow-control adaptation and existing
unbuffered streams remain enabled; arbitrary fixed windows and shorter
startup deadlines are not substitutes for a nearer relay.

For what the server does and doesn't protect, and how to lock it down, see
[SECURITY.md](../SECURITY.md).
