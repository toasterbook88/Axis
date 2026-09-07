#!/usr/bin/env bash
# Regression tests for hack/verify-deadcode.sh failure paths.
#
# The live gate is exercised by CI against a healthy tree. These cases stub
# `go` so the fail-closed branches (tool did not run, go:-prefixed error,
# download-progress stripping) and the unallowlisted-symbol path stay locked
# without a cold module cache or a real deadcode pin bump.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

fail() {
  printf 'verify-deadcode-tests: %s\n' "$1" >&2
  exit 1
}

test_root="$(mktemp -d "${TMPDIR:-/tmp}/axis-verify-deadcode-tests.XXXXXX")"
trap 'rm -rf "$test_root"' EXIT
mkdir -p "$test_root/bin"

cat >"$test_root/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
mode="${AXIS_DEADCODE_STUB_MODE:?AXIS_DEADCODE_STUB_MODE is required}"
printf '%s\n' "$*" >"${AXIS_DEADCODE_STUB_LOG:?}"
case "$mode" in
  fail-run)
    printf 'go: golang.org/x/tools/cmd/deadcode@v9.9.9: unknown revision v9.9.9\n' >&2
    exit 1
    ;;
  go-error)
    printf 'go: downloading golang.org/x/tools v0.49.0\n'
    printf 'go: malformed module path\n'
    exit 0
    ;;
  cold-cache)
    printf 'go: downloading golang.org/x/tools v0.49.0\n'
    printf 'go: downloading golang.org/x/mod v0.40.0\n'
    printf 'internal/fake/dead.go:10:1: unreachable func: leftoverFunc\n'
    exit 0
    ;;
  finding)
    printf 'internal/fake/dead.go:10:1: unreachable func: leftoverFunc\n'
    exit 0
    ;;
  empty)
    exit 0
    ;;
  *)
    printf 'unexpected AXIS_DEADCODE_STUB_MODE: %s\n' "$mode" >&2
    exit 2
    ;;
esac
EOF
chmod +x "$test_root/bin/go"

run_gate() {
  local mode="$1"
  local allow="$2"
  local case_root out err rc
  case_root="$(mktemp -d "$test_root/case.XXXXXX")"
  mkdir -p "$case_root/hack"
  cp "$repo_root/hack/verify-deadcode.sh" "$case_root/hack/verify-deadcode.sh"
  printf '%s\n' "$allow" >"$case_root/hack/deadcode-allowlist.txt"
  out="$case_root/out"
  err="$case_root/err"
  : >"$case_root/go-args"
  set +e
  PATH="$test_root/bin:$PATH" \
    AXIS_DEADCODE_STUB_MODE="$mode" \
    AXIS_DEADCODE_STUB_LOG="$case_root/go-args" \
    "$case_root/hack/verify-deadcode.sh" >"$out" 2>"$err"
  rc=$?
  set -e
  GATE_RC="$rc"
  GATE_OUT="$(cat "$out")"
  GATE_ERR="$(cat "$err")"
  GATE_GO_ARGS="$(cat "$case_root/go-args")"
}

expect_pin() {
  printf '%s' "$GATE_GO_ARGS" | grep -q 'golang.org/x/tools/cmd/deadcode@v0.49.0' \
    || fail "go run was not invoked with the pinned deadcode version (args: $GATE_GO_ARGS)"
}

run_gate fail-run '# empty allowlist'
expect_pin
[[ "$GATE_RC" -eq 1 ]] || fail "fail-run: expected exit 1, got $GATE_RC"
printf '%s' "$GATE_ERR" | grep -qF 'pinned tool did not run' \
  || fail "fail-run: missing 'pinned tool did not run' (stderr: $GATE_ERR)"

run_gate go-error '# empty allowlist'
expect_pin
[[ "$GATE_RC" -eq 1 ]] || fail "go-error: expected exit 1, got $GATE_RC"
printf '%s' "$GATE_ERR" | grep -qF 'pinned tool errored' \
  || fail "go-error: missing 'pinned tool errored' (stderr: $GATE_ERR)"
if printf '%s' "$GATE_ERR" | grep -qF 'pinned tool did not run'; then
  fail "go-error: download progress must not be classified as a run failure (stderr: $GATE_ERR)"
fi

run_gate cold-cache 'internal/fake/dead.go leftoverFunc'
expect_pin
[[ "$GATE_RC" -eq 0 ]] || fail "cold-cache: expected exit 0, got $GATE_RC (stderr: $GATE_ERR)"
printf '%s' "$GATE_OUT" | grep -qF 'deadcode gate passed:' \
  || fail "cold-cache: missing pass line (stdout: $GATE_OUT)"

run_gate finding '# no matching allowlist entry'
expect_pin
[[ "$GATE_RC" -eq 1 ]] || fail "unallowlisted: expected exit 1, got $GATE_RC"
printf '%s' "$GATE_ERR" | grep -qF 'unreachable symbol(s) not in' \
  || fail "unallowlisted: missing violation message (stderr: $GATE_ERR)"
printf '%s' "$GATE_ERR" | grep -qF 'leftoverFunc' \
  || fail "unallowlisted: missing symbol name (stderr: $GATE_ERR)"

run_gate finding 'internal/fake/dead.go leftoverFunc'
expect_pin
[[ "$GATE_RC" -eq 0 ]] || fail "allowlisted: expected exit 0, got $GATE_RC (stderr: $GATE_ERR)"
printf '%s' "$GATE_OUT" | grep -qF 'all allowlisted' \
  || fail "allowlisted: missing pass line (stdout: $GATE_OUT)"

run_gate empty '# unused'
expect_pin
[[ "$GATE_RC" -eq 0 ]] || fail "empty: expected exit 0, got $GATE_RC (stderr: $GATE_ERR)"
printf '%s' "$GATE_OUT" | grep -qF 'no unreachable functions' \
  || fail "empty: missing clean-tree pass line (stdout: $GATE_OUT)"

grep -qF './hack/verify-deadcode.sh' hack/ci-preflight.sh \
  || fail 'ci-preflight.sh does not run ./hack/verify-deadcode.sh'

grep -qF './hack/verify-deadcode.sh' .github/workflows/ci.yml \
  || fail 'ci.yml does not run ./hack/verify-deadcode.sh'

printf 'deadcode gate regression tests passed\n'
