#!/usr/bin/env bash
#
# Drive phase 2 end to end: register two accounts through the real BAAS API,
# then run scripts/smoke-npln against the game server with the id tokens BAAS
# handed out.
#
# CLAUDE.md rule 4: run it, do not just test it. scripts/seed-dev-data.sh does
# this for phase 1; the gRPC half cannot be driven with curl, so the second stage
# is a small Go client.
#
# Usage:
#   make up
#   scripts/seed-phase2.sh
#
# Two accounts are created each time it runs. That is deliberate: it exercises
# registration, and a private server with a handful of leftover smoke accounts is
# not a problem worth adding cleanup code for. `make clean-data` removes them.

set -euo pipefail

DAUTH="${SWITCHNET_DAUTH_URL:-http://localhost:8081}"
AAUTH="${SWITCHNET_AAUTH_URL:-http://localhost:8082}"
BAAS="${SWITCHNET_BAAS_URL:-http://localhost:8083}"
GATEWAY="${SWITCHNET_NPLN_GATEWAY:-localhost:8085}"
GAMESYNC="${SWITCHNET_NPLN_GAMESYNC:-localhost:8088}"
TENANT="${SWITCHNET_NPLN_TENANT:-dce9377b}"
REGION="${SWITCHNET_SMOKE_REGION:-EU}"

# Splatoon 3, so the application token carries the title the game server expects.
SPLATOON3="0100c2500fc20000"

command -v curl >/dev/null || { echo "seed-phase2: curl is not installed" >&2; exit 1; }
command -v jq   >/dev/null || { echo "seed-phase2: jq is not installed" >&2; exit 1; }

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "seed-phase2: checking the servers are up"
for url in "$DAUTH/keys" "$AAUTH/keys" "$BAAS/1.0.0/certificates"; do
    curl -fsS -o /dev/null "$url" || { echo "  $url is not answering" >&2; exit 1; }
done

tokens=()
users=()

for n in 1 2; do
    echo "seed-phase2: account $n/2"

    # Each console gets its own client-certificate fingerprint, so they get
    # distinct device ids -- what a real deployment behind nginx looks like.
    dtoken="$(curl -fsS -X POST "$DAUTH/v7/device_auth_token" \
        -H "X-SwitchNet-Client-Cert: phase2-console-$n" \
        --data-raw "client_id=8f849b5d34778d8e&ist=false&key_generation=13" \
        | jq -r .device_auth_token)"

    anon="$(curl -fsS -X POST "$BAAS/1.0.0/application/token" \
        --data-urlencode "grantType=public_client" \
        --data-urlencode "assertion=$dtoken" | jq -r .accessToken)"

    created="$(curl -fsS -X POST "$BAAS/1.0.0/users" -H "Authorization: Bearer $anon")"
    uid="$(jq -r .id <<<"$created")"
    daid="$(jq -r '.deviceAccounts[0].id' <<<"$created")"
    # The password is returned exactly once, here, which is why the console
    # stores it immediately.
    password="$(jq -r '.deviceAccounts[0].password' <<<"$created")"

    atoken="$(curl -fsS -X POST "$AAUTH/v3/application_auth_token" \
        --data-urlencode "application_id=$SPLATOON3" \
        --data-urlencode "application_version=00010000" \
        --data-urlencode "device_auth_token=$dtoken" \
        --data-urlencode "media_type=DIGITAL" \
        | jq -r .application_auth_token)"

    # The id token this returns is the phase 1 -> phase 2 seam: it is what the
    # game presents to the NPLN gateway, and the gateway verifies it against the
    # same key BAAS publishes at /1.0.0/certificates.
    login="$(curl -fsS -X POST "$BAAS/1.0.0/login" \
        -H "Authorization: Bearer $anon" \
        --data-urlencode "id=$daid" \
        --data-urlencode "password=$password" \
        --data-urlencode "appAuthNToken=$atoken" \
        --data-urlencode "naCountry=NL" \
        --data-urlencode "isPersistent=true")"

    access="$(jq -r .accessToken <<<"$login")"
    jq -r .idToken <<<"$login" > "$WORK/idtoken$n"

    # A nickname, so the dashboard's match list reads as people rather than ids.
    curl -fsS -o /dev/null -X PATCH "$BAAS/1.0.0/users/$uid" \
        -H "Authorization: Bearer $access" \
        -H "Content-Type: application/json-patch+json" \
        --data "[{\"op\":\"replace\",\"path\":\"/nickname\",\"value\":\"Smoke$n\"}]"

    echo "      user $uid (Smoke$n)"
    tokens+=("$WORK/idtoken$n")
    users+=("$uid")
done

echo
echo "seed-phase2: driving the game server"
echo

go run "$ROOT/scripts/smoke-npln" \
    -tenant "$TENANT" \
    -gateway "$GATEWAY" \
    -gamesync "$GAMESYNC" \
    -region "$REGION" \
    -tokens "$(IFS=,; echo "${tokens[*]}")" \
    -users "$(IFS=,; echo "${users[*]}")"
