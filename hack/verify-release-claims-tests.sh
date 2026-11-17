#!/usr/bin/env bash
# Regression tests for hack/verify-release-claims.sh.
#
# The guard exists because v0.25.0 shipped with README still pinning v0.24.0
# and SECURITY.md still declaring v0.24.x supported. These tests drive the
# guard with synthetic fixtures so each failure mode is proven to fail closed
# without needing a real tag history.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

fail() {
  printf 'verify-release-claims-tests: %s\n' "$1" >&2
  exit 1
}

test_root="$(mktemp -d "${TMPDIR:-/tmp}/axis-release-claims-tests.XXXXXX")"
trap 'rm -rf "$test_root"' EXIT

tags_file="$test_root/tags.txt"
readme="$test_root/README.md"
security="$test_root/SECURITY.md"

write_tags() {
  printf '%s\n' "$@" >"$tags_file"
}

# A fixture README carrying the three claims the guard reads, in the shapes
# README.md actually uses.
write_readme() {
  local pin="$1" status_version="$2" status_commit="$3" security_line="$4"
  cat >"$readme" <<EOF
# AXIS

\`\`\`bash
curl -fsSL https://example.invalid/install.sh \\
  | AXIS_VERSION=${pin} AXIS_REQUIRE_PINNED=1 bash
\`\`\`

Historical rollback example, deliberately older:

\`\`\`bash
curl -fsSL https://example.invalid/install.sh | AXIS_VERSION=v0.19.6 bash
\`\`\`

| Item | Value |
| --- | --- |
| Latest release | [**${status_version}**](https://example.invalid/releases/tag/${status_version}) (tag commit \`${status_commit}\`) |
| Supported security line | \`${security_line}\` (see [\`SECURITY.md\`](SECURITY.md)) |
EOF
}

write_security() {
  local line="$1"
  cat >"$security" <<EOF
# Security Policy

| Version | Supported |
| --- | --- |
| \`${line}\` | yes |
| older tagged releases | no |
EOF
}

# run_guard <expected exit> <tag commit override>
run_guard() {
  local want_exit="$1" commit="${2:-}"
  local out err status
  out="$test_root/out"
  err="$test_root/err"
  set +e
  AXIS_RELEASE_CLAIMS_README="$readme" \
    AXIS_RELEASE_CLAIMS_SECURITY="$security" \
    AXIS_RELEASE_CLAIMS_TAGS_FILE="$tags_file" \
    AXIS_RELEASE_CLAIMS_TAG_COMMIT="$commit" \
    ./hack/verify-release-claims.sh >"$out" 2>"$err"
  status=$?
  set -e
  [[ "$status" == "$want_exit" ]] \
    || fail "expected exit $want_exit, got $status: $(cat "$err")$(cat "$out")"
}

expect_failure_mentioning() {
  local needle="$1"
  grep -qF "$needle" "$test_root/err" \
    || fail "failure message must mention '$needle'; got: $(cat "$test_root/err")"
}

# --- happy path --------------------------------------------------------------
write_tags v0.24.0 v0.24.2 v0.25.0
write_readme v0.25.0 v0.25.0 8578cb2a v0.25.x
write_security v0.25.x
run_guard 0 8578cb2a
grep -qF 'release claims passed: v0.25.0 at 8578cb2a, security line v0.25.x' "$test_root/out" \
  || fail 'a passing run must report the checked release, commit, and security line'

# A full 40-character SHA must satisfy the same prefix comparison.
run_guard 0 8578cb2a4f0e1d2c3b4a5968778695a4b3c2d1e0

# --- stale Quick start pin ---------------------------------------------------
write_tags v0.25.0
write_readme v0.24.0 v0.25.0 8578cb2 v0.25.x
write_security v0.25.x
run_guard 1
expect_failure_mentioning 'Quick start pins v0.24.0 but the newest release tag is v0.25.0'

# --- stale Status table version ---------------------------------------------
write_readme v0.25.0 v0.24.0 8578cb2 v0.25.x
run_guard 1
expect_failure_mentioning "'Latest release' row says v0.24.0"

# --- stale Status table tag commit ------------------------------------------
write_readme v0.25.0 v0.25.0 16eeb3b v0.25.x
run_guard 1 8578cb2a
expect_failure_mentioning "cites tag commit 16eeb3b but v0.25.0 is at 8578cb2a"

# A cited commit that is not a prefix of the tag commit must fail.
write_readme v0.25.0 v0.25.0 8578cc9 v0.25.x
run_guard 1 8578cb2a
expect_failure_mentioning 'cites tag commit 8578cc9'

# --- security line drift -----------------------------------------------------
write_readme v0.25.0 v0.25.0 8578cb2 v0.24.x
write_security v0.24.x
run_guard 1 8578cb2a
expect_failure_mentioning 'supported security line is v0.24.x but the newest release is v0.25.0'

write_readme v0.25.0 v0.25.0 8578cb2 v0.25.x
write_security v0.24.x
run_guard 1 8578cb2a
expect_failure_mentioning 'supported line as v0.24.x but the newest release is v0.25.0'

# README and SECURITY.md disagreeing in the other direction is also a failure.
write_security v0.25.x
write_readme v0.25.0 v0.25.0 8578cb2 v0.24.x
run_guard 1 8578cb2a

# --- prerelease tags must not demand promotion -------------------------------
# v0.26.0-rc.1 sorts above v0.25.0 in git's version sort, but README must not
# advertise a prerelease, so the guard considers releases only.
write_tags v0.25.0 v0.26.0-rc.1
write_readme v0.25.0 v0.25.0 8578cb2 v0.25.x
write_security v0.25.x
run_guard 0 8578cb2a
grep -qF 'release claims passed: v0.25.0' "$test_root/out" \
  || fail 'a prerelease tag must be ignored when choosing the newest release'

# Build metadata and pre-release spellings never count as releases either.
write_tags v0.25.0 v0.25.1-rc.1 v0.26.0-alpha
run_guard 0 8578cb2a

# --- no tags at all ----------------------------------------------------------
: >"$tags_file"
write_readme v0.25.0 v0.25.0 8578cb2 v0.25.x
write_security v0.25.x
run_guard 0 8578cb2a
grep -qF 'no release tags available; skipping' "$test_root/out" \
  || fail 'an empty tag list must skip with a notice, not pass silently'

# --- missing claims ----------------------------------------------------------
printf '# AXIS\n\nno install command here\n' >"$readme"
write_tags v0.25.0
run_guard 1 8578cb2a
expect_failure_mentioning "has no 'AXIS_VERSION=vX.Y.Z' install pin to check"

printf '# AXIS\n\nAXIS_VERSION=v0.25.0\n\nno status table here\n' >"$readme"
run_guard 1 8578cb2a
expect_failure_mentioning "no '| Latest release |' row"

printf 'verify-release-claims regression tests passed\n'
