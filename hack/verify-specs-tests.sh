#!/usr/bin/env bash
# Regression tests for hack/verify-specs.go.
#
# The live gate runs against the real specs in CI. These cases run the gate on
# throwaway fixture trees so every rule in
# docs/superpowers/specs/2026-10-31-verify-specs-design.md stays locked.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

fail() {
  printf 'verify-specs-tests: %s\n' "$1" >&2
  exit 1
}

test_root="$(mktemp -d "${TMPDIR:-/tmp}/axis-verify-specs-tests.XXXXXX")"
trap 'rm -rf "$test_root"' EXIT

go build -o "$test_root/gate" hack/verify-specs.go

name=2026-11-01-example-design.md

sections='# Example

## Problem

p

## Non-goals

n

## Design

d

## Alternatives

a

## Research

r
'

header() { # header <kind> <verdict>
  printf 'Kind: %s\nReviewer: fresh-context subagent\nModel: same family\nBase: 5c9975b5\nHead: 272fe232\nDate: 2026-10-31\nVerdict: %s\n' "$1" "$2"
}

good_review="$(header spec revise)

1. Major: a gap. Fixed: closed; test.

Questions:
- A doubt? Became Design 2.

$(header spec approve)

Note: code starts."

# new_case: a fresh fixture tree with an empty specs directory.
new_case() {
  CASE="$(mktemp -d "$test_root/case.XXXXXX")"
  mkdir -p "$CASE/docs/superpowers/specs"
}

# spec <review text> [file name]: write one spec with the given ## Review body.
spec() {
  printf '%s\n## Review\n\n%s\n' "$sections" "$1" >"$CASE/docs/superpowers/specs/${2:-$name}"
}

run_gate() {
  set +e
  OUT="$(cd "$CASE" && "$test_root/gate" 2>&1)"
  CODE=$?
  set -e
}

# pass <label>: the gate must exit 0.
pass() {
  run_gate
  [[ "$CODE" == 0 ]] || fail "$1: expected pass, got exit $CODE: $OUT"
}

# reject <label> <expected output fragment>: the gate must exit 1 naming the problem.
reject() {
  run_gate
  [[ "$CODE" == 1 ]] || fail "$1: expected exit 1, got $CODE: $OUT"
  [[ "$OUT" == *"$2"* ]] || fail "$1: output lacks '$2': $OUT"
}

# review_case <label> pass|<fragment> <review text>: one spec with that review.
review_case() {
  new_case
  spec "$3"
  if [[ "$2" == pass ]]; then pass "$1"; else reject "$1" "$2"; fi
}

# --- Naming and selection

new_case
spec "$good_review"
pass conforming
[[ "$OUT" == "specs passed: 1 checked, 0 grandfathered" ]] || fail "conforming: summary line: $OUT"

new_case
spec 'no review blocks at all' 2026-10-19-axis-start-screen-design.md
pass grandfathered
[[ "$OUT" == *"0 checked, 1 grandfathered"* ]] || fail "grandfathered: summary line: $OUT"

new_case
spec 'no review blocks at all' 2020-01-01-backdated-design.md
reject backdated 'rule 2: text before the first Kind: block'

new_case
spec "$good_review" notes.md
reject misnamed 'file name must be YYYY-MM-DD-<topic>-design.md'

new_case
mkdir "$CASE/docs/superpowers/specs/drafts"
reject subdirectory 'subdirectory in the specs directory'

# --- Rule 1: sections

new_case
printf '%s\n## Review\n\n%s\n' "${sections/'## Research'/'## Reading'}" "$good_review" >"$CASE/docs/superpowers/specs/$name"
reject missing-section 'missing section ## Research'

new_case
printf '%s\n## Review\n\n%s\n\n## Review\n\n%s\n' "$sections" "$good_review" "$good_review" >"$CASE/docs/superpowers/specs/$name"
reject second-review 'second ## Review section'

# --- Rule 2: block headers

# Without Kind: there is no block, so the fields are text before the first block.
review_case missing-Kind 'text before the first Kind: block' "$(header spec approve | grep -v '^Kind:')"
for field in Reviewer Model Base Head Date Verdict; do
  review_case "missing-$field" "block has no $field: line" \
    "$(header spec approve | grep -v "^$field:")"
