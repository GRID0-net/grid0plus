#!/usr/bin/env bash
#
# Install GRID0+'s Go services as systemd units, for the always-on box
# serving your consoles: creates a dedicated switchnet user, builds every
# service, installs deploy/systemd/*.service, and enables switchnet.target.
# Only the steps that need root (creating the user, changing ownership,
# installing units) use sudo -- the build runs as you, so the build cache
# and bin/ stay owned by you rather than root.
#
# Does not start anything. That is a separate, deliberate step:
#
#   scripts/install-systemd.sh
#   sudo systemctl start switchnet.target
#
set -euo pipefail

cd "$(dirname "$0")/.."

say()  { printf '\033[1m%s\033[0m\n' "$*"; }
warn() { printf '\033[33mwarning:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# The unit files in deploy/systemd/ hardcode /opt/switchnet (WorkingDirectory=,
# ExecStart=), since a static file can't discover an arbitrary checkout path at
# install time. Installing from anywhere else would enable units pointing at a
# checkout that isn't there.
[ "$(pwd)" = "/opt/switchnet" ] || die "this must be run from an /opt/switchnet checkout, not $(pwd).
  Move or clone this repo to /opt/switchnet and run this again from there."

source scripts/preflight.sh

say "==> building"
make build

say "==> creating the switchnet user"
if id switchnet >/dev/null 2>&1; then
    say "    switchnet already exists"
else
    sudo useradd --system --no-create-home --shell /usr/sbin/nologin switchnet
fi

say "==> setting ownership"
mkdir -p certs/keys data/images data/save-archives data/keepalive
sudo chown -R switchnet:switchnet certs/keys data/images data/save-archives data/keepalive

say "==> installing the systemd units"
sudo cp deploy/systemd/switchnet-*.service deploy/systemd/switchnet.target /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable switchnet.target
# The keepalive is enabled here, and not as part of switchnet.target, so that
# one install step covers the whole deployment. It is a host helper rather
# than a Nintendo-facing service: it never talks to a console, and it idles
# by itself on a host that is not a cloud instance with an idle policy -- see
# deploy/keepalive/README.md.
sudo systemctl enable switchnet-keepalive.service

echo
say "==> installed, not started"
echo "  sudo systemctl start switchnet.target      # start everything"
echo "  systemctl status switchnet.target           # is it up"
echo "  journalctl -u switchnet-baas -f              # one service's logs"
echo "  systemctl status switchnet-keepalive         # the idle-reclaim keepalive"
echo
echo "Re-run this script after 'git pull' to pick up new/changed units; it is"
echo "safe to run again (the user and ownership steps are no-ops once done)."
echo "For code changes with no unit changes, use scripts/update.sh instead."
