# `pr-review-cycle.sh` waits for requested reviews and fails while one is pending

Date: 2026-10-31. Base: `origin/main` 4b846e4. Status: draft, awaiting spec review.

## Problem

`hack/pr-review-cycle.sh` is the PR review loop `AGENTS.md` §3 points agents at. After required
checks pass it sleeps a fixed `--wait-bots` interval (default 120 s), then counts unresolved review
threads and requires a CLEAN merge state. It never asks whether a requested reviewer has responded.

Measured over the last 120 merged PRs (GitHub GraphQL timeline, 2026-10-31):

- 46 PRs had a review request; all 46 requests were for `copilot-pull-request-reviewer`, created by
  the repository ruleset `copilot_code_review` (`review_draft_pull_requests: true`,
  `review_on_push: false`). There were no request-removal events.
- Time from request to first review: median 2.4 min, p90 6.0 min, max 16.4 min. The fixed 120 s wait
  is shorter than the median, so the script usually finishes before the requested review exists.
- One PR merged while its requested review was pending: #577 merged at 19:22:23Z; Copilot, requested
  at 19:11:23Z, posted five findings at 19:22:32Z. None was read. One still holds on `main`
  (`internal/llmrouter/probe.go:262` matches `coder:small` to `coder:large`).

## Non-goals

- Merging. The script stays read-only and never merges.
- Reviews nobody requested, such as the Grok gate's review comments (two of the three post-merge
  reviews in the window were these). A request-based check cannot see them.
- Requesting or re-requesting reviews, or changing rulesets.
- Making the rule hold for merges done without the script. That needs a required status check (see
  Alternatives) and the maintainer's ruleset change.

## Design

1. **Pending reviewers.** A reviewer is pending when either holds:
   - it is in the PR's `reviewRequests` (GraphQL; `requestedReviewer` as `User`, `Bot` or `Team`), or
   - the timeline has a `ReviewRequestedEvent` for it with no later `PullRequestReview` by the same
     login and no later `ReviewRequestRemovedEvent` for it.

   The second rule covers a reviewer that drops off `reviewRequests` when it starts but before it
   submits, which other Copilot integrations report. Logins compare after removing a `[bot]`
   suffix. A team request counts as pending only while it is in `reviewRequests`, since any member's
   review answers it.
2. **Wait instead of sleep.** The fixed `sleep "$WAIT_BOTS"` becomes a poll: every 15 s, re-read the
   pending set until it is empty or `--wait-bots SEC` has elapsed. The default rises from 120 to 1200
   (20 min), above the 16.4 min maximum observed. `--wait-bots 0` checks once without waiting.
3. **Fail closed.** If reviewers are still pending when the wait ends, print each login with its
   request time and exit 3, a new code: "requested review still pending". Codes 0, 1 and 2 keep their
   meanings. The enforcement re-read at the end (head moved, merge state CLEAN) also re-checks the
   pending set, so a review requested during the run is not missed.
4. **Settle threads after a late review.** Review threads are read after the pending set empties. If
   the newest review was submitted less than 60 s before that read, read the threads once more after
   10 s, because inline comments can reach `reviewThreads` after the review itself.
5. **Docs.** `AGENTS.md` §3 describes the new exit code. The sentence in §8 (proposed in #586) that
   says the script does not check pending requests is removed by whichever of the two PRs merges
   second.

## Tests

`hack/pr-review-cycle-tests.sh` keeps its `gh` shim and gains scenarios, each run with
`--wait-bots 0`:

- request in `reviewRequests` → exit 3, output names the reviewer;
- timeline request with no later review and not in `reviewRequests` → exit 3;
- request followed by a review from the same login, including the `[bot]` suffix difference → passes;
- request followed by a removal event → passes;
- team request in `reviewRequests` → exit 3; team no longer requested → passes;
- pending set empty but a request appears at the final re-read → exit 3;
- the existing clean, dirty, moved, failed_final, discovery and unresolved-thread cases keep their codes.

Plus a unit check of the pending-set function on fixture JSON, in the style of the existing
`count_unresolved_threads` check.

## Alternatives

| Option | Why not chosen now |
| --- | --- |
| Keep the fixed wait, raise it to 20 min | Waits 20 min on every PR, including PRs with no request, and still passes when a review is slower. |
| Mergify `#review-requested=0` | Same rule, but a new hosted service and config to own for one condition. |
| Branch protection "require approvals" | Copilot reviews are comments, not approvals; it cannot express "the requested bot has reviewed". |
| Required status check: a workflow on `pull_request` and `pull_request_review` that fails while a request is pending | Enforces the rule for every merge, not only for agents running the script. Needs a ruleset change by the maintainer. Recommended as the follow-up; it can reuse this pending-set logic. |

## Research

- Mergify gates merges on `#review-requested=0`, noting that a reviewer leaves `review-requested`
  once they review (<https://articles.mergify.com/merging-pull-requests-on-requested-reviewers-approvals/>,
  <https://docs.mergify.com/configuration/conditions>).
- A published PR-finishing workflow warns that an empty `reviewRequests` list is not proof: a
  reviewer that has started drops off it before submitting, so check the timeline too
  (<https://github.com/netresearch/git-workflow-skill/blob/main/commands/pr-finish.md>).
- A Copilot merge tool added an always-on "review not pending" gate after a merge landed 5 s before
  Copilot's inline comments, and retries when review threads lag the review
  (<https://github.com/Adamkadaban/copilot-second-opinion/commit/d679d9b5922e3f37ee4a4e195f0f4a058df179ca>).
- GitHub's ruleset can request Copilot review automatically, on drafts and optionally on each push
  (<https://docs.github.com/en/copilot/how-tos/copilot-on-github/set-up-copilot/configure-code-review>).

Optimization chosen: poll for pending reviews instead of sleeping a fixed time. On the measured
latencies, a PR with a request waits about as long as its reviewer takes (median 2.4 min) instead of
a fixed 2 min that is usually too short; a PR with no request waits not at all. Cost is one GraphQL
query per 15 s poll.

## Success metric

PRs merged while a requested review was pending, over the next 60 merged PRs: 0 (baseline 1 in 120).
Measured with the same timeline query used above.
