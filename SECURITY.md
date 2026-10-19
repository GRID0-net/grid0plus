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

**Only sign-in is rate limited.** Login attempts are limited per client address
(`baas.login_attempts_per_window` per `baas.login_window_seconds`). Nothing else is.
Friend codes are a large space, but not an unreachable one, and turning registration
off removes most of that risk. If you need more, nginx's `limit_req` is the place for
it.

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

**The dashboard is plain HTTP, and its token is its session.** The dashboard listens
without TLS, so the login cookie is not marked `Secure` and the token crosses the
network in the clear on every request. Anyone who captures it can use everything the
dashboard can do: schedule fests, force matches, edit database rows and delete
accounts. Don't expose the dashboard port to the internet. Bind it to loopback and
reach it over an SSH tunnel or a VPN, or put it behind a TLS proxy, and don't use
`?token=` links, which end up in browser history and proxy logs.

**Set the dashboard token.** Without `dashboard.token`, anyone who can reach the
dashboard can schedule and cancel Splatfests. The same goes for the admin tokens of
the services that have one.

## Hardening a deployment

None of this is on by default.

- **Restrict the TURN relay.** coturn will relay to any address unless you tell it
  not to, including your private network. Add `denied-peer-ip` ranges for loopback,
  RFC 1918, link-local and your cloud's metadata network, plus `no-multicast-peers`,
  `no-tcp-relay` and a `user-quota`, to `turnserver.conf`.
- **Sandbox the services.** All the Go services run as one user and share the key
  directory, so a flaw in the one that parses the most network input exposes every
  key. Add `NoNewPrivileges=yes`, `ProtectSystem=strict`, `ProtectHome=yes`,
  `PrivateTmp=yes` and explicit `ReadWritePaths=` to the systemd units, and check each
  service still starts.
- **The Toolbox download endpoints are unauthenticated.** With `toolbox.github_token`
  set, anyone who can reach them can fetch any release asset of the configured
  repository by id. Use a repository whose releases are public, or a token that can
  only read public data.
- **Keep copies of the config private.** An editor or deploy script that leaves a
  `config.yaml.bak` behind with default permissions leaves your tokens readable. Use
  `chmod 600`, and rotate any secret that has been printed to a terminal or a log.
- **Close what you don't use.** Turn off `baas.allow_registration` once everyone has
  an account, and remove services such as `rpcbind` that the machine doesn't need.
