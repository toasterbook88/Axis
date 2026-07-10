#!/usr/bin/env bash
# Optimized PR review cycle for AXIS (solo-operator + bot reviews).
#
# Goal: compress the loop
#   push → checks → bot comments → evaluate → fix if credible → reply →
#   re-check → merge
# without requiring bot-thread resolution as a branch rule, and without
# waiting forever for Gemini/Copilot.
#
# Usage:
#   ./hack/pr-review-cycle.sh <pr-number> [--wait-bots SEC] [--merge]
#
# Default --wait-bots is 120s after checks go green (bots usually land by then).
# Exit codes: 0 ready/merged, 2 needs human judgment, 1 tool failure.
set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "Usage: $0 <pr-number> [--wait-bots SEC] [--merge]" >&2
  exit 1
fi

PR="$1"
shift
WAIT_BOTS=120
DO_MERGE=0
while (($# > 0)); do
  case "$1" in
    --wait-bots) WAIT_BOTS="$2"; shift 2 ;;
    --merge) DO_MERGE=1; shift ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

echo "== PR #$PR =="
gh pr view "$PR" --json url,title,state,statusCheckRollup,reviewDecision --jq '{url,title,state,reviewDecision}'

echo "== Wait for required checks =="
# Poll until Test & Build and govulncheck are not pending (or timeout ~15m)
deadline=$((SECONDS + 900))
while (( SECONDS < deadline )); do
  mapfile -t lines < <(gh pr checks "$PR" 2>/dev/null || true)
  pending=0
  failed=0
  for line in "${lines[@]}"; do
    # format: NAME  STATUS  ...
    name=$(echo "$line" | awk '{print $1}')
    status=$(echo "$line" | awk '{print $2}')
    case "$name" in
      Test|govulncheck)
        # "Test & Build" splits — match Build line too via full line
        ;;
    esac
    if echo "$line" | grep -qE 'Test & Build|govulncheck'; then
      if echo "$line" | grep -qiE 'pending|queued|in_progress'; then
        pending=1
      fi
      if echo "$line" | grep -qiE 'fail'; then
        failed=1
      fi
    fi
  done
  if (( failed )); then
    echo "Required check failed:" >&2
    gh pr checks "$PR" >&2 || true
    exit 1
  fi
  if (( pending == 0 )); then
    break
  fi
  sleep 15
done
gh pr checks "$PR" || true

echo "== Wait ${WAIT_BOTS}s for bot review comments (non-blocking SLA) =="
sleep "$WAIT_BOTS"

echo "== Inline review comments =="
comments_json="$(gh api "repos/toasterbook88/Axis/pulls/${PR}/comments")"
count=$(echo "$comments_json" | jq 'length')
echo "inline_count=$count"
echo "$comments_json" | jq -r '.[] | "---\n@\(.user.login) \(.path):\(.line // .original_line)\n\(.body)\nURL: \(.html_url)\n"'

echo
echo "EVALUATION RULES (operator/agent):"
echo "  1. Credible + cheap (docs, error-masking, clear bugs) → apply, commit, push, reply."
echo "  2. Wrong or speculative → reply with evidence; do NOT change code."
echo "  3. Do not wait for more bots after SLA; do not require thread resolution to merge."
echo "  4. Re-run: push triggers PR CI once (push-to-main only on main)."
echo "  5. Merge only when Test & Build + govulncheck are green."
echo

if (( DO_MERGE == 1 )); then
  echo "== Merge (squash) =="
  gh pr merge "$PR" --squash --delete-branch
  echo "merged"
else
  echo "Dry run complete. Fix/reply as needed, push, re-run this script, then --merge."
fi
