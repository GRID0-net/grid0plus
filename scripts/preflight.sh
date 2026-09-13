#!/usr/bin/env bash
#
# Checks that turn a silent misconfiguration into a message before anything
# starts, native or containerized: the things this stack fails on are almost
# never a process not starting.
#
# Not run directly -- sourced by scripts/start.sh and
# scripts/install-systemd.sh, so both get the same checks (and the same
# say/warn/die helpers) without duplicating them. Sourcing rather than
# `exec`ing a separate script is what lets a preflight failure's `exit`
# actually stop the caller, and what lets the caller keep using say/warn/die
# afterwards.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

say()  { printf '\033[1m%s\033[0m\n' "$*"; }
warn() { printf '\033[33mwarning:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# ---- what has to exist before anything starts -----------------------------

# Postgres, Redis and the nginx edge still run in Docker -- the Go services
# do not, but these three are off-the-shelf images this repo never rebuilds,
# so Docker was never the slow part for them.
command -v docker >/dev/null || die "docker is not installed (needed for Postgres, Redis and the nginx edge)"
docker compose version >/dev/null 2>&1 || die "docker compose v2 is not installed"
docker info >/dev/null 2>&1 || die "cannot talk to the docker daemon (are you in the docker group?)"

[ -f config/config.yaml ] || die "config/config.yaml does not exist.
  cp config/config.example.yaml config/config.yaml and edit it first."

# The TLS edge will not start without a certificate, and its failure names a
# missing file rather than the step that was skipped.
if [ ! -f certs/edge.crt ] || [ ! -f certs/edge.key ]; then
    say "==> generating the edge certificate"
    scripts/gen-certs.sh -c config/config.yaml
fi

# nginx.generated.conf is generated from the host list, and an out-of-date one
# silently routes a hostname to the wrong backend.
say "==> regenerating the nginx configuration"
go run ./cmd/switchnetctl -config config/config.yaml nginx > deploy/nginx.generated.conf

# ---- configuration worth stopping for -------------------------------------

say "==> checking the configuration"
if ! go run ./cmd/switchnetctl -config config/config.yaml check; then
    die "the configuration is not valid; nothing was started"
fi
