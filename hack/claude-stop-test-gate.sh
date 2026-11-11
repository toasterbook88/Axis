#!/usr/bin/env bash
set -euo pipefail

root="${CLAUDE_PROJECT_DIR:-$(git rev-parse --show-toplevel 2>/dev/null || pwd)}"
cd "$root"

# Claude re-runs a blocked Stop hook with stop_hook_active=true. Blocking again
# on a condition the agent cannot fix (an environmental test failure) would
# only loop, so let the turn end and leave the failure in the transcript.
payload=""
if [[ ! -t 0 ]]; then
  payload="$(cat || true)"
fi
if grep -Eq '"stop_hook_active"[[:space:]]*:[[:space:]]*true' <<<"$payload"; then
  echo "Stop gate: already continued after a hook block; not blocking again." >&2
  exit 0
fi

# Uncommitted work plus everything this branch added since it left main, so an
# agent that commits before finishing is still covered. A single-branch clone
# has no origin/main, so fall back to other refs and finally to the last commit
# rather than silently diffing HEAD against itself.
base=""
for ref in origin/main main origin/HEAD; do
  if git rev-parse --verify --quiet "$ref" >/dev/null 2>&1; then
    base="$(git merge-base HEAD "$ref" 2>/dev/null || true)"
    [[ -n "$base" ]] && break
  fi
done
if [[ -z "$base" ]] && git rev-parse --verify --quiet HEAD^ >/dev/null 2>&1; then
  base="$(git rev-parse HEAD^)"
fi
changed_files="$({
  git diff --name-only
  git diff --cached --name-only
  git ls-files --others --exclude-standard
  if [[ -n "$base" ]]; then
    git diff --name-only "$base"...HEAD
  fi
} | sort -u)"

if grep -Eq '(^|/)([^/]+\.go|go\.mod|go\.sum|Makefile)$' <<<"$changed_files"; then
  if ! command -v go >/dev/null 2>&1 || ! command -v make >/dev/null 2>&1; then
    echo "Stop gate: go or make is not on PATH; skipping the test run." >&2
    exit 1
  fi
  # t.TempDir() inherits TMPDIR, and the transport tests bind unix sockets
  # inside it. A long TMPDIR pushes those paths past the 107-byte sun_path
  # limit and fails the package for no code reason, so shorten it for the run.
  if [[ -n "${TMPDIR:-}" ]] && (( ${#TMPDIR} > 40 )); then
    TMPDIR="$(mktemp -d /tmp/axis-stop-gate.XXXXXX)"
    export TMPDIR
    trap 'rm -rf -- "$TMPDIR"' EXIT
  fi
  state="$({
    git rev-parse HEAD
    git -c color.ui=false status --porcelain
    git -c color.ui=false diff
    git -c color.ui=false diff --cached
    go version 2>/dev/null || true
    printf '%s\n' "${TMPDIR:-}"
  } | git hash-object --stdin)"
  stamp="$(git rev-parse --git-dir)/axis-stop-gate.stamp"
  if [[ -f "$stamp" ]] && [[ "$(cat "$stamp")" == "$state" ]]; then
    echo "Stop gate: tree unchanged since the last green run; skipping make test." >&2
  else
    echo "Stop gate: Go source or module/build files changed; running make test." >&2
    if ! make test; then
      echo "Stop gate: make test failed; fix the failure and try to finish again." >&2
      exit 2
    fi
    printf '%s\n' "$state" >"$stamp"
  fi
fi

if grep -Eq '^hack/claude-pretool-secret-scan(_test)?\.py$' <<<"$changed_files"; then
  echo "Stop gate: secret scanner changed; running its unit tests." >&2
  if ! PYTHONDONTWRITEBYTECODE=1 python3 -m unittest hack/claude-pretool-secret-scan_test.py; then
    echo "Stop gate: secret scanner tests failed; fix the failure and try to finish again." >&2
    exit 2
  fi
fi

if grep -Fxq 'hack/claude-stop-test-gate.sh' <<<"$changed_files"; then
  echo "Stop gate: shell test gate changed; checking syntax." >&2
  if ! bash -n hack/claude-stop-test-gate.sh; then
    echo "Stop gate: shell syntax check failed; fix the failure and try to finish again." >&2
    exit 2
  fi
fi

exit 0
