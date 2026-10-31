#!/usr/bin/env bash
# Regression tests for hack/verify-import-tiers.go.
#
# The live gate runs against the real tree in CI. These cases run the gate on
# throwaway fixture modules so every failure path stays locked: upward
# imports, Support rules, sub-package inheritance, allowlist semantics, tier
# block parsing, package inventory and load errors.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

fail() {
  printf 'verify-import-tiers-tests: %s\n' "$1" >&2
  exit 1
}

test_root="$(mktemp -d "${TMPDIR:-/tmp}/axis-verify-import-tiers-tests.XXXXXX")"
trap 'rm -rf "$test_root"' EXIT

go build -o "$test_root/gate" hack/verify-import-tiers.go

fixture_env=(GOWORK=off GOTOOLCHAIN=local GOFLAGS=-buildvcs=false GOPROXY=off)

default_block='Layer 2  Snapshot    internal/a internal/c
Layer 1  Fact Plane  internal/b
Support              internal/s'

# new_case <tier block>: a fixture module whose AGENTS.md carries the block.
new_case() {
  CASE="$(mktemp -d "$test_root/case.XXXXXX")"
  mkdir -p "$CASE/internal" "$CASE/hack"
  printf 'module example.com/fixture\n\ngo 1.21\n' >"$CASE/go.mod"
  # shellcheck disable=SC2016 # the backticks are a literal markdown fence
  printf '# AGENTS.md\n\n## 4. Architecture\n\n```text\n%s\n```\n' "$1" >"$CASE/AGENTS.md"
}

# pkg <dir under internal/> [imported dirs under internal/...]: one package
# whose only content is blank imports of the given fixture packages.
pkg() {
  local dir="$1" base
  shift
  base="$(basename "$dir")"
  mkdir -p "$CASE/internal/$dir"
  {
    printf 'package %s\n\n' "$base"
    for imp in "$@"; do
      printf 'import _ "example.com/fixture/internal/%s"\n' "$imp"
    done
  } >"$CASE/internal/$dir/${base#_}.go" # Go ignores files whose names start with _
}

allow() {
  printf '%s\n' "$@" >"$CASE/hack/import-tiers-allowlist.txt"
}

# base_packages is acyclic so each case can add one edge without creating an
# import cycle; a cycle would surface as a load error and mask the real check.
base_packages() {
  pkg a s
  pkg b s
  pkg c a
  pkg s
}

extra_env=()

run_gate() {
  set +e
  GATE_OUT="$(cd "$CASE" && env "${fixture_env[@]}" ${extra_env[@]+"${extra_env[@]}"} "$test_root/gate" 2>"$CASE/err")"
  GATE_RC=$?
  set -e
  GATE_ERR="$(cat "$CASE/err")"
}

expect_pass() {
  run_gate
  [[ "$GATE_RC" -eq 0 ]] || fail "$1: expected exit 0, got $GATE_RC (stderr: $GATE_ERR)"
  printf '%s' "$GATE_OUT" | grep -qF 'import tiers passed:' || fail "$1: missing pass line (stdout: $GATE_OUT)"
}

expect_fail() {
  run_gate
  [[ "$GATE_RC" -eq 1 ]] || fail "$1: expected exit 1, got $GATE_RC (stderr: $GATE_ERR)"
  printf '%s' "$GATE_ERR" | grep -qF -- "$2" || fail "$1: stderr lacks '$2' (stderr: $GATE_ERR)"
  if [[ "${3:-}" != load ]] && printf '%s' "$GATE_ERR" | grep -qE 'failed to load|dependency error'; then
    fail "$1: fixture did not load cleanly (stderr: $GATE_ERR)"
  fi
}

new_case "$default_block"
base_packages
pkg a b s
expect_pass 'downward and same-tier imports'
printf '%s' "$GATE_OUT" | grep -qF '4 packages, 4 internal import edges, 0 allowlisted' \
  || fail "summary counts wrong (stdout: $GATE_OUT)"

new_case "$default_block"
base_packages
pkg b a
expect_fail 'upward import' 'internal/b -> internal/a: Layer 1 (Fact Plane) may not import Layer 2 (Snapshot).'
printf '%s' "$GATE_ERR" | grep -qF 'add "b -> a  # <why>" to hack/import-tiers-allowlist.txt' \
  || fail "upward import: failure text lacks the allowlist hint (stderr: $GATE_ERR)"

new_case "$default_block"
base_packages
pkg b
pkg s b
expect_fail 'Support importing a layer' 'internal/s -> internal/b: Support may not import Layer 1 (Fact Plane).'

new_case "$default_block"
base_packages
pkg a/sub b
pkg a a/sub b s
expect_pass 'nested package inherits its parent tier; same-directory imports are skipped'

new_case "$default_block"
base_packages
pkg a/sub s
pkg b a/sub
expect_fail 'import of a nested package counts as its parent' 'internal/b -> internal/a:'

# A top-level hidden package has no tier of its own, so an import through it must fail, not vanish.
new_case "$default_block"
base_packages
pkg testdata/helper a
pkg b testdata/helper s
expect_fail 'import through a top-level hidden package' 'internal/testdata is imported by internal/b but is not in the AGENTS.md tier layout'

for hidden in testdata/helper _hidden; do
  new_case "$default_block"
  base_packages
  pkg "b/$hidden" a
  pkg b "b/$hidden" s
  expect_fail "upward import through internal/b/$hidden" 'internal/b -> internal/a:'
done

