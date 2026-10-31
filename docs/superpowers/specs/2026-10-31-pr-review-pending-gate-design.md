# `pr-review-cycle.sh` waits for requested reviews and fails while one is pending

Date: 2026-10-31. Base: `origin/main` 4b846e4. Status: revised after a `revise` spec review; awaiting
a second spec review.

## Problem

`hack/pr-review-cycle.sh` is the PR review loop `AGENTS.md` §3 points agents at. After
`gh pr checks --watch` reports required checks green, it sleeps a fixed `--wait-bots` interval
(default 120 s), counts unresolved review threads and requires a CLEAN merge state. It never asks
whether a requested reviewer has responded.

Measured over the 120 most recent merged PRs (GitHub GraphQL, 2026-10-31):

- 46 PRs had a review request, all for `copilot-pull-request-reviewer`, created by the repository
  ruleset `copilot_code_review` (`review_draft_pull_requests: true`, `review_on_push: false`), with
  no request-removal events.
- Counted from when `Test & Build` finished on the final head, which is when the script's wait
  starts, Copilot's review arrived more than 2 min later in 1 of the 46: #577, 6.7 min after CI.
  In the other 45 the fixed wait would have seen it. The problem is that tail case, not the common
  one.
- #577 is the one PR that merged while its requested review was pending: requested 19:11:23Z, CI
  finished 19:15:52Z, merged 19:22:23Z, Copilot posted five findings at 19:22:32Z. None was read.
  One still holds on `main` (`internal/llmrouter/probe.go:262` matches `coder:small` to
  `coder:large`). Whether that merge went through this script is not known, so this change prevents
  a repeat only for merges that run it.

## Non-goals

- Merging. The script stays read-only and never merges.
- Reviews nobody requested. The Grok gate's two post-merge reviews in the window came 97 and 155 min
  after CI, beyond any wait a script should impose. The current 120 s floor stays, so they lose
  nothing.
- Requesting or re-requesting reviews, or changing rulesets.
- Holding the rule for merges done without the script: that needs a required status check (see
  Alternatives) and a ruleset change by the maintainer.

## Design

1. **Pending reviewers.** A reviewer is pending when either holds:
   - it is in the PR's `reviewRequests` (`requestedReviewer` as `User`, `Bot` or `Team`), or
   - the timeline has a `ReviewRequestedEvent` for it with no later `PullRequestReview` by the same
     login and no later `ReviewRequestRemovedEvent` for it.

   The second rule covers a reviewer that drops off `reviewRequests` when it starts and before it
   submits, which other Copilot integrations report; it has not been observed on this repo. Logins
   compare after removing a `[bot]` suffix (on this repo the request and the review both use
   `copilot-pull-request-reviewer`). A team request is pending only while it is in
   `reviewRequests`. The data comes from its own GraphQL query, separate from the paginated thread
   query: `reviewRequests(first: 100)` and `timelineItems(last: 100, itemTypes: [REVIEW_REQUESTED_EVENT,
   REVIEW_REQUEST_REMOVED_EVENT, PULL_REQUEST_REVIEW])`.
2. **Floor, then poll.** Keep `--wait-bots SEC` as a minimum wait (default 120, unchanged). Then poll
   the pending set every 15 s until it is empty or `--wait-reviews SEC` has elapsed since the checks
   went green (default 1200, a margin over the 6.7 min worst case and the 16.4 min longest
   request-to-review time seen). `--wait-reviews 0` checks once.
3. **Fail closed on reads.** Every pending-set read is an assignment checked with `|| exit 1`, never a
   loop or `[[ ]]` condition, so a gh or GraphQL error, a rate limit or malformed JSON exits 1 instead
   of reading as "nothing pending".
4. **Exit 3 means one thing.** If reviewers are still pending when the wait ends, print each login
   with its request time and exit 3: "requested review still pending". The new code paths map every
   gh and jq failure to 1, so 3 is only ever returned with that list printed. Codes 0, 1 and 2 keep
   their meanings, and the existing pass-through of `gh` exit codes in the final re-read is unchanged.
5. **Re-check at enforcement.** The final re-read (head moved, merge state CLEAN) also re-reads the
   pending set, so a review requested during the run is not missed.
6. **Settle threads after a late review.** Threads are read after the pending set empties. If the
   newest review (`reviews(last: 1)`) was submitted less than 60 s before that read, read the threads
   once more after 10 s, because inline comments can reach `reviewThreads` after the review itself.
7. **Escape hatch.** `--allow-pending LOGIN` (repeatable) excludes a named reviewer that will not
   answer (quota, outage) and prints `waived pending review: LOGIN`. Under the every-PR rule in #586,
   the waiver and its reason go in the PR body.
