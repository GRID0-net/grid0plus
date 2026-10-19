# Security

GRID0+ is a private server for a small group of people who know each other. It is
not hardened for the open internet. This page says what it protects and what it
doesn't, so you can decide how to expose it.

## Reporting a vulnerability

Please report security problems privately to the maintainers instead of opening a
public issue. Include what you found, how to reproduce it, and which version you
tested.

## What is and isn't protected

**Registration is open until you close it.** While `baas.allow_registration` is
true, anyone who can reach the server can create an account. Accounts normally come
from the Discord bot, so turn registration off once your group has signed up. On a
VPS, firewall the edge ports to your friends' addresses as well.

**Nintendo credentials are not checked.** Device certificates, game tickets and
gamecard challenges are accepted as they arrive. Verifying them would need Nintendo
key material, and on a private network of consoles you own there is nobody to prove
ownership to. Nothing in GRID0+ treats a device id as proof of identity. See
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md#what-is-not-verified).

**GRID0+'s own tokens are checked strictly.** A token must verify against the key
the server generated, with the right key id and token type, and must not be
expired. Tokens with `alg: none`, tokens from another issuer and tokens with an
edited claim are all refused, and there is a test for each.

**There is no rate limiting.** Friend codes are a large space, but not an
unreachable one. Turning registration off removes most of the risk. If you need
more, nginx's `limit_req` is the place for it.

**Keep Postgres and Redis private.** The Docker Compose file binds them to
`127.0.0.1`. Redis has no password and the Postgres password is a development
default.

**Keep the internal services on loopback.** `dauth`, `aauth` and `stubs` listen on
`127.0.0.1` by default and only nginx is meant to reach them. Changing a `listen`
setting to bind all addresses puts that service on your network with nothing in
front of it.

**Protect your secrets.** `config/config.yaml`, the `certs/` directory and
`switch/hosts.txt` are git-ignored because they hold your addresses and keys. Back
up `certs/keys`: losing it invalidates every token your consoles hold.

**Set the dashboard token.** Without `dashboard.token`, anyone who can reach the
dashboard can schedule and cancel Splatfests. The same goes for the admin tokens of
the services that have one.
