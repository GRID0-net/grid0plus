#!/usr/bin/env bash
#
# Run baas natively on the host instead of rebuilding+recreating its Docker
# image on every change. Iteration is a `go build` (sub-second) instead of a
# full image rebuild + container recreate.
#
# Needs: `docker compose up -d postgres redis dauth aauth stubs nginx` already
# running (everything except baas itself), and `docker compose stop baas` so
# nothing else is bound to :8083.
#
# Shares the exact same signing keys as the Dockerized services: both read
# ./dev/keys, a bind mount (see docker-compose.yml), not the old `keys` named
# volume. Pointing services at different key directories does not error --
# every request is rejected with a confusing "token expired". See CLAUDE.md.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_DIR"

if [ -f .env ]; then
    set -a
    # shellcheck disable=SC1091
    source .env
    set +a
fi

mkdir -p dev/keys dev/images

export SWITCHNET_KEYS_DIR="$REPO_DIR/dev/keys"
export SWITCHNET_KEYS_GENERATE_IF_MISSING=true
export SWITCHNET_BAAS_LISTEN=":8083"
export SWITCHNET_BAAS_IMAGE_DIR="$REPO_DIR/dev/images"
# localhost, not the Docker service names ('postgres'/'redis') -- this runs
# outside the Docker network, but Postgres and Redis both publish to
# 127.0.0.1 already (see docker-compose.yml), so this just works.
export SWITCHNET_DATABASE_URL="${SWITCHNET_DATABASE_URL:-postgres://switchnet:switchnet@localhost:5432/switchnet?sslmode=disable}"
export SWITCHNET_DATABASE_AUTO_MIGRATE=true
export SWITCHNET_REDIS_URL="${SWITCHNET_REDIS_URL:-redis://localhost:6379/0}"
export SWITCHNET_DEVICE_ID_SECRET="${SWITCHNET_DEVICE_ID_SECRET:-dev-only-change-me}"
export SWITCHNET_LOG_LEVEL="${SWITCHNET_LOG_LEVEL:-debug}"
export SWITCHNET_LOG_FORMAT="${SWITCHNET_LOG_FORMAT:-text}"

exec go run ./cmd/baas
