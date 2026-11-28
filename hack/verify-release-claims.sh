#!/usr/bin/env bash
# verify-release-claims.sh — README's install claims must name a live release.
#
# A release is tagged on main; README.md is edited by hand. When a tag lands
# but README still pins an old version, every operator who follows Quick start
# installs a binary SECURITY.md no longer supports, and the Status table
# advertises a superseded release. `verify-repo-truth.sh` rejects *unpublished*
# tags and stale `current release is` claims, but it never compares the install
# pins or the Status table against the tags present in the checkout, so this
# drift survives every gate (`v0.25.0` shipped with README pinned to v0.24.0
# while `v0.24.2` was already out).
#
# Local and network-free: the tags in the checkout are the authority. The check
# covers the three claims that present themselves as "install this now":
#
#   1. The first `AXIS_VERSION=vX.Y.Z` in README.md (the Quick start command).
#   2. The Status table `Latest release` row: its version and its `tag commit`.
#   3. The supported security line, in both README.md and SECURITY.md.
#
# Two-tag window. README may name the newest release tag or the one
# immediately before it. `.github/workflows/release.yml` runs this gate in its
# `test` job with `fetch-depth: 0`, so on a tag push the new tag is already in
# the checkout while the tree still documents the previous release — and the
# README cannot cite the tag's own commit, which does not exist until the
# commit is made. Allowing exactly one release of lag keeps publishing
# possible and still fails on real drift: when v0.25.0 was tagged and README
# named v0.24.0 while v0.24.2 was already published, the claim was two
# releases behind and failed. A lagging README must be caught up in the same
# release cycle; two consecutive releases without a docs update fails.
#
# Historical material stays free: the rollback example and the "Upgrading from
# an older release?" links name older tags deliberately.
#
# Overridable for regression tests (see hack/verify-release-claims-tests.sh):
#   AXIS_RELEASE_CLAIMS_README        default README.md
#   AXIS_RELEASE_CLAIMS_SECURITY      default SECURITY.md
#   AXIS_RELEASE_CLAIMS_TAGS_FILE     default `git tag --list 'v*'`
#   AXIS_RELEASE_CLAIMS_COMMITS_FILE  "<tag> <full-sha>" lines; default: `git
#                                     rev-parse <tag>^{commit}` per tag
#   AXIS_RELEASE_CLAIMS_PENDING       pending release version (vX.Y.Z); default
#                                     v + buildinfo.Version, unset when
#                                     AXIS_RELEASE_CLAIMS_TAGS_FILE is set
#
# Pending release. buildinfo.Version is bumped in the release-prep PR before
# the tag exists. Counting it as the newest release makes that PR fail on the
# same drift the tag run would find. Before this, the gate first failed on the
# tag push itself, and the only way out was to move a published tag (v0.29.0).
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

readme="${AXIS_RELEASE_CLAIMS_README:-README.md}"
security="${AXIS_RELEASE_CLAIMS_SECURITY:-SECURITY.md}"

fail() { printf 'verify-release-claims: %s\n' "$1" >&2; exit 1; }

[[ -f "$readme" ]] || fail "README not found at $readme"
[[ -f "$security" ]] || fail "security policy not found at $security"

# Release tags only: a prerelease (v0.26.0-rc.1) must not demand that README
# advertise an unpublished binary, and its own tag object is not a release.
release_tags="$(
  {
    if [[ -n "${AXIS_RELEASE_CLAIMS_TAGS_FILE:-}" ]]; then
      cat "$AXIS_RELEASE_CLAIMS_TAGS_FILE"
    else
      git tag --list 'v*' 2>/dev/null || true
    fi
  } | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V || true
)"

if [[ -n "${AXIS_RELEASE_CLAIMS_PENDING+set}" ]]; then
  pending="$AXIS_RELEASE_CLAIMS_PENDING"
elif [[ -n "${AXIS_RELEASE_CLAIMS_TAGS_FILE:-}" ]]; then
  pending=""
else
  pending="v$(sed -n 's/^const Version = "\(.*\)"$/\1/p' internal/buildinfo/version.go | head -1)"
