# Fit every agent request to the model's context window

Date: 2026-11-05. Status: revision 4, implemented on this branch; spec review 2 and diff reviews A
and B approved with changes, all findings resolved.

Follows PR #614 (tool-result entry cap), which bounded single results and left the logged
overflows to this spec.

## Problem

Over 14 days of the private agent session log, 10 turns failed with the model server's
"request (M tokens) exceeds the available context size (32768 tokens)" error. In every one the
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

Measured fixed overhead per request, from `New()` with the built-in tools and no MCP servers
(spike, not merged; reproduced by spec review 1):

| Scope | System prompt | Tools | Tool definitions | Total fixed |
| --- | --- | --- | --- | --- |
| cluster | 14,720 chars | 5 | 2,512 bytes | ≈ 4,310 tokens |
| observe (default) | 14,896 chars | 13 | 6,565 bytes | ≈ 5,370 tokens |
| edit | 14,809 chars | 18 | 9,466 bytes | ≈ 6,070 tokens |
| exec (full) | 14,793 chars | 20 | 11,341 bytes | ≈ 6,530 tokens |

The system prompt includes the working directory's `AGENTS.md` (this repo's is 12,708 bytes);
the 32 KiB cap on repo instructions (`internal/agent/repo_instructions.go`) allows about 4,900
more tokens. MCP tools add definitions on top; their size depends on the servers configured and
is not measured here (Questions).

The system prompt is already in the conversation and counted. The tool definitions (≈ 630 to
2,840 tokens) plus the turn context are not, which matches the logged overshoot.

The window is already known for llama.cpp servers: fact collection reads
`GET /props default_generation_settings.n_ctx` into the model catalog's `ContextWindow`
(`internal/facts/tools.go`, `internal/modelinventory/catalog.go`). The agent's model picker
reads catalog entries (`cmd/axis/agent_model_choices.go`) but does not copy that field into
`agent.ModelTarget`. Only llama.cpp entries without an API key and Apple models carry a window;
Ollama entries, `ai.yaml` roles and cloud providers do not. All 10 logged requests went through
an OpenAI-compatible proxy, where the catalog may not know the window. The server's error text
states both the window and the request size.

## Non-goals

- No new flag, config key, state file or session-log field. `--max-tokens` keeps its meaning
  as the operator's ceiling; this spec never raises the budget above it.
- No tokenizer. Estimates stay at 4 characters per token, corrected by the factor learned from
  an overflow error (Design 6). Calibration against a served tokenizer is later work.
- No change to the #614 tool-result cap; it stays `maxTokens` bytes.
- No change to the summarization prompt, `protectLastTurns`, or the newest-4 protection in
  `compactOldest`.
- No guarantee when the fixed part plus the protected newest messages exceed the usable budget
  (for example 8,192 in exec scope, where the fixed part is about 6,530 of `U` = 7,168 and one
  round of results fills the rest). Compaction cannot make room there. The agent warns once and
  sends, and the server stays the authority.
- No overflow detection on the native Ollama client: it discards the error body
  (`internal/chat/client.go`), and Ollama may truncate silently instead. The proactive budget
  (Design 2-5) still applies there.

## Design

1. **Window.** `agent.ModelTarget` gains `ContextWindow int`, copied from the catalog entry in
   `catalogModelChoices` (0 = unknown). The agent holds, under `clientMu` beside the backend:
   the active target's window, a **learned window** (0 = none) and an **estimate factor**
   `k` (starts at 1.0, never below 1.0, at most 2.0). `agent.Config` gains `ContextWindow`,
   filled from the active target by `buildAgentSessionConfig`, so the plain `axis agent` path
   (built by `agent.New`, never `SetBackend`) has the window too. `SetBackend` gains the
   target's window; it sets it, resets the learned window and `k`, and bumps a backend
   generation. Every subagent copies the parent's values at start, and what it learns stays its
   own. (Revision 4, in the code PR: a synchronous subagent copied instead of sharing, because
   it is a separate Agent with its own `clientMu`; sharing would need a lock across both.)
2. **Budget.** `B = min(maxTokens, target window if > 0, learned window if > 0)`. Usable
   `U = B − R`, with the reply reserve `R = min(4096, B / 8)` (4,096 at 32,768; 1,024 at
   8,192). The conversation's own character cap stays `maxTokens × 4`.
