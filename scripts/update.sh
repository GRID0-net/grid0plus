#!/usr/bin/env bash
#
# Pull the latest changes and restart only the services they actually affect.
#
# "Affect" is computed, not guessed. Each service's Go package graph is read
# with `go list -deps`, so a change to internal/db restarts every service
# that imports it and a change to internal/baas restarts only baas. Guessing
# from directory names instead is how a shared package changes and half the
# stack keeps running the old code -- which then shows up as an
# inconsistency nobody can reproduce.
#
# No `make check` gate: this operator's own call is that the test suite
# never runs against this deployment, so a broken commit reaching
# `systemctl restart` has nothing standing in the way except the build
# itself. Run `make check` yourself before pushing if you want that safety
# net -- it belongs in CI or a pre-push hook, not this script.
#
#   scripts/update.sh              pull, then restart what changed
#   scripts/update.sh --dry-run    say what would restart, change nothing
#   scripts/update.sh --all        restart everything regardless
#
# Already on the latest commit? There is nothing to diff against to work out
# which services a pull touched, so this rebuilds and restarts everything
# rather than reporting "up to date" and exiting -- the same as --all.
#
set -euo pipefail

cd "$(dirname "$0")/.."

DRY=0
ALL=0
for arg in "$@"; do
    case "$arg" in
        --dry-run) DRY=1 ;;
        --all) ALL=1 ;;
        -h|--help) sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "update.sh: unknown option $arg" >&2; exit 2 ;;
    esac
done

say()  { printf '\033[1m%s\033[0m\n' "$*"; }
warn() { printf '\033[33mwarning:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

command -v go >/dev/null || die "go is not installed (needed to work out what changed, and to build it)"

# The systemd units hardcode /opt/switchnet (see deploy/systemd/*.service). A
# 'go build' from anywhere else would write a bin/ that systemctl restart
# never looks at, restart the service, and leave the old binary running --
# silently, since every step up to the restart itself would still succeed.
[ "$(pwd)" = "/opt/switchnet" ] || die "this must be run from an /opt/switchnet checkout, not $(pwd)."

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

if [ "$BEFORE" = "$AFTER" ]; then
    # Nothing new to diff against for affected-service detection -- rebuild
    # and restart everything instead of doing nothing, since the point of
    # running this at all is to end up with freshly-built binaries running,
    # not to report that git had nothing to pull.
    say "==> already on the latest commit; rebuilding and restarting everything anyway"
    ALL=1
fi

if [ "$BEFORE" != "$AFTER" ]; then
    echo
    git --no-pager log --oneline "$BEFORE..$AFTER" | sed 's/^/  /'
    echo
fi

# ---- which services does this change reach? -------------------------------

# Every service that runs as a systemd unit. switchnetctl is a CLI, not a
# service, so it is deliberately not here -- 'make build' always rebuilds it.
SERVICES="dauth aauth baas scsi stubs natcheck npln dashboard toolbox"

CHANGED=$(git diff --name-only "$BEFORE" "$AFTER")

restart=""

if [ "$ALL" -eq 1 ]; then
    restart="$SERVICES nginx"
    say "==> --all: restarting everything"
else
    # A change to the build itself affects every binary.
    if echo "$CHANGED" | grep -qE '^(go\.mod|go\.sum)$'; then
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
    go run ./cmd/switchnetctl -config config/config.yaml nginx > deploy/nginx.generated.conf
fi

go_restart=$(echo "$restart" | tr ' ' '\n' | grep -v '^nginx$' | tr '\n' ' ')

# ---- build (no test gate) --------------------------------------------------

if [ -n "$go_restart" ]; then
    say "==> building (no fmt/vet/test gate): $go_restart"
    for svc in $go_restart; do
        go build -o "bin/$svc" "./cmd/$svc"
    done
fi

# ---- restart ----------------------------------------------------------

say "==> restarting"
for svc in $go_restart; do
    sudo systemctl restart "switchnet-$svc.service"
done
if echo "$restart" | grep -q nginx; then
    command -v docker >/dev/null || die "docker is not installed (needed for the nginx edge)"
    docker compose exec nginx nginx -s reload
fi

if [ -n "$go_restart" ]; then
    echo
    # shellcheck disable=SC2046
    systemctl status --no-pager --lines=0 $(printf 'switchnet-%s.service ' $go_restart) || true
fi

# ---- did they come back? ---------------------------------------------------

ADMIN_TOKEN=$(sed -n 's/^[[:space:]]*admin_token:[[:space:]]*"\(.*\)".*/\1/p' config/config.yaml | head -1)

say "==> waiting for services"
sleep 2
failed=""
for svc in $go_restart; do
    case "$svc" in
        baas)      port=8083 ;;
        dauth)     port=8081 ;;
        aauth)     port=8082 ;;
        stubs)     port=8084 ;;
        dashboard) port=8087 ;;
        toolbox)   port=8090 ;;
        scsi)      port=8091 ;;
        npln)
            [ -n "$ADMIN_TOKEN" ] || continue
            port=8089 ;;
        *) continue ;;   # natcheck is UDP; nginx has no /healthz
    esac

    ok=0
    for _ in $(seq 1 20); do
        if curl -fsS "http://127.0.0.1:$port/healthz" >/dev/null 2>&1; then ok=1; break; fi
        sleep 1
    done
    [ "$ok" -eq 1 ] && echo "  $svc ok" || { echo "  $svc NOT healthy"; failed="$failed $svc"; }
done

if [ -n "$failed" ]; then
    warn "these did not come back healthy:$failed
  journalctl -u switchnet-<name> -n 50 --no-pager, replacing <name> with one of:$failed"
    exit 1
fi

say "==> updated to $(git rev-parse --short HEAD)"
