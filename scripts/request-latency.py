#!/usr/bin/env python3
"""Summarize backend RPC time without printing credentials or player data."""
import argparse
import collections
import json
import shlex
import subprocess


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--since", default="15 minutes ago")
    args = parser.parse_args()
    process = subprocess.Popen(
        ["journalctl", "-u", "switchnet-npln", "--since", args.since,
         "--no-pager", "-o", "json"], stdout=subprocess.PIPE, text=True)
    groups = collections.defaultdict(list)
    refusals = collections.Counter()
    for line in process.stdout:
        try:
            message = json.loads(line).get("MESSAGE", "")
            if not isinstance(message, str):
                continue
            fields = dict(part.split("=", 1) for part in shlex.split(message) if "=" in part)
            if "duration_us" not in fields or "method" not in fields:
                continue
            groups[fields["method"]].append(int(fields["duration_us"]))
            if fields.get("code", "OK") != "OK":
                refusals[fields["method"]] += 1
        except (ValueError, TypeError):
            continue
    if process.wait():
        raise SystemExit("journalctl failed")
    print("Backend time includes auth and handler work; it excludes WAN round trips.")
    print("calls errors p50_ms p95_ms p99_ms max_ms method")
    for method, values in sorted(groups.items()):
        values.sort()
        def percentile(ratio):
            return values[max(0, (len(values) * ratio + 99) // 100 - 1)] / 1000
        print(len(values), refusals[method],
              *(f"{percentile(p):.3f}" for p in (50, 95, 99)),
              f"{values[-1] / 1000:.3f}", method)
    if not groups:
        print("No timed unary RPCs in this window.")


if __name__ == "__main__":
    main()