fi
if [[ -n "$release_tags" && "$pending" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] && ! printf '%s\n' "$release_tags" | grep -qxF "$pending"; then
  release_tags="$(printf '%s\n%s\n' "$release_tags" "$pending" | grep -v '^$' | sort -V)"
fi

if [[ -z "$release_tags" ]]; then
  printf 'verify-release-claims: no release tags available; skipping release-claim freshness check\n'
  exit 0
fi

newest="$(printf '%s\n' "$release_tags" | tail -1)"
previous="$(printf '%s\n' "$release_tags" | tail -2 | head -1)"

# commit_of <tag> — the full 40-character commit the tag points at.
# README's "tag commit" quotes the commit, never an annotated tag object's own
# SHA, and never a `--short` abbreviation: `git rev-parse --short` returns
# seven characters in a small clone and eight or more in a developer's
# repository, which would make the result depend on how many objects the
# checkout happens to hold.
commit_of() {
  local tag="$1" sha=""
  if [[ -n "${AXIS_RELEASE_CLAIMS_COMMITS_FILE:-}" ]]; then
    sha="$(awk -v t="$tag" '$1 == t { print $2; exit }' "$AXIS_RELEASE_CLAIMS_COMMITS_FILE")"
  else
    sha="$(git rev-parse "${tag}^{commit}" 2>/dev/null || true)"
  fi
  printf '%s' "$sha"
}

is_allowed_claim() {
  local v="$1"
  [[ "$v" == "$newest" ]] && return 0
  if [[ -n "$previous" && "$v" == "$previous" ]]; then
    return 0
  fi
  return 1
}

window="the newest release ($newest)"
if [[ -n "$previous" ]]; then
  window="the newest release ($newest) or the one before it ($previous)"
fi

# --- 1. Quick start install pin ---------------------------------------------
first_pin="$(grep -oE 'AXIS_VERSION=v[0-9]+\.[0-9]+\.[0-9]+' "$readme" | head -1 | sed 's/^AXIS_VERSION=//' || true)"
[[ -n "$first_pin" ]] \
  || fail "$readme has no 'AXIS_VERSION=vX.Y.Z' install pin to check; if the Quick start install changed shape, update this script"
is_allowed_claim "$first_pin" \
  || fail "$readme Quick start pins $first_pin, but the tags say $window; update the current-version install commands"

# --- 2. Status table: Latest release version and tag commit ------------------
status_row="$(grep -E '^\| Latest release \|' "$readme" || true)"
[[ -n "$status_row" ]] \
  || fail "$readme Status table has no '| Latest release |' row to check; if the table changed shape, update this script"

claimed_version="$(printf '%s\n' "$status_row" | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | head -1 || true)"
[[ -n "$claimed_version" ]] \
  || fail "$readme 'Latest release' row names no release version: $status_row"
[[ "$claimed_version" == "$first_pin" ]] \
  || fail "$readme Quick start pins $first_pin but the 'Latest release' row says $claimed_version; they must agree"
# No separate freshness test for $claimed_version: it must equal $first_pin,
# which section 1 already checked against the same window.

# The single-quoted backticks below are literals matched in the docs, not
# command substitution.
# shellcheck disable=SC2016
claimed_commit="$(printf '%s\n' "$status_row" | sed -n 's/.*tag commit `\([0-9a-f][0-9a-f]*\)`.*/\1/p' | head -1 || true)"
[[ -n "$claimed_commit" ]] \
  || fail "$readme 'Latest release' row cites no 'tag commit \`<sha>\`': $status_row"
# README quotes a short form; the reference is the claimed tag's full SHA, so
# the reference must start with the cited prefix. Seven characters is the
# shortest git uses.
[[ "$claimed_commit" =~ ^[0-9a-f]{7,40}$ ]] \
  || fail "$readme 'Latest release' row cites '$claimed_commit', which is not a 7-to-40-character commit prefix: $status_row"

claimed_tag_commit="$(commit_of "$claimed_version")"
[[ "$claimed_tag_commit" =~ ^[0-9a-f]{40}$ ]] \
  || fail "cannot resolve $claimed_version to a full 40-character commit SHA; pass AXIS_RELEASE_CLAIMS_COMMITS_FILE"
short_commit="${claimed_tag_commit:0:8}"
case "$claimed_tag_commit" in
  "$claimed_commit"*) ;;
  *) fail "$readme 'Latest release' row cites tag commit $claimed_commit but $claimed_version is at $short_commit" ;;
esac

# --- 3. Supported security line ---------------------------------------------
# SECURITY.md supports the line README advertises, so both must name the
# claimed release's minor line. Keying on the claimed version rather than the
# newest tag keeps the release window open: after a minor bump is tagged, the
# tree still documents the previous line until the follow-up docs change.
claimed_minor="${claimed_version%.*}"   # v0.25.0 -> v0.25

# shellcheck disable=SC2016
readme_line="$(grep -oE '`v[0-9]+\.[0-9]+\.x`' "$readme" | head -1 | tr -d '`' || true)"
[[ -n "$readme_line" ]] \
  || fail "$readme declares no 'supported security line' \`vX.Y.x\`; if the wording changed, update this script"
[[ "$readme_line" == "$claimed_minor.x" ]] \
  || fail "$readme says the supported security line is $readme_line but its latest release is $claimed_version ($claimed_minor.x); update it together with SECURITY.md"

# shellcheck disable=SC2016
security_line="$(grep -oE '^\| `v[0-9]+\.[0-9]+\.x` \| yes \|' "$security" | grep -oE 'v[0-9]+\.[0-9]+\.x' | head -1 || true)"
[[ -n "$security_line" ]] \
  || fail "$security declares no supported '| \`vX.Y.x\` | yes |' row; if the table changed shape, update this script"
[[ "$readme_line" == "$security_line" ]] \
  || fail "$readme says $readme_line and $security says $security_line for the supported security line; they must agree"

printf 'release claims passed: %s at %s, security line %s (newest tag %s)\n' \
  "$claimed_version" "$short_commit" "$readme_line" "$newest"
