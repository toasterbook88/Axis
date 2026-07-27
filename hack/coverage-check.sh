#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

# Test isolation (audit finding C5). This script runs the full suite five more
# times; unisolated tests resolve every AXIS store to ~/.axis, so without a
# redirect it writes real operator state. Done here rather than only in the
# Makefile because CI invokes this script directly.
#
# GOCACHE, GOPATH, and GOMODCACHE derive from HOME, so they are captured from
# the real home *before* HOME is reassigned — otherwise every run would start
# from a cold build cache. AXIS_HOME is deliberately not exported: it outranks
# HOME in persist.AxisDir and would collapse the ~133 tests that isolate
# themselves with t.Setenv("HOME", ...) onto one shared AXIS root.
export GOCACHE="${GOCACHE:-$(go env GOCACHE)}"
export GOPATH="${GOPATH:-$(go env GOPATH)}"
export GOMODCACHE="${GOMODCACHE:-$(go env GOMODCACHE)}"

axis_test_home="$(mktemp -d "${TMPDIR:-/tmp}/axis-test-home.XXXXXX")"
export HOME="$axis_test_home"
unset AXIS_HOME

# Single EXIT trap for the whole script: a second `trap ... EXIT` replaces this
# one rather than adding to it. total_profile is created here for that reason.
total_profile="$(mktemp)"
trap 'rm -f "$total_profile"; rm -rf "$axis_test_home"' EXIT

check_threshold() {
  local label="$1"
  local actual="$2"
  local minimum="$3"

  if [ -z "$actual" ]; then
    printf 'coverage gate failed: %s coverage value is empty\n' "$label" >&2
    return 1
  fi

  if awk -v actual="$actual" -v minimum="$minimum" 'BEGIN { exit !(actual + 0 >= minimum + 0) }'; then
    printf 'coverage gate passed: %s %.1f%% >= %.1f%%\n' "$label" "$actual" "$minimum"
    return 0
  fi

  printf 'coverage gate failed: %s %.1f%% < %.1f%%\n' "$label" "$actual" "$minimum" >&2
  return 1
}

package_coverage() {
  local pkg="$1"
  local tmp_out
  tmp_out="$(mktemp)"
  if ! go test "$pkg" -cover > "$tmp_out" 2>&1; then
    echo "ERROR: go test $pkg -cover failed:" >&2
    cat "$tmp_out" >&2
    rm -f "$tmp_out"
    return 1
  fi
  local cov
  cov="$(sed -n 's/.*coverage: \([0-9.][0-9.]*\)%.*/\1/p' "$tmp_out")"
  rm -f "$tmp_out"
  echo "$cov"
}

PKG_LIST=$(go list ./... | grep -v '/examples/' || true)

if ! go test $PKG_LIST -coverprofile="$total_profile" >/dev/null; then
  echo "ERROR: go test $PKG_LIST -coverprofile failed. Re-running tests to show failure logs:" >&2
  go test $PKG_LIST -coverprofile="$total_profile" -count=1
  exit 1
fi

total_cov="$(go tool cover -func="$total_profile" | awk '/^total:/ {gsub("%", "", $3); print $3}')"
knowledge_cov="$(package_coverage ./internal/knowledge)"
api_cov="$(package_coverage ./internal/api)"
mcp_cov="$(package_coverage ./internal/mcp)"
ui_cov="$(package_coverage ./internal/ui)"

check_threshold "internal/knowledge" "$knowledge_cov" "90.0"
check_threshold "internal/api" "$api_cov" "50.0"
check_threshold "internal/mcp" "$mcp_cov" "35.0"
check_threshold "internal/ui" "$ui_cov" "80.0"
check_threshold "total" "$total_cov" "45.0"
