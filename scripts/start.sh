#!/usr/bin/env bash
#
# Start the whole SwitchNet stack.
#
# Everything is a container, including the phase-2 services, so this is mostly a
# wrapper around `docker compose up`. What it adds is the checks that turn a
# silent misconfiguration into a message: the things this stack fails on are
# almost never the containers not starting.
#
#   scripts/start.sh              start everything
#   scripts/start.sh --no-build   skip the image build (faster, use after update.sh)
#
set -euo pipefail

cd "$(dirname "$0")/.."

BUILD=1
for arg in "$@"; do
    case "$arg" in
        --no-build) BUILD=0 ;;
        -h|--help) sed -n '2,14p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "start.sh: unknown option $arg" >&2; exit 2 ;;
    esac
done

say()  { printf '\033[1m%s\033[0m\n' "$*"; }
warn() { printf '\033[33mwarning:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# ---- what has to exist before anything starts -----------------------------

command -v docker >/dev/null || die "docker is not installed"
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

# The matchmaking controls live in npln and the dashboard reaches them over the
# compose network. Both containers read the same variable, so exporting it here
# is what keeps them in step.
ADMIN_TOKEN=$(sed -n 's/^[[:space:]]*admin_token:[[:space:]]*"\(.*\)".*/\1/p' config/config.yaml | head -1)
if [ -n "$ADMIN_TOKEN" ]; then
    export SWITCHNET_NPLN_ADMIN_TOKEN="$ADMIN_TOKEN"
else
    warn "npln.admin_token is not set, so the dashboard's matchmaking controls
  will say they are not configured. Set it in config/config.yaml to enable them."
fi

DASH_TOKEN=$(sed -n 's/^[[:space:]]*token:[[:space:]]*"\(.*\)".*/\1/p' config/config.yaml | tail -1)
if [ -n "$DASH_TOKEN" ]; then
    export SWITCHNET_DASHBOARD_TOKEN="$DASH_TOKEN"
fi

# ---- start ----------------------------------------------------------------

say "==> starting"
if [ "$BUILD" -eq 1 ]; then
    docker compose up -d --build
else
    docker compose up -d
fi

echo
docker compose ps

# ---- did it actually come up? ---------------------------------------------
#
# `docker compose ps` says the containers are running, which is not the same as
# the services working. Every HTTP service answers /healthz, so ask them.

say "==> waiting for services"
DASHBOARD_PORT=$(sed -n 's/^[[:space:]]*listen:[[:space:]]*"\(.*\)".*/\1/p' config/config.yaml | tail -1)
DASHBOARD_PORT="${DASHBOARD_PORT##*:}"
DASHBOARD_PORT="${DASHBOARD_PORT:-8087}"

healthy=0
for _ in $(seq 1 30); do
    if curl -fsS "http://127.0.0.1:8083/healthz" >/dev/null 2>&1; then
        healthy=1
        break
    fi
    sleep 1
done

if [ "$healthy" -eq 1 ]; then
    say "==> up"
    echo "  dashboard   http://$(hostname -I 2>/dev/null | awk '{print $1}'):${DASHBOARD_PORT}"
    echo "  health      the Health tab there, or curl 127.0.0.1:8083/healthz"
    echo
    echo "Next: create an account and generate the console's hosts file --"
    echo "  go run ./cmd/switchnetctl -config config/config.yaml account create -h"
    echo "  go run ./cmd/switchnetctl -config config/config.yaml hosts"
else
    warn "baas did not become healthy within 30s. The containers may still be
  starting, or something is wrong. Look at:
    docker compose logs --tail=50 baas"
    exit 1
fi
