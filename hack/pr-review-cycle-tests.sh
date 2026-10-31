#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=hack/pr-review-cycle.sh
source "$repo_root/hack/pr-review-cycle.sh"

fixture='{
  "data": {
    "repository": {
      "pullRequest": {
        "reviewThreads": {
          "nodes": [{
            "isResolved": false,
            "isOutdated": false,
            "path": "internal/example.go",
            "comments": {"nodes": [{
              "author": {"login": "reviewer"},
              "body": "keep the fallback honest",
              "createdAt": "2026-10-10T00:00:00Z",
              "url": "https://example.invalid/thread",
              "commit": {"oid": "abcdef1234567890"}
            }]}
          }]
        },
        "reviews": {"nodes": [{
          "author": {"login": "reviewer"},
          "state": "COMMENTED",
          "body": "review body",
          "commit": {"oid": "abcdef1234567890"}
        }]},
        "comments": {"nodes": [{
          "author": {"login": "operator"},
          "body": "general comment",
          "createdAt": "2026-10-10T00:00:00Z"
        }]}
      }
    }
  }
}'

review_output="$(printf '%s\n' "$fixture" | render_review_bodies)"
[[ "$review_output" == *'commit=abcdef1'* ]]
[[ "$review_output" == *'review body'* ]]

general_output="$(printf '%s\n' "$fixture" | render_general_comments)"
[[ "$general_output" == *'general comment'* ]]

thread_output="$(printf '%s\n' "$fixture" | render_inline_threads abcdef1234567890)"
[[ "$thread_output" == *'path=internal/example.go'* ]]
[[ "$thread_output" == *'head_match=true'* ]]
[[ "$thread_output" == *'keep the fallback honest'* ]]

[[ "$(printf '%s\n' "$fixture" | count_unresolved_threads)" == "1" ]]
require_clean_merge_state CLEAN
if require_clean_merge_state DIRTY 2>/dev/null; then
  echo "expected DIRTY merge state to fail closed" >&2
  exit 1
fi


# Verify multiline renderers keep their record structure; never line-sort output.
multiline_fixture="$(printf '%s\n' "$fixture" | jq '.data.repository.pullRequest.reviews.nodes[0].body = "z-first\na-second" | .data.repository.pullRequest.comments.nodes[0].body = "z-first\na-second"')"
multi_review="$(printf '%s\n' "$multiline_fixture" | render_review_bodies)"
[[ "$multi_review" == *$'z-first\na-second'* ]]
multi_comment="$(printf '%s\n' "$multiline_fixture" | render_general_comments)"
[[ "$multi_comment" == *$'z-first\na-second'* ]]
! grep -F 'render_review_bodies | sort -u' "$repo_root/hack/pr-review-cycle.sh" >/dev/null
! grep -F 'render_general_comments | sort -u' "$repo_root/hack/pr-review-cycle.sh" >/dev/null

# Exercise main() with a deterministic GitHub CLI shim. No network or writes.
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
printf '%s\n' "$fixture" | jq '.data.repository.pullRequest.reviewThreads.nodes = []' >"$tmpdir/fixture.json"
cat >"$tmpdir/gh" <<'MOCK_GH'
#!/usr/bin/env bash
set -euo pipefail
case "$1 $2" in
  "repo view")
    case "$*" in
      *".owner.login"*) echo toasterbook88 ;;
      *".name"*) echo Axis ;;
      *) exit 50 ;;
    esac ;;
  "pr view")
    case "$*" in
      *"baseRefName"*) echo main ;;
      *"--json headRefOid,mergeStateStatus"*)
        case "$GH_MOCK_CASE" in
          clean) echo '{"headRefOid":"abcdef1234567890","mergeStateStatus":"CLEAN"}' ;;
          dirty) echo '{"headRefOid":"abcdef1234567890","mergeStateStatus":"DIRTY"}' ;;
          moved) echo '{"headRefOid":"ffffef1234567890","mergeStateStatus":"CLEAN"}' ;;
          failed_final) exit 52 ;;
          *) exit 53 ;;
        esac ;;
      *) echo '{"url":"https://example.invalid/pr","title":"fixture","state":"OPEN","headRefOid":"abcdef1234567890","mergeStateStatus":"CLEAN","statusCheckRollup":[]}' ;;
    esac ;;
  "api repos/"*)
    case "$GH_MOCK_CASE" in
      discovery_error) exit 54 ;;
      discovery_malformed) echo '{"contexts":"not-an-array"}' ;;
      *) echo '{"contexts":[]}' ;;
    esac ;;
  "api graphql")
    case "$*" in
      *timelineItems*)
        # Pending-review reads: serve pending/1.json, 2.json, ... and repeat the last.
        n=$(($(cat "$GH_MOCK_PENDING_DIR/count" 2>/dev/null || echo 0) + 1))
        echo "$n" >"$GH_MOCK_PENDING_DIR/count"
        [[ "${GH_MOCK_PENDING_FAIL:-}" == "$n" ]] && exit 56
        f="$GH_MOCK_PENDING_DIR/$n.json"
        [[ -f "$f" ]] || f="$(ls "$GH_MOCK_PENDING_DIR"/*.json | sort -V | tail -1)"
        cat "$f" ;;
      *)
        echo x >>"$GH_MOCK_PENDING_DIR/thread-reads"
        echo threads >>"$GH_MOCK_PENDING_DIR/events"
        cat "$GH_MOCK_FIXTURE" ;;
    esac ;;
  *) echo "unexpected mock gh call: $*" >&2; exit 55 ;;
