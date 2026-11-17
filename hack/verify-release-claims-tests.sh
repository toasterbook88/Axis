#!/usr/bin/env bash
# Regression tests for hack/verify-release-claims.sh.
#
# The guard exists because v0.25.0 shipped with README still pinning v0.24.0
# while v0.24.2 was already out, and SECURITY.md still declared v0.24.x
# supported. These tests drive the guard with synthetic fixtures so each
# failure mode is proven to fail closed without needing a real tag history.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

fail() {
  printf 'verify-release-claims-tests: %s\n' "$1" >&2
  exit 1
}

test_root="$(mktemp -d "${TMPDIR:-/tmp}/axis-release-claims-tests.XXXXXX")"
trap 'rm -rf "$test_root"' EXIT

# Commits are always full SHAs. `git rev-parse --short` returns seven
# characters in a small CI clone and eight or more in a developer's
# repository, so a short reference makes the result depend on how many objects
# the checkout holds — that is how this guard first failed in CI while passing
# locally.
C240=16eeb3bd5a6c7d8e9f0a1b2c3d4e5f60718293a4
C242=c4f2a1b3d5e6f708192a3b4c5d6e7f8091a2b3c4
C250=8578cb2a00e85008d3dd95935bbf201e42a4ca6d
C251=b2d4f6a8c0e21436587a9b0c1d2e3f4051627384

tags_file="$test_root/tags.txt"
commits_file="$test_root/commits.txt"
readme="$test_root/README.md"
security="$test_root/SECURITY.md"

# write_tags <tag>...  and  write_commits "<tag> <sha>"...
write_tags() {
  printf '%s\n' "$@" >"$tags_file"
}

