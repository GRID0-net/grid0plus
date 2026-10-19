# aauth

Application authentication: it tells a game server that a console owns a game.

| | |
| --- | --- |
| Stands in for | `aauth-lp1.ndas.srv.nintendo.net`, `aauth.hac.lp1.ndas.srv.nintendo.net` (firmware 20.0.0 and later) and `acert-lp1.ndas.srv.nintendo.net` (the JWK set) |
| Reference | [AAuth Server](https://github.com/kinnay/NintendoClients/wiki/AAuth-Server), [Switch Tokens](https://github.com/kinnay/NintendoClients/wiki/Switch-Tokens#aauth-tokens) |
| Needs a database | no |

## Endpoints

| Method | Path | What |
| --- | --- | --- |
| GET | `/v1/time` | server time in milliseconds, and the client's public IP |
| POST | `/v{3,4,5}/challenge` | a gamecard-presence challenge |
| POST | `/v{1..5}/application_auth_token` | an application token, as a JWT |
| GET | `/keys` | the JWK set the token's `jku` points at |

## `/v1/time` matters more than it looks

It has nothing to do with application tokens. It is how a console up to firmware
17.0.1 learns the time and its own public address. If it is wrong, the console sets its
clock from a bad value and then every token in the chain is rejected as expired, with an
error that blames the token rather than the clock.

The client address comes from the last `X-Forwarded-For` entry, the one the nearest
proxy wrote. Trusting the first would let a console put anything it liked in the header
and have the server repeat it back.

## Game ownership is not checked

The ticket, the gamecard certificate and the cartridge challenge reply are accepted
without being inspected. On a private network of consoles the operator owns there is
nobody to prove ownership to, and checking any of it would need Nintendo key material.
Nothing here produces a credential Nintendo would accept.

## Online play policy

`aauth.online_play_policy` is `FREE` by default, which is right for a private network:
the console never asks for a Nintendo Switch Online membership.

Set it to `MEMBERSHIP_REQUIRED` with `policy_handler: GAME_SERVER` for a title that
behaves differently when the policy doesn't match what it saw on retail servers (Splatoon
3 is one). That combination makes the BAAS id token carry the claim a game server reads to
decide whether online play is allowed. Per-title overrides go in `aauth.title_overrides`.

## The `esv` claim

Every application token's `nintendo` block also carries `esv`, an opaque random value
generated fresh for each token. It isn't derived from the device or the title. See
`DeviceInfo` in [`internal/jwt`](../jwt/README.md).