esac
MOCK_GH
chmod +x "$tmpdir/gh"

# Fake clock shared by sleep and date: sleep N advances it by N without waiting.
cat >"$tmpdir/sleep" <<'MOCK_SLEEP'
#!/usr/bin/env bash
echo "$1" >>"$GH_MOCK_PENDING_DIR/sleeps"
echo "sleep $1" >>"$GH_MOCK_PENDING_DIR/events"
echo "$(($(cat "$GH_MOCK_CLOCK") + ${1%.*}))" >"$GH_MOCK_CLOCK"
MOCK_SLEEP
cat >"$tmpdir/date" <<'MOCK_DATE'
#!/usr/bin/env bash
[[ "$1" == "+%s" ]] && { cat "$GH_MOCK_CLOCK"; exit 0; }
exec /usr/bin/date "$@"
MOCK_DATE
chmod +x "$tmpdir/sleep" "$tmpdir/date"
clock_start=1793000000

# pending_fixture <requests-json> <timeline-json>: one pending-review read.
pending_fixture() {
  jq -n --argjson r "$1" --argjson t "$2" \
    '{data:{repository:{pullRequest:{reviewRequests:{nodes:$r},timelineItems:{nodes:$t}}}}}'
}
no_pending="$(pending_fixture '[]' '[]')"
copilot_req='{"requestedReviewer":{"__typename":"Bot","login":"copilot-pull-request-reviewer"}}'
copilot_ev='{"__typename":"ReviewRequestedEvent","createdAt":"2026-10-28T19:11:23Z","requestedReviewer":{"__typename":"Bot","login":"copilot-pull-request-reviewer"}}'

# set_pending <json>...: the pending reads for the next run, in order.
set_pending() {
  rm -rf "$tmpdir/pending"
  mkdir -p "$tmpdir/pending"
  local i=0
  for j in "$@"; do i=$((i + 1)); printf '%s\n' "$j" >"$tmpdir/pending/$i.json"; done
  echo "$clock_start" >"$tmpdir/clock"
}

run_case() {
  local scenario="$1" expected="$2" actual=0
  shift 2
  [[ -d "$tmpdir/pending" ]] || set_pending "$no_pending"
  GH_MOCK_CASE="$scenario" GH_MOCK_FIXTURE="$tmpdir/fixture.json" \
    GH_MOCK_PENDING_DIR="$tmpdir/pending" GH_MOCK_CLOCK="$tmpdir/clock" \
    PATH="$tmpdir:$PATH" "$repo_root/hack/pr-review-cycle.sh" 510 --wait-bots 0 "$@" \
    >"$tmpdir/$scenario.out" 2>&1 || actual=$?
  if [[ "$actual" != "$expected" ]]; then
    echo "scenario $scenario: expected exit $expected, got $actual" >&2
    cat "$tmpdir/$scenario.out" >&2
    exit 1
  fi
  # Keep this run's reads, sleeps and output for assertions; the next run starts fresh.
  rm -rf "$tmpdir/last"
  mv "$tmpdir/pending" "$tmpdir/last"
  cp "$tmpdir/$scenario.out" "$tmpdir/last/out"
}
run_case clean 0
run_case dirty 1
run_case moved 1
run_case failed_final 52
run_case discovery_error 1
run_case discovery_malformed 1
printf '%s\n' "$fixture" >"$tmpdir/fixture.json"
run_case dirty 2
printf '%s\n' "$fixture" | jq '.data.repository.pullRequest.reviewThreads.nodes = []' >"$tmpdir/fixture.json"