write_commits() {
  printf '%s\n' "$@" >"$commits_file"
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

# run_guard <expected exit>
run_guard() {
  local want_exit="$1"
  local out err status
  out="$test_root/out"
  err="$test_root/err"
  set +e
  AXIS_RELEASE_CLAIMS_README="$readme" \
    AXIS_RELEASE_CLAIMS_SECURITY="$security" \
    AXIS_RELEASE_CLAIMS_TAGS_FILE="$tags_file" \
    AXIS_RELEASE_CLAIMS_COMMITS_FILE="$commits_file" \
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

# The published tag history the drift was found in.
write_tags v0.24.0 v0.24.1 v0.24.2 v0.25.0
write_commits "v0.24.0 $C240" "v0.24.1 $C240" "v0.24.2 $C242" "v0.25.0 $C250"

# --- happy path --------------------------------------------------------------
write_security v0.25.x

# An eight-character citation, the form README.md uses.
write_readme v0.25.0 v0.25.0 8578cb2a v0.25.x
run_guard 0
grep -qF 'release claims passed: v0.25.0 at 8578cb2a, security line v0.25.x (newest tag v0.25.0)' "$test_root/out" \
  || fail 'a passing run must report the claimed release, commit, security line, and newest tag'

# A full 40-character citation passes too, as does the seven-character form.
write_readme v0.25.0 v0.25.0 "$C250" v0.25.x
run_guard 0
write_readme v0.25.0 v0.25.0 8578cb2 v0.25.x
run_guard 0

# --- the release window ------------------------------------------------------
# `.github/workflows/release.yml` runs this gate with fetch-depth: 0, so a tag
# push has the new tag in the checkout while the tree still documents the
# previous release. A tree that names the tag immediately before the newest one
# must pass, or publishing becomes impossible: README cannot cite the commit of
# the tag being created.
write_tags v0.24.0 v0.24.1 v0.24.2 v0.25.0 v0.25.1
write_commits "v0.24.0 $C240" "v0.24.1 $C240" "v0.24.2 $C242" "v0.25.0 $C250" "v0.25.1 $C251"
write_readme v0.25.0 v0.25.0 8578cb2a v0.25.x
write_security v0.25.x
run_guard 0
grep -qF '(newest tag v0.25.1)' "$test_root/out" \
  || fail 'a tree one release behind must pass and name the real newest tag'

# --- real drift --------------------------------------------------------------
# The state this PR fixes: v0.25.0 is tagged, v0.24.2 is already published, and
# README still names v0.24.0 — two releases behind.
write_tags v0.24.0 v0.24.1 v0.24.2 v0.25.0
write_commits "v0.24.0 $C240" "v0.24.1 $C240" "v0.24.2 $C242" "v0.25.0 $C250"
write_readme v0.24.0 v0.24.0 16eeb3b v0.24.x
write_security v0.24.x
run_guard 1
expect_failure_mentioning 'Quick start pins v0.24.0, but the tags say the newest release (v0.25.0) or the one before it (v0.24.2)'

# Naming the tag two releases back fails even when a newer tag exists. The
# Quick start pin is checked first, so that is the message the operator sees.
write_tags v0.24.0 v0.24.1 v0.24.2 v0.25.0 v0.25.1
write_commits "v0.24.0 $C240" "v0.24.1 $C240" "v0.24.2 $C242" "v0.25.0 $C250" "v0.25.1 $C251"
write_readme v0.24.2 v0.24.2 c4f2a1b3 v0.24.x
write_security v0.24.x
run_guard 1
expect_failure_mentioning 'Quick start pins v0.24.2, but the tags say the newest release (v0.25.1) or the one before it (v0.25.0)'

# --- Status table details ----------------------------------------------------
write_tags v0.24.0 v0.24.1 v0.24.2 v0.25.0
write_commits "v0.24.0 $C240" "v0.24.1 $C240" "v0.24.2 $C242" "v0.25.0 $C250"

# Pin and Status row disagreeing is a failure of its own.
write_readme v0.25.0 v0.24.2 c4f2a1b3 v0.25.x
write_security v0.25.x
run_guard 1
expect_failure_mentioning "Quick start pins v0.25.0 but the 'Latest release' row says v0.24.2"

# A stale tag commit for the claimed release fails.
write_readme v0.25.0 v0.25.0 16eeb3b v0.25.x
run_guard 1
expect_failure_mentioning 'cites tag commit 16eeb3b but v0.25.0 is at 8578cb2a'

# A cited commit that is not a prefix of the claimed tag's commit fails.
write_readme v0.25.0 v0.25.0 8578cc9 v0.25.x
run_guard 1
expect_failure_mentioning 'cites tag commit 8578cc9'

# A citation shorter than seven characters is ambiguous and is refused.
write_readme v0.25.0 v0.25.0 8578cb v0.25.x
run_guard 1
expect_failure_mentioning 'is not a 7-to-40-character commit prefix'

# A reference that is not a full SHA must be refused rather than compared, or
# every correct citation is rejected.
write_commits "v0.24.0 $C240" "v0.24.1 $C240" "v0.24.2 $C242" "v0.25.0 8578cb2"
write_readme v0.25.0 v0.25.0 8578cb2a v0.25.x
run_guard 1
expect_failure_mentioning 'cannot resolve v0.25.0 to a full 40-character commit SHA'

# A claimed tag absent from the commit map must also be refused, not guessed.
write_commits "v0.24.0 $C240" "v0.25.0 $C250"
write_readme v0.24.2 v0.24.2 c4f2a1b3 v0.24.x
write_security v0.24.x
run_guard 1
expect_failure_mentioning 'cannot resolve v0.24.2 to a full 40-character commit SHA'

# --- security line drift -----------------------------------------------------
write_commits "v0.24.0 $C240" "v0.24.1 $C240" "v0.24.2 $C242" "v0.25.0 $C250"
write_readme v0.25.0 v0.25.0 8578cb2a v0.24.x
write_security v0.24.x
run_guard 1
expect_failure_mentioning 'supported security line is v0.24.x but its latest release is v0.25.0'

write_readme v0.25.0 v0.25.0 8578cb2a v0.25.x
write_security v0.24.x
run_guard 1
expect_failure_mentioning "SECURITY.md says v0.24.x for the supported security line"

# --- prerelease tags must not demand promotion -------------------------------
# v0.26.0-rc.1 sorts above v0.25.0 in git's version sort, but README must not
# advertise a prerelease, so only releases are considered.
write_tags v0.25.0 v0.26.0-rc.1
write_commits "v0.25.0 $C250"
write_readme v0.25.0 v0.25.0 8578cb2a v0.25.x
write_security v0.25.x
run_guard 0
grep -qF '(newest tag v0.25.0)' "$test_root/out" \
  || fail 'a prerelease tag must be ignored when choosing the newest release'

write_tags v0.25.0 v0.25.1-rc.1 v0.26.0-alpha
run_guard 0

# --- no tags at all ----------------------------------------------------------
: >"$tags_file"
write_readme v0.25.0 v0.25.0 8578cb2a v0.25.x
write_security v0.25.x
run_guard 0
grep -qF 'no release tags available; skipping' "$test_root/out" \
  || fail 'an empty tag list must skip with a notice, not pass silently'

# --- missing claims ----------------------------------------------------------
write_tags v0.25.0
write_commits "v0.25.0 $C250"
printf '# AXIS\n\nno install command here\n' >"$readme"
write_security v0.25.x
run_guard 1
expect_failure_mentioning "has no 'AXIS_VERSION=vX.Y.Z' install pin to check"

printf '# AXIS\n\nAXIS_VERSION=v0.25.0\n\nno status table here\n' >"$readme"
run_guard 1
expect_failure_mentioning "no '| Latest release |' row"

printf '# AXIS\n\nAXIS_VERSION=v0.25.0\n\n| Latest release | [**v0.25.0**](x) (no commit cited) |\n' >"$readme"
run_guard 1
expect_failure_mentioning "cites no 'tag commit"

printf 'verify-release-claims regression tests passed\n'
