# Count the whole request when compacting, and cap tool results on entry

Date: 2026-11-04. Status: design, not approved. Nothing built.

A narrow first step. Window discovery, calibration and full-autonomy rules are separate, later
specs.

## Problem

Over 14 days of the private agent session log, 10 turns failed with the model server's
"request exceeds the available context size" error, and long tool output has no limit.
Verified at the base of this spec:

1. **The compaction trigger sees part of the request.** `compactContext` compares
   `a.conv.EstimateTokens()` with `maxTokens × 0.70` (`internal/agent/compaction.go:49`). That
   estimate is conversation characters / 4 (`internal/chat/message.go:112-128`). Each request also
   carries the tool definitions (`a.tools.Defs()`, `internal/agent/agent.go:478`; 2.5K–11.3K JSON
   characters depending on scope) and the per-turn context built at `agent.go:489` (session
   summary, git context, date, retrieved evidence, cluster snapshot). The trigger runs before
   either is built (`agent.go:474`), so it fires late or not at all.
2. **Some tool results are unbounded.** `read_file` (32,000 chars), shell (16,000 runes) and
   `web_fetch` cap themselves, but `grep_search`, `git_log` and MCP results do not
   (`internal/agent/tools.go:1088-1091`, `tools_git.go:111-124`, `tools_mcp.go:63-74`). A probe
   in review returned 10 MB from `grep_search`. `Conversation.compact` never shortens the last 4
   messages (`message.go:131-170`) and `compactContext` keeps the last 8, so one oversized result
   stays in every later request.

## Non-goals

- No window discovery, no `/props` with a key, no LiteLLM `/model/info`.
- No calibration of chars/4 against reported usage.
- No refusal and no overflow retry: requests are still sent as today.
- No `max_tokens` on requests, no change to `--max-tokens` or any flag, config key or state file.
- No full-autonomy or safety changes.
- Not bounding a round that returns several large results. The existing 4-message protect window
  in `Conversation.compact` is unchanged, so in a round with more than 4 results the older ones
  are still shortened to 180 characters when the conversation is over budget, as today. Bounding
  the whole round belongs to the later budget spec.
- No reply reserve: the pre-send check fills to `maxTokens`, as today's compaction does.
- No change to the footer or `/context` meters; they still show the conversation estimate.

## Design

1. **Whole-request overhead.** Build the turn context and tool definitions before the compaction
   check (moving the existing `agent.go:478` and `:489` lines above `:474`). Their size, as
   characters / 4, is passed to `compactContext`, which triggers on
   `conversation estimate + overhead ≥ maxTokens × 0.70`. To avoid a summarizer call every round
   when the fixed part is large, the trigger also requires the summarizable part — the chars / 4
   of `SummarizableMessages(protectLastTurns)`, which excludes system messages and the protected
   last 8 — to exceed `maxTokens × 0.25`. The existing `minSummarizable` check still applies.
   (OpenCode's compaction docs make the same point: compaction "cannot create room when a request
   is mostly fixed instructions and tool schemas".)
2. **Pre-send check.** After compaction, if `conversation estimate + overhead > maxTokens`, run the
   existing tool-result shortening (`Conversation.compact`) against `maxTokens × 4 − overhead`
   characters instead of `maxTokens × 4`. It changes the stored conversation, as the existing
   compaction does. The request is then sent, as today; nothing is refused.
3. **Cap every tool result on entry**, in bytes: `cap = min(32 KiB, maxTokens) bytes`, never below
   2,048. Valid UTF-8 `read_file` output (at most 32,000 runes of text, about 32,037 bytes for
   ASCII) fits at the default budget; Latin-1 or binary files, which `read_file` expands to U+FFFD,
   can exceed it and get the entry-cap marker. A longer result keeps its first `cap` bytes, cut at a
   UTF-8 rune boundary, plus
   a marker: `… [truncated: tool output was N bytes; the first M are kept]`. One helper applies the
   cap in `Conversation.Append` and when history is loaded (`LoadFromFile`, `RestoreAll`), so a
   resumed session, including one resumed with a smaller `--max-tokens`, is capped too.

The session log keeps recording each raw result before it enters the conversation, with its
own 16 KB clip (`sessionLogToolCap`) and `truncated_bytes`, as today.

## Tests

- Trigger: a conversation under today's threshold but over it once tool definitions and turn
  context are counted is compacted (fails today). A second case uses a large session summary in
  the turn context, so the test fails if only tool definitions are counted.
- No thrash, using `New()`'s real system prompt: at `maxTokens` 8192 in edit scope with a short
  conversation, 10 rounds make 0 summarizer calls; at 32768 with a 40% tool set and one capped
  result in the protected tail, 0 calls until the summarizable part passes 25%.
- Pre-send check: with overhead taking most of the budget, older tool results are shortened so
  conversation + overhead fits `maxTokens`; the request is still sent.
- Entry cap: a 10 MB `grep_search`-sized result is stored at the cap with the marker; ASCII
  `read_file` output on a 40,000-byte file is unchanged at the default budget, and a Latin-1 one
  gets the entry-cap marker; at `maxTokens` 8192 the cap is 8,192 bytes; below 2,048 the floor
  applies; a multi-byte UTF-8 result is cut on a rune boundary.
- Resume: a saved history whose newest tool result is 10 MB is capped on load; a history saved
  at 32768 and resumed at 8192 is re-capped.
- No wedge: after an oversized newest tool result, the next turn's request estimate is below the
  budget (fails today: the result sits whole in the protected tail).
- Characterization: a short conversation produces the same request as before.

## Alternatives

- **Cap each unbounded tool separately** (`grep_search`, `git_log`, MCP). Misses the next tool
  added without a cap; one cap at the conversation boundary covers all of them.
- **Trim the protected tail before sending** (review 1's suggestion, tried in revision 2). It
  bounds a many-result round but splits the round across the 4-message protect window and needs
  its own spread rule; review 2 showed both problems, so it moves to the later budget spec.
- **The full budget design** (window discovery, calibration, refusal, retry). Parked for separate
  specs; this step fixes the measured wedge and most of the trigger gap without them.

## Research

- OpenCode compacts before the limit, counts the full request, and notes that compaction cannot
  make room when a request is mostly fixed instructions and tool schemas
  (https://opencode.ai/v2/docs/compaction/). This step adopts the first two and the guard the third
  implies; its reserve (15%, at least 16K) belongs to the later budget spec.
- Optimization: none beyond counting what is sent. Measurement: the trigger and wedge tests above,
  and over the next 14 days of logs, fewer context-overflow errors with no rise in non-answer turns.

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
- Does a request trimmed to exactly the budget leave room for the reply? Became Design 2: the trim
  targets 85% of `maxTokens`.
- Does overhead alone above 70% make compaction run every round? Became Design 1: the trigger also
  requires the conversation part above 25%, with a no-thrash test.
- Were the logged failures one oversized result or several per round? Open: the session log is
  private; the design covers both cases.
- Can a trigger test pass when only tool definitions are counted? Became a test: a second trigger
  case uses a large session summary.

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
