#!/usr/bin/env bash
# Run locally with sudo only after the agent has armed both GDB probes.
# Drop direct UDP to/from one Switch; leave TCP GDB and Internet traffic alone.
# The private table is removed on exit, Ctrl+C, or after the bounded capture.
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "Run with sudo: $0 SWITCH_IP [SECONDS]" >&2
  exit 1
fi
switch_ip=${1:?Usage: capture-delayed-peer.sh SWITCH_IP [SECONDS]}
seconds=${2:-90}
python3 - "$switch_ip" "$seconds" <<'PY'
import ipaddress, sys
address = ipaddress.IPv4Address(sys.argv[1])
assert address.is_private and not address.is_loopback and not address.is_multicast
assert 15 <= int(sys.argv[2]) <= 180
PY
command -v nft >/dev/null
command -v tcpdump >/dev/null
table="grid0_probe_$(date -u +%H%M%S)_$$"
out="/tmp/${table}.pcap"
capture_pid=''
created=0
cleanup() {
  trap - EXIT INT TERM
  if [[ -n $capture_pid ]] && kill -0 "$capture_pid" 2>/dev/null; then
    kill -INT "$capture_pid" 2>/dev/null || true
    wait "$capture_pid" 2>/dev/null || true
  fi
  if [[ $created == 1 ]]; then
    nft list table inet "$table" || true
    nft delete table inet "$table"
    echo "Removed temporary UDP block: $table"
  fi
  if [[ -f $out ]]; then
    chmod 600 "$out"
    if [[ -n ${SUDO_UID:-} && -n ${SUDO_GID:-} ]]; then
      chown "$SUDO_UID:$SUDO_GID" "$out"
    fi
    echo "Capture preserved: $out"
  fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
if nft list table inet "$table" >/dev/null 2>&1; then
  echo "Refusing to reuse existing table: $table" >&2
  exit 1
fi
nft add table inet "$table"
created=1
nft -f - <<RULES
add chain inet $table output { type filter hook output priority 0; policy accept; }
add chain inet $table input { type filter hook input priority 0; policy accept; }
add rule inet $table output ip daddr $switch_ip meta l4proto udp counter drop
add rule inet $table input ip saddr $switch_ip meta l4proto udp counter drop
RULES
umask 077
timeout -s INT "$seconds" tcpdump -i any -nn -s 0 -U -w "$out" udp &
capture_pid=$!
sleep 1
kill -0 "$capture_pid"
echo "Direct UDP to/from $switch_ip blocked for at most $seconds seconds. TCP GDB is unaffected."
echo "Queue/force-start only when the agent confirms both probes are ready."
wait "$capture_pid" || status=$?
if [[ ${status:-0} != 0 && ${status:-0} != 124 && ${status:-0} != 130 ]]; then
  exit "$status"
fi
