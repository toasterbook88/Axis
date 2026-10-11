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
  "api graphql") cat "$GH_MOCK_FIXTURE" ;;
  *) echo "unexpected mock gh call: $*" >&2; exit 55 ;;
esac
MOCK_GH
chmod +x "$tmpdir/gh"

run_case() {
  local scenario="$1" expected="$2" actual=0
  GH_MOCK_CASE="$scenario" GH_MOCK_FIXTURE="$tmpdir/fixture.json" \
    PATH="$tmpdir:$PATH" "$repo_root/hack/pr-review-cycle.sh" 510 --wait-bots 0 \
    >"$tmpdir/$scenario.out" 2>&1 || actual=$?
  if [[ "$actual" != "$expected" ]]; then
    echo "scenario $scenario: expected exit $expected, got $actual" >&2
    cat "$tmpdir/$scenario.out" >&2
    exit 1
  fi
}
run_case clean 0
run_case dirty 1
run_case moved 1
run_case failed_final 52
run_case discovery_error 1
run_case discovery_malformed 1
printf '%s\n' "$fixture" >"$tmpdir/fixture.json"
run_case dirty 2

echo "pr-review-cycle tests passed"
