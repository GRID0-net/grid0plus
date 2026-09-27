#!/usr/bin/env bash
#
# Start the whole GRID0+ stack: Postgres, Redis and the nginx edge in
# Docker (the off-the-shelf pieces this repo never rebuilds), the eight Go
# services as systemd units. Run scripts/install-systemd.sh once first, from
# /opt/switchnet, to install those units -- this script only starts them.
#
#   scripts/start.sh
#
set -euo pipefail

cd "$(dirname "$0")/.."

for arg in "$@"; do
    case "$arg" in
        -h|--help) sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "start.sh: unknown option $arg" >&2; exit 2 ;;
    esac
done

source scripts/preflight.sh

if ! systemctl list-unit-files switchnet.target >/dev/null 2>&1; then
    die "switchnet.target is not installed yet. Run this once, from /opt/switchnet:
  scripts/install-systemd.sh"
fi

say "==> starting Postgres, Redis and nginx"
docker compose up -d

say "==> starting the Go services"
sudo systemctl start switchnet.target

echo
docker compose ps
echo
systemctl status switchnet.target --no-pager --lines=0 || true

# ---- did it actually come up? ---------------------------------------------
#
# systemd reporting the units active is not the same as the services working.
# Every HTTP service answers /healthz, so ask baas -- the one every other
# call in this script's own "next steps" depends on.

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
    warn "baas did not become healthy within 30s. Look at:
  journalctl -u switchnet-baas -n 50 --no-pager"
    exit 1
fi
