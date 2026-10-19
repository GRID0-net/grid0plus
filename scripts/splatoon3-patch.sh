#!/usr/bin/env bash
# Build the Splatoon 3 certificate-verification patch from YOUR OWN game dump,
# and install it where Ryujinx or Atmosphere will find it.
#
# Why this is a script and not a checked-in .ips file
# ---------------------------------------------------
# The patch is one four-byte instruction, but its ADDRESS is found by searching
# the game's own .text segment for a known instruction sequence. So the address
# -- and therefore the whole patch -- differs per build, and the file has to be
# named after that build's module id for the loader to match it. Both of those
# come out of the game binary. There is no dump in this repository and there is
# not going to be one, so there is nothing to ship: a prebuilt patch would be a
# patch for somebody else's copy, and it would go stale on the next title
# update anyway.
#
# The search-and-patch logic is Kinnay's, in generate_patch.py at
# github.com/kinnay/NPLN-Protocols. It is NOT vendored here -- that repository
# carries no licence, so copying it into ours would be helping ourselves to it.
# It is fetched at the same pinned commit scripts/fetch-proto.sh uses, and run
# as-is, which is the same arrangement the protobuf definitions already have.
set -euo pipefail

readonly ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly UPSTREAM="${ROOT}/.proto-upstream"
readonly GENERATOR="${UPSTREAM}/generate_patch.py"

# The name of the directory the patch is installed into. Cosmetic to the
# loader, which matches on the filename's build id, but it is what you will see
# in a mod list, so it should say what it does.
readonly PATCH_NAME="disable_ca_verification"

usage() {
    cat >&2 <<'USAGE'
usage: scripts/splatoon3-patch.sh <main-nso-or-exefs-dir> [--out <dir>] [--sd <dir>]

  <main-nso-or-exefs-dir>  The game's `main` NSO, or a directory holding it.
                           Ryujinx dumps one: right-click the title, then
                           Extract Data, ExeFS.

  --out <dir>   Install into <dir>/exefs_patches/. Defaults to Ryujinx's mods
                directory for this platform.
  --sd  <dir>   Install into an Atmosphere SD card root instead, i.e.
                <dir>/atmosphere/exefs_patches/. Repeatable with --out.
  --print-only  Build the patch and say where it would go, without writing it.

The patch DISABLES certificate verification in the game rather than adding
trust for a certificate, so it applies to every server the game talks to, not
only yours. It is also matched by build id, which means a title update silently
stops it applying -- the symptom is the game going quiet again, and the fix is
re-running this against the updated dump.
USAGE
}

die() { echo "splatoon3-patch: $*" >&2; exit 1; }

# Global rather than a local in main, because the EXIT trap that removes it
# runs after main has returned and its locals are gone.
workdir=""
cleanup() {
    if [[ -n "${workdir}" ]]; then
        rm -rf "${workdir}"
    fi
}

