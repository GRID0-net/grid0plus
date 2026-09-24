#!/usr/bin/env bash
# Generate Go server stubs from the upstream NPLN definitions.
#
#   scripts/fetch-proto.sh && scripts/gen-proto.sh
#
# Regenerates proto/ from scratch. The generated files ARE committed, so that
# building SwitchNet needs neither the network nor a protobuf toolchain — only
# changing the definitions does.
#
# Do not hand-edit anything under proto/. Every file there carries a
# "Code generated ... DO NOT EDIT." header and the next run overwrites it.
#
# .proto-upstream itself carries local patches, not tracked by git (the
# directory is gitignored -- fetch-proto.sh pins and checks out an upstream
# commit there):
#   - userscreening.proto's GetViolationResponse has a `name` field added,
#     upstream's own copy still marking it "[UNKNOWN]".
#   - resources.proto's SeasonSchedule has a `target` field (8) added,
#     entirely absent from upstream's own reconstruction.
# Re-running fetch-proto.sh discards both patches. See
# internal/toyohr/README.md's GetViolation and SeasonSchedule sections and
# docs/CAPTURES/README.md before regenerating, and reapply both fields first.
set -euo pipefail

readonly ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly UPSTREAM="${ROOT}/.proto-upstream"

# Pinned to match go.mod's runtime versions. A generator newer than the runtime
# it generates against emits code the runtime rejects at init time, which
# surfaces as a panic on the first message rather than a build failure.
readonly BUF_VERSION="v1.47.2"
readonly PROTOC_GEN_GO_VERSION="v1.36.6"
readonly PROTOC_GEN_GO_GRPC_VERSION="v1.5.1"

if [[ ! -d "${UPSTREAM}/latest/proto" ]]; then
    echo "gen-proto: no definitions — run scripts/fetch-proto.sh first" >&2
    exit 1
fi

export PATH="$(go env GOPATH)/bin:${PATH}"

need() {
    local bin="$1" pkg="$2" version="$3"
    if command -v "${bin}" >/dev/null 2>&1; then
        return
    fi
    echo "gen-proto: installing ${bin}@${version}"
    GOFLAGS=-mod=mod go install "${pkg}@${version}"
}

need buf               github.com/bufbuild/buf/cmd/buf                 "${BUF_VERSION}"
need protoc-gen-go     google.golang.org/protobuf/cmd/protoc-gen-go    "${PROTOC_GEN_GO_VERSION}"
need protoc-gen-go-grpc google.golang.org/grpc/cmd/protoc-gen-go-grpc  "${PROTOC_GEN_GO_GRPC_VERSION}"

# Wipe first: a service deleted upstream must disappear here too, or a stale
# stub keeps compiling against a message that no longer exists.
rm -rf "${ROOT}/proto"

cd "${ROOT}"
buf generate

count=$(find "${ROOT}/proto" -name '*.pb.go' | wc -l)
echo "gen-proto: ${count} files generated into proto/"

gofmt -l "${ROOT}/proto" | while read -r f; do
    echo "gen-proto: warning: ${f} is not gofmt-clean" >&2
done
