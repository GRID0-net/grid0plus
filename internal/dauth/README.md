# dauth

Device authentication: the first server a Switch talks to when it comes online.

| | |
| --- | --- |
| Stands in for | `dauth-lp1.ndas.srv.nintendo.net`, `dauth.hac.lp1.ndas.srv.nintendo.net` (firmware 20.0.0 and later) and `dcert-lp1.ndas.srv.nintendo.net` (the JWK set) |
| Reference | [DAuth Server](https://github.com/kinnay/NintendoClients/wiki/DAuth-Server), [Switch Tokens](https://github.com/kinnay/NintendoClients/wiki/Switch-Tokens#dauth-tokens) |
| Needs a database | no |

## Endpoints

| Method | Path | What |
| --- | --- | --- |
| POST | `/v{1..8}/challenge` | issues a challenge |
| POST | `/v{1..7}/device_auth_token` | a device token, as a JWT |
| POST | `/v8/device_auth_tokens` | several device tokens, JSON body |
| POST | `/v{1..7}/edge_token` | a CDN token (not a JWT) |
| POST | `/v8/edge_tokens` | several CDN tokens |
| GET | `/keys` | the JWK set the token's `jku` points at |

All eight API versions are registered, including the v2 and v4 paths with the hex
strings Nintendo obfuscated them with. Which one a console uses depends on its firmware.

## What is not verified

Nintendo's dauth requires a client certificate unique to each console and signed by
Nintendo, and requires the client to answer a challenge with a MAC keyed on the console's
master key. GRID0+ does neither. The certificate can't be validated without Nintendo's CA,
and the MAC can't be checked without the master key, which would mean shipping Nintendo
key material. So the challenge is issued and echoed but never checked. That is safe only
because the network is private and trusted.

## Device identity

The TLS client certificate is the only thing in a dauth request that differs between two
consoles. When nginx passes its fingerprint through (`ssl_verify_client optional_no_ca`,
header `X-SwitchNet-Client-Cert`), each console gets its own stable device id. Without
it they all share one.

That is harmless on purpose: **nothing in GRID0+ authorises anything on the device id.**
Account identity comes from the BAAS device account and only from there. A shared device
id costs a useful device list on the dashboard and nothing else. Mixing the two is how a
friends system ends up showing every console the same account; see
[ARCHITECTURE.md](../../docs/ARCHITECTURE.md#identity).

## The `x5t#S256` and `esv` claims

Every real device token carries both in its `nintendo` block, though neither is on the
Kinnay wiki's device-information page. `x5t#S256` is a SHA-256 thumbprint of the console's
TLS client certificate. `esv` is opaque random padding generated fresh for each token and
never cached or derived from the device (see `DeviceInfo` in [`internal/jwt`](../jwt/README.md)).
