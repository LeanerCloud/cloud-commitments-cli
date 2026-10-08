#!/usr/bin/env bash
set -euo pipefail

readonly GOSEC_VERSION="v2.28.0"
readonly GOSEC_MODULE="github.com/securego/gosec/v2"
readonly GOSEC_BIN="${XDG_CACHE_HOME:-${HOME}/.cache}/pre-commit-gosec/${GOSEC_VERSION}/gosec"
format=text
output=""
install_only=0
while [[ $# -gt 0 ]]; do
    case "$1" in
        --format|--output)
            [[ $# -ge 2 ]] || { echo "gosec: $1 needs a value" >&2; exit 2; }
            if [[ "$1" == --format ]]; then format="$2"; else output="$2"; fi
            shift 2 ;;
        --install-only) install_only=1; shift ;;
        --) shift; break ;;
        *) echo "gosec: unsupported option: $1" >&2; exit 2 ;;
    esac
done
case "$format" in text|json|sarif) ;; *) echo "gosec: unsupported format: $format" >&2; exit 2 ;; esac
if [[ "$format" != text && -z "$output" ]]; then
    echo "gosec: $format requires --output" >&2
    exit 2
fi

installed_version() {
    go version -m "$GOSEC_BIN" 2>/dev/null | awk -v module="$GOSEC_MODULE" '$1 == "mod" && $2 == module { print $3 }'
}
if [[ ! -x "$GOSEC_BIN" ]] || [[ "$(installed_version)" != "$GOSEC_VERSION" ]]; then
    echo "gosec: installing ${GOSEC_MODULE}/cmd/gosec@${GOSEC_VERSION}" >&2
    if ! mkdir -p "$(dirname "$GOSEC_BIN")" || ! GOBIN="$(dirname "$GOSEC_BIN")" go install "${GOSEC_MODULE}/cmd/gosec@${GOSEC_VERSION}"; then
        echo "gosec: install failed" >&2
        exit 2
    fi
fi
if [[ ! -x "$GOSEC_BIN" ]] || [[ "$(installed_version)" != "$GOSEC_VERSION" ]]; then
    echo "gosec: installed binary does not match ${GOSEC_VERSION}" >&2
    exit 2
fi
[[ "$install_only" -eq 0 ]] || exit 0
cd "$(dirname "${BASH_SOURCE[0]}")/.."
[[ $# -gt 0 ]] || set -- ./...
args=(-fmt "$format")
if [[ -n "$output" ]]; then
    [[ ! -d "$output" ]] || { echo "gosec: output is a directory: $output" >&2; exit 2; }
    if ! report=$(mktemp "${output}.XXXXXX"); then
        echo "gosec: cannot create report beside $output" >&2
        exit 2
    fi
    args+=(-out "$report")
fi
status=0
"$GOSEC_BIN" "${args[@]}" -- "$@" || status=$?
if [[ -n "$output" ]]; then
    if [[ ! -s "$report" ]]; then
        echo "gosec: scanner produced no report" >&2
        exit 2
    fi
    if ! mv "$report" "$output"; then
        echo "gosec: cannot publish report to $output" >&2
        exit 2
    fi
fi
exit "$status"
