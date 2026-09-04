#!/usr/bin/env bash
#
# Pull the latest changes and restart only the services they actually affect.
#
# "Affect" is computed, not guessed. Each service's Go package graph is read with
# `go list -deps`, so a change to internal/db restarts every service that imports
# it and a change to internal/baas restarts only baas. Guessing from directory
# names instead is how a shared package changes and half the stack keeps running
# the old code -- which then shows up as an inconsistency nobody can reproduce.
#
# There is no readiness check after restarting: none of the Go services expose
# a health endpoint this script can poll (only Postgres and Redis have a
# docker-compose `healthcheck:`). `docker compose ps` after the restart shows
# container state, not "answering requests correctly" -- watch
# `docker compose logs -f <service>` for that.
#
#   scripts/update.sh              pull, then restart what changed
#   scripts/update.sh --dry-run    say what would restart, change nothing
#   scripts/update.sh --all        restart everything regardless
#
set -euo pipefail

cd "$(dirname "$0")/.."

DRY=0
ALL=0
for arg in "$@"; do
    case "$arg" in
        --dry-run) DRY=1 ;;
        --all) ALL=1 ;;
        -h|--help) sed -n '2,18p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "update.sh: unknown option $arg" >&2; exit 2 ;;
    esac
done

say()  { printf '\033[1m%s\033[0m\n' "$*"; }
warn() { printf '\033[33mwarning:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

command -v docker >/dev/null || die "docker is not installed"
command -v go >/dev/null || die "go is not installed (needed to work out what changed)"

# Uncommitted work would be silently reverted or would block the pull half way
# through, leaving the tree in a state neither version.
if [ -n "$(git status --porcelain)" ]; then
    die "there are uncommitted changes. Commit or stash them first:
$(git status --short | sed 's/^/  /')"
fi

BEFORE=$(git rev-parse HEAD)

say "==> pulling"
BRANCH=$(git rev-parse --abbrev-ref HEAD)
git pull --ff-only origin "$BRANCH"

AFTER=$(git rev-parse HEAD)

if [ "$BEFORE" = "$AFTER" ] && [ "$ALL" -eq 0 ]; then
    say "==> already up to date; nothing to restart"
    exit 0
fi

if [ "$BEFORE" != "$AFTER" ]; then
    echo
    git --no-pager log --oneline "$BEFORE..$AFTER" | sed 's/^/  /'
    echo
fi

# ---- which services does this change reach? -------------------------------

# Every service that runs as a container. switchnetctl is a CLI, not a service,
# so it is deliberately not here -- it is rebuilt with the image either way.
# gamesync is not listed separately: it is served from npln's own process, on
# its own internal port, not a separate cmd/ binary.
SERVICES="dns dauth aauth baas stubs natcheck npln dashboard"

CHANGED=$(git diff --name-only "$BEFORE" "$AFTER")

restart=""

if [ "$ALL" -eq 1 ]; then
    restart="$SERVICES nginx"
    say "==> --all: restarting everything"
else
    # A change to the build itself, or to a dependency, affects every binary.
    if echo "$CHANGED" | grep -qE '^(go\.mod|go\.sum|Dockerfile)$'; then
        say "==> the build inputs changed; every service is affected"
        restart="$SERVICES"
    else
        MODULE=$(go list -m)
        for svc in $SERVICES; do
            # The package graph for this one binary, module-local packages only.
            deps=$(go list -deps "./cmd/$svc" 2>/dev/null \
                   | grep "^$MODULE" \
                   | sed "s|^$MODULE/||; s|^$MODULE\$|.|")

            for file in $CHANGED; do
                # Walk UP from the changed file until a directory is one of the
                # packages this binary compiles in.
                #
                # Up, not just the file's own directory: assets embedded with
                # embed.FS live in a subdirectory that is not itself a Go package
                # -- internal/dashboard/assets and .../templates are compiled
                # into the dashboard binary but would match nothing. Missing
                # those means a CSS change does not restart the dashboard and
                # the operator concludes the update did not work.
                dir=$(dirname "$file")
                while [ "$dir" != "." ] && [ "$dir" != "/" ]; do
                    if echo "$deps" | grep -qx "$dir"; then
                        restart="$restart $svc"
                        break 2
                    fi
                    dir=$(dirname "$dir")
                done
            done
        done
    fi

    # nginx serves generated configuration and static files rather than Go code.
    if echo "$CHANGED" | grep -qE '^(deploy/|internal/nintendo/)'; then
        restart="$restart nginx"
        say "==> the host list or edge configuration changed; nginx is affected"
    fi
fi

# Deduplicate, keeping it readable.
restart=$(echo "$restart" | tr ' ' '\n' | grep -v '^$' | sort -u | tr '\n' ' ')

if [ -z "$restart" ]; then
    say "==> nothing that runs as a service changed"
    echo "  (documentation, tests or tooling only)"
    exit 0
fi

say "==> affected services:"
echo "  $restart"

if [ "$DRY" -eq 1 ]; then
    echo
    say "--dry-run: nothing was changed"
    exit 0
fi

# ---- regenerate anything derived ------------------------------------------

if echo "$restart" | grep -q nginx; then
    say "==> regenerating the nginx configuration"
    mkdir -p deploy
    go run ./cmd/switchnetctl -config config/config.yaml nginx > deploy/nginx.generated.conf
fi

# ---- rebuild and restart ---------------------------------------------------

# One image serves every Go service, so a rebuild is one build regardless of how
# many services are affected. Only the named containers are recreated.
say "==> rebuilding"
docker compose build $(echo "$restart" | tr ' ' '\n' | grep -v nginx | tr '\n' ' ')

DASH_TOKEN=$(sed -n 's/^[[:space:]]*token:[[:space:]]*"\(.*\)".*/\1/p' config/config.yaml | tail -1)
[ -n "$DASH_TOKEN" ] && export SWITCHNET_DASHBOARD_TOKEN="$DASH_TOKEN"

say "==> restarting"
# up, not restart: `docker compose restart` reuses the OLD container and would
# run the previous image, which looks like the update silently not working.
docker compose up -d --no-deps $restart

echo
docker compose ps $restart

say "==> updated to $(git rev-parse --short HEAD)"
