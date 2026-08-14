#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

doc_path="${AXIS_CURRENT_STATE_DOC_PATH:-docs/current-state.md}"
facts_start='<!-- BEGIN GENERATED CURRENT STATE FACTS -->'
facts_end='<!-- END GENERATED CURRENT STATE FACTS -->'
verify_start='<!-- BEGIN GENERATED CURRENT STATE VERIFICATION -->'
verify_end='<!-- END GENERATED CURRENT STATE VERIFICATION -->'
facts_only=0

while (($# > 0)); do
  case "$1" in
    --facts-only)
      facts_only=1
      shift
      ;;
    *)
      printf 'unknown argument: %s\n' "$1" >&2
      exit 1
      ;;
  esac
done

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    printf 'missing required command: %s\n' "$1" >&2
    exit 1
  fi
}

require_command awk
require_command sed
if (( facts_only == 0 )); then
  require_command go
  require_command make
fi

axis_version="$(sed -n 's/^const Version = "\(.*\)"/\1/p' internal/buildinfo/version.go)"
if [[ -z "$axis_version" ]]; then
  printf 'failed to parse axis version from internal/buildinfo/version.go; check that the Version const format matches the sed pattern in hack/refresh-current-state.sh\n' >&2
  exit 1
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
  if [[ "$cmd" == "make coverage" ]]; then
    printf '  - Coverage gates:\n'
    while IFS= read -r line; do
      printf '    - `%s`\n' "$line"
    done <"$tmp"
  fi
  rm -f "$tmp"
}

facts_tmp="$(mktemp)"
doc_tmp="$(mktemp)"
verify_tmp=""
if (( facts_only == 0 )); then
  verify_tmp="$(mktemp)"
fi
trap 'rm -f "$facts_tmp" "${verify_tmp:-}" "$doc_tmp"' EXIT

cat >"$facts_tmp" <<EOF
$facts_start
- Repo version: \`$axis_version\`
$facts_end
EOF

if (( facts_only == 0 )); then
  {
    printf '%s\n' "$verify_start"
    run_and_report "make test"
    run_and_report "make test-race"
    # Linked worktrees can make Go's implicit VCS probe fail even when the
    # source tree is otherwise healthy. Release binaries carry explicit
    # version metadata; this verification build only needs to compile.
    run_and_report "go build -buildvcs=false ./..."
    run_and_report "make coverage"
    printf '%s\n' "$verify_end"
  } >"$verify_tmp"
fi

awk -v facts_file="$facts_tmp" \
  -v verify_file="$verify_tmp" \
  -v facts_start="$facts_start" \
  -v facts_end="$facts_end" \
  -v verify_start="$verify_start" \
  -v verify_end="$verify_end" \
  -v replace_verify="$((facts_only == 0))" '
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
    if (replace_verify == 1) {
      print_file(verify_file)
      skip = verify_end
      next
    }
    print
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
