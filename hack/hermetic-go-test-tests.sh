#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

fail() {
  printf 'hermetic-go-test-tests: %s\n' "$1" >&2
  exit 1
}

test_root="$(mktemp -d "${TMPDIR:-/tmp}/axis-hermetic-go-test-tests.XXXXXX")"
trap 'rm -rf "$test_root"' EXIT
mkdir -p "$test_root/bin" "$test_root/operator-home/.axis" "$test_root/log"
printf 'operator state\n' >"$test_root/operator-home/.axis/sentinel"

cat >"$test_root/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "env" ]]; then
  printf '%s/cache/%s\n' "$AXIS_TEST_ROOT" "${2:-unknown}"
  exit 0
fi
if [[ "${1:-}" != "test" ]]; then
  printf 'unexpected go command: %s\n' "$*" >&2
  exit 2
fi
printf '%s\n' "$HOME" >"$AXIS_TEST_ROOT/log/home"
printf '%s\n' "${AXIS_HOME+x}:${AXIS_HOME:-}" >"$AXIS_TEST_ROOT/log/axis-home"
printf '%s\n' "$*" >"$AXIS_TEST_ROOT/log/args"
printf '%s\n' "$GOCACHE" "$GOPATH" "$GOMODCACHE" >"$AXIS_TEST_ROOT/log/caches"
EOF
chmod +x "$test_root/bin/go"

before_listing="$(LC_ALL=C ls -A "$test_root/operator-home/.axis")"
before_cksum="$(cksum <"$test_root/operator-home/.axis/sentinel")"
PATH="$test_root/bin:$PATH" \
  AXIS_TEST_ROOT="$test_root" \
  AXIS_HOME="$test_root/operator-axis-home" \
  HOME="$test_root/operator-home" \
  ./hack/hermetic-go-test.sh -race ./...
after_listing="$(LC_ALL=C ls -A "$test_root/operator-home/.axis")"
after_cksum="$(cksum <"$test_root/operator-home/.axis/sentinel")"

[[ "$before_listing" == "$after_listing" && "$before_cksum" == "$after_cksum" ]] \
  || fail 'operator ~/.axis changed'
[[ "$(<"$test_root/log/home")" != "$test_root/operator-home" ]] \
  || fail 'go test received the operator HOME'
[[ "$(<"$test_root/log/axis-home")" == 'x:' ]] \
  || fail 'go test did not receive an explicitly empty AXIS_HOME'
[[ "$(<"$test_root/log/args")" == 'test -race ./...' ]] \
  || fail 'go test arguments changed'
[[ "$(sed -n '1p' "$test_root/log/caches")" == "$test_root/cache/GOCACHE" ]] \
  || fail 'GOCACHE was not preserved'
[[ "$(sed -n '2p' "$test_root/log/caches")" == "$test_root/cache/GOPATH" ]] \
  || fail 'GOPATH was not preserved'
[[ "$(sed -n '3p' "$test_root/log/caches")" == "$test_root/cache/GOMODCACHE" ]] \
  || fail 'GOMODCACHE was not preserved'
[[ ! -d "$(<"$test_root/log/home")" ]] || fail 'temporary test HOME was not removed'

printf 'hermetic Go test runner regression tests passed\n'
