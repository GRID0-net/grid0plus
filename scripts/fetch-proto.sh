#!/usr/bin/env bash
# Fetch the upstream NPLN protobuf definitions.
#
# The definitions are Kinnay's, decompiled from the game and published at
# github.com/kinnay/NPLN-Protocols. They are NOT vendored as .proto files in
# this repository: they are fetched here and compiled by gen-proto.sh, so an
# upstream update is one command rather than a diffing exercise. Only the
# GENERATED Go lands in git (see proto/), which keeps `go build` working with
# no network and no protobuf toolchain.
#
# The commit is pinned. An unpinned fetch means the wire types can change under
# a build that did not ask for it — and a silently changed field number is
# exactly the class of bug that costs a day on the console side.
set -euo pipefail

readonly UPSTREAM_URL="https://github.com/kinnay/NPLN-Protocols"

# Pinned 2026-08-28. Bump deliberately, regenerate, and read the diff: a field
# number that moved is a wire break, not a cosmetic change.
readonly UPSTREAM_COMMIT="e55caa5385cf72f0282209e95756a71ad3911b6b"

# Which upstream tree to compile. `latest` is the manual decompilation that
# covers current firmware, and the only one carrying Splatoon 3's own
# toyohr/v1 services. 0.20.x and 0.21.x are the automatic decompilations from
# Monster Hunter Rise's reflection data and predate the services this needs.
readonly UPSTREAM_TREE="latest"

readonly ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly DEST="${ROOT}/.proto-upstream"

if [[ -d "${DEST}/.git" ]]; then
    have="$(git -C "${DEST}" rev-parse HEAD 2>/dev/null || echo none)"
    if [[ "${have}" == "${UPSTREAM_COMMIT}" ]]; then
        echo "proto: already at ${UPSTREAM_COMMIT:0:12}"
        exit 0
    fi
    echo "proto: updating ${have:0:12} -> ${UPSTREAM_COMMIT:0:12}"
    git -C "${DEST}" fetch --depth 1 origin "${UPSTREAM_COMMIT}"
    git -C "${DEST}" checkout -q --detach "${UPSTREAM_COMMIT}"
else
    echo "proto: fetching ${UPSTREAM_URL} @ ${UPSTREAM_COMMIT:0:12}"
    rm -rf "${DEST}"
    git init -q "${DEST}"
    git -C "${DEST}" remote add origin "${UPSTREAM_URL}"
    git -C "${DEST}" fetch -q --depth 1 origin "${UPSTREAM_COMMIT}"
    git -C "${DEST}" checkout -q --detach FETCH_HEAD
fi

if [[ ! -d "${DEST}/${UPSTREAM_TREE}/proto" ]]; then
    echo "proto: ${UPSTREAM_TREE}/proto missing from upstream — has the layout changed?" >&2
    exit 1
fi

# Drop upstream's vendored copies of the well-known types.
#
# They are stripped of their `go_package` options, and a local copy SHADOWS the
# ones buf carries internally — so the generator resolves timestamp.proto to a
# descriptor with no Go import path and fails with "unable to determine Go
# import path", naming a Google file nobody here edited. Removing them lets buf
# supply its own, which carry the correct paths and match the runtime already
# in go.mod.
rm -rf "${DEST}/${UPSTREAM_TREE}/google/protobuf"

# Reapply GRID0+'s own additions: fields and messages settled by RE or
# captures that upstream's reconstruction lacks. Kept as one tracked patch so a
# re-fetch cannot silently drop them; see the patch's own comments and
# docs/CAPTURES/README.md for where each came from.
for patch in "${ROOT}"/scripts/proto-patches/*.patch; do
    git -C "${DEST}" apply --whitespace=nowarn "${patch}"
    echo "proto: applied $(basename "${patch}")"
done

echo "proto: $(find "${DEST}/${UPSTREAM_TREE}/proto" -name '*.proto' | wc -l) definitions in ${UPSTREAM_TREE}/"