done
review_case bad-kind 'Kind: "design"' "$(header design approve)"
review_case bad-verdict 'Verdict: "approved"' "$(header spec approved)"
review_case bad-date 'Date: "Oct 31"' "$(header spec approve | sed 's/^Date: .*/Date: Oct 31/')"
review_case bad-head 'Head: "HEAD"' "$(header spec approve | sed 's/^Head: .*/Head: HEAD/')"
review_case bad-base 'Base: "main"' "$(header spec approve | sed 's/^Base: .*/Base: main/')"
h="$(header spec approve)"
nl=$'\n'
review_case duplicate-field 'block has 2 Reviewer: lines' "${h/Model:/Reviewer: someone else${nl}Model:}"
review_case model-continuation pass "${h/Model: same family/Model: same family. The second family${nl}was out of weekly usage.}"
review_case verdict-trailing-text pass "$(header spec approve | sed 's/^Verdict: .*/Verdict: approve-with-changes (six inline findings)/')"
review_case head-trailing-text pass "$(header spec approve | sed 's/^Head: .*/Head: 272fe232 (after the merge)/')"
review_case header-dash-line 'a header line starting "- "' "$(header spec approve)
- a question with no Questions: line"
review_case item-in-header 'an item in the block header' "$(header spec approve)
1. A gap. Fixed."

# --- Rule 2: block bodies

review_case before-first-kind 'text before the first Kind: block' "Reviewer: fresh-context subagent
Model: same family
Base: 5c9975b5
Date: 2026-10-31
Verdict: approve

$good_review"
review_case restarted-numbering 'finding 1 where 2 was expected' "$(header spec approve)

1. A gap. Fixed.

Fifth review: all good.
1. Another gap. Fixed."
review_case restarted-numbering-items-only 'finding 1 where 2 was expected' "$(header spec approve)

1. A gap. Fixed.
1. Another gap. Fixed."
review_case prose-after-findings 'line is not an item, Questions: or Note:' "$(header spec approve)

1. A gap. Fixed.

The fifth review found nothing new."
review_case prose-after-empty-block 'line is not an item, Questions: or Note:' "$(header spec approve)

The fifth review found nothing new."
review_case review-after-empty-block 'line is not an item, Questions: or Note:' "$(header spec approve)

Fifth review:
1. A gap. Fixed."
review_case glued-prose 'line is not an item, Questions: or Note:' "$(header spec approve)

1. A gap. Fixed.
Sixth review (Copilot): one race, not addressed."
review_case unindented-number 'line is not an item, Questions: or Note:' "$(header spec approve)

1. A gap, measured
6.7 min after CI. Fixed."
review_case indented-number pass "$(header spec approve)

1. A gap, measured
   6.7 min after CI. Fixed."
review_case note-after-items pass "$(header spec approve)

1. A gap. Fixed.

Note: every fix has a test."
review_case question-without-questions-line 'with no Questions: line before it' "$(header spec approve)

- A doubt? Became Design 2."
review_case uppercase-questions pass "$(header spec approve)

QUESTIONS:
- A doubt? Became Design 2."
review_case raw-finding 'raw reviewer finding' "$(header spec approve)

F1 [major] [defect] A gap"
review_case indented-f-continuation pass "$(header spec approve)

1. A gap that F1 named. Fixed: the reviewer's
  F2 is the same class."
review_case indented-paragraph 'paragraph does not start with an item' "$(header spec approve)

  An indented paragraph of prose."

# --- Rule 3: order

review_case no-spec-block 'no Kind: spec block' "$(header diff approve)"
review_case last-spec-revise 'the last spec review is revise' "$(header spec approve)

$(header spec revise)"
review_case last-diff-revise 'the last diff review is revise' "$(header spec approve)

$(header diff revise)"
review_case superseded-revise pass "$(header spec revise)

$(header spec approve)

$(header diff revise)

$(header diff approve-with-changes)"

# --- Rule 4: statuses

review_case finding-without-status 'finding has no status' "$(header spec approve)

1. A gap that nobody closed."
review_case question-without-status 'question has no status' "$(header spec approve)

Questions:
- A doubt nobody answered?"
review_case status-on-continuation pass "$(header spec approve)

1. A gap that runs over
   two lines. Fixed: closed."
review_case status-forms pass "$(header spec approve)

