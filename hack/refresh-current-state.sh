#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

doc_path="docs/current-state.md"
facts_start='<!-- BEGIN GENERATED CURRENT STATE FACTS -->'
facts_end='<!-- END GENERATED CURRENT STATE FACTS -->'
verify_start='<!-- BEGIN GENERATED CURRENT STATE VERIFICATION -->'
verify_end='<!-- END GENERATED CURRENT STATE VERIFICATION -->'

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    printf 'missing required command: %s\n' "$1" >&2
    exit 1
  fi
}

require_command git
require_command go
require_command awk
require_command sed
require_command curl

axis_version="$(sed -n 's/^const Version = "\(.*\)"/\1/p' internal/buildinfo/version.go)"
refreshed_at="$(TZ=America/New_York date '+%Y-%m-%d %Z')"

release_json="$(curl -fsSL "https://api.github.com/repos/toasterbook88/axis/releases/latest" || true)"
latest_release_tag="$(printf '%s' "$release_json" | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
latest_release_published="$(printf '%s' "$release_json" | sed -n 's/.*"published_at":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"

compare_versions() {
  local left="${1#v}"
  local right="${2#v}"
  local left_part right_part
  local i

  IFS='.' read -r -a left_parts <<<"${left%%[-+]*}"
  IFS='.' read -r -a right_parts <<<"${right%%[-+]*}"

  local max_len="${#left_parts[@]}"
  if (( ${#right_parts[@]} > max_len )); then
    max_len="${#right_parts[@]}"
  fi

  for ((i = 0; i < max_len; i++)); do
    left_part="${left_parts[i]:-0}"
    right_part="${right_parts[i]:-0}"
    if (( 10#$left_part < 10#$right_part )); then
      return 1
    fi
    if (( 10#$left_part > 10#$right_part )); then
      return 2
    fi
  done

  return 0
}

if [[ -z "$latest_release_tag" ]]; then
  latest_release_tag="unavailable"
  latest_release_published="unavailable"
fi

release_truth="repo version matches the latest published release"
if [[ "$latest_release_tag" == "unavailable" ]]; then
  release_truth="latest published release is unavailable from the GitHub API"
else
  compare_status=0
  compare_versions "$axis_version" "$latest_release_tag" || compare_status=$?
  case "$compare_status" in
    0) release_truth="repo version matches the latest published release" ;;
    1) release_truth="repo version is behind the latest published release" ;;
    2) release_truth="repo version is ahead of the latest published release" ;;
  esac
fi

run_and_report() {
  local cmd="$1"
  local tmp
  tmp="$(mktemp)"
  if bash -lc "$cmd" >"$tmp" 2>&1; then
    printf -- '- `%s` -> passes\n' "$cmd"
  else
    printf -- '- `%s` -> fails\n' "$cmd"
    sed 's/^/  /' "$tmp" >&2
    rm -f "$tmp"
    return 1
  fi
  if [[ "$cmd" == "./hack/coverage-check.sh" ]]; then
    printf '  - Coverage gates:\n'
    while IFS= read -r line; do
      printf '    - `%s`\n' "$line"
    done <"$tmp"
  fi
  rm -f "$tmp"
}

facts_tmp="$(mktemp)"
verify_tmp="$(mktemp)"
doc_tmp="$(mktemp)"
trap 'rm -f "$facts_tmp" "$verify_tmp" "$doc_tmp"' EXIT

cat >"$facts_tmp" <<EOF
$facts_start
- Refreshed: $refreshed_at
- Repo version: \`$axis_version\`
- Latest published GitHub release: \`$latest_release_tag\` ($latest_release_published)
- Release truth: $release_truth
$facts_end
EOF

{
  printf '%s\n' "$verify_start"
  run_and_report "go test ./... -count=1"
  run_and_report "go test -race ./... -count=1"
  run_and_report "go build ./..."
  run_and_report "./hack/coverage-check.sh"
  printf '%s\n' "$verify_end"
} >"$verify_tmp"

awk -v facts_file="$facts_tmp" \
  -v verify_file="$verify_tmp" \
  -v facts_start="$facts_start" \
  -v facts_end="$facts_end" \
  -v verify_start="$verify_start" \
  -v verify_end="$verify_end" '
function print_file(path, line) {
  while ((getline line < path) > 0) {
    print line
  }
  close(path)
}
{
  if ($0 == facts_start) {
    print_file(facts_file)
    skip = facts_end
    next
  }
  if ($0 == verify_start) {
    print_file(verify_file)
    skip = verify_end
    next
  }
  if (skip != "") {
    if ($0 == skip) {
      skip = ""
    }
    next
  }
  print
}
' "$doc_path" >"$doc_tmp"

mv "$doc_tmp" "$doc_path"
printf 'refreshed %s\n' "$doc_path"
