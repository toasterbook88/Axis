#!/usr/bin/env bash
set -euo pipefail

version_file="${AXIS_VERSION_FILE:-internal/buildinfo/version.go}"
tag="${1:-}"

if [[ -z "$tag" ]]; then
  printf 'usage: %s <vX.Y.Z tag>\n' "$0" >&2
  exit 2
fi

source_versions="$(sed -n 's/^const Version = "\([^"]*\)"$/\1/p' "$version_file")"
if [[ -z "$source_versions" || "$(printf '%s\n' "$source_versions" | wc -l)" -ne 1 ]]; then
  printf 'could not parse exactly one Version constant from %s\n' "$version_file" >&2
  exit 1
fi

source_version="$source_versions"
normalized_tag="v${source_version}"

# SemVer 2.0.0, including prerelease and build metadata. Numeric identifiers
# may not contain leading zeroes; alphanumeric prerelease identifiers may.
numeric='0|[1-9][0-9]*'
prerelease_id="(${numeric}|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)"
build_id='[0-9A-Za-z-]+'
semver_re="^v(${numeric})\.(${numeric})\.(${numeric})(-${prerelease_id}(\.${prerelease_id})*)?(\+${build_id}(\.${build_id})*)?$"

if [[ ! "$normalized_tag" =~ $semver_re ]]; then
  printf 'source version %s is not valid SemVer\n' "$source_version" >&2
  exit 1
fi

if [[ "$tag" == "--source" ]]; then
  printf '%s\n' "$normalized_tag"
  exit 0
fi

if [[ ! "$tag" =~ $semver_re ]]; then
  printf 'tag %s is not valid SemVer (expected vX.Y.Z with optional prerelease/build metadata)\n' "$tag" >&2
  exit 1
fi

tag_version="${tag#v}"
if [[ "$tag" == "$tag_version" || "$tag_version" != "$source_version" ]]; then
  printf 'tag %s does not match source version %s\n' "$tag" "$source_version" >&2
  exit 1
fi

printf '%s\n' "$tag"
