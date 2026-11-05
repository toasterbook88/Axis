# Fit every agent request to the model's context window

Date: 2026-11-05. Status: draft, awaiting spec review.

Follows PR #614 (tool-result entry cap), which bounded single results and left the logged
overflows to this spec.

## Problem

Over 14 days of the private agent session log, 10 turns failed with the model server's
"request (N tokens) exceeds the available context size (32768 tokens)" error. In every one the
window was 32,768, equal to the default `--max-tokens`, and the overshoot was 61 to 3,167
tokens. Seven of the ten failed on the first request of a prompt.

The cause, in code:

- `chat.NewConversation(maxTokens)` budgets `maxTokens × 4` characters of **messages only**
  (`internal/chat/message.go`). Each request also carries the tool definitions and the per-turn
  context (session summary, git context, date, evidence, cluster snapshot;
  `internal/agent/agent.go`, `injectTurnContext`), which nothing counts.
- LLM compaction (`internal/agent/compaction.go`) triggers on the same messages-only estimate
  at 70% of `maxTokens`.
- `--max-tokens` defaults to 32,768. It is not compared with the served window, so on a 32K
  model the messages alone may fill the window before the uncounted part is added.
- When the server rejects a request as too long, the turn fails. Nothing compacts and retries.

Measured fixed overhead per request, from `New()` with the built-in tools (spike, not merged):

| Scope | System prompt | Tools | Tool definitions | Total fixed |
| --- | --- | --- | --- | --- |
| cluster | 14,720 chars | 5 | 2,512 bytes | ≈ 4,330 tokens |
| observe (default) | 14,896 chars | 13 | 6,565 bytes | ≈ 5,390 tokens |
| edit | 14,809 chars | 18 | 9,466 bytes | ≈ 6,090 tokens |
| exec (full) | 14,793 chars | 20 | 11,341 bytes | ≈ 6,560 tokens |

The system prompt is already in the conversation and counted. The tool definitions (≈ 630 to
2,840 tokens) plus the turn context are not, which matches the logged overshoot. MCP tools add
more definitions on top.

The window is already known for llama.cpp servers: fact collection reads
`GET /props default_generation_settings.n_ctx` into the model catalog's `ContextWindow`
(`internal/facts/tools.go`, `internal/modelinventory/catalog.go`). The agent's model picker
reads catalog entries (`cmd/axis/agent_model_choices.go`) but does not copy that field into
`agent.ModelTarget`. All 10 logged requests went through an OpenAI-compatible proxy, where the
catalog may not know the window. The server's error text states it.

## Non-goals

- No new flag, config key, state file or session-log field. `--max-tokens` keeps its meaning
  as the operator's ceiling; this spec never raises the budget above it.
- No tokenizer. Estimates stay at 4 characters per token. Calibration against a served
  tokenizer (llama.cpp `/tokenize`) is later work; the reserve and the overflow retry absorb the
  error.
- No change to the #614 tool-result cap; it stays `maxTokens` bytes.
- No change to the summarization prompt, `protectLastTurns` or `minSummarizable`.
- No guarantee when the fixed part alone exceeds the usable budget (for example 8,192 in exec
  scope with MCP tools). Compaction cannot make room there. The agent warns once and sends,
  and the server stays the authority.

## Design

1. **Window.** `agent.ModelTarget` gains `ContextWindow int`, copied from the catalog entry
   (0 = unknown). The agent holds the active target's window, set at startup and on every
   model switch beside `SetBackend`, and a **learned window** that starts at 0 and is cleared on
   a model switch. Subagents inherit both.
2. **Budget.** `B = min(maxTokens, W)`, where `W` is the target window, else the learned window,
   else `maxTokens`. Usable `U = B − R`, with the reply reserve `R = min(4096, B / 8)`
   (4,096 at 32,768; 1,024 at 8,192). The conversation's own character cap stays
   `maxTokens × 4`.
