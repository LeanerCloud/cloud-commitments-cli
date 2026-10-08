#!/usr/bin/env bash
set -euo pipefail

packages=()
for file in "$@"; do
    [[ -f "$file" ]] || continue
    case "$file" in testdata/*|*/testdata/*) continue ;; esac
    directory=$(dirname "$file")
    package="./$directory"
    [[ "$directory" != . ]] || package=.
    duplicate=0
    for existing in "${packages[@]+"${packages[@]}"}"; do
        if [[ "$existing" == "$package" ]]; then duplicate=1; break; fi
    done
    [[ "$duplicate" -eq 1 ]] || packages+=("$package")
done
[[ ${#packages[@]} -gt 0 ]] || exit 0
exec bash "$(dirname "${BASH_SOURCE[0]}")/run-gosec.sh" -- "${packages[@]}"
