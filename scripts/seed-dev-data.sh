#!/usr/bin/env bash
#
# Create a few accounts, friendships and presence states by driving the real
# HTTP API, so there is something to look at while developing.
#
# This deliberately goes through the API rather than writing rows directly: it
# then also serves as an end-to-end smoke test of the whole chain, and it cannot
# create a state the server would refuse to produce itself. If this script
# works, a console has a good chance of working.
#
# Usage:
#   scripts/seed-dev-data.sh [-d http://localhost:8081] [-a http://localhost:8082] \
#                            [-b http://localhost:8083] [-n 4]

set -euo pipefail

DAUTH="${SWITCHNET_DAUTH_URL:-http://localhost:8081}"
AAUTH="${SWITCHNET_AAUTH_URL:-http://localhost:8082}"
BAAS="${SWITCHNET_BAAS_URL:-http://localhost:8083}"
COUNT=4

# Splatoon 3 and Mario Kart 8 Deluxe, so the seeded presence looks like
# something and the title-id path is exercised.
SPLATOON3="0100c2500fc20000"
MK8D="0100152000022000"

while getopts "d:a:b:n:h" opt; do
    case "$opt" in
        d) DAUTH="$OPTARG" ;;
        a) AAUTH="$OPTARG" ;;
        b) BAAS="$OPTARG" ;;
        n) COUNT="$OPTARG" ;;
        h) sed -n '2,/^set -euo/p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) exit 2 ;;
    esac
done

command -v curl >/dev/null || { echo "seed: curl is not installed" >&2; exit 1; }
command -v jq   >/dev/null || { echo "seed: jq is not installed" >&2; exit 1; }

NICKNAMES=(Ayame Kai Rin Sora Nova Pip Juno Wren)

# api METHOD PATH [BEARER] [CONTENT_TYPE] [BODY]
api() {
    local method="$1" url="$2" token="${3:-}" ctype="${4:-}" body="${5:-}"
    local args=(-fsS -X "$method" "$url")
    [ -n "$token" ] && args+=(-H "Authorization: Bearer $token")
    [ -n "$ctype" ] && args+=(-H "Content-Type: $ctype")
    [ -n "$body" ]  && args+=(--data-raw "$body")
    curl "${args[@]}"
}

echo "seed: checking the servers are up"
for url in "$DAUTH/keys" "$AAUTH/keys" "$BAAS/1.0.0/certificates"; do
    if ! curl -fsS -o /dev/null "$url"; then
        echo "seed: $url is not answering. Is docker compose up?" >&2
        exit 1
    fi
done

echo "seed: walking the auth chain"

# 1. A device token. Each seeded console gets its own client certificate
# fingerprint, so they get distinct device ids -- which is what a real
# deployment behind nginx with ssl_verify_client looks like.
device_token() {
    local fingerprint="$1"
    curl -fsS -X POST "$DAUTH/v7/device_auth_token" \
        -H "Content-Type: application/x-www-form-urlencoded" \
        -H "X-SwitchNet-Client-Cert: $fingerprint" \
        --data-raw "client_id=8f849b5d34778d8e&ist=false&key_generation=13" \
        | jq -r .device_auth_token
}

# 2. An application token for a title.
app_token() {
    local dtoken="$1" title="$2"
    curl -fsS -X POST "$AAUTH/v3/application_auth_token" \
        -H "Content-Type: application/x-www-form-urlencoded" \
        --data-urlencode "application_id=$title" \
        --data-urlencode "application_version=00010000" \
        --data-urlencode "device_auth_token=$dtoken" \
        --data-urlencode "media_type=DIGITAL" \
        | jq -r .application_auth_token
}

# 3. An anonymous BAAS token.
anon_token() {
    local dtoken="$1"
    curl -fsS -X POST "$BAAS/1.0.0/application/token" \
        -H "Content-Type: application/x-www-form-urlencoded" \
        --data-urlencode "grantType=public_client" \
        --data-urlencode "assertion=$dtoken" \
        | jq -r .accessToken
}

USER_IDS=()
DEVICE_ACCOUNT_IDS=()
ACCESS_TOKENS=()
FRIEND_CODES=()

