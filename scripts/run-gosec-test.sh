#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
scratch=$(mktemp -d)
export XDG_CACHE_HOME="$scratch/cache"
export GOSEC_TEST_DIR="$scratch"
mkdir -p "$scratch/bin" "$scratch/repo/scripts" "$scratch/repo/pkg space" "$scratch/repo/pkg*glob" "$scratch/repo/testdata"
cp "$root/scripts/run-gosec.sh" "$root/scripts/gosec-hook.sh" "$scratch/repo/scripts/"
cat > "$scratch/bin/go" <<'GO'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == version ]]; then
    version=$(cat "$GOSEC_TEST_DIR/version")
    printf 'mod\t%s\t%s\n' "${GOSEC_TEST_MODULE:-github.com/securego/gosec/v2}" "$version"
else
    [[ "${GOSEC_TEST_INSTALL_FAIL:-0}" == 0 ]] || exit 1
    printf '%s\n' "${2##*@}" > "$GOSEC_TEST_DIR/version"
    cp "$GOSEC_TEST_DIR/scanner" "$GOBIN/gosec"
    chmod +x "$GOBIN/gosec"
    echo installed >> "$GOSEC_TEST_DIR/installs"
fi
GO
cat > "$scratch/scanner" <<'SCANNER'
#!/usr/bin/env bash
set -euo pipefail
printf '<%s>\n' "$@" > "$GOSEC_TEST_DIR/argv"
while [[ $# -gt 0 ]]; do
    if [[ "$1" == -out && "${GOSEC_TEST_REPORT:-1}" == 1 ]]; then
        printf '{"Issues":[]}\n' > "$2"
    fi
    shift
done
exit "${GOSEC_TEST_STATUS:-0}"
SCANNER
chmod +x "$scratch/bin/go"
export PATH="$scratch/bin:$PATH"
cd "$scratch/repo"
expect_status() {
    local expected="$1" actual=0
    shift
    "$@" > "$scratch/stdout" 2> "$scratch/stderr" || actual=$?
    [[ "$actual" == "$expected" ]] || { cat "$scratch/stderr" >&2; echo "expected $expected, got $actual" >&2; exit 1; }
}
expect_status 0 bash scripts/gosec-hook.sh missing.go testdata/ignored.go
[[ ! -f "$scratch/installs" ]]
touch "pkg space/a.go" "pkg space/b.go" "pkg*glob/a.go" testdata/ignored.go
expect_status 0 bash scripts/gosec-hook.sh "pkg space/a.go" "pkg space/b.go" "pkg*glob/a.go" testdata/ignored.go missing.go
printf '<%s>\n' -fmt text -- './pkg space' './pkg*glob' > "$scratch/expected"
diff -u "$scratch/expected" "$scratch/argv"
expect_status 0 bash scripts/run-gosec.sh -- -exclude=G304
printf '<%s>\n' -fmt text -- -exclude=G304 > "$scratch/expected"
diff -u "$scratch/expected" "$scratch/argv"
expect_status 0 bash scripts/run-gosec.sh --format json --output "$scratch/report.json"
[[ -s "$scratch/report.json" && $(wc -l < "$scratch/installs") -eq 1 ]]
expect_status 0 bash scripts/run-gosec.sh --install-only
export GOSEC_TEST_STATUS=7
expect_status 7 bash scripts/run-gosec.sh --format json --output "$scratch/finding.json"
[[ -s "$scratch/finding.json" ]]
export GOSEC_TEST_REPORT=0
expect_status 2 bash scripts/run-gosec.sh --format json --output "$scratch/report.json"
export GOSEC_TEST_STATUS=0 GOSEC_TEST_REPORT=1
expect_status 2 bash scripts/run-gosec.sh --format sarif
expect_status 2 bash scripts/run-gosec.sh --quiet
expect_status 2 bash scripts/run-gosec.sh --format json --output "$scratch/missing/report.json"
expect_status 2 bash scripts/run-gosec.sh --format json --output testdata
printf 'wrong\n' > "$scratch/version"
export GOSEC_TEST_INSTALL_FAIL=1
expect_status 2 bash scripts/run-gosec.sh --install-only
export GOSEC_TEST_INSTALL_FAIL=0
expect_status 0 bash scripts/run-gosec.sh --install-only
[[ $(wc -l < "$scratch/installs") -eq 2 ]]
export GOSEC_TEST_MODULE=github.com/unrelated/gosec/v2
expect_status 2 bash scripts/run-gosec.sh --install-only
echo "gosec contract checks passed ($scratch)"
