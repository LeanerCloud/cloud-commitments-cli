#!/usr/bin/env bash
# scripts/gosec-hook.sh
#
# Pre-commit gosec hook: runs gosec on only the Go packages that contain staged
# files, resolved to their owning module.  Fast by design: never scans the whole
# repo; each commit triggers at most one gosec invocation per affected module.
#
# Version pin: gosec v2.28.0 (matches the CI pin in ci.yml, bumped by #1384).
# Installed on first use to ~/.cache/pre-commit-gosec/v2.28.0/gosec; never
# modifies the system-wide gosec binary.
#
# Called by pre-commit with pass_filenames: true and files: \.go$.
# Exits 0 when no staged .go files survive filtering (deleted / testdata).
# Exits 1 on any gosec finding; exits 2 on setup failure.
#
# Exclusion rationale (kept in sync with .pre-commit-config.yaml):
#   G101 - variable names containing password/secret/token -> false positives
#   G104 - unchecked errors -> covered by errcheck
#   G115 - integer overflow -> safe conversions flagged
#   G117 - unsafe pointer arithmetic -> vendor/generated code
#   G118 - net/http serve without timeout -> timeouts set at handler level
#   G122 - unsafe operations -> low-level helpers, pre-existing
#   G204 - subprocess with variable -> CLI tool needs dynamic commands
#   G301 - dir permissions > 0750 -> acceptable for dev tooling
#   G304 - file path from variable -> CLI reads user-specified paths
#   G402 - TLS MinVersion not set -> handled by cloud SDK defaults
#   G505 - import of crypto/sha1 -> checksums, not security primitives
#   G702 - TLS InsecureSkipVerify -> test helpers only, pre-existing
#   G703 - unhandled defer error -> deferred close errors logged separately
#   G705 - unhandled goroutine error -> pre-existing pattern
#   G706 - ignored errors -> pre-existing; covered by go vet errcheck

set -euo pipefail

GOSEC_VERSION="2.28.0"
GOSEC_BIN="${HOME}/.cache/pre-commit-gosec/v${GOSEC_VERSION}/gosec"

GOSEC_EXCLUDE="G101,G104,G115,G117,G118,G122,G204,G301,G304,G402,G505,G702,G703,G705,G706"

# Module roots in longest-prefix order (so "providers/azure" is checked before
# a hypothetical "providers" root).
MODULE_DIRS="providers/azure providers/aws providers/gcp pkg"

# ---- helpers ----------------------------------------------------------------

ensure_gosec() {
    local need_install=0
    if [[ -x "$GOSEC_BIN" ]]; then
        # gosec built via `go install` embeds "dev" in -h regardless of tag;
        # read the real module version from the binary's build info instead.
        local installed_ver
        installed_ver=$(go version -m "$GOSEC_BIN" 2>/dev/null \
            | awk '$1=="mod" && $2~/gosec/{print $3}')
        # installed_ver is e.g. "v2.28.0"; compare against "v$GOSEC_VERSION".
        if [[ "$installed_ver" != "v${GOSEC_VERSION}" ]]; then
            echo "pre-commit/gosec: cached binary is ${installed_ver:-unknown}, need v${GOSEC_VERSION}; reinstalling" >&2
            need_install=1
        fi
    else
        need_install=1
    fi

    if [[ $need_install -eq 1 ]]; then
        echo "pre-commit/gosec: installing gosec@v${GOSEC_VERSION} -> $(dirname "$GOSEC_BIN")" >&2
        mkdir -p "$(dirname "$GOSEC_BIN")"
        GOBIN="$(dirname "$GOSEC_BIN")" go install \
            "github.com/securego/gosec/v2/cmd/gosec@v${GOSEC_VERSION}" || {
            echo "pre-commit/gosec: install failed (is Go on PATH?)" >&2
            exit 2
        }
    fi
}

# Print the module root (relative to repo root) that owns a given relative file
# path, or empty string when the file belongs to the root module.
module_for() {
    local f="$1" mod
    for mod in $MODULE_DIRS; do
        case "$f" in
            "$mod"/*) printf '%s' "$mod"; return ;;
        esac
    done
    printf ''
}

# ---- main -------------------------------------------------------------------

[[ $# -eq 0 ]] && exit 0

REPO_ROOT="$(git rev-parse --show-toplevel)"

# Filter: skip deleted files and files under testdata/.
live_files=()
for f in "$@"; do
    [[ -f "$f" ]] || continue
    case "$f" in
        */testdata/*) continue ;;
        testdata/*)   continue ;;
    esac
    live_files+=("$f")
done

[[ ${#live_files[@]} -eq 0 ]] && exit 0

ensure_gosec

# Build "module|package" pairs from the live file list.
# Package path is relative to the owning module root, in ./pkg notation.
pairs_raw=()
for f in "${live_files[@]}"; do
    mod=$(module_for "$f")
    pkg_dir=$(dirname "$f")

    if [[ -n "$mod" ]]; then
        # Strip the module prefix (plus the separating slash).
        rel="${pkg_dir:$((${#mod}+1))}"
        [[ -z "$rel" ]] && rel="."   # file sits directly in the module root
    else
        rel="$pkg_dir"               # root module; pkg_dir is already relative
    fi

    # Normalise to go-tool notation.
    if [[ "$rel" == "." ]]; then
        pkg="."
    else
        pkg="./$rel"
    fi

    pairs_raw+=("${mod}|${pkg}")
done

# Deduplicate.
pairs_sorted=$(printf '%s\n' "${pairs_raw[@]}" | sort -u)

# Enumerate unique module roots.
mods=$(printf '%s\n' "$pairs_sorted" | cut -d'|' -f1 | sort -u)

fail=0
while IFS= read -r mod; do
    pkgs=$(printf '%s\n' "$pairs_sorted" \
        | awk -F'|' -v m="$mod" '$1==m{print $2}' \
        | tr '\n' ' ')
    mod_dir="${REPO_ROOT}${mod:+/${mod}}"

    echo "gosec [${mod:-.}]: scanning package(s): $pkgs" >&2

    # pkgs intentionally unquoted: space-separated package paths, no glob chars.
    # shellcheck disable=SC2086
    if ! (cd "$mod_dir" && "$GOSEC_BIN" \
            -quiet \
            -exclude-dir=.legacy \
            -exclude-dir=.dev-notes \
            -exclude-dir=vendor \
            "-exclude=${GOSEC_EXCLUDE}" \
            $pkgs); then
        fail=1
    fi
done <<< "$mods"

exit $fail
