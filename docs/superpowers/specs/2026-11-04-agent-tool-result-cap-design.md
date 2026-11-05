# Cap every tool result when it enters the conversation

Date: 2026-11-04. Status: design, not approved. Nothing built.

A narrow first step. Counting the whole request (tool definitions, turn context) in the compaction
trigger, window discovery, calibration and full-autonomy rules are separate, later specs.

## Problem

Over 14 days of the private agent session log, 10 turns failed with the model server's
"request exceeds the available context size" error. One cause is verified at the base of this
spec: some tool results have no size limit.

- `read_file` (32,000 chars) and shell (16,000 runes) cap themselves. `web_fetch` caps at its
  `max_chars` argument, which the model chooses and which has no upper bound
  (`internal/agent/tools_p1.go:195-225`). `grep_search`, `git_log` and MCP results do not cap at
  all (`internal/agent/tools.go:1088-1091`, `tools_git.go:111-124`, `tools_mcp.go:63-74`). A probe
  in review returned 10 MB from `grep_search`.
- `Conversation.compact` never shortens the last 4 messages (`internal/chat/message.go:131-170`)
  and `compactContext` keeps the last 8, so one oversized result stays in every later request and
  the session is stuck until it is cleared. Resuming it (`LoadFromFile`) or rolling back to a
  snapshot (`RestoreAll`) brings the oversized result back.

## Non-goals

- No change to the compaction trigger or its estimate; tool definitions and turn context are
  still not counted. That moves to the later budget spec with window discovery, where small
  budgets with a large fixed prompt (reviews 3 F1-F3) are handled together.
- No pre-send check, no refusal and no overflow retry: requests are sent as today.
- No new flag, default, config key or state file; `--max-tokens` keeps its meaning.
- Not bounding a round that returns several large results; the 4-message protect window is
  unchanged (reviews 1 and 2).
- No guarantee at small budgets: at `--max-tokens` 8192 in full scope the fixed prompt alone is
  about 81% of the budget (review 3 F3), so a capped result can still overflow. The claim is for
  the default 32768.
- No change to the session log, footer or `/context` meters.

## Design

One helper, `capToolResult(content string, cap int) string`, in `internal/chat`.

- `cap = min(32 KiB, maxTokens)` bytes, never below 2,048. A conversation built with
  `maxTokens` 0 (truncation disabled) uses 32 KiB.
- A result of at most `cap` bytes is unchanged. Valid UTF-8 `read_file` output (at most 32,000
  runes, about 32,037 bytes for ASCII) fits at the default budget.
- A longer result becomes its first bytes, cut at a UTF-8 rune boundary, then `\n` and the marker
  `… [truncated: tool output was N bytes; the first M are kept]`. The whole result, marker
  included, is at most `cap` bytes.
- Idempotent: a result that already ends with the marker keeps the marker's original N when it is
  cut again (a history saved at 32768 and resumed at 8192), and a capped result passed through the
  helper again is unchanged.
- Applied to `RoleTool` messages in `Conversation.Append`, `LoadFromFile` and `RestoreAll`. Other
  roles are untouched.

The session log keeps recording each raw result before it enters the conversation, with its own
16 KB clip, as today.

## Tests

All in `internal/chat`; each fails today.

- Entry cap: a 10 MB result (the `grep_search` probe size, and `web_fetch` with a large
  `max_chars`) is stored within the cap with the marker; 20,000 bytes and ASCII `read_file`
  output of 32,037 bytes are unchanged at the default budget; at `maxTokens` 8192 the cap is
  8,192 bytes; below 2,048 the floor applies; a multi-byte UTF-8 result is cut on a rune
  boundary; assistant messages are not capped.
- Idempotent: capping a capped result returns it unchanged; re-capping at a smaller cap keeps
  the original N.
- Resume and rollback: a saved history whose newest tool result is 10 MB is capped on load, a
  history resumed at a smaller budget is re-capped, and `RestoreAll` caps.
- No wedge at the default budget: after a 10 MB newest tool result, the conversation estimate is
  under 32768.

## Alternatives

- **Cap each unbounded tool separately** (`grep_search`, `git_log`, MCP, `web_fetch`). Misses the
  next tool added without a cap; one cap at the conversation boundary covers all of them.
- **Also count the whole request in the trigger and add a pre-send check** (revisions 1-3).
  Reviews 2 and 3 showed its guard can block compactions that work today, its pre-send budget
  mixed units, and it cannot help when the fixed prompt fills most of a small budget. Moved to
  the later budget spec.
- **Trim the protected tail before sending** (revision 2). Splits the newest round across the
  protect window (review 2); moved to the later budget spec.

## Research

