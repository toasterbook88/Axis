#!/usr/bin/env bash
# Enforce a dead-code budget with an explicit allowlist.
#
# `go run golang.org/x/tools/cmd/deadcode` reports unreachable functions.
# Some reports are false positives the tool cannot see through:
#   - interface satisfaction via compile-time assertions (var _ Iface = (*T)(nil))
#   - build-tagged consumers (//go:build fleet → internal/fleettest)
#   - intentionally retained API surface pending a wiring decision
# Those belong in hack/deadcode-allowlist.txt (one fully-qualified symbol per
# line, matching the deadcode output format "pkg/path.go:line: func name").
#
# The gate: every reported symbol must either be deleted or allowlisted.
# New allowlist entries require a rationale comment on the same line (# ...).
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    printf 'missing required command: %s\n' "$1" >&2
    exit 1
  fi
}
require_command go
require_command awk
require_command sort
require_command comm

# Pinned tool version — same reproducibility policy as actionlint in CI.
# Bump deliberately after checking `deadcode` output stability.
DEADCODE_VERSION=v0.49.0

allowfile="hack/deadcode-allowlist.txt"
[[ -f "$allowfile" ]] || fail_missing_allowfile() { :; } # created below if absent

if [[ ! -f "$allowfile" ]]; then
  printf '# Deadcode allowlist: one pkg/file.go:line: func-name entry per line.\n# See docs/quality/deadcode-triage.md for the triage rationale.\n' > "$allowfile"
fi

# Run deadcode pinned to DEADCODE_VERSION. The gate is fail-closed: with
# `set -e`, a failing command substitution would abort the script silently,
# so the failure is captured explicitly and converted into a gate failure.
#
# First-run nuance: `go run` prints "go: downloading ..." progress lines to
# stderr on cold module caches (CI), and those lines land in dead_raw via
# 2>&1. They are progress, not errors — strip them before interpreting, and
# only treat remaining `go: ...` output as a tool failure.
if ! raw_out="$(go run golang.org/x/tools/cmd/deadcode@${DEADCODE_VERSION} ./... 2>&1)"; then
  printf 'deadcode gate FAILED: pinned tool did not run (version %s):\n%s\n' "$DEADCODE_VERSION" "$raw_out" >&2
  exit 1
fi
# Strip download-progress lines (only at line starts, keep real findings).
dead_raw="$(printf '%s\n' "$raw_out" | grep -v '^go: downloading ' || true)"
if [[ -z "$(printf '%s\n' "$dead_raw" | grep -v '^go: ' || true)" ]]; then
  # nothing but download lines and/or go: errors → tool did not produce findings
  if printf '%s\n' "$dead_raw" | grep -q '^go: ' ; then
    printf 'deadcode gate FAILED: pinned tool errored (version %s):\n%s\n' "$DEADCODE_VERSION" "$dead_raw" >&2
    exit 1
  fi
fi
if [[ -z "$dead_raw" ]]; then
  echo "deadcode gate passed: no unreachable functions"
  exit 0
fi
# Every deadcode line looks like: path/file.go:123:45 funcName  (or with :col)
# Allowlist lines look like:  path/file.go  funcName     (path + func name, no line)
norm() {
  awk '{
    # $1 = path:line:col (or path:line), rest = func signature
    loc=$1
    sub(/:[0-9]+$/, "", loc)        # strip col if present
    sub(/:[0-9]+$/, "", loc)        # strip line -> path
    $1=""
    sub(/^ /, "")
    sig=$0
    # signature may be "pkg.(*T).Method" — take the func name after the last dot
    n=split(sig, seg, ".")
    fname=seg[n]
    print loc "\t" fname
  }'
}

allow_ok() {
  # allowlist match: the allowlist path fragment must be a prefix of (or equal
  # to) the deadcode file path, AND the function name must match exactly.
  # Allowlist entries may be a directory prefix ("internal/fleettest") or a
  # file path ("internal/llmrouter/cloud.go"); line numbers are ignored so
  # entries survive upstream edits.
  local path="$1" fn="$2"
  awk -v p="$path" -v f="$fn" '
    { line=$0
      sub(/#.*/, "", line)                 # strip trailing comments
      gsub(/^[ \t]+|[ \t]+$/, "", line)
      if (line == "" || line ~ /^#/) next
      split(line, a, " ")
      if (index(p, a[1]) == 1 && a[2] == f) found=1
    }
    END { if (found) exit 0; exit 1 }
  ' "$allowfile"
}

violations=()
stale_allow=()

while IFS=$'\t' read -r loc fn; do
  [[ -n "$loc" ]] || continue
  # path for matching: strip line:col from loc before allowlist lookup
  path_only="${loc%:*}"; path_only="${path_only%:*}"
  if ! allow_ok "$path_only" "$fn"; then
    violations+=("$loc: $fn")
  fi
done < <(printf '%s\n' "$dead_raw" | grep -v '^$' | awk '{
  # $1 = path:line:col: (trailing colon), so fields: 1=path 2=line 3=col 4=empty
  n=split($1, p, ":")
  loc=p[1]
  for (i=2; i < n; i++) loc = loc ":" p[i]
  rest=$0
  sub(/^[^ ]+ */, "", rest)
  gsub(/unreachable func: /, "", rest)
  gsub(/unreachable method: /, "", rest)
  m=split(rest, seg, ".")
  print loc "\t" seg[m]
}' | sort -u)

# Stale allowlist entries (allowed but no longer reported) are informational:
# they mean the dead code was removed or wired up — a good thing. Surface them.
while IFS= read -r entry; do
  line="$(printf '%s' "$entry" | sed 's/#.*//' | xargs)"
  [[ -z "$line" || "$line" == \#* ]] && continue
  path="$(printf '%s' "$line" | awk '{print $1}')"
  fn="$(printf '%s' "$line" | awk '{print $2}')"
  found=0
  while IFS=$'\t' read -r loc fn2; do
    if [[ "$loc" == *"$path"* && "$fn2" == "$fn" ]]; then found=1; break; fi
  done < <(printf '%s\n' "$dead_raw" | awk '{
    n=split($1, p, ":")
    loc=p[1]
    for (i=2; i < n; i++) loc = loc ":" p[i]
    rest=$0
    sub(/^[^ ]+ */, "", rest)
    gsub(/unreachable func: /, "", rest)
    gsub(/unreachable method: /, "", rest)
    m=split(rest, seg, ".")
    print loc "\t" seg[m]
  }')
  if [[ $found -eq 0 ]]; then
    stale_allow+=("$path $fn (no longer reported — remove from allowlist)")
  fi
done < "$allowfile"

if (( ${#violations[@]} > 0 )); then
  printf 'deadcode gate: %d unreachable symbol(s) not in %s:\n' "${#violations[@]}" "$allowfile" >&2
  printf '  %s\n' "${violations[@]}" >&2
  printf 'Either delete the symbol or allowlist it with a rationale (see docs/quality/deadcode-triage.md).\n' >&2
  exit 1
fi

echo "deadcode gate passed: $(printf '%s\n' "$dead_raw" | grep -cv '^$') unreachable symbols, all allowlisted"
if (( ${#stale_allow[@]} > 0 )); then
  echo "  stale allowlist entries (harmless, consider cleaning):" >&2
  printf '  %s\n' "${stale_allow[@]}" >&2
fi