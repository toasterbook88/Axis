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
if [[ "$tag" == "--source" ]]; then
  printf 'v%s\n' "$source_version"
  exit 0
fi

tag_version="${tag#v}"
if [[ "$tag" == "$tag_version" || "$tag_version" != "$source_version" ]]; then
  printf 'tag %s does not match source version %s\n' "$tag" "$source_version" >&2
  exit 1
fi

printf '%s\n' "$tag"