- OpenCode shortens long tool output in the recent conversation it keeps after compaction, and
  keeps earlier messages stored even when they are no longer sent
  (https://opencode.ai/v2/docs/compaction/). This step shortens a long result earlier, when it
  enters the conversation, so the protected tail never holds it whole; the session log keeps the
  raw output, as OpenCode keeps stored messages.
- Optimization: none beyond bounding each result. Measurement: the no-wedge and resume tests
  above, and over the next 14 days of logs, fewer context-overflow errors at the default budget.

## Review

Kind: spec
Reviewer: fresh-context subagent (Claude Code)
Model: Anthropic Claude (same family as the author; fresh session)
Base: 6cc8d3f8fe2de1d5269239d3179180910124796c
Head: 1bf888f5a088adb91ec52d1fdbff3399378d4545
Date: 2026-11-04
Verdict: revise

1. Major: a per-result cap plus the protected tail does not bound a round with several large
   results (4 x 40,000 chars reached 105% of budget). Declined: a tail trim was added in revision
   2, but review 2 (items 2-4, probe TestReviewProbeNewestRoundSplitByCompactWindow) showed it
   splits rounds across the protect window and cannot meet its own 8192 test; it is now a
   non-goal for the later budget spec.
2. Major: resuming a session skips the entry cap. Fixed: one helper caps in `Append`,
   `LoadFromFile` and `RestoreAll`; resume tests for a 10 MB result and a smaller budget.
3. Minor: the cap's unit was unstated and `read_file`'s real output exceeds 32,000. Fixed: the cap
   is bytes, 32 KiB, rune-boundary cut, floor 2,048; the unreachable `maxTokens` 0 clause is
   removed; tests use real `read_file` output, 8192 and the floor.
4. Minor: the session log clips tool output at 16 KB, not 4 KB. Fixed: wording corrected.

Questions:
- Does a request trimmed to exactly the budget leave room for the reply? Open: revision 2
  trimmed to 85% of `maxTokens`; revision 3 removed the trim, and a reply reserve is the later
  budget spec (review 2 item 2).
- Does overhead alone above 70% make compaction run every round? Open: revision 2 added a 25%
  guard; revision 4 removes the trigger change, so the question moves to the later budget spec.
- Were the logged failures one oversized result or several per round? Open: the session log is
  private; the design covers both cases.
- Can a trigger test pass when only tool definitions are counted? Open: the trigger change is
  removed in revision 4 and moves to the later budget spec.

Kind: spec
Reviewer: fresh-context subagent (Claude Code)
Model: Anthropic Claude (same family as the author; fresh session)
Base: 6cc8d3f8fe2de1d5269239d3179180910124796c
Head: 3659aa950b5dd257e1c05d2c213fb87ebde256b5
Date: 2026-11-04
Verdict: revise

1. Major: the no-thrash guard counted system messages and the protected tail, which compaction
   cannot remove (7 summarizer calls in 10 rounds at 8192). Fixed: the guard uses the
   summarizable part only; no-thrash tests use `New()`'s real system prompt (Design 1, Tests).
2. Major: the 4-message protect window splits the newest round, so its earlier results drop to
   180 chars before the tail trim. Declined: the tail trim (step b) is removed; the existing
   protect window is unchanged and listed as a non-goal, per the reviewer's probe
   TestReviewProbeNewestRoundSplitByCompactWindow.
3. Minor: "largest first" did not define the spread. Fixed: step b removed with item 2.
4. Minor: the 8192 parallel-round assertion could not be met with the real prompt. Fixed: that
   test is removed with step b.
5. Minor: "read_file always fits" is false for Latin-1 and binary files. Fixed: reworded to valid
   UTF-8, with a Latin-1 test that expects the entry-cap marker.
6. Minor: the footer and `/context` keep the conversation-only estimate. Declined: listed as a
   non-goal for this step; reviewer's static reading at `agent.go:633-637` supports it.
7. Minor: the cited research does not back a 15% reserve. Fixed: the reserve claim is removed and
   the research note now cites OpenCode's fixed-instructions limit behind the guard.

Questions:
- Were the 10 overflows caused by `--max-tokens` above the served window? Open: needs the private
  session log's provenance; window discovery is the later spec.
- How far off is chars/4 for this request mix? Open: calibration is the later spec.
- Does the pre-send check change the stored conversation? Verified: Design 2 now says it does, as
  the existing compaction does.

Kind: spec
Reviewer: fresh-context subagent (Claude Code)
Model: Anthropic Claude (same family as the author; fresh session)
Base: 6cc8d3f8fe2de1d5269239d3179180910124796c
Head: 4097ebe62f576934ddb1971b0064fb85a3642e05
Date: 2026-11-04
Verdict: revise

1. Major: the 25% guard blocks compactions that today's trigger makes, so at 8192 in edit scope a
   turn that fits today is sent over budget. Declined: revision 4 removes the trigger change; the
   trigger is today's, per the reviewer's 8192 edit-scope probe; moved to the later budget spec.
2. Major: the pre-send budget `maxTokens × 4 − overhead` subtracts tokens from characters.
   Declined: revision 4 removes the pre-send check; moved to the later budget spec.
3. Major: at 8192 in full or edit scope a result at the cap still overflows, because the fixed
   prompt is about 81% of the budget. Declined: listed as a non-goal; the no-wedge test is pinned
   to the default 32768, per the reviewer's 8192 full-scope measurement.
4. Minor: the cap helper is not idempotent; re-capping rewrites the marker with the wrong N.
   Fixed: the marker fits inside the cap and keeps the original N; idempotence tests added.
5. Minor: `web_fetch` is also unbounded through `max_chars`. Fixed: listed in Problem and covered
   by the 10 MB entry-cap test.
6. Minor: review 1's question record still says the trim targets 85%. Fixed: restated as declined.
7. Minor: `--max-tokens` help text and the Config comment would no longer match. Declined: with the
   estimate change removed, `--max-tokens` keeps its meaning; non-goal reworded to "no new flag,
   default, config key or state file".

Questions:
- What caused the 10 logged overflows? Open: needs the private session log's provenance; window
  discovery is the later spec.
- Can the session summary alone exceed the guard? Open: the guard is removed in revision 4.
- How large is the fixed prompt with MCP tools loaded? Open: measured in the later budget spec.
