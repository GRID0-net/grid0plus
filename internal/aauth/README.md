# internal/aauth

Application authentication: proves to a game server that a console owns a game.

| | |
| --- | --- |
| Reimplements | `https://aauth-lp1.ndas.srv.nintendo.net` |
| | `https://aauth.hac.lp1.ndas.srv.nintendo.net` (20.0.0 and later) |
| | `https://acert-lp1.ndas.srv.nintendo.net` (the JWK set) |
| Reference | [AAuth Server](https://github.com/kinnay/NintendoClients/wiki/AAuth-Server) |
| | [Switch Tokens](https://github.com/kinnay/NintendoClients/wiki/Switch-Tokens#aauth-tokens) |
| Needs a database | no |

## Endpoints

| Method | Path | What |
| --- | --- | --- |
| GET | `/v1/time` | server time in milliseconds, and the client's public IP |
| POST | `/v{3,4,5}/challenge` | a gamecard-presence challenge |
| POST | `/v{1..5}/application_auth_token` | an application token, as a JWT |
| GET | `/keys` | the JWK set the token's `jku` points at |

## `/v1/time` matters more than it looks

This has nothing to do with application tokens: it is how a console up to 17.0.1
learns the time and its own public address. Getting it wrong means the console
sets its clock from a wrong value, and then **every token in the chain is rejected
as expired** — with an error that blames the token, not the clock.

The client address is taken from the *last* `X-Forwarded-For` entry, the one the
immediately preceding proxy wrote. Trusting the first would let a console put
whatever it liked in the header and have the server repeat it back.

## Game ownership is not checked

The ticket, the gamecard certificate and the cartridge challenge reply are all
accepted without being looked at. On a private network of consoles the operator
owns there is nobody to prove ownership to, and verifying any of it would need
Nintendo key material.

Nothing here produces a credential Nintendo would accept, so this is not a route
to getting an unowned game online anywhere but on the operator's own network.

## Online play policy

`aauth.online_play_policy` is `FREE` by default, which is right for a private
network: the console then never asks for a Nintendo Switch Online membership.

Set it to `MEMBERSHIP_REQUIRED` with `policy_handler: GAME_SERVER` for a title
that behaves differently when the policy does not match what it saw on retail
servers — Splatoon 3 is one. That combination is what makes the BAAS id token
carry the `hm` claim a game server reads to decide whether online play is
allowed. Per-title overrides are in `aauth.title_overrides`.
