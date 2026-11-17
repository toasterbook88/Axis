#!/usr/bin/env bash
# verify-release-claims.sh — README's install claims must name the newest tag.
#
# A release is tagged on main; README.md is edited by hand. When a tag lands
# but README still pins the previous version, every operator who follows Quick
# start installs a binary SECURITY.md no longer supports, and the Status table
# advertises a superseded release. `verify-repo-truth.sh` rejects *unpublished*
# tags and stale `current release is` claims, but it never compares the install
# pins or the Status table against the tags present in the checkout, so this
# drift survives every gate (`v0.25.0` shipped with README pinned to v0.24.0).
#
# Local and network-free: the tags in the checkout are the authority. The check
# covers three claims that present themselves as "install this now":
#
#   1. The first `AXIS_VERSION=vX.Y.Z` in README.md (the Quick start command).
#   2. The Status table `Latest release` row: its version and its `tag commit`.
#   3. The supported security line, in both README.md and SECURITY.md.
#
# Historical material stays free: the rollback example and the "Upgrading from
# an older release?" links name older tags deliberately.
#
# Overridable for regression tests (see hack/verify-release-claims-tests.sh):
#   AXIS_RELEASE_CLAIMS_README        default README.md
#   AXIS_RELEASE_CLAIMS_SECURITY      default SECURITY.md
#   AXIS_RELEASE_CLAIMS_TAGS_FILE     default `git tag --list 'v*'`
#   AXIS_RELEASE_CLAIMS_TAG_COMMIT    default `git rev-parse --short <tag>`
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

readme="${AXIS_RELEASE_CLAIMS_README:-README.md}"
security="${AXIS_RELEASE_CLAIMS_SECURITY:-SECURITY.md}"

fail() { printf 'verify-release-claims: %s\n' "$1" >&2; exit 1; }

[[ -f "$readme" ]] || fail "README not found at $readme"
[[ -f "$security" ]] || fail "security policy not found at $security"

# Release tags only: `main` plus any prerelease (v0.26.0-rc.1) must not demand
# that README advertise an unpublished or pre-release binary. Git's version
# sort orders a prerelease below its release, so an unfiltered `head -1` would
# otherwise pick the prerelease.
latest="$(
  {
    if [[ -n "${AXIS_RELEASE_CLAIMS_TAGS_FILE:-}" ]]; then
      cat "$AXIS_RELEASE_CLAIMS_TAGS_FILE"
    else
      git tag --list 'v*' 2>/dev/null || true
    fi
  } | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -1 || true
)"

if [[ -z "$latest" ]]; then
  printf 'verify-release-claims: no release tags available; skipping release-claim freshness check\n'
  exit 0
fi

latest_commit="${AXIS_RELEASE_CLAIMS_TAG_COMMIT:-}"
if [[ -z "$latest_commit" ]]; then
  # ^{commit} dereferences an annotated tag object to the commit it points at;
  # README's "tag commit" quotes the commit (e.g. v0.24.0 -> 16eeb3b), never
  # the tag object's own SHA.
  latest_commit="$(git rev-parse --short "${latest}^{commit}" 2>/dev/null || true)"
fi
[[ -n "$latest_commit" ]] \
  || fail "cannot resolve commit for tag $latest; pass AXIS_RELEASE_CLAIMS_TAG_COMMIT"

latest_minor="${latest%.*}"   # v0.25.0 -> v0.25

# --- 1. Quick start install pin ---------------------------------------------
first_pin="$(grep -oE 'AXIS_VERSION=v[0-9]+\.[0-9]+\.[0-9]+' "$readme" | head -1 | sed 's/^AXIS_VERSION=//' || true)"
[[ -n "$first_pin" ]] \
  || fail "$readme has no 'AXIS_VERSION=vX.Y.Z' install pin to check; if the Quick start install changed shape, update this script"
[[ "$first_pin" == "$latest" ]] \
  || fail "$readme Quick start pins $first_pin but the newest release tag is $latest; update the install command (and the other current-version commands) to $latest"

# --- 2. Status table: Latest release version and tag commit ------------------
status_row="$(grep -E '^\| Latest release \|' "$readme" || true)"
[[ -n "$status_row" ]] \
  || fail "$readme Status table has no '| Latest release |' row to check; if the table changed shape, update this script"

claimed_version="$(printf '%s\n' "$status_row" | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | head -1 || true)"
[[ -n "$claimed_version" ]] \
  || fail "$readme 'Latest release' row names no release version: $status_row"
[[ "$claimed_version" == "$latest" ]] \
  || fail "$readme 'Latest release' row says $claimed_version but the newest release tag is $latest"

claimed_commit="$(printf '%s\n' "$status_row" | sed -n 's/.*tag commit `\([0-9a-f][0-9a-f]*\)`.*/\1/p' | head -1 || true)"
[[ -n "$claimed_commit" ]] \
  || fail "$readme 'Latest release' row cites no 'tag commit \`<sha>\`': $status_row"
# A prefix match keeps both the seven-character short form and a full SHA valid.
case "$latest_commit" in
  "$claimed_commit"*) ;;
  *) fail "$readme 'Latest release' row cites tag commit $claimed_commit but $latest is at $latest_commit" ;;
esac

# --- 3. Supported security line ---------------------------------------------
# SECURITY.md supports "the latest tagged release line". A release that moves
# the minor version must move both statements, or the policy advertises a line
# nobody ships. Hotfixes on an older line are not produced from main, so the
# newest tag is always the supported line.
readme_line="$(grep -oE '`v[0-9]+\.[0-9]+\.x`' "$readme" | head -1 | tr -d '`' || true)"
[[ -n "$readme_line" ]] \
  || fail "$readme declares no 'supported security line' \`vX.Y.x\`; if the wording changed, update this script"
[[ "$readme_line" == "$latest_minor.x" ]] \
  || fail "$readme says the supported security line is $readme_line but the newest release is $latest ($latest_minor.x); update it together with SECURITY.md"

security_line="$(grep -oE '^\| `v[0-9]+\.[0-9]+\.x` \| yes \|' "$security" | grep -oE 'v[0-9]+\.[0-9]+\.x' | head -1 || true)"
[[ -n "$security_line" ]] \
  || fail "$security declares no supported '| \`vX.Y.x\` | yes |' row; if the table changed shape, update this script"
[[ "$security_line" == "$latest_minor.x" ]] \
  || fail "$security declares the supported line as $security_line but the newest release is $latest ($latest_minor.x)"

[[ "$readme_line" == "$security_line" ]] \
  || fail "$readme says $readme_line and $security says $security_line for the supported security line; they must agree"

printf 'release claims passed: %s at %s, security line %s\n' "$latest" "$latest_commit" "$readme_line"
