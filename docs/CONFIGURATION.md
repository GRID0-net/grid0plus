# Configuration

GRID0+ reads one YAML file, `config/config.yaml`. Start from the example, which
explains every setting in comments:

```sh
cp config/config.example.yaml config/config.yaml
```

`config/config.yaml` is git-ignored, so it is safe to put real secrets in it.

## Rules that apply everywhere

- **Environment variables win.** Any setting can be overridden with
  `SWITCHNET_<SECTION>_<KEY>`, for example `SWITCHNET_NETWORK_PUBLIC_IP`. This is
  handy for a one-off change without editing the file.
- **Unknown keys are rejected.** A typo in a key is an error, not a silently ignored
  setting.
- **Validate before you start anything:**
  ```sh
  switchnetctl -config config/config.yaml check     # or: make config-check
  ```
  It refuses configurations that can't work and warns about ones that are valid but
  cause trouble.
- **Every service reads the same file.** They all need to see the same `keys.dir`
  and the same database, so don't give them different configs.

## The settings you must set

| Setting | Why |
| --- | --- |
| `network.public_ip` | The address consoles are sent to. It must be reachable from the consoles: not a hostname, and not `127.0.0.1`. |
| `network.natcheck_secondary_ip` | A second address, different from the first, for the NAT check. See [DEPLOYMENT.md](DEPLOYMENT.md#the-two-addresses). |
| `keys.device_id_secret` | A long random string, kept constant. `openssl rand -hex 32` makes one. |

## Sections

| Section | What it controls |
| --- | --- |
| `network` | Where the server is from a console's point of view: the public address, the second NAT-check address, the edge port. |
| `dns` | The lifetime of the DNS answers GRID0+ generates. It is short, so switching a console back takes effect quickly. GRID0+ has no DNS server of its own; see [CONSOLE_SETUP.md](CONSOLE_SETUP.md). |
| `database` | The PostgreSQL URL, pool size, and whether to run migrations at start-up (`auto_migrate`). |
| `redis` | The presence bus. Leave `url` empty only if BAAS is the only process using presence. Set it for a real deployment. |
| `keys` | Where signing keys live (`dir`), whether to create missing ones (`generate_if_missing`), and the device id secret. |
| `dauth`, `aauth` | The device and application authentication services. The defaults are right for a private network. `aauth.online_play_policy` can require a membership claim per title. |
| `baas` | Accounts and friends: token lifetimes, the friend code cooldown, presence timeouts, registration, login rate limit, image storage, and the Discord bot API. |
| `scsi` | The cloud save backup: where blobs are stored and the per-user quota. |
| `stubs` | The catch-all service: request body logging and the BCAT request recorder. |
| `bcat` | Splatfest content: the texture converter command, the seed directory served to consoles, and fest id substitution. |
| `dashboard` | The operator's web page: listen address, the shared token, and where to reach the `npln` and `nex` admin APIs. |
| `npln` | The Splatoon 3 game server. See below. |
| `nex` | The Splatoon 2 game server, its admin API and the optional relay. |
| `toolbox` | The Toolbox update and status service. See [TOOLBOX.md](TOOLBOX.md). |
| `natcheck` | The UDP NAT-check ports, the alternate-address socket, and the STUN port. |
| `log` | `level` (`debug`, `info`, `warn`, `error`) and `format` (`text` or `json`). |

## The `npln` section

This is the one operators touch most.

**Match sizes.** `players_per_match` is 8, the full 4v4. For Turf War and Anarchy it
is a cap, not a target. A full lobby starts immediately. Below it, a match with an
even number of players starts once `versus_grace_period` (20 seconds) passes with
nobody arriving or leaving. An odd count stops the countdown until one more player
joins, so the teams are always equal. Set the grace period to 0 and matches start
only when full.

Salmon Run is formed differently: one team, no even split. List its modes in
`coop_modes`, give each a size in `mode_players`, and tune `coop_grace_period`.

The two grace periods can be changed while people are playing. Edit the file and
reload:

```sh
sudo systemctl reload switchnet-npln
```

Only those two settings are reloaded; anything else needs a restart.

**NAT traversal.** Splatoon 3 uses ICE, not the NAT-check pair. `ice_servers` lists
the STUN and TURN servers it is told about. They must be reachable from the console.
Leave it empty only if your consoles share one LAN. If you run coturn with the REST
credential scheme, set `turn_auth_secret` and omit the username and password so each
player gets an expiring credential.

**The schedule.** `rotation` gives the stage and rule ids for the lobby schedule.
GRID0+ supplies the timing around them (two-hour slots, order, the etag). Leave it
empty and the game sees no schedule, as it does between seasons on a real server.

**Admin API.** `admin_listen` and `admin_token` expose the matchmaking controls the
dashboard uses (the live queue, force-start, cancelling a ticket). It is only served
when `admin_token` is set, and it must match `dashboard.npln_admin_token`.

**Debugging.** `rest_record_dir` records every request and response of the JSON front
end, hexdumped, with credentials redacted. It is off by default because it writes
whatever a console sends.

## Settings that need a decision before you go live

- `baas.allow_registration`: turn it off once everyone has an account.
- `baas.bot_api.listen` and `.token`: set these to let the Discord bot create
  accounts. See [DISCORD_BOT.md](DISCORD_BOT.md).
- `dashboard.token`: without it the dashboard has no authentication.
- `keys.generate_if_missing`: set to `false` once `certs/keys` is backed up.
- `scsi.per_user_quota_bytes`: leave it at the default, or at least not 0. Zero means
  one console can fill the disk.
- `redis.url`: set it.