main() {
    local input="" print_only=0
    local -a dests=()

    while [[ $# -gt 0 ]]; do
        case "$1" in
            -h|--help) usage; exit 0 ;;
            --out) [[ $# -ge 2 ]] || die "--out needs a directory"; dests+=("$2"); shift 2 ;;
            --sd)  [[ $# -ge 2 ]] || die "--sd needs a directory";  dests+=("$2/atmosphere"); shift 2 ;;
            --print-only) print_only=1; shift ;;
            -*) usage; die "unknown option $1" ;;
            *) [[ -z "${input}" ]] || die "more than one input given"; input="$1"; shift ;;
        esac
    done

    [[ -n "${input}" ]] || { usage; exit 2; }

    local nso
    nso="$(resolve_nso "${input}")"

    require_python
    require_generator

    workdir="$(mktemp -d)"
    trap cleanup EXIT

    # generate_patch.py writes <MODULEID>.ips into the working directory and
    # takes no output path, so it is run somewhere disposable and the result is
    # picked up by name.
    ( cd "${workdir}" && python3 "${GENERATOR}" "${nso}" ) \
        || die "generate_patch.py failed. If it said \"Couldn't find place to patch\", this build is not one it knows -- report the game version upstream rather than editing the offsets."

    local patch
    patch="$(find "${workdir}" -maxdepth 1 -name '*.ips' -print -quit)"
    [[ -n "${patch}" ]] || die "generate_patch.py wrote no .ips file"

    local build_id
    build_id="$(basename "${patch}" .ips)"

    echo "splatoon3-patch: built $(wc -c < "${patch}" | tr -d ' ') byte patch for build ${build_id}"

    if [[ ${#dests[@]} -eq 0 ]]; then
        local guess
        guess="$(default_mods_dir)" || die "could not work out Ryujinx's mods directory on this platform; pass --out"
        dests+=("${guess}")
    fi

    local dest
    for dest in "${dests[@]}"; do
        local target="${dest}/exefs_patches/${PATCH_NAME}"
        if [[ ${print_only} -eq 1 ]]; then
            echo "splatoon3-patch: would install ${target}/${build_id}.ips"
            continue
        fi
        mkdir -p "${target}"
        cp "${patch}" "${target}/${build_id}.ips"
        echo "splatoon3-patch: installed ${target}/${build_id}.ips"
    done

    if [[ ${print_only} -eq 1 ]]; then
        return 0
    fi

    cat <<NEXT

Start the game and check the log for a line reading

    Matching IPS patch '${build_id}.ips' in '${PATCH_NAME}'

If it is absent the patch did not apply, and the game will keep refusing your
server's certificate with nothing else to show for it.
NEXT
}

# Accepts the NSO itself or a directory holding it, and checks the magic so a
# wrong file is caught here rather than as an assertion inside the generator.
resolve_nso() {
    local input="$1" candidate="$1"

    [[ -e "${input}" ]] || die "no such file or directory: ${input}"

    if [[ -d "${input}" ]]; then
        candidate=""
        local name
        for name in main exefs/main; do
            if [[ -f "${input}/${name}" ]]; then
                candidate="${input}/${name}"
                break
            fi
        done
        [[ -n "${candidate}" ]] || die "no 'main' in ${input} -- point at the extracted ExeFS, or at the NSO itself"
    fi

    local magic
    magic="$(head -c 4 "${candidate}")"
    [[ "${magic}" == "NSO0" ]] || die "${candidate} is not an NSO (no NSO0 magic). The main NSO is the one to patch, not the NPDM or an NCA."

    printf '%s' "${candidate}"
}

require_python() {
    command -v python3 >/dev/null 2>&1 || die "python3 is not installed"
    python3 -c 'import lz4.block' 2>/dev/null \
        || die "the python lz4 module is missing (the NSO's segments are lz4-compressed). Install it with: pip3 install lz4"
}

require_generator() {
    if [[ ! -f "${GENERATOR}" ]]; then
        echo "splatoon3-patch: fetching the upstream generator" >&2
        "${ROOT}/scripts/fetch-proto.sh" >&2
    fi
    [[ -f "${GENERATOR}" ]] || die "generate_patch.py is still missing from ${UPSTREAM} -- has the upstream layout changed?"
}

# Ryujinx's own mods path, from AppDataManager: <app data>/Ryujinx/mods.
# A portable install keeps its mods beside the executable instead, so that one
# needs --out.
default_mods_dir() {
    case "$(uname -s)" in
        Linux)  printf '%s' "${XDG_CONFIG_HOME:-${HOME}/.config}/Ryujinx/mods" ;;
        Darwin) printf '%s' "${HOME}/Library/Application Support/Ryujinx/mods" ;;
        MINGW*|MSYS*|CYGWIN*)
            [[ -n "${APPDATA:-}" ]] || return 1
            printf '%s' "${APPDATA}/Ryujinx/mods" ;;
        *) return 1 ;;
    esac
}

main "$@"