3. **Request estimate.** `E = (conversation chars + JSON bytes of the tool definitions sent +
   turn-context chars) / 4`. Everything is counted in characters and divided once, so the units
   match (spec review 2 of #614, F2). The same function measures what `ChatStream` is given.
4. **Trigger.** LLM compaction runs when `E ≥ 0.70 × U`, and only when the summarizable
   messages hold at least `0.10 × U` tokens. Below that, summarizing frees too little, and with
   a large fixed part it would run every round (#614 review 1 question).
5. **Pre-send check.** After compaction, if `E > U`, the existing tool-result truncation
   (`compactOldest`, the newest 4 messages protected) runs until `E ≤ U` or nothing is left to
   truncate. It changes the stored conversation, as compaction already does. If `E > U`
   remains, the request is sent unchanged, and a one-time `compaction skipped` event names the
   fixed part and `U`.
6. **Overflow recovery.** When `ChatStream` fails with HTTP 400 and a known context-overflow
   message (`exceeds the available context size`, `context_length_exceeded`,
   `maximum context length`, `ContextWindowExceededError`), the agent:
   - parses `context size (N tokens)` or `maximum context length is N` if present and sets the
     learned window to `N`, only if `N` is below the current `B` (never raises);
   - forces one compaction (ignoring the trigger in 4) and the pre-send check against the new
     `U`;
   - retries the request once.

   A second overflow, or any other error, fails the turn as today, and the first-request prompt
   is dropped only then. No other error is retried.
7. **Display.** `/context` and the footer show `E` against `U`, not conversation-only tokens
   against `maxTokens`.

## Tests

- `ModelTarget` copies `ContextWindow` from a catalog entry with `n_ctx`, and is 0 without one.
- Budget: `--max-tokens` 32768 with a 16,384 window gives `U` = 14,336; with no window it gives
  28,672; a learned window of 20,000 never rises on a later error that states 40,000.
- Estimate counts tool definitions and turn context: the same conversation in exec scope
  estimates higher than in cluster scope, by the definitions' JSON size / 4.
- No overflow at the default budget: in exec scope, a fake backend records each request's
  estimate across 30 rounds of 6 KB tool results, and none exceeds `U`.
- Overflow recovery: a fake backend that returns the logged error text (the window stated) once
  is called twice and the turn succeeds; the learned window is 32,768. If it returns it twice,
  the turn fails after exactly 2 calls. A non-overflow 400 is called once.
- No thrash: at `--max-tokens` 8192 in exec scope, the summarizer is not called while the
  summarizable part is under `0.10 × U`, and the warning event fires once per session.
- A model switch clears the learned window and takes the new target's window.

## Alternatives

- **Lower the default `--max-tokens`** (e.g. 28,000). One line, but it is wrong for every other
  window, and it still ignores definitions that grow with MCP tools.
- **Count with the server's tokenizer** (`/tokenize`). Exact for llama.cpp, but it adds a round
  trip per request and proxies don't offer it. Kept for calibration later.
- **Retry only** (step 6 alone). It recovers every logged case, because each error stated the
  window. But it costs a failed request each time on known windows, and cloud providers bill
  failed prompt tokens.
- **Proxy-side fallbacks** (`context_window_fallbacks` in the proxy). Outside AXIS, and they
  change the model.

## Research

- OpenCode compacts when the conversation reaches the window minus a kept-free buffer (15%, at
  least 16k), recovers once when a provider rejects a request as too long, and fails on a
  second rejection. It also states that compaction "cannot create room when a request is mostly
  fixed instructions and tool schemas" (https://opencode.ai/v2/docs/compaction/). This spec
  takes the same shape. Its buffer is sized for large windows (16k is half of a 32K window), so
  the reserve here is measured instead.
- Reply reserve measurement: over the same 14 days, 324 answers were p50 334, p95 2,799 and
  max 6,761 characters (≈ 1,690 tokens). 4,096 tokens covers the longest answer plus
  tool-call arguments, and it matches the cloud backend's `max_tokens` of 4,096
  (`internal/agent/cloud_backend.go`).
- Optimization: none beyond counting what is sent. Measurement: the tests above. After install,
  over the next 14 days of logs: no turn fails with a context-overflow error that states a
  window, and the fixed-part warning count per scope.

## Review
