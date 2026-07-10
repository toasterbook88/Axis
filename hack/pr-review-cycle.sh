#!/usr/bin/env bash
# PR review cycle helper for AXIS (Cranium / Bash 4+; not stock macOS bash 3.2).
#
# Purpose: compress the solo-operator loop
#   checks green → collect full review state → print for evaluation
# without fail-open check parsing and without auto-merge.
#
# Usage:
#   ./hack/pr-review-cycle.sh <pr-number> [--wait-bots SEC]
#
# Exit codes:
#   0  required checks green; review state printed (operator decides next)
#   1  tool failure, timeout, or required checks not green
#   2  checks green but unresolved review threads remain (judgment needed)
#
# Does NOT: auto-patch, auto-reply, auto-resolve, or merge.
# Bot comments are advisory; evaluate credibility before applying.
set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "Usage: $0 <pr-number> [--wait-bots SEC]" >&2
  exit 1
fi

PR="$1"
shift
WAIT_BOTS=120
while (($# > 0)); do
  case "$1" in
    --wait-bots)
      WAIT_BOTS="$2"
      shift 2
      ;;
    --merge)
      echo "error: --merge removed; this script is fail-closed and will not merge" >&2
      echo "merge manually after evaluating comments: gh pr merge $PR --squash" >&2
      exit 1
      ;;
    *)
      echo "unknown arg: $1" >&2
      exit 1
      ;;
  esac
done

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

# Required status contexts (must match branch protection).
REQUIRED_CHECKS=("Test & Build" "govulncheck")

echo "== PR #${PR} =="
gh pr view "$PR" --json url,title,state,headRefOid --jq '{url,title,state,head: .headRefOid[0:7]}'

echo "== Required checks (fail-closed) =="
# Official gate: only required contexts; watch until done; non-zero on failure.
# See: https://cli.github.com/manual/gh_pr_checks
if ! gh pr checks "$PR" --required --watch --fail-fast --interval 15; then
  echo "error: required checks failed or did not complete successfully" >&2
  gh pr checks "$PR" >&2 || true
  exit 1
fi

# Explicit presence check: --required can pass vacuously in some edge cases if
# contexts are misconfigured; verify our named checks show pass.
checks_json="$(gh pr view "$PR" --json statusCheckRollup)"
for name in "${REQUIRED_CHECKS[@]}"; do
  # statusCheckRollup entries use .name and .conclusion
  conclusion="$(echo "$checks_json" | jq -r --arg n "$name" '
    [.statusCheckRollup[]? | select(.name == $n) | .conclusion] | first // empty
  ')"
  if [[ -z "$conclusion" ]]; then
    echo "error: required check context missing from rollup: $name" >&2
    exit 1
  fi
  if [[ "$conclusion" != "SUCCESS" ]]; then
    echo "error: required check not SUCCESS: $name=$conclusion" >&2
    exit 1
  fi
done
echo "required checks: green"

echo "== Bot SLA wait (${WAIT_BOTS}s; not a quality gate) =="
sleep "$WAIT_BOTS"

head_oid="$(gh pr view "$PR" --json headRefOid --jq .headRefOid)"
echo "== Review state (head ${head_oid:0:7}) =="

# Full review thread state via GraphQL (not first-page REST only).
thread_json="$(gh api graphql -f query="
query {
  repository(owner: \"toasterbook88\", name: \"Axis\") {
    pullRequest(number: ${PR}) {
      reviewThreads(first: 100) {
        nodes {
          isResolved
          isOutdated
          path
          comments(first: 50) {
            nodes {
              author { login }
              body
              createdAt
              outdated
              url
              originalCommit { oid }
              commit { oid }
            }
          }
        }
      }
      reviews(first: 50) {
        nodes {
          author { login }
          state
          body
          submittedAt
          commit { oid }
        }
      }
      comments(first: 50) {
        nodes {
          author { login }
          body
          createdAt
        }
      }
    }
  }
}")"

echo "--- Review bodies ---"
echo "$thread_json" | jq -r '
  .data.repository.pullRequest.reviews.nodes[]?
  | select((.body // "") != "")
  | "### @\(.author.login) \(.state) commit=\(.commit.oid[0:7] // "?")\n\(.body)\n"
'

echo "--- General PR comments ---"
echo "$thread_json" | jq -r '
  .data.repository.pullRequest.comments.nodes[]?
  | "### @\(.author.login) @ \(.createdAt)\n\(.body)\n"
'

echo "--- Inline threads ---"
echo "$thread_json" | jq -r --arg head "$head_oid" '
  .data.repository.pullRequest.reviewThreads.nodes[]?
  | . as $t
  | ($t.comments.nodes[0] // {}) as $c
  | "thread resolved=\($t.isResolved) outdated=\($t.isOutdated) path=\($t.path // "?")\n  @\($c.author.login // "?") head_match=\(($c.commit.oid // "") == $head)\n  \($c.body // "")\n  \($c.url // "")\n"
'

unresolved="$(echo "$thread_json" | jq '[.data.repository.pullRequest.reviewThreads.nodes[]? | select(.isResolved == false)] | length')"
echo "unresolved_threads=${unresolved}"

echo
echo "OPERATOR NEXT STEPS"
echo "  1. Evaluate each unresolved/current-head comment for credibility."
echo "  2. If credible and cheap: patch, push, re-run this script."
echo "  3. Reply on threads; resolve only after handling."
echo "  4. Merge manually when green: gh pr merge ${PR} --squash"
echo "  This script will not merge."

if [[ "$unresolved" != "0" ]]; then
  exit 2
fi
exit 0
