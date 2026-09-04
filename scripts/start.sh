#!/usr/bin/env bash
#
# Start the whole SwitchNet stack.
#
# Everything is a container, so this is mostly a wrapper around
# `docker compose up`. What it adds is the checks that turn a silent
# misconfiguration into a message: the things this stack fails on are almost
# never the containers not starting.
#
# There is no readiness check here: none of the Go services expose a
# health endpoint main can poll (only Postgres and Redis have a
# docker-compose `healthcheck:`). "started" below means the containers are
# up, not that each one has confirmed it can serve a request -- `docker
# compose ps` and `docker compose logs` are how you check that.
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
        -h|--help) sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
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
mkdir -p deploy
go run ./cmd/switchnetctl -config config/config.yaml nginx > deploy/nginx.generated.conf

# ---- configuration worth stopping for -------------------------------------

say "==> checking the configuration"
if ! go run ./cmd/switchnetctl -config config/config.yaml check; then
    die "the configuration is not valid; nothing was started"
fi

# The dashboard reads this to authenticate its own admin surface; a person
# following this script by hand would otherwise have to export it separately
# every time, and forgetting is silent -- the dashboard just answers 401.
DASH_TOKEN=$(sed -n 's/^[[:space:]]*token:[[:space:]]*"\(.*\)".*/\1/p' config/config.yaml | tail -1)
if [ -n "$DASH_TOKEN" ]; then
    export SWITCHNET_DASHBOARD_TOKEN="$DASH_TOKEN"
else
    warn "dashboard.token is not set, so the dashboard has no authentication:
  anyone who can reach its port can schedule and cancel Splatfests. Set it in
  config/config.yaml once phase 3's Nintendo Account work adds a real login."
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

DASHBOARD_PORT=$(sed -n 's/^[[:space:]]*listen:[[:space:]]*"\(.*\)".*/\1/p' config/config.yaml | tail -1)
DASHBOARD_PORT="${DASHBOARD_PORT##*:}"
DASHBOARD_PORT="${DASHBOARD_PORT:-8087}"

say "==> started"
echo "  dashboard   http://$(hostname -I 2>/dev/null | awk '{print $1}'):${DASHBOARD_PORT}"
echo "  logs        docker compose logs --tail=50 <service>, if something looks wrong"
echo
echo "Next: create an account and generate the console's hosts file --"
echo "  go run ./cmd/switchnetctl -config config/config.yaml account create -h"
echo "  go run ./cmd/switchnetctl -config config/config.yaml hosts"