# --- Pending requested reviews (docs/superpowers/specs/2026-10-31-pr-review-pending-gate-design.md)

expect_pending() { # expect_pending <name> <expected logins, space-separated> <json>
  local name="$1" want="$2" json="$3" got
  got="$(printf '%s\n' "$json" | pending_reviewers | cut -f1 | sort | tr '\n' ' ' | sed 's/ $//')" \
    || { echo "pending_reviewers failed on $name" >&2; exit 1; }
  [[ "$got" == "$want" ]] || { echo "pending $name: want '$want', got '$got'" >&2; exit 1; }
}
review_by() { printf '{"__typename":"PullRequestReview","submittedAt":"%s","author":{"login":"%s"}}' "$1" "$2"; }
removed_ev='{"__typename":"ReviewRequestRemovedEvent","createdAt":"2026-10-28T19:13:00Z","requestedReviewer":{"__typename":"Bot","login":"copilot-pull-request-reviewer"}}'
team_req='{"requestedReviewer":{"__typename":"Team","name":"core"}}'

expect_pending in-request-list 'copilot-pull-request-reviewer' "$(pending_fixture "[$copilot_req]" '[]')"
expect_pending timeline-only 'copilot-pull-request-reviewer' "$(pending_fixture '[]' "[$copilot_ev]")"
expect_pending answered '' "$(pending_fixture '[]' "[$copilot_ev, $(review_by 2026-10-28T19:20:00Z 'copilot-pull-request-reviewer[bot]')]")"
expect_pending removed '' "$(pending_fixture '[]' "[$copilot_ev, $removed_ev]")"
expect_pending re-requested-after-review 'copilot-pull-request-reviewer' \
  "$(pending_fixture '[]' "[$copilot_ev, $(review_by 2026-10-28T19:20:00Z copilot-pull-request-reviewer), $copilot_ev]")"
expect_pending re-requested-after-removal 'copilot-pull-request-reviewer' "$(pending_fixture '[]' "[$copilot_ev, $removed_ev, $copilot_ev]")"
expect_pending team-requested '@core' "$(pending_fixture "[$team_req]" '[]')"
team_ev='{"__typename":"ReviewRequestedEvent","createdAt":"2026-10-28T19:11:23Z","requestedReviewer":{"__typename":"Team","name":"core"}}'
expect_pending team-no-longer-requested '' "$(pending_fixture '[]' "[$team_ev]")"
expect_pending null-reviewer '?unknown' "$(pending_fixture '[{"requestedReviewer":null}]' '[]')"
expect_pending mannequin-reviewer '?Mannequin' "$(pending_fixture '[{"requestedReviewer":{"__typename":"Mannequin"}}]' '[]')"
draft_review='{"__typename":"PullRequestReview","submittedAt":null,"author":{"login":"copilot-pull-request-reviewer"}}'
expect_pending unsubmitted-draft-review 'copilot-pull-request-reviewer' "$(pending_fixture '[]' "[$copilot_ev, $draft_review]")"
# #577 as it stood at its merge (19:22:23Z): requested 19:11:23Z, only toasterbook88 had reviewed.
expect_pending pr577-at-merge 'copilot-pull-request-reviewer' \
  "$(pending_fixture "[$copilot_req]" "[$copilot_ev, $(review_by 2026-10-28T19:12:09Z toasterbook88)]")"
for bad in '{}' '{"data":null}' '{"data":{"repository":{"pullRequest":null}}}' \
  '{"data":{"repository":{"pullRequest":{"timelineItems":{"nodes":[]}}}}}' 'not json'; do
  if printf '%s\n' "$bad" | pending_reviewers >/dev/null 2>&1; then
    echo "pending_reviewers accepted a wrong-shape response: $bad" >&2
    exit 1
  fi
done

