#!/usr/bin/env bash
# PR review cycle helper for AXIS (Linux / Bash 4+; not stock macOS bash 3.2).
#
# Purpose: compress the solo-operator loop
#   checks green → collect full review state → print for evaluation
# without fail-open check parsing and without auto-merge.
#
# Usage:
#   ./hack/pr-review-cycle.sh <pr-number> [--wait-bots SEC] [--wait-reviews SEC]
#                             [--allow-pending LOGIN]...
#
#   --wait-bots SEC      minimum wait after checks go green, for bots nobody requested (default 120)
#   --wait-reviews SEC   how long to wait for requested reviews, counted from when checks go green
#                        (default 1200); 0 checks once
#   --allow-pending LOGIN  do not wait for this reviewer (it will not answer); printed as a waiver
#
# Exit codes:
#   0  required checks green; no pending requested review; no unresolved threads
#   1  tool failure, timeout, or required checks not green
#   2  checks green but unresolved review threads remain (judgment needed)
#   3  a requested review is still pending (the list is printed); do not merge
#
# Does NOT: auto-patch, auto-reply, auto-resolve, or merge.
# Bot comments are advisory; evaluate credibility before applying.
set -euo pipefail

render_review_bodies() {
  jq -r '
    .. | .reviews?.nodes[]?
    | select((.body // "") != "")
    | "### @\(.author.login) \(.state) commit=\(.commit.oid[0:7] // "?")\n\(.body)\n"
  '
}

render_general_comments() {
  jq -r '
    .. | .comments?.nodes[]?
    | select(.url == null) # Exclude inline comments
    | "### @\(.author.login) @ \(.createdAt)\n\(.body)\n"
  '
}

render_inline_threads() {
  local head_oid="$1"
  jq -r --arg head "$head_oid" '
    .. | .reviewThreads?.nodes[]?
    | . as $t
    | ($t.comments.nodes[0] // {}) as $c
    | "thread resolved=\($t.isResolved) outdated=\($t.isOutdated) path=\($t.path // "?")\n  @\($c.author.login // "?") head_match=\(($c.commit.oid // "") == $head)\n  \($c.body // "")\n  \($c.url // "")\n"
  '
}

count_unresolved_threads() {
  jq -r '[.. | .reviewThreads?.nodes[]? | select(.isResolved == false)] | length'
}

require_clean_merge_state() {
  local merge_state="$1"
  if [[ "$merge_state" != "CLEAN" ]]; then
    echo "error: merge state is not CLEAN: $merge_state" >&2
    return 1
  fi
}

now_epoch() { date +%s; }

# pending_reviewers reads one requested-review GraphQL response on stdin and prints
# "login<TAB>requested-at" for each requested reviewer that has not answered: one still in
# reviewRequests, or one whose latest request has no later review or removal (a reviewer can leave
# reviewRequests when it starts, before it submits). It fails on a response of the wrong shape, so
# an API error never reads as "nothing pending".
pending_reviewers() {
  local json
  json="$(cat)" || return 1
  printf '%s\n' "$json" | jq -e '
    (.data.repository.pullRequest.reviewRequests.nodes | type == "array")
    and (.data.repository.pullRequest.timelineItems.nodes | type == "array")' >/dev/null 2>&1 \
    || return 1
  printf '%s\n' "$json" | jq -r '
    def norm: sub("\\[bot\\]$"; "");
    .data.repository.pullRequest as $pr
    | $pr.timelineItems.nodes as $ev
    | ([$pr.reviewRequests.nodes[].requestedReviewer
        | if . == null then {login: "?unknown", at: "requested"}
          elif .__typename == "Team" then {login: ("@" + (.name // "team")), at: "requested"}
          elif (.login // null) == null then {login: ("?" + (.__typename // "unknown")), at: "requested"}
          else {login: (.login | norm), at: "requested"} end]
      + [range(0; $ev | length) as $i
        | $ev[$i]
        | select(.__typename == "ReviewRequestedEvent" and (.requestedReviewer.login // null) != null)
        | (.requestedReviewer.login | norm) as $l
        | select([$ev[$i + 1:][]
            | select((.__typename == "PullRequestReview" and .submittedAt != null
                and ((.author.login // "") | norm) == $l)
              or (.__typename == "ReviewRequestRemovedEvent"
                and ((.requestedReviewer.login // "") | norm) == $l))] | length == 0)
        | {login: $l, at: .createdAt}])
    | group_by(.login)
    | map({login: .[0].login, at: ([.[].at | select(. != "requested")] | first // "requested")})
    | .[]
    | "\(.login)\t\(.at)"' || return 1
}

fetch_pending_json() {
  local reviewer='requestedReviewer { __typename ... on User { login } ... on Bot { login } ... on Team { name } }'
  gh api graphql -f query="
query {
  repository(owner: \"$repo_owner\", name: \"$repo_name\") {
    pullRequest(number: ${PR}) {
      reviewRequests(first: 100) { nodes { $reviewer } }
      timelineItems(last: 100, itemTypes: [REVIEW_REQUESTED_EVENT, REVIEW_REQUEST_REMOVED_EVENT, PULL_REQUEST_REVIEW]) {
        nodes {
          __typename
          ... on ReviewRequestedEvent { createdAt $reviewer }
          ... on ReviewRequestRemovedEvent { createdAt $reviewer }
          ... on PullRequestReview { submittedAt author { login } }
        }
      }
    }
  }
}"
}

# read_pending prints the pending reviewers not waived with --allow-pending and records waivers.
# Every step checks its own status: set -e does not apply inside a caller's || or $(...).
read_pending() {
  local json all login at
  json="$(fetch_pending_json)" || { echo "error: unable to read requested reviews for PR #${PR}" >&2; return 1; }
  all="$(printf '%s\n' "$json" | pending_reviewers)" \
    || { echo "error: malformed requested-review response for PR #${PR}" >&2; return 1; }
  while IFS=$'\t' read -r login at; do
    [[ -n "$login" ]] || continue
    if [[ " ${ALLOW_PENDING[*]-} " == *" ${login} "* ]]; then
      printf '%s\n' "$login" >>"$waiver_log"
    else
      printf '%s\t%s\n' "$login" "$at"
    fi
  done <<<"$all"
}

fetch_thread_json() {
  gh api graphql --paginate -f query="
query(\$endCursor: String) {
  repository(owner: \"$repo_owner\", name: \"$repo_name\") {
    pullRequest(number: ${PR}) {
      reviewThreads(first: 100, after: \$endCursor) {
        pageInfo { hasNextPage endCursor }
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
      latest: reviews(last: 1) { nodes { submittedAt } }
      comments(first: 50) {
        nodes {
          author { login }
          body
          createdAt
        }
      }
    }
  }
}"
}

# review_is_fresh succeeds when the newest review was submitted under 60 s ago: its inline
# comments may not have reached reviewThreads yet.
review_is_fresh() {
  jq -e --argjson now "$(now_epoch)" '
    [.. | .latest?.nodes[]?.submittedAt // empty] | last
    | . != null and ($now - fromdateiso8601) < 60' >/dev/null 2>&1
}

main() {
if [[ $# -lt 1 ]]; then
  echo "Usage: $0 <pr-number> [--wait-bots SEC] [--wait-reviews SEC] [--allow-pending LOGIN]..." >&2
  exit 1
fi

PR="$1"
shift
WAIT_BOTS=120
WAIT_REVIEWS=1200
ALLOW_PENDING=()
while (($# > 0)); do
  case "$1" in
    --wait-bots)
      WAIT_BOTS="$2"
      shift 2
      ;;
    --wait-reviews)
      WAIT_REVIEWS="$2"
      shift 2
      ;;
    --allow-pending)
      ALLOW_PENDING+=("${2%\[bot\]}")
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

if ! [[ "$PR" =~ ^[0-9]+$ ]]; then
  echo "error: PR number must be an integer, got: $PR" >&2
  exit 1
fi
if ! [[ "$WAIT_BOTS" =~ ^[0-9]+$ && "$WAIT_REVIEWS" =~ ^[0-9]+$ ]]; then
  echo "error: --wait-bots and --wait-reviews take whole seconds" >&2
  exit 1
fi
waiver_log="$(mktemp)"
trap 'rm -f "$waiver_log"' EXIT

for dep in gh jq; do
  if ! command -v "$dep" >/dev/null 2>&1; then
    echo "error: required dependency not found: $dep" >&2
    exit 1
  fi
done

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

# Dynamically fetch required checks and repo info.
base_branch="$(gh pr view "$PR" --json baseRefName --jq .baseRefName)"
repo_owner="$(gh repo view --json owner --jq .owner.login)"
repo_name="$(gh repo view --json name --jq .name)"

# Keep API errors and malformed responses distinct from a confirmed empty list.
# Never treat a failed discovery request as "no required checks".
if ! required_json="$(gh api "repos/$repo_owner/$repo_name/branches/$base_branch/protection/required_status_checks")"; then
  echo "error: unable to discover required checks for $base_branch" >&2
  exit 1
fi
if ! printf '%s\n' "$required_json" | jq -e '.contexts | type == "array" and all(.[]; type == "string" and length > 0)' >/dev/null; then
  echo "error: malformed required-check response for $base_branch" >&2
  exit 1
fi
mapfile -t REQUIRED_CHECKS < <(printf '%s\n' "$required_json" | jq -r '.contexts[]')
if [[ ${#REQUIRED_CHECKS[@]} -eq 0 ]]; then
  echo "warning: no required checks found for branch $base_branch; proceeding without checks verification" >&2
fi

echo "== PR #${PR} =="
pr_json="$(gh pr view "$PR" --json url,title,state,headRefOid,mergeStateStatus,statusCheckRollup)"
echo "$pr_json" | jq '{url,title,state,mergeStateStatus,head: .headRefOid[0:7]}'

if [[ ${#REQUIRED_CHECKS[@]} -gt 0 ]]; then
  echo "== Required checks (fail-closed) =="
  # Official gate: only required contexts; watch until done; non-zero on failure.
  if ! gh pr checks "$PR" --required --watch --fail-fast --interval 15; then
    echo "error: required checks failed or did not complete successfully" >&2
    gh pr checks "$PR" >&2 || true
    exit 1
  fi

  # Re-fetch rollup after watch.
  pr_json="$(gh pr view "$PR" --json url,title,state,headRefOid,mergeStateStatus,statusCheckRollup)"
  for name in "${REQUIRED_CHECKS[@]}"; do
    conclusion="$(echo "$pr_json" | jq -r --arg n "$name" '
      [
        .statusCheckRollup[]?
        | select((.name // "") == $n or (.context // "") == $n)
        | (.conclusion // .state // "")
      ] | map(select(. != "")) | first // empty
    ')"
    if [[ -z "$conclusion" ]]; then
      echo "error: required check context missing from rollup: $name" >&2
      exit 1
    fi
    # Normalize: SUCCESS (CheckRun) or success (StatusContext)
    conclusion_up="$(printf '%s' "$conclusion" | tr '[:lower:]' '[:upper:]')"
    if [[ "$conclusion_up" != "SUCCESS" ]]; then
      echo "error: required check not SUCCESS: $name=$conclusion" >&2
      exit 1
    fi
  done
  echo "required checks: green"
fi

checks_green_at="$(now_epoch)"
echo "== Bot floor wait (${WAIT_BOTS}s, for bots nobody requested; not a quality gate) =="
sleep "$WAIT_BOTS"

echo "== Requested reviews (wait up to ${WAIT_REVIEWS}s from checks green) =="
while :; do
  pending="$(read_pending)" || exit 1
  [[ -z "$pending" ]] && break
  (( $(now_epoch) - checks_green_at >= WAIT_REVIEWS )) && break
  sleep 15
done
if [[ -s "$waiver_log" ]]; then
  sort -u "$waiver_log" | while read -r login; do echo "waived pending review: $login"; done
fi
if [[ -n "$pending" ]]; then
  printf '%s\n' "$pending" | while IFS=$'\t' read -r login at; do echo "pending: $login (requested $at)"; done
else
  echo "requested reviews: none pending"
fi

head_oid="$(echo "$pr_json" | jq -r .headRefOid)"
echo "== Review state (head ${head_oid:0:7}) =="

# Review thread state via GraphQL with pagination support (up to 100 items/page).
thread_json="$(fetch_thread_json)"
if printf '%s\n' "$thread_json" | review_is_fresh; then
  # Inline comments can reach reviewThreads after their review; read once more.
  sleep 10
  thread_json="$(fetch_thread_json)" || exit 1
fi

echo "--- Review bodies ---"
echo "$thread_json" | render_review_bodies

echo "--- General PR comments ---"
echo "$thread_json" | render_general_comments

echo "--- Inline threads ---"
echo "$thread_json" | render_inline_threads "$head_oid"

unresolved="$(echo "$thread_json" | count_unresolved_threads)"
echo "unresolved_threads=${unresolved}"

echo
echo "OPERATOR NEXT STEPS"
echo "  1. Evaluate each unresolved/current-head comment for credibility."
echo "  2. If credible and cheap: patch, push, re-run this script."
echo "  3. Reply on threads; resolve only after handling."
echo "  4. Merge manually when green: gh pr merge ${PR} --squash"
echo "  This script will not merge."

if [[ -n "$pending" ]]; then
  echo "error: requested review still pending after ${WAIT_REVIEWS}s; do not merge (re-run later, or --allow-pending LOGIN)" >&2
  exit 3
fi
if [[ "$unresolved" != "0" ]]; then
  exit 2
fi
# Re-read at enforcement: the bot wait and review collection take minutes,
# and an earlier CLEAN must not pass a PR whose base or head has moved, or
# that gained a review request, since.
pending="$(read_pending)" || exit 1
if [[ -n "$pending" ]]; then
  printf '%s\n' "$pending" | while IFS=$'\t' read -r login at; do echo "pending: $login (requested $at)"; done
  echo "error: requested review still pending at the final re-read; do not merge" >&2
  exit 3
fi
final_json="$(gh pr view "$PR" --json headRefOid,mergeStateStatus)"
if [[ "$(echo "$final_json" | jq -r .headRefOid)" != "$head_oid" ]]; then
  echo "error: head moved during review (was ${head_oid:0:7}); re-run" >&2
  exit 1
fi
require_clean_merge_state "$(echo "$final_json" | jq -r .mergeStateStatus)"
exit 0
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
