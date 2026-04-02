#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    printf 'missing required command: %s\n' "$1" >&2
    exit 1
  fi
}

require_command curl
require_command jq
require_command rg
require_command git
require_command diff

curl_args=(-fsSL --retry 3 --connect-timeout 15 --max-time 30)
if [[ -n "${GITHUB_TOKEN:-}" ]]; then
  curl_args+=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
fi

latest_release_tag="$(curl "${curl_args[@]}" "https://api.github.com/repos/toasterbook88/axis/releases/latest" | jq -r '.tag_name // ""')"
if [[ -z "$latest_release_tag" ]]; then
  printf 'failed to determine latest published release tag from GitHub\n' >&2
  exit 1
fi

tmp_doc="$(mktemp)"
trap 'rm -f "$tmp_doc"' EXIT
cp docs/current-state.md "$tmp_doc"
AXIS_CURRENT_STATE_DOC_PATH="$tmp_doc" ./hack/refresh-current-state.sh --facts-only >/dev/null
if ! diff -u \
    <(grep -v '^- Refreshed:' docs/current-state.md) \
    <(grep -v '^- Refreshed:' "$tmp_doc") >/dev/null; then
  printf 'docs/current-state.md generated facts are stale; run ./hack/refresh-current-state.sh\n' >&2
  diff -u docs/current-state.md "$tmp_doc" >&2 || true
  exit 1
fi

release_refs=()
while IFS= read -r tag; do
  [[ -n "$tag" ]] && release_refs+=("$tag")
done < <(
  {
    rg --no-filename -o '@v[0-9]+\.[0-9]+\.[0-9]+([-.+][A-Za-z0-9.]+)?' README.md docs/current-state.md | sed 's/^@//'
    rg --no-filename -o 'releases/tag/v[0-9]+\.[0-9]+\.[0-9]+\([-.+][A-Za-z0-9.]+\)\?' README.md docs/current-state.md | sed 's#releases/tag/##'
  } | sort -u
)

for tag in "${release_refs[@]}"; do
  if ! curl "${curl_args[@]}" "https://api.github.com/repos/toasterbook88/axis/releases/tags/${tag}" >/dev/null; then
    printf 'operator-facing docs reference unpublished release %s\n' "$tag" >&2
    exit 1
  fi
done

while IFS= read -r line; do
  claim_tag="$(printf '%s\n' "$line" | rg -o 'v[0-9]+\.[0-9]+\.[0-9]+([-.+][A-Za-z0-9.]+)?' | head -n1)"
  if [[ -n "$claim_tag" && "$claim_tag" != "$latest_release_tag" ]]; then
    printf 'current-release claim %s does not match latest published release %s\n' "$claim_tag" "$latest_release_tag" >&2
    exit 1
  fi
done < <(rg -n 'current release is|live `v[0-9].*GitHub release' README.md docs/current-state.md || true)

printf 'repo truth guardrails passed\n'