8. **Docs.** `AGENTS.md` §3 lists exit code 3. #586 tells readers to check `gh pr view <PR> --json
   reviewRequests` in two places, AGENTS.md §8 and `.github/pull_request_template.md`; whichever of
   the two PRs merges second replaces both with "run `hack/pr-review-cycle.sh`", since that list alone
   is not proof.

## Tests

`hack/pr-review-cycle-tests.sh` keeps its `gh` shim and adds two things: a per-call fixture counter,
so successive GraphQL reads can differ, and a `sleep` shim on `PATH` that records calls instead of
sleeping. New scenarios:

- request in `reviewRequests`, `--wait-reviews 0` → exit 3, output names the reviewer;
- timeline request with no later review and not in `reviewRequests` → exit 3;
- request then a review by the same login, including a `[bot]` suffix difference → passes;
- request then a removal event → passes;
- team in `reviewRequests` → exit 3; team no longer requested → passes;
- pending on the first read, cleared on the second, `--wait-reviews 30` → exit 0, at least two
  pending reads, `sleep` called;
- still pending at the deadline → exit 3;
- pending set empty, then a request at the final re-read → exit 3;
- newest review under 60 s old → a second thread read is observed;
- pending read fails (`gh` exits non-zero) → exit 1; pending read returns malformed JSON → exit 1,
  never 3;
- `--allow-pending copilot-pull-request-reviewer` with Copilot pending → passes and prints the waiver;
- #577's real timeline as a fixture, evaluated at its merge time → Copilot pending;
- the existing clean, dirty, moved, failed_final, discovery and unresolved-thread cases keep their
  codes.

Plus unit checks of the pending-set function on fixture JSON, in the style of the existing
`count_unresolved_threads` check.

## Alternatives

| Option | Why not chosen now |
| --- | --- |
| Raise the fixed wait to 20 min | Waits 20 min on every run, including the 45 of 46 where the review is already in, and still passes when a review is slower. |
| Mergify `#review-requested=0` | The same rule, but a new hosted service and config to own for one condition. |
| Branch protection "require approvals" | Copilot reviews are comments, not approvals; it cannot express "the requested bot has reviewed". |
| Required status check: a workflow on `pull_request` and `pull_request_review` that fails while a request is pending | Holds for every merge, not only runs of this script. Needs a ruleset change by the maintainer. Recommended as the follow-up; it can reuse the pending-set function. |

## Research

- Mergify gates merges on `#review-requested=0`; a reviewer leaves `review-requested` once they
  review (<https://articles.mergify.com/merging-pull-requests-on-requested-reviewers-approvals/>,
  <https://docs.mergify.com/configuration/conditions>).
- A published PR-finishing workflow warns that an empty `reviewRequests` list is not proof: a
  reviewer that has started drops off it before submitting, so check the timeline too
  (<https://github.com/netresearch/git-workflow-skill/blob/main/commands/pr-finish.md>).
- A Copilot merge tool added an always-on "review not pending" gate after a merge landed 5 s before
  Copilot's inline comments, and retries when review threads lag the review
  (<https://github.com/Adamkadaban/copilot-second-opinion/commit/d679d9b5922e3f37ee4a4e195f0f4a058df179ca>).
- GitHub's ruleset can request Copilot review automatically, on drafts and optionally on each push
  (<https://docs.github.com/en/copilot/how-tos/copilot-on-github/set-up-copilot/configure-code-review>).

Optimization chosen: poll above the existing floor instead of lengthening the fixed wait. Runs whose
review is already in, 45 of 46 measured, wait the same 120 s as today; only the tail waits longer, and
only as long as its reviewer takes. Cost is one GraphQL query per 15 s poll while a review is pending.

## Success metric

This is a tripwire, not an improvement measure. At the baseline rate of 1 in 120, a window of 60
merged PRs shows 0 incidents with probability (119/120)^60 ≈ 0.61 even without this change, so 0
cannot prove it works. What is checked: any merged PR whose requested review was still pending at
merge is a defect to investigate, found with the timeline query above, run over each 60 merged PRs.
The #577 replay test is the evidence that the design catches the known case.

## Review

Kind: spec
Reviewer: fresh-context subagent (Cursor)
Model: same vendor family as the author. Claude Opus 5.5, the chosen second family, and GPT-5.6 Sol
were out of weekly usage; the maintainer chose a fresh same-model session.
Base: 4b846e44
Head: 52bc239b
Date: 2026-10-31
Verdict: revise

1. Major: removing the fixed sleep shortened the window for unrequested bots. Fixed: `--wait-bots`
   stays as a 120 s floor and polling runs on top (Design 2).
2. Major: no fail-closed rule for a failed pending read. Fixed: Design 3; tests for a failing and a
   malformed read.
3. Major: the tests could not fail if polling or the thread re-read broke. Fixed: per-call fixtures,
   a `sleep` shim and four new scenarios.
4. Minor: exit 3 could come from a jq error. Fixed: Design 4 maps tool failures in the new paths to 1;
   test that a malformed read never yields 3.
5. Minor: the problem compared the wrong intervals. Fixed: Problem restated from CI completion; the
   author re-measured it (1 of 46 late, #577, 6.7 min).
6. Minor: the success metric could not tell improvement from chance. Fixed: stated as a tripwire, with
   the #577 replay test as the evidence.
7. Minor: the #586 cleanup missed the PR template. Fixed: Design 8 names both places.

Questions:
- Was #577 merged through the script? Open: not known; Problem says the change only helps merges
  that run it, and Alternatives keeps the required check as the follow-up.
- Does Copilot drop off `reviewRequests` before submitting? Open: not observed on this repo (0 removal
  events in 46 requests); rule 2 is kept for that case at no cost.
- No escape hatch for a reviewer that never answers. Became Design 7.
- `--paginate` and the timeline in one query. Became Design 1: a separate, unpaginated query.
- `reviews(first: 50)` returns the oldest reviews. Became Design 6: `reviews(last: 1)`.
- p90 6.0 vs 7.9 min: different windows. Problem now uses latency from CI completion, which both
  windows agree on for the case that matters (#577 is the only late one).