assert_out() { grep -qF -- "$1" "$tmpdir/last/out" || { echo "output lacks '$1':" >&2; cat "$tmpdir/last/out" >&2; exit 1; }; }

set_pending "$(pending_fixture "[$copilot_req]" "[$copilot_ev]")"
run_case clean 3 --wait-reviews 0
assert_out 'copilot-pull-request-reviewer'
assert_out 'requested review still pending'

set_pending "$(pending_fixture '[]' "[$copilot_ev]")"
run_case clean 3 --wait-reviews 0

set_pending "$(pending_fixture "[$copilot_req]" "[$copilot_ev]")" "$no_pending"
run_case clean 0 --wait-reviews 30
[[ "$(cat "$tmpdir/last/count")" -ge 2 ]] || { echo "pending set was not re-read while waiting" >&2; exit 1; }
grep -qx 15 "$tmpdir/last/sleeps" || { echo "no 15 s poll sleep recorded" >&2; exit 1; }

set_pending "$(pending_fixture "[$copilot_req]" "[$copilot_ev]")"
run_case clean 3 --wait-reviews 30

# First read (the wait) is clear; the second (the final re-read) has a new request.
set_pending "$no_pending" "$(pending_fixture "[$copilot_req]" "[$copilot_ev]")"
run_case clean 3 --wait-reviews 0
assert_out 'requested review still pending'

set_pending "$no_pending"
run_case clean 0 --wait-bots 7
[[ "$(head -1 "$tmpdir/last/sleeps")" == 7 ]] || { echo "floor sleep of 7 s not recorded first" >&2; exit 1; }
[[ "$(grep --max-count=1 -n 'sleep 7' "$tmpdir/last/events" | cut -d: -f1)" -lt "$(grep --max-count=1 -n threads "$tmpdir/last/events" | cut -d: -f1)" ]] \
  || { echo "floor sleep did not come before the first thread read" >&2; exit 1; }

# The deadline counts from checks green, so the floor uses up part of it: 20 s floor + one 15 s poll.
set_pending "$(pending_fixture "[$copilot_req]" "[$copilot_ev]")"
run_case clean 3 --wait-bots 20 --wait-reviews 30
[[ "$(grep -cx 15 "$tmpdir/last/sleeps")" == 1 ]] || { echo "deadline not counted from checks green: $(tr '\n' ' ' <"$tmpdir/last/sleeps")" >&2; exit 1; }

recent="$(jq -rn --argjson now "$clock_start" '$now - 20 | todate')"
printf '%s\n' "$fixture" | jq --arg t "$recent" '.data.repository.pullRequest.reviewThreads.nodes = [] | .data.repository.pullRequest.latest = {nodes: [{submittedAt: $t}]}' >"$tmpdir/fixture.json"
set_pending "$no_pending"
run_case clean 0
[[ "$(wc -l <"$tmpdir/last/thread-reads")" -ge 2 ]] || { echo "threads not re-read after a review under 60 s old" >&2; exit 1; }
printf '%s\n' "$fixture" | jq '.data.repository.pullRequest.reviewThreads.nodes = []' >"$tmpdir/fixture.json"

# A failed read fails closed both in the wait (read 1) and at the final re-read (read 2).
set_pending "$no_pending"
GH_MOCK_PENDING_FAIL=1 run_case clean 1
assert_out 'unable to read requested reviews'
set_pending "$no_pending"
GH_MOCK_PENDING_FAIL=2 run_case clean 1
set_pending 'not json'
run_case clean 1
set_pending '{"data":{"repository":{"pullRequest":null}}}'
run_case clean 1

set_pending "$(pending_fixture "[$copilot_req]" "[$copilot_ev]")"
run_case clean 0 --wait-reviews 0 --allow-pending copilot-pull-request-reviewer
assert_out 'waived pending review: copilot-pull-request-reviewer'

set_pending "$(pending_fixture "[$copilot_req]" "[$copilot_ev]")"
run_case clean 0 --wait-reviews 0 --allow-pending 'copilot-pull-request-reviewer[bot]'
assert_out 'waived pending review: copilot-pull-request-reviewer'

set_pending "$(pending_fixture '[{"requestedReviewer":null}]' '[]')"
run_case clean 3 --wait-reviews 0
assert_out 'pending: ?unknown'

echo "pr-review-cycle tests passed"
