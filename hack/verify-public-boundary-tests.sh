#!/usr/bin/env bash
# Regression tests for hack/verify-public-boundary.sh.
#
# The gate scans files that git tracks. These cases run a copy of it in
# throwaway repositories so it cannot pass without scanning: outside a git
# work tree, or with nothing tracked, it must fail. Forbidden values are
# assembled at run time; this file is itself scanned by the real gate.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

fail() {
  printf 'verify-public-boundary-tests: %s\n' "$1" >&2
  exit 1
}

test_root="$(mktemp -d "${TMPDIR:-/tmp}/axis-verify-public-boundary-tests.XXXXXX")"
trap 'rm -rf "$test_root"' EXIT

# new_case [git]: a fixture directory holding a copy of the gate; with "git", a repository.
new_case() {
  CASE="$(mktemp -d "$test_root/case.XXXXXX")"
  mkdir -p "$CASE/hack"
  cp hack/verify-public-boundary.sh "$CASE/hack/"
  if [[ "${1:-}" == git ]]; then
    git -C "$CASE" init -q
    git -C "$CASE" add hack/verify-public-boundary.sh
  fi
}

track() { # track <path> <content>
  printf '%s\n' "$2" >"$CASE/$1"
  git -C "$CASE" add "$1"
}

run_gate() {
  set +e
  GATE_ERR="$("$CASE/hack/verify-public-boundary.sh" 2>&1 >/dev/null)"
  GATE_RC=$?
  set -e
}

expect() { # expect <name> <rc> [stderr substring]
  run_gate
  [[ "$GATE_RC" -eq "$2" ]] || fail "$1: expected exit $2, got $GATE_RC (stderr: $GATE_ERR)"
  [[ -z "${3:-}" ]] || printf '%s' "$GATE_ERR" | grep -qF -- "$3" || fail "$1: stderr lacks '$3' (stderr: $GATE_ERR)"
}

net_suffix="$(printf '.%s.%s' ts net)"
private_ip="$(printf '192.168.1.%s' 77)"

new_case
printf 'docs only\n' >"$CASE/README.md"
expect 'outside a git work tree' 1 'cannot list tracked files'

new_case git
git -C "$CASE" rm -q --cached hack/verify-public-boundary.sh
expect 'nothing tracked' 1 'no tracked files'

new_case git
track README.md 'see https://example.com and 192.0.2.10'
expect 'clean tree' 0

new_case git
track README.md "host node-a${net_suffix}"
expect 'private tailnet domain' 1 'private cluster domain'

new_case git
track README.md "node at ${private_ip}"
expect 'private LAN address' 1 "$private_ip"

grep -qF './hack/verify-public-boundary-tests.sh' hack/ci-preflight.sh \
  || fail 'ci-preflight.sh does not run ./hack/verify-public-boundary-tests.sh'
grep -qF './hack/verify-public-boundary-tests.sh' .github/workflows/ci.yml \
  || fail 'ci.yml does not run ./hack/verify-public-boundary-tests.sh'

printf 'public boundary gate regression tests passed\n'