1. A gap. Fixed: closed.
2. A gap. Fixed; test.
3. A gap. Fixed in d6d2773f: closed.
4. A gap. Fixed when parsed; test.
5. Minor: a gap. fixed.
6. A gap. declined: the probe shows otherwise.
7. A gap. Declined: measured.

Questions:
- A doubt? open: not observed.
- A doubt? Verified: by the author.
- A doubt. became Design 3."
review_case fixed-in-description 'finding has no status' "$(header spec approve)

1. Minor: fixed timeouts are too short."
review_case fixed-in-passing 'finding has no status' "$(header spec approve)

1. The fixed timeout and its fixed-size buffer are wrong."
review_case verified-in-passing 'question has no status' "$(header spec approve)

Questions:
- Is the verified count right, as verified by nobody?"
review_case note-does-not-resolve 'finding has no status' "$(header spec approve)

1. A gap.
Note: fixed later."
review_case fence-in-review 'code fence in ## Review' "$good_review

\`\`\`text
$(header spec revise)

1. An unresolved finding.
\`\`\`"
new_case
printf '%s\n%s\n\n## Review\n\n%s\n' "$sections" '```text
Kind: spec
1. An example finding without a status.
```' "$good_review" >"$CASE/docs/superpowers/specs/$name"
pass fenced-example-outside-review
new_case
printf '%s\n```text\nKind: spec\n\n## Review\n\n%s\n' "$sections" "$good_review" >"$CASE/docs/superpowers/specs/$name"
reject unclosed-fence 'code fence opened here is never closed'

# fence_case <label> <fence lines placed in ## Design>: CommonMark pairing, so a conforming spec passes.
fence_case() {
  new_case
  printf '%s\n%s\n\n## Review\n\n%s\n' "$sections" "$2" "$good_review" >"$CASE/docs/superpowers/specs/$name"
  pass "$1"
}
fence_case longer-fence-holds-shorter '````md
```
Kind: spec
````'
fence_case tilde-fence-holds-backticks '~~~text
```
~~~'
fence_case info-string-does-not-close '```text
Kind: spec
```text
```'

# --- Lines that belong to no item

review_case glued-prose-in-header 'line after Verdict: in the block header' "$(header spec approve)
Sixth review (Copilot): one race, not addressed."
review_case glued-raw-finding-in-header 'raw reviewer finding' "$(header spec approve | sed 's/^Model: .*/Model: same family/')
F1 [major] [defect] race in the cache, not addressed"
review_case raw-finding-before-verdict 'raw reviewer finding' "$(header spec approve | sed 's/^Date: .*/Date: 2026-10-31\
F1 [major] race, not addressed/')"
review_case indented-under-questions 'indented line under Questions:' "$(header spec approve)

Questions:
  Is the cache racy? Nobody answered."
review_case note-mid-block 'after the closing Note:' "$(header spec approve)

1. A gap. Fixed.

Note: second review by Copilot found a race, not addressed.

2. Another gap. Fixed."
review_case impossible-date 'Date: "2026-13-45"' "$(header spec approve | sed 's/^Date: .*/Date: 2026-13-45/')"
review_case empty-verdict 'Verdict: is empty' "$(header spec approve | sed 's/^Verdict: .*/Verdict:/')"
review_case marker-word-prefix 'finding has no status' "$(header spec approve)

1. A gap. Fixedness matters."
review_case colon-marker-mid-clause 'question has no status' "$(header spec approve)

Questions:
- Should the doc say open: here?"

# --- The repo itself

for f in AGENTS.md docs/superpowers/review-prompt.md .github/pull_request_template.md; do
  grep -q 'verified:' "$f" || fail "$f no longer defines the verified: status the checker accepts"
done
grep -q 'go run hack/verify-specs.go' .github/workflows/ci.yml || fail "ci.yml does not run the spec gate"
grep -q 'hack/verify-specs-tests.sh' .github/workflows/ci.yml || fail "ci.yml does not run these tests"
grep -q 'go run hack/verify-specs.go' hack/ci-preflight.sh || fail "ci-preflight.sh does not run the spec gate"
grep -q 'hack/verify-specs-tests.sh' hack/ci-preflight.sh || fail "ci-preflight.sh does not run these tests"

echo "verify-specs gate regression tests passed"