new_case "$default_block"
base_packages
pkg b a
allow '# comment' '' 'b -> a  # fixture rationale'
expect_pass 'allowlisted upward import'
printf '%s' "$GATE_OUT" | grep -qF '1 allowlisted' || fail "allowlisted count missing (stdout: $GATE_OUT)"

new_case "$default_block"
base_packages
pkg a b s
allow 'a -> b  # downward, so stale'
expect_fail 'stale allowlist entry' 'a -> b is no longer an upward import'

new_case "$default_block"
base_packages
pkg b a
allow 'b -> a  # one' 'b -> a  # two'
expect_fail 'duplicate allowlist entry' 'b -> a is listed twice'

new_case "$default_block"
base_packages
allow 'b -> nope  # unknown'
expect_fail 'allowlist names an unknown package' 'internal/nope is not in the AGENTS.md tier layout'

new_case "$default_block"
base_packages
pkg b a
allow 'b -> a'
expect_fail 'allowlist entry without rationale' 'b -> a has no rationale'

new_case "$default_block"
base_packages
allow 'b to a  # malformed'
expect_fail 'malformed allowlist entry' 'expected "<pkg> -> <pkg>  # <rationale>"'

new_case "$default_block"
base_packages
pkg z
expect_fail 'untiered package' 'internal/z is not in the AGENTS.md tier layout'

new_case "$default_block
                     internal/gone"
base_packages
expect_fail 'tier block lists a missing package' 'lists internal/gone, which does not exist'

new_case "$default_block
                     internal/a"
base_packages
expect_fail 'package listed twice' 'internal/a is listed twice'

new_case 'Layer 2  Snapshot    internal/a/deep'
base_packages
expect_fail 'nested path in tier block' '"internal/a/deep" is not an internal/<package> entry'

new_case "$default_block"
base_packages
printf '# AGENTS.md\n\n## 4. Architecture\n\nNo block here.\n' >"$CASE/AGENTS.md"
expect_fail 'missing tier block' 'no ```text block after the §4 heading'

new_case "$default_block"
base_packages
pkg a missing
expect_fail 'package with an unresolvable import' 'example.com/fixture/internal/a has a dependency error' load

new_case "$default_block"
base_packages
printf 'package a\n' >"$CASE/internal/a/other.go"
printf 'package other\n' >"$CASE/internal/a/mismatch.go"
expect_fail 'package that fails to load' 'example.com/fixture/internal/a failed to load' load

new_case "$default_block"
base_packages
printf '# AGENTS.md\n\nNo architecture section.\n' >"$CASE/AGENTS.md"
expect_fail 'missing section 4 heading' 'no "## 4." heading'

new_case "$default_block"
base_packages
# shellcheck disable=SC2016 # the backticks are a literal markdown fence
printf '# AGENTS.md\n\n## 4. Architecture\n\nNo block.\n\n## 5. Other\n\n```text\n%s\n```\n' "$default_block" >"$CASE/AGENTS.md"
expect_fail 'tier block under a later section is not read' 'no ```text block after the §4 heading'

new_case "$default_block
Bogus line"
base_packages
expect_fail 'stray line in tier block' 'not a tier header or continuation'

new_case "                     internal/a
$default_block"
base_packages
expect_fail 'continuation before any header' 'continuation before any tier header'

new_case "$default_block"
base_packages
printf 'package b\n\nimport _ "example.com/fixture/internal/a"\n' >"$CASE/internal/b/b_darwin.go"
expect_fail 'darwin-only upward import' 'internal/b -> internal/a:'

new_case "$default_block"
base_packages
printf 'package b\n\nimport _ "example.com/fixture/internal/a"\n' >"$CASE/internal/b/b_arm64.go"
expect_fail 'arm64-only upward import' 'internal/b -> internal/a:'

new_case "$default_block"
base_packages
pkg b
pkg s b
mkdir -p "$CASE/other"
printf 'module example.com/other\n\ngo 1.21\n' >"$CASE/other/go.mod"
printf 'package other\n' >"$CASE/other/other.go"
printf 'go 1.21\n\nuse (\n\t.\n\t./other\n)\n' >"$CASE/go.work"
extra_env=("GOWORK=$CASE/go.work")
expect_fail 'multi-module go.work does not hide imports' 'internal/s -> internal/b: Support may not import'
extra_env=()

# The gate hardcodes the release platforms; fail when .goreleaser.yml ships a different set.
goreleaser_platforms="$(awk '
  /^[[:space:]]*goos:/ { key = "os"; next }
  /^[[:space:]]*goarch:/ { key = "arch"; next }
  key != "" && /^[[:space:]]*- / { sub(/^[[:space:]]*- /, ""); list[key] = list[key] " " $0; next }
  { key = "" }
  END { print list["os"] "|" list["arch"] }' .goreleaser.yml)"
[[ "$goreleaser_platforms" == " darwin linux| amd64 arm64" ]] \
  || fail ".goreleaser.yml ships '$goreleaser_platforms'; update releaseTargets in hack/verify-import-tiers.go to match"

for target in hack/ci-preflight.sh .github/workflows/ci.yml .github/workflows/release.yml; do
  grep -qF 'go run hack/verify-import-tiers.go' "$target" \
    || fail "$target does not run go run hack/verify-import-tiers.go"
done
for target in hack/ci-preflight.sh .github/workflows/ci.yml; do
  grep -qF './hack/verify-import-tiers-tests.sh' "$target" \
    || fail "$target does not run ./hack/verify-import-tiers-tests.sh"
done

printf 'import tiers gate regression tests passed\n'
