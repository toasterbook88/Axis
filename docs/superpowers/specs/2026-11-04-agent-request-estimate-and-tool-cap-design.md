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

## Design

1. **Whole-request overhead.** Build the turn context and tool definitions before the compaction
   check (moving the existing `agent.go:478` and `:489` lines above `:474`). Their size, as
   characters / 4, is passed to `compactContext`, which triggers on
   `conversation estimate + overhead ≥ maxTokens × 0.70`. To avoid summarizing every round when
   the fixed overhead alone is near the threshold, the trigger also requires the conversation
   part to exceed `maxTokens × 0.25`; the existing `minSummarizable` check still applies.
2. **Pre-send trim**, after compaction, toward a target of `maxTokens × 0.85` (headroom for the
   reply and chars/4 error):
   a. shorten older tool results with the existing `Conversation.compact`, against the target
      minus overhead instead of the full budget;
   b. if still over, shorten the tool results in the protected recent messages, largest first,
      until the request reaches the target or each is at the 2,048-byte floor. This bounds a round
      that returned several large results, which a per-result cap alone does not.
   The request is then sent, as today; nothing is refused.
3. **Cap every tool result on entry**, in bytes: `cap = min(32 KiB, maxTokens) bytes`, never below
   2,048. `read_file`'s own output (at most 32,000 bytes plus its 37-byte marker) always fits at the
   default budget. A longer result keeps its first `cap` bytes, cut at a UTF-8 rune boundary, plus
   a marker: `… [truncated: tool output was N bytes; the first M are kept]`. One helper applies the
   cap in `Conversation.Append` and when history is loaded (`LoadFromFile`, `RestoreAll`), so a
   resumed session, including one resumed with a smaller `--max-tokens`, is capped too.

The session log keeps recording each raw result before it enters the conversation, with its
own 16 KB clip (`sessionLogToolCap`) and `truncated_bytes`, as today.

## Tests

- Trigger: a conversation under today's threshold but over it once tool definitions and turn
  context are counted is compacted (fails today). A second case uses a large session summary in
  the turn context, so the test fails if only tool definitions are counted.
- No thrash: overhead alone above 70% with a short conversation does not summarize every round
  (count summarizer calls over 10 rounds).
- Pre-send trim: with overhead taking most of the budget, older tool results are shortened so
  the request reaches the 85% target; the request is still sent.
- Parallel round: 4 and 6 results of 40,000 bytes in one round, at `maxTokens` 32768 and 8192 —
  the next request estimate is at or below the target (fails with a per-result cap alone).
- Entry cap: a 10 MB `grep_search`-sized result is stored at the cap with the marker;
  `read_file`'s real output on a 40,000-byte file is unchanged at the default budget; at
  `maxTokens` 8192 the cap is 8,192 bytes; below 2,048 the floor applies; a multi-byte UTF-8
  result is cut on a rune boundary.
- Resume: a saved history whose newest tool result is 10 MB is capped on load; a history saved
  at 32768 and resumed at 8192 is re-capped.
- No wedge: after an oversized newest tool result, the next turn's request estimate is below the
  budget (fails today: the result sits whole in the protected tail).
- Characterization: a short conversation produces the same request as before.

## Alternatives

- **Cap each unbounded tool separately** (`grep_search`, `git_log`, MCP). Misses the next tool
  added without a cap; one cap at the conversation boundary covers all of them.
- **Split the cap across a round's results** (`cap / N` each). Also bounds a round, but cuts every
  result of a large round up front even when older history could have been shortened instead;
  the trim shortens the recent tail only as a last step.
- **The full budget design** (window discovery, calibration, refusal, retry). Parked for separate
  specs; this step fixes the measured wedge and most of the trigger gap without them.

## Research

- OpenCode compacts before the limit with a reserve, counts the full request, and shortens long
  tool output in the recent conversation it keeps (https://opencode.ai/v2/docs/compaction/). Here
  compaction triggers at 70% of `maxTokens` on the whole request, and the trim targets 85%, leaving
  15% for the reply and estimate error.
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
   results (4 x 40,000 chars reached 105% of budget). Fixed: pre-send trim step b shortens tool
   results in the protected tail, largest first, to the 85% target; parallel-round tests at
   32768 and 8192 (Design 2, Tests).
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
