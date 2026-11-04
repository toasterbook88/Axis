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
   `conversation estimate + overhead ≥ maxTokens × 0.70`.
2. **Pre-send trim.** After compaction, if `conversation estimate + overhead > maxTokens`, run the
   existing tool-result shortening (`Conversation.compact`) against `maxTokens × 4 − overhead
   characters` instead of the full budget. The request is then sent, as today.
3. **Cap every tool result on entry.** `Conversation.Append` shortens a `tool` message longer than
   the cap to the cap, with a marker:
   `… [truncated: tool output was N chars; the first M are kept]`. Cap = 32,000 characters
   (`read_file`'s own limit, so its results never change), lowered to `maxTokens` characters
   when that is smaller, never below 2,000. It applies to every tool, built-ins and MCP alike,
   and also when truncation is otherwise disabled (`maxTokens` 0).

The session log keeps recording each raw result before it enters the conversation, with its
own 4 KB clip and `truncated_bytes`, as today.

## Tests

- Trigger: a conversation under today's threshold but over it once tool definitions and turn
  context are counted is compacted (fails today).
- Pre-send trim: with overhead taking most of the budget, older tool results are shortened so
  conversation + overhead fits; the request is still sent.
- Entry cap: a 10 MB `grep_search`-sized result is stored at the cap with the marker; a
  20,000-char result and a 32,000-char `read_file` result are unchanged at the default budget.
- No wedge: after an oversized newest tool result, the next turn's request estimate is below the
  budget (fails today: the result sits whole in the protected tail).
- Characterization: a short conversation produces the same request as before.

## Alternatives

- **Cap each unbounded tool separately** (`grep_search`, `git_log`, MCP). Misses the next tool
  added without a cap; one cap at the conversation boundary covers all of them.
- **Compact the protected tail.** Changes what the model sees of its most recent work; capping on
  entry keeps the tail intact and bounded.
- **The full budget design** (window discovery, calibration, refusal, retry). Parked for separate
  specs; this step fixes the measured wedge and most of the trigger gap without them.

## Research

- OpenCode compacts before the limit with a reserve, and counts the full request
  (https://opencode.ai/v2/docs/compaction/). Here the reserve is the existing 30% headroom below
  `maxTokens`; this change makes that headroom measure the whole request.
- Optimization: none beyond counting what is sent. Measurement: the trigger and wedge tests above,
  and over the next 14 days of logs, fewer context-overflow errors with no rise in non-answer turns.

## Review