3. **Request estimate.** Each round builds the turn context first, before compaction (today
   `retrieveEvidence` and `clusterContextSnippet` run after it). `E` is measured on the final
   list `ChatStream` is given, after `ConsolidateMessages`, the user re-inject,
   `injectTurnContext` and `fillEmptyContent`:
   `E = k × (message content and tool-call chars + JSON bytes of the tool definitions) / 4`.
   All terms are bytes and divided once, so the units match (#614 spec review 3, F2).
   Compaction and truncation change the conversation; the round then rebuilds the final list
   and measures it again.
4. **Trigger.** LLM compaction runs when `E ≥ 0.70 × U`, and only when what it would free
   holds at least `0.10 × U` tokens: the summarizable messages **excluding an earlier compacted
   summary** (the message starting with `compactedSummaryPrefix`). Below that, summarizing frees
   too little, and with a large fixed part it would run every round, re-summarizing its own
   summary (#614 spec review 2 question). `minSummarizable` (4) still applies here. It is also
   skipped when the request would exceed `U` even with every summarizable message removed: a
   summary cannot help then (revision 4, diff review B F5). Every compaction cut moves back off
   a tool result, so an assistant tool call stays with its results (diff reviews A F2, B F2).
5. **Pre-send check.** After compaction, if `E > U`, the existing tool-result truncation
   (`compactOldest`, the newest 4 messages protected) runs until `E ≤ U` or nothing is left to
   truncate. It changes the stored conversation, as compaction already does. If `E > U`
   remains, the request is sent unchanged, and a one-time `compaction skipped` event states
   `E`, `U` and the fixed part (system prompt, tool definitions, turn context); the rest is
   the protected newest messages and truncated older results.
6. **Overflow recovery.** When `ChatStream` fails with HTTP 400 and a known context-overflow
   message (`exceeds the available context size`, `context_length_exceeded`,
   `maximum context length`, `ContextWindowExceededError`, `prompt is too long`):
   - If the backend generation changed since the round began, the error is returned as today
     and nothing is learned.
   - The stated window `N` (`context size (N tokens)`, `maximum context length is N`) sets the
     learned window when `N ≤ B`. The stated request size `M` (`request (M tokens)`,
     `resulted in M tokens`) is compared with the uncorrected estimate
     `raw_sent = E_sent / k_old`: `k = max(k, M / raw_sent)` when `M > N`, so a later, larger
     ratio raises `k`. A ratio above 2.0 is not learned (the window still is) and the skip is
     logged as an event, so one badly tokenizing request cannot shrink the budget for the
     session.
   - Recovery then forces one compaction: it ignores the 70% trigger and the 0.10 guard, and
     needs only 1 summarizable message outside the protected 8 other than an earlier summary
     (revision 4, diff review A question). The pre-send check follows, against the new `U`
     and `k`.
   - It retries once, only if the rebuilt request is predicted to fit. `E_new` uses the new
     `k`. When `N` is known, the condition is `E_new ≤ U` after learning, the pre-send success
     condition; this equals `N − R` when `N ≤ B` and is stricter otherwise (revision 4, diff
     review A F5e). Without `N` it is `E_new ≤ 0.9 × E_sent`. If not, the overflow error is
     returned without a second request.
   - The generation is read together with the backend (one locked read at the start of the
     round), so a model switch while the turn context is built teaches nothing (diff review A
     F3).
   - A second overflow, or any other error, fails the turn as today. The first-request prompt
     is dropped only then. The retry does not use a `maxTurns` round, emit a second
     `TurnStarted`, or add a session-log event. The 400 is checked before streaming, so the
     failed request wrote no output.

   The learned window and `k` apply to the whole backend. Behind `RoutingBackend` they are
   learned from whichever model overflowed and lower the budget for both; this is conservative.
7. **Display.** `/context` and the footer show the `E` of the last request sent, cached when it
   is sent, against the current `U` (so a model switch shows the new budget at once; revision
   4, diff review A F5b). The UI loop never builds the turn context (which reads state files).

## Tests

- `ModelTarget` copies `ContextWindow` from a catalog entry with `n_ctx`, and is 0 without one.
- The plain path: `buildAgentSessionConfig` with a catalog window of 16,384 gives an agent with
  `U` = 14,336, without `SetBackend`.
- Budget: `--max-tokens` 32768 with a 16,384 target window gives `U` = 14,336; with no window,
  28,672. A learned window of 20,000 lowers `B` even when the target window is 32,768, and a
  later error stating 40,000 does not raise it.
- Estimate counts tool definitions and turn context on the final list: the same conversation in
  exec scope estimates higher than in cluster scope by the definitions' JSON size / 4, and a
  large turn context raises `E` by its size / 4.
- No overflow at the default budget, failing at Base: exec scope, 32768, a summarizer that
  always fails, and a turn context of 20,000 chars. A fake backend records each request's `E`
  across 30 rounds of 6 KB tool results. At Base, the conversation-only estimate stays under its
  limit while some requests exceed `U`; after the change, none does.
- Overflow recovery at `--max-tokens` 65536, with a fake backend that returns the logged error
  text (`request (34870 tokens) … context size (32768 tokens)`) and a conversation whose raw
  estimate is about 30,000 tokens, consistent with `M`. Chat calls (tools present) and
  summarizer calls are counted separately:
  - one overflow, then success: 2 chat calls, the turn succeeds, the learned window is 32,768,
    `k` ≈ 1.16;
  - only truncation (summarizer fails) brings `E` under `N − R`: the retry is still sent;
  - two overflows: the turn fails after exactly 2 chat calls;
  - a rebuilt request that cannot shrink (only the newest round, no summarizable messages):
    1 chat call, the overflow error is returned, the learned window is 32,768 and `k` changed,
    so the test shows the fit rule was reached;
  - a non-overflow 400: 1 chat call, learned window 0;
  - `k` ratchets: overflows in separate prompts with ratios 1.3 then 1.4 leave `k` = 1.4;
  - an absurd ratio (`M` / `raw_sent` = 7) is not learned, the window is, and a skip event is
    logged.
- A model switch during a round: an overflow from the old backend does not set the learned
  window of the new one.
- No thrash: at `--max-tokens` 8192 in exec scope, the summarizer is not called while the
  summarizable part is under `0.10 × U`, and the warning event fires once per session. With a
  summarizer that returns a 3,000-char summary, it is called at most once over 10 rounds.
- `ContextTokens` reports the cached `E` of the last request and `MaxTokens` the current `U`.
  (Revision 4: that they build nothing is checked by reading the code; they read two fields
  under `clientMu`, and there is no seam to count builder calls.)
- Compaction keeps a tool call with its results when the cut falls on a result; it is skipped
  when the fixed part alone exceeds `U`; forced compaction skips a lone earlier summary; an
  overflow after a switch made before sending teaches the new backend nothing; the console
  startup path passes the window.

## Alternatives

- **Lower the default `--max-tokens`** (e.g. 28,000). One line, but it is wrong for every other
  window, and it still ignores definitions that grow with MCP tools.
- **Count with the server's tokenizer** (`/tokenize`). Exact for llama.cpp, but it adds a round
  trip per request and proxies don't offer it. Kept for calibration later.
- **Retry only** (Design 6 alone). It would recover every logged case, because each error
  stated the window and request size. But it costs a failed request each time on known windows,
  and cloud providers bill failed prompt tokens.
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
- Estimate error, measured from the log: the 10 errors state request sizes of 32,829 to 35,935
  tokens, the evidence for learning `k` from `M` rather than assuming chars/4.
- Optimization: none beyond counting what is sent. Measurement: the tests above. After install,
  over the next 14 days of logs: no turn fails with a context-overflow error that states a
  window, and the fixed-part warning count per scope.

## Review

Kind: spec
Reviewer: fresh-context subagent (Claude Code)
Model: Anthropic Claude (same family as the author; fresh session)
Base: 3abc2798c6344032754b596875e6367f8f3f074e
Head: ce638f227ccbe30958f446761a22ad08dfea0d5e
Date: 2026-11-05
Verdict: revise

1. Major: a learned window is ignored whenever the target has a window (stale catalog after a
   server restart with a smaller window). Fixed: `B` takes the minimum of all known windows
   (Design 2); test added.
2. Major: the retry can resend a byte-identical request (estimate error with `N = B`, or too
   few summarizable messages). Fixed: `N ≤ B` sets the learned window, `k` is learned from the
   stated request size, forced compaction needs 1 message, and the retry is skipped unless the
   rebuilt request is predicted to fit (Design 6); a no-shrink test added.
3. Major: the default-budget no-overflow test passes at Base. Fixed: it uses a failing
   summarizer and a 20,000-char turn context, and must fail at Base (Tests).
4. Minor: the recovery test contradicted Design 6 and miscounted calls. Fixed: run at 65536,
   `N ≤ B`, chat and summarizer calls counted separately.
5. Minor: the non-goal and warning blamed only the fixed part; the protected newest messages
   also stay. Fixed: both now state the fixed part and the protected newest messages.
6. Minor: the order of work was unstated; turn context ran after compaction and `E` was not on
   the sent list. Fixed: turn context is built first, and `E` is measured on the final list
   (Design 3).
7. Minor: the learned window races a model switch. Fixed: it is stored with the backend under
   `clientMu`, with a generation check (Design 1, 6); test added.
8. Minor: showing `E` in the footer would read state files on the UI loop. Fixed: the last
   request's `E`/`U` are cached and shown (Design 7); test added.
9. Minor: wrong #614 review citation, no MCP measurement, `AGENTS.md` dependence unstated.
   Fixed: citation is review 3 F2; the `AGENTS.md` dependence and the 32 KiB cap are stated;
   MCP size is an open question below.

Questions:
- How far off are chars/4 and per-message template overhead with the served tokenizer, and does
  `R` = 1,024 at 8,192 absorb it? Open: needs a served tokenizer; `k` learned from an overflow
  covers it after the first error.
- Is the pattern list complete for every backend? Became Design 6 (`prompt is too long` added)
  and a non-goal (native Ollama discards the body).
- Should a learned window from `RoutingBackend`'s cheap model lower the primary's budget?
  Became Design 6: yes, per backend, conservative.
- Does a retry consume a `maxTurns` round, re-emit `TurnStarted`, or change session-log
  accounting? Became Design 6: none of these.
- Is it intended that an async subagent's learned window never reaches the parent? Became
  Design 1: yes, each async subagent owns its values.
- Can the summarization request itself overflow a small window, so forced compaction silently
  does nothing? Open: not probed; the no-shrink rule in Design 6 then skips the retry.
- Is `/props` `n_ctx` the per-slot window under `--parallel`? Open: needs a live llama.cpp; the
  learned window corrects a too-large value after one error.
- How large is the fixed prompt with MCP tools loaded? Open: depends on configured servers; to
  be measured from the cached `E` after install.

Kind: spec
Reviewer: fresh-context subagent (Claude Code)
Model: Anthropic Claude (same family as the author; fresh session)
Base: 3abc2798c6344032754b596875e6367f8f3f074e
Head: 654115f47a363422832031963815b0593f2e0ae8
Date: 2026-11-05
Verdict: approve-with-changes

1. Major: `k` and the retry-fit rule mixed corrected and uncorrected units (`k` stuck at 1.3
   when the true ratio rose to 1.4; a retry refused for a request that fits `U`). Fixed: `k`
   learns from `raw_sent = E_sent / k_old`, and with `N` known the retry condition is
   `E_new ≤ N − R` under the new `k` (Design 6); ratchet and truncation-only tests added.
2. Major: compaction thrash when the fixed part is at or above `0.70 × U`, because the previous
   summary passed the 0.10 guard alone. Fixed: the guard excludes an earlier compacted summary,
   and `minSummarizable` stays at 4 for the trigger (Design 4); 3,000-char-summary test added.
3. Minor: `k` had no upper bound, and the recovery fixture's size was unspecified. Fixed: `k` is
   at most 2.0 and a larger ratio is not learned, with an event logged (Design 1, 6); the
   fixture's raw size is about 30,000; an absurd-ratio test added.
4. Minor: the window reached only the `/model` path. Fixed: `agent.Config.ContextWindow` is
   filled by `buildAgentSessionConfig`, and `SetBackend` takes the window (Design 1); plain-path
   test added.
5. Minor: the no-shrink and non-overflow tests could pass at Base. Fixed: no-shrink also asserts
   the learned window and the changed `k`; non-overflow asserts no learned window (Tests).

Questions:
- Can forced compaction with 1 summarizable message cut between an assistant tool-call message
  and its tool results? Open: exists at Base; the implementation adds a test with the tool-call
  message at index len − 9 and keeps the pair together if it fails.
- Is the generation check atomic for a synchronous subagent, which has its own `clientMu`?
  Became Design 1 (revision 4): every subagent copies the budget state, so the check runs under
  the child's own lock and never spans two agents.
- Is the overflow always a non-200 status before streaming? Open: the logged errors start with
  `http status 400`, so the logged path is covered; a mid-stream overflow behind a proxy would go
  undetected, as at Base.

Note: review 1's nine fixes are all present; findings 1 and 4 are defects inside the fixes for review-1 findings 2 and 7, and review 1's question 1 ("k covers it after the first error") holds only with finding 1 fixed. Code starts; every finding is resolved.

Kind: diff
Reviewer: fresh-context subagent A (Claude Code)
Model: Anthropic Claude (same family as the author; fresh session)
Base: 3abc2798c6344032754b596875e6367f8f3f074e
Head: d04703a9478f018ebde12668871ddd096ecebe5a
Date: 2026-11-05
Verdict: approve-with-changes

1. Major: the deadcode gate fails at Head (`ContextBudget` used only by a test; the
   `MCPRegistry` allowlist entry still names `agent.go`). Fixed in bcf4a2fa: accessor removed,
   entry repointed; `./hack/verify-deadcode.sh` passes there.
2. Major: forced compaction can split an assistant tool call from its results, and the retry
   sends an orphaned tool result; the promised len − 9 test was missing. Fixed in 5cd2b28d:
   `pairSafeCut` in both compaction paths; `TestCompactionKeepsToolCallWithResults` fails
   without it.
3. Minor: the backend generation was read after the backend, so a switch in between taught the
   new backend the old one's window; subagents read backend and budget under separate locks.
   Fixed in 5cd2b28d: `backendState` returns all three in one read, passed to `sendRound` and
   to the child; `TestOverflowAfterSwitchBeforeSendIsNotLearned`.
4. Minor: the thrash test passed without running the summarizer or the warning. Fixed in
   5cd2b28d: 20 rounds, exactly 1 summary and 1 warning; counting the summary as freeable makes
   it fail (2 calls), checked by mutation.
5. Minor: spec departures. (a) warning wording: fixed in 5cd2b28d, states the fixed part. (b)
   live `U` vs cached: declined, the live `U` shows a model switch at once; recorded in Design 7
   and `lastU` removed. (c) footer builders: declined, no seam to count calls; the accessors read
   two fields under `clientMu` (static), test line reworded. (d) `k` ratchet through
   `sendRound`: declined, covered by `TestEstimateFactorRatchetsAndIsCapped` and diff review B's
   probe through real overflows (1.3 then 1.4). (e) retry limit is the current `U`: recorded in
   Design 6. (f) `Config.MaxTokens` comment: a correct fix, no change.

Questions:
- Should forced compaction exclude an earlier summary, so the no-shrink rule refuses an
  equal-size retry? Became Design 6 (revision 4): it needs a message other than the summary;
  `TestForcedCompactionSkipsLoneSummary`.
- After `/clear`, `ContextTokens` shows the last request's size until the next request. Open:
  matches Design 7; the next request corrects it, not probed in the UI.

Kind: diff
Reviewer: fresh-context subagent B (Claude Code)
Model: Anthropic Claude (same family as the author; fresh session)
Base: 3abc2798c6344032754b596875e6367f8f3f074e
Head: d04703a9478f018ebde12668871ddd096ecebe5a
Date: 2026-11-05
Verdict: approve-with-changes

1. Major: the deadcode gate fails at Head. Fixed in bcf4a2fa (as A F1).
2. Major: forced compaction can leave an orphaned tool result that the retry sends. Fixed in
   5cd2b28d (as A F2).
3. Minor: the console/TUI startup path with a resolved model never passes the served window.
   Fixed in 5cd2b28d: `ContextWindow` in `buildAgent`; `TestConsoleStartupAgentUsesTargetWindow`.
4. Minor: the over-budget warning blamed the fixed part, which can be false. Fixed in 5cd2b28d:
   it states `E`, `U` and the measured fixed part; `TestNoCompactionWhenFixedPartExceedsBudget`
   checks the number.
5. Minor: at `--max-tokens` 1000 compaction runs repeatedly though it can never fit. Fixed in
   5cd2b28d: skipped when the request exceeds `U` with every summarizable message removed;
   `TestNoCompactionWhenFixedPartExceedsBudget` (0 summarizer calls). Two older tests that used
   a 200-token budget only to force compaction now use 8,000 and 9,000.
6. Minor: the footer test does not show the builders are skipped. Declined: no seam to count
   builder calls; the accessors read two fields under `clientMu` (static, as A F5c).

Questions:
- With a failing summarizer and a session past about 600 tool rounds, truncated stubs alone can
  exceed `U`. Open: same as Base and within the non-goal; dropping old messages is later work.
- Should the summarizer request be capped to `U`? Open: in B's resume probe truncation still made
  the turn fit; one wasted call per such prompt, later work.
- Anthropic's "prompt is too long: N tokens > M maximum" did not match the regexes. Became
  Design 6 (revision 4): both numbers are parsed; `TestParseContextOverflow`.

Note: every diff-review finding is resolved; merge needs the maintainer's go.
