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

echo "pr-review-cycle tests passed"
