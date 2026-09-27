#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -eq 0 ]]; then
  exit 0
fi

version_file="go.mod"
if [[ ! -f "$version_file" ]]; then
  echo "missing Go version source: $version_file" >&2
  exit 1
fi

versions="$(awk '
  /^[[:space:]]*go([[:space:]]|$)/ {
    if (NF != 2) { print "__malformed__"; next }
    print $2
  }
' "$version_file")"
version_count="$(printf '%s\n' "$versions" | awk 'NF { count++ } END { print count + 0 }')"
if [[ "$version_count" -ne 1 || ! "$versions" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "expected exactly one patch-level Go version in $version_file" >&2
  exit 1
fi

if ! goroot="$(GOTOOLCHAIN="go${versions}" go env GOROOT)"; then
  echo "failed to resolve GOROOT for Go ${versions}" >&2
  exit 1
fi
if [[ -z "$goroot" || ! -x "$goroot/bin/gofmt" ]]; then
  echo "selected GOROOT has no executable gofmt: ${goroot:-<empty>}" >&2
  exit 1
fi

status=0
if output="$("$goroot/bin/gofmt" -l "$@")"; then
  :
else
  status=$?
fi
if [[ -n "$output" ]]; then
  printf '%s\n' "$output"
  [[ "$status" -eq 0 ]] && status=1
fi
exit "$status"
