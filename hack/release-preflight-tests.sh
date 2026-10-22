#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/axis-release-preflight-tests.XXXXXX")"
trap 'rm -rf "$test_root"' EXIT
mkdir -p "$test_root/hack" "$test_root/bin"
cp "$repo_root/hack/release-preflight.sh" "$test_root/hack/"

cat >"$test_root/hack/ci-preflight.sh" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
printf 'ci|%s|%s\n' "$GOTOOLCHAIN" "$*" >>"$AXIS_TEST_LOG"
exit "${AXIS_TEST_CI_EXIT:-0}"
STUB
cat >"$test_root/bin/go" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
printf 'go|%s|%s\n' "$GOTOOLCHAIN" "$*" >>"$AXIS_TEST_LOG"
if [[ "$*" == *goreleaser* ]]; then
  exit "${AXIS_TEST_VALIDATOR_EXIT:-0}"
fi
STUB
cat >"$test_root/bin/git" <<'STUB'
#!/usr/bin/env bash
printf 'test-commit\n'
STUB
chmod +x "$test_root/hack/ci-preflight.sh" "$test_root/bin/"*

export PATH="$test_root/bin:$PATH"
export GOTOOLCHAIN=go1.26.10
export AXIS_TEST_LOG="$test_root/commands"

fail() { printf 'release-preflight-tests: %s\n' "$1" >&2; exit 1; }
run_preflight() { bash "$test_root/hack/release-preflight.sh" --require-clean >"$test_root/output" 2>&1; }

run_preflight || fail 'successful validation failed'
cat >"$test_root/expected" <<'EXPECTED'
ci|go1.26.10|--require-clean
go|go1.27.0|run github.com/goreleaser/goreleaser/v2@v2.18.0 check
go|go1.26.10|run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...
EXPECTED
diff -u "$test_root/expected" "$AXIS_TEST_LOG" || fail 'validator compiler leaked into application gates or scan'

: >"$AXIS_TEST_LOG"
if AXIS_TEST_CI_EXIT=9 run_preflight; then fail 'ignored CI failure'; else status=$?; fi
[[ "$status" == 9 && "$(wc -l <"$AXIS_TEST_LOG" | tr -d ' ')" == 1 ]] || fail 'continued after CI failure'

: >"$AXIS_TEST_LOG"
if AXIS_TEST_VALIDATOR_EXIT=17 run_preflight; then fail 'ignored validator failure'; else status=$?; fi
[[ "$status" == 17 && "$(wc -l <"$AXIS_TEST_LOG" | tr -d ' ')" == 2 ]] || fail 'continued after validator failure'

printf 'release preflight toolchain isolation and fail-closed regression tests passed\n'
