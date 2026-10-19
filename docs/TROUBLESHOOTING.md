# Troubleshooting

Start with where in the chain things stop. Each of these is checked in order in
[DEPLOYMENT.md](DEPLOYMENT.md#checklist-from-nothing-to-a-console-with-a-friend) and
[CONSOLE_SETUP.md](CONSOLE_SETUP.md#verify-in-order); this page is the shortcut from a
symptom to the usual cause.

## Reading what the server saw

- **Service logs.** `journalctl -u switchnet-<service> -f` (for example
  `switchnet-baas`), or the terminal running `make run`. `make logs` follows
  Postgres, Redis and nginx.
- **More detail.** `log.level: debug` logs every NAT-check datagram. `stubs.log_bodies`
  logs what unimplemented endpoints are being asked for. `baas.log_bodies` logs
  request and response bodies, **including real tokens and passwords**, so use it
  briefly.
- **The first Splatoon 3 boot.** Set `npln.rest_record_dir`, boot a console once, and read
  the transcript. It shows every request and response of the JSON front end, in order,
  with credentials redacted.
- **BCAT requests.** Set `stubs.bcat_record_dir` to record every request to the `bcat-*`
  hosts.
- **The dashboard's Health tab** asks every service directly and shows what each one's
  own dependencies are doing.
- **Your resolver's query log** (dnsmasq `--log-queries`, Pi-hole's Query Log, Unbound
  `verbosity: 2`) shows whether the console is using it and what it was told.

## Symptoms and usual causes

### The console can't reach the server

| Symptom | Almost certainly |
| --- | --- |
| Nothing for the console in your resolver's query log | The console isn't using your resolver. Renew its DHCP lease or check its manual DNS setting. |
| The query log shows the console being redirected, but nothing reaches dauth or baas | DNS works and TLS doesn't. Certificate trust (part 2 of the console setup), and if you use the firmware patch, check that it matches your firmware. |
| Nothing anywhere, and the console reports a network error | The same. Failed certificate trust produces total silence, because the connection never becomes a request. |
| Everything worked, then stopped after a system update | A firmware patch that no longer matches the firmware's build id. The certificate-store route avoids this. |
| The eShop or browser stopped working | A `ssl_TrustedCerts.bdf` override that replaced the store instead of extending it. Delete it and rebuild with `--input`. |
| Every token is rejected as expired | The console's clock. System Settings, System, Date and Time, synchronise via the internet. |
| `token is invalid` right after a device token was issued | dauth and baas are reading different key directories. They must share `keys.dir`. |
| Non-Nintendo sites stop resolving on the console | Your resolver's own upstream is misconfigured or unreachable. Check its settings, not GRID0+'s. |

### Accounts, friends and presence

| Symptom | Almost certainly |
| --- | --- |
| `invalid_device_account` in the log, repeating for one id | The console holds a device account this server never issued: it was linked to a different install, or the database was reset while the console kept its cached credential. GRID0+ refuses it on purpose. On the console, add a new user and link it with the player's GRID0+ account, which issues a fresh device account. First check the id is really absent (`select * from device_accounts where id = '<id>'`). One that exists but is deleted was revoked, for example by a password reset, and relinking fixes it the same way. |
| Friends show, but everybody is offline | Check whether `presence reported` appears in the baas log at all. |
| A friend shows online but never "Playing X" | The title id isn't arriving. Set `log.level: debug` and look at the presence patch. |
| The received-requests list shows error 2124-0104 | A known console-side issue with that view. Sending and accepting requests still works. |

### Matches and games

| Symptom | Almost certainly |
| --- | --- |
| Matchmaking finds a match that never starts | NAT check. `nncs1` and `nncs2` must resolve to two different addresses. |
| The lobby fills and the match never starts (Splatoon 3) | `npln.ice_servers`. The addresses must be reachable from the console, not from the server. |
| Splatoon 3 says it can't connect, and the server log shows a connection but no request | The game pins its own certificates. Install the Splatoon 3 patches (part 3 of the console setup). |
| Splatoon 3 worked and stopped after a game update | The patches are tied to the game version and silently stopped applying. Rebuild or reinstall them. |
| The JSON front end is reached but the frontline connection never opens | `penne`'s answer didn't name a frontline host the game recognises. Set `npln.rest_record_dir` and read the transcript. |
| A smaller match (2, 4 or 6 players) is refused by the game | The patch that lets a match start with fewer than eight players isn't installed, or the game build isn't one it supports. |
| Another game says it can't connect | Expected. Only Splatoon 3 and Splatoon 2 have game servers here. |

### Splatfests and BCAT

| Symptom | Almost certainly |
| --- | --- |
| A fest is announced and the game reports a communication error | The announced id, resource or revision don't match the pack on the console. `switchnetctl fest build` makes them match. |
| The BCAT list probe returns 304 | `bcat.seed_dir` is unset or has no folder for the title id. |
| The console downloads no fest content | The BCAT signature patch isn't installed for this firmware, or the `bcat-*` hosts aren't redirected. |

See [SPLATFESTS.md](SPLATFESTS.md) for the full procedure.

### Running the server

| Symptom | Almost certainly |
| --- | --- |
| A service won't start and complains about the config | Run `switchnetctl -config config/config.yaml check`. Unknown keys are rejected. |
| The dashboard's live view never updates | `redis.url` isn't set. The in-process bus doesn't cross processes. |
| The matchmaking panel says it isn't configured | `npln.admin_token` and `dashboard.npln_admin_token` are unset or don't match. |
| Two services show the same key id | They are sharing a key. Each issuer should have its own. |
