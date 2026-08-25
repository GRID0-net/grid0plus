# internal/dauth

Device authentication: the first server a Switch talks to when it comes online.

| | |
| --- | --- |
| Reimplements | `https://dauth-lp1.ndas.srv.nintendo.net` |
| | `https://dauth.hac.lp1.ndas.srv.nintendo.net` (20.0.0 and later) |
| | `https://dcert-lp1.ndas.srv.nintendo.net` (the JWK set) |
| Reference | [DAuth Server](https://github.com/kinnay/NintendoClients/wiki/DAuth-Server) |
| | [Switch Tokens](https://github.com/kinnay/NintendoClients/wiki/Switch-Tokens#dauth-tokens) |
| Needs a database | no |

## Endpoints

| Method | Path | What |
| --- | --- | --- |
| POST | `/v{1..8}/challenge` | issues a challenge |
| POST | `/v{1..7}/device_auth_token` | a device token, as a JWT |
| POST | `/v8/device_auth_tokens` | several device tokens, JSON body |
| POST | `/v{1..7}/edge_token` | a CDN token, not a JWT |
| POST | `/v8/edge_tokens` | several CDN tokens |
| GET | `/keys` | the JWK set the token's `jku` points at |

All eight API versions are registered, including the v2 and v4 paths with the hex
strings Nintendo obfuscated them with. Which one a console uses is decided by its
firmware, not by the operator.

## What is not verified, and why

Nintendo's dauth requires a client certificate unique to each console and signed
by Nintendo, and requires the client to answer a challenge with a MAC keyed on the
console's master key. SwitchNet does neither:

- the certificate cannot be validated without Nintendo's CA;
- the MAC cannot be checked without the master key, and deriving one would mean
  shipping Nintendo key material.

So the challenge is issued and echoed but never checked. This is safe **only**
because the operator's network is private and trusted, and it is not a way to
defeat device attestation on Nintendo's own servers.

## Device identity

The TLS client certificate is the only thing in a dauth request that differs
between two consoles, so when nginx passes its fingerprint through
(`ssl_verify_client optional_no_ca`, header `X-SwitchNet-Client-Cert`) each
console gets its own stable device id. Without it, they all share one.

That is deliberately harmless: **nothing in SwitchNet authorises anything on the
device id.** Account identity is resolved from the BAAS device account and only
from there. A shared device id costs a useful device list on the dashboard and
nothing else. Conflating the two is how a friends system ends up showing every
console the same account — see
[`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md#identity-and-the-bug-this-design-exists-to-avoid).