for i in $(seq 0 $((COUNT - 1))); do
    nickname="${NICKNAMES[$((i % ${#NICKNAMES[@]}))]}"
    echo "seed: account $((i + 1))/$COUNT ($nickname)"

    dtoken="$(device_token "seed-console-$i")"
    anon="$(anon_token "$dtoken")"

    # Register.
    created="$(api POST "$BAAS/1.0.0/users" "$anon")"
    uid="$(echo "$created" | jq -r .id)"
    daid="$(echo "$created" | jq -r '.deviceAccounts[0].id')"
    # The password is returned exactly once, here. There is no way to read it
    # back, which is why the console stores it immediately.
    password="$(echo "$created" | jq -r '.deviceAccounts[0].password')"

    # Log in, with an application token so the id token carries the title.
    atoken="$(app_token "$dtoken" "$SPLATOON3")"
    login="$(curl -fsS -X POST "$BAAS/1.0.0/login" \
        -H "Authorization: Bearer $anon" \
        -H "Content-Type: application/x-www-form-urlencoded" \
        --data-urlencode "id=$daid" \
        --data-urlencode "password=$password" \
        --data-urlencode "appAuthNToken=$atoken" \
        --data-urlencode "naCountry=NL" \
        --data-urlencode "isPersistent=true")"
    access="$(echo "$login" | jq -r .accessToken)"

    # A nickname, so the dashboard and the friends applet show something.
    api PATCH "$BAAS/1.0.0/users/$uid" "$access" "application/json-patch+json" \
        "[{\"op\":\"replace\",\"path\":\"/nickname\",\"value\":\"$nickname\"}]" > /dev/null

    code="$(api POST "$BAAS/1.0.0/users/$uid/generate_code" "$access" \
        "application/x-www-form-urlencoded" "type=NX" | jq -r '.links.friendCode.id')"

    USER_IDS+=("$uid")
    DEVICE_ACCOUNT_IDS+=("$daid")
    ACCESS_TOKENS+=("$access")
    FRIEND_CODES+=("$code")

    echo "      user $uid  friend code $code"
done

echo "seed: making everybody friends with account 1"
for i in $(seq 1 $((COUNT - 1))); do
    request="$(api POST "$BAAS/2.0.0/friend_requests" "${ACCESS_TOKENS[0]}" \
        "application/json" "{\"friendCode\":\"${FRIEND_CODES[$i]}\"}")"
    rid="$(echo "$request" | jq -r .id)"

    api PATCH "$BAAS/2.0.0/friend_requests/$rid" "${ACCESS_TOKENS[$i]}" \
        "application/json-patch+json" \
        '[{"op":"replace","path":"/state","value":"ACCEPTED"}]' > /dev/null

    echo "      ${USER_IDS[0]} <-> ${USER_IDS[$i]}"
done

echo "seed: setting presence"

# report INDEX STATE TITLE APPFIELD
report() {
    local i="$1" state="$2" title="$3" field="$4"
    local ops="[{\"op\":\"replace\",\"path\":\"/presence/state\",\"value\":\"$state\"}"
    if [ -n "$title" ]; then
        ops="$ops,{\"op\":\"add\",\"path\":\"/presence/extras/friends/appField\",\"value\":$(jq -Rn --arg v "$field" '$v')}"
        ops="$ops,{\"op\":\"add\",\"path\":\"/presence/extras/friends/appInfo:appId\",\"value\":\"$title\"}"
        ops="$ops,{\"op\":\"add\",\"path\":\"/presence/extras/friends/appInfo:presenceGroupId\",\"value\":\"$title\"}"
    fi
    ops="$ops]"

    api PATCH "$BAAS/1.0.0/users/${USER_IDS[$i]}/device_accounts/${DEVICE_ACCOUNT_IDS[$i]}" \
        "${ACCESS_TOKENS[$i]}" "application/json-patch+json" "$ops" > /dev/null
}

# A mix, so every presence state is represented.
[ "$COUNT" -gt 0 ] && report 0 PLAYING "$SPLATOON3" '{"mode":"regular"}'
[ "$COUNT" -gt 1 ] && report 1 PLAYING "$MK8D" '{"mode":"grandprix"}'
[ "$COUNT" -gt 2 ] && report 2 ONLINE  ""         ''
[ "$COUNT" -gt 3 ] && report 3 OFFLINE ""         ''

echo
echo "seed: done. $COUNT accounts, all friends with ${USER_IDS[0]}."
echo
echo "Account 1's friend list, as the console would fetch it:"
#
# The response is a {count, etag, itemsPerPage, items} ENVELOPE, not a bare
# array, and each item is a relationship object whose `friend` sub-object holds
# the person. Both were settled by the 2026-08-25 capture -- see
# docs/CAPTURES/baas-2.0.0-friends.md. This read used to assume a bare array of
# users and printed a jq error instead of a friend list, which is a small thing
# except that this script is the smoke test somebody runs to find out whether
# the chain works.
# itemsPerPage, not count: `count` echoes the REQUESTED page size, which the
# capture confirms and which reads confusingly next to a one-entry list.
api GET "$BAAS/2.0.0/users/${USER_IDS[0]}/friends?count=300" "${ACCESS_TOKENS[0]}" \
    | jq '{friends: .itemsPerPage, items: [.items[] | {
             nickname:  .friend.nickname,
             favourite: .isFavorite,
             state:     .friend.presence.state,
             playing:   .friend.presence.extras.friends["appInfo:appId"]}]}'
