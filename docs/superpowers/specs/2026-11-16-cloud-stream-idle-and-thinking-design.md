# CloudBackend stream idle timeout and reasoning support

Date: 2026-11-16. Base: `95b588b7`. Branch: `feat/cloud-stream-idle-and-thinking`.

## Problem

1. **Fixed 120s wall-clock client timeout kills healthy slow streams.** Both `NewCloudBackendWithKey` and `NewOpenAICompatibleBackend` in `internal/agent/cloud_backend.go` construct `http.Client{Timeout: 120 * time.Second}`, whereas `internal/chat/client.go` uses `Timeout: 0` and lets `ctx` bound the turn. Because `cmd/axis/agent.go` defaults `--timeout` to `5m`, a slow prompt prefill plus generation exceeding 120s is aborted mid-stream even while tokens are actively arriving, and stalled connections wait on the caller's full context rather than failing quickly on stream silence.
2. **Separated reasoning deltas are silently dropped.** OpenAI-compatible servers that separate chain-of-thought fields (`delta.reasoning_content`, `delta.reasoning`) and Anthropic `thinking_delta` events are ignored by `openAIChunk` and `anthropicDelta`. During thinking phases in `axis agent`, the interactive console (`internal/console`) sees no bytes, shows no thinking phase or duration, and cannot render a collapsible `ThinkingEntry`.
3. **Compaction cannot request non-thinking summaries.** `summarizeViaBackend` in `internal/agent/compaction.go` invokes `ChatStream` with `tools = nil` and `w = io.Discard`. Backends whose default chat template enables reasoning spend turn latency and token budget inside `reasoning_content` (and can return empty visible `content`) unless `chat_template_kwargs.enable_thinking: false` is sent.
4. **Multi-round thinking spans leak or drop timing in console rendering.** `cmd/axis/agent_console.go` creates one `console.NewStreamWriter` per operator prompt across multiple `runLocked` model rounds. At Base, `extractThought` and `parseStreamThought` in `internal/console/thought.go` only strip the first `<think>...</think>` block, and `Model.stampThinkingSpan` in `internal/console/model.go` overwrites a single `thoughtStart`/`thoughtEnd` pair instead of accumulating spans across rounds. Furthermore, wrapping out-of-band reasoning in literal `<think>...</think>` text lets reasoning that mentions `</think>` leak into the answer.

## Non-goals

- Parsing or suppressing inline `<think>...</think>` tags inside `delta.content` within `CloudBackend` (the console's `thought.go` parser owns inline tag extraction).
- Passing Anthropic native `thinking` request parameters (`budget_tokens` / adaptive config): `WithDisableThinking` and `CloudBackendOptions.Thinking` are intentionally scoped to OpenAI-compatible requests (`chat_template_kwargs.enable_thinking`); `streamAnthropic` ignores them and shares only idle-timeout protection plus `thinking_delta` output forwarding.
- Changing `internal/chat.Client` (`/api/chat` Ollama client) behavior, `~/.axis/ai.yaml` config schema, or `cmd/axis` CLI flags (no new flags or config keys are added here; `CloudBackendOptions` exposes programmatic fields for future wiring).
- Adding a hard-coded provider/model name allowlist for `chat_template_kwargs`: capability detection uses explicit opt-in (`WithDisableThinking` / `CloudBackendOptions.Thinking`) plus reactive 4xx fallback matching `stream_options`.
- Modifying `consult` or sibling WIP branches not on `main`.

## Design

1. **Client timeout removal and shared idle stream guard (`internal/agent/cloud_backend.go`).**
   - Set `client: &http.Client{Timeout: 0}` on both `NewCloudBackendWithKey` and `NewOpenAICompatibleBackend` so caller `ctx` bounds total request lifetime, and set `streamIdleTimeout: defaultStreamIdleTimeout` (`120 * time.Second`).
   - Route both `streamOpenAI` and `streamAnthropic` HTTP calls through one shared helper `doStreamRequest` backed by `streamIdleGuard`:
     - Derives `reqCtx, cancel := context.WithCancel(parent)` and, when `timeout > 0`, arms `time.AfterFunc(timeout, ...)`. The timer callback acquires `g.mu`, checks `!g.stopped`, sets `g.expired.Store(true)`, and only then cancels `reqCtx`.
     - Resets the timer under `g.mu` when HTTP response headers arrive and wraps `resp.Body` in `idleReadCloser`, resetting the timer on every `Read` returning `n > 0` bytes.
     - On `Do` or `scanner.Err()` failure, `guard.wrapErr` returns `fmt.Errorf("stream idle timeout after %s: %w", g.timeout, context.DeadlineExceeded)` **only** when `g.expired.Load()` is true, `errors.Is(err, context.Canceled)`, and `parent.Err() == nil`. Genuine transport/EOF errors, stopped guards, and parent cancellation/deadline expiry preserve their own error without misreporting an idle timeout.
     - On `CloudBackendOptions`, `< 0` disables the idle watchdog (`streamIdleTimeout = 0`), `0` keeps the 120s default, and `> 0` sets a custom idle threshold.

2. **Out-of-band reasoning stream (`internal/agent/color_writer.go`, `internal/agent/cloud_backend.go`, `internal/console/bridge.go`, `internal/console/model.go`, `internal/console/activity.go`).**
   - Define `agent.ThoughtWriter` (`WriteThought(thought string)`, `CloseThought()`). `*ColorWriter` implements `ThoughtWriter` and forwards to its wrapped sink if that sink implements `ThoughtWriter`; plain writers (`os.Stdout` in `-p` piped mode, `bytes.Buffer`) receive nothing, keeping piped stdout and conversation history (`resp.Content`) free of raw thinking text. `ColorWriter.Close()` calls `CloseThought()` before closing/flushing.
   - `*console.streamWriter` implements `agent.ThoughtWriter` (checked with `var _ agent.ThoughtWriter = (*streamWriter)(nil)`), emitting dedicated `console.ThoughtChunkMsg{Turn, Text, Done}` messages instead of injecting `<think>` tags into `StreamChunkMsg`:
     - `WriteThought` flushes any buffered answer bytes first (preserving turn order), marks `inThought = true`, and sends `ThoughtChunkMsg{Turn, Text}`.
     - `CloseThought`, `Write` (when transitioning out of thought), and `Close` emit `ThoughtChunkMsg{Turn, Done: true}` once per open span.
   - `console.Model` handles `ThoughtChunkMsg` into a separate `thoughtBuf *strings.Builder` (`inOutOfBandThought`), separates closed rounds with `"\n\n"`, merges out-of-band and inline thoughts on `finishTurn`, and drives `activity.go` `"thinking"` status while `inOutOfBandThought` is active (even when `m.stream` is empty). Literal `</think>` tokens inside out-of-band reasoning never touch `StreamChunkMsg` or `extractThought`.
   - `streamOpenAI` reads `delta.ReasoningContent` (falling back to `delta.Reasoning`) before `delta.Content`, emitting via `writeThought(w, ...)` and closing via `closeThought(w)` on `delta.Content != ""`, `len(delta.ToolCalls) > 0`, or stream end. `streamAnthropic` handles `event.Delta.Type == "thinking_delta"` identically, closing on `text_delta`, `tool_use` `content_block_start`, `input_json_delta`, or stream end.

3. **Thinking toggle and compaction wiring (`internal/agent/cloud_backend.go`, `internal/agent/compaction.go`, `internal/agent/model_target.go`).**
   - Add `WithDisableThinking(ctx context.Context) context.Context` and `CloudBackendOptions.StreamIdleTimeout` / `CloudBackendOptions.Thinking` (`*bool`). `BuildBackend` applies `CloudBackendOptions` via `applyOptions` on both `ProtocolOpenAI` and `ProtocolCloud` backends; `resolveThinking(ctx)` lets `WithDisableThinking(ctx)` override `b.thinking` to `false` for OpenAI-compatible requests (`streamOpenAI`).
   - Update `summarizeViaBackend` in `internal/agent/compaction.go` to pass `WithDisableThinking(ctx)`.
   - Extract `openOpenAIStream(ctx, reqBody, thinking)` in `internal/agent/cloud_backend.go` using shared `rejectsRequestField(status, body, field)` across up to 3 attempts so servers rejecting `stream_options` and/or `chat_template_kwargs` / `enable_thinking` via HTTP 4xx strip the unsupported field cleanly, record sticky `noStreamUsage` / `noChatTemplateKwargs` flags per backend, and retry.

4. **Multi-round inline span parsing and cumulative timing (`internal/console/thought.go`, `internal/console/model.go`).**
   - Share one `splitThoughtSpans(s)` scanner between `extractThought` and `parseStreamThought` so every `<think>...</think>` block across multiple model rounds on a single stream is collected and joined with `"\n\n"` in order.
   - `extractThought` preserves all text outside `<think>...</think>` tags in `answer` (`strings.TrimSpace`) when any `<think>` tag was present, and leaves non-tag strings untouched. `parseStreamThought` returns `answer == ""` whenever the final `<think>` span is unclosed (`inThought == true`, keeping existing reversed-tag expectations).
   - `Model.stampThinkingSpan` accumulates closed span durations (`m.thoughtElapsed += now.Sub(m.thoughtStart)`) across both inline and out-of-band transitions; `thinkingElapsed()` returns `m.thoughtElapsed`; `resetThoughtState` clears all thinking buffers, timers, and accumulators on `beginTurn` and `finishTurn`.

## Tests

- `internal/agent/cloud_backend_stream_test.go`:
  - `TestCloudBackend_DefaultsUseIdleTimeoutNotClientTimeout`: asserts `client.Timeout == 0` and `streamIdleTimeout == 120s` for both constructors.
  - `TestCloudBackend_HealthySlowStreamSurvivesBeyondIdleWindow` & `TestCloudBackend_DisabledIdleTimeoutAllowsLongPause`: active slow stream succeeds past idle window; `< 0` disables the watchdog.
  - `TestCloudBackend_PreHeaderIdleTimeout`, `TestCloudBackend_HeaderResetExtendsFirstChunkBudget`, `TestCloudBackend_OpenAI_IdleTimeoutAbortsStalledStream`, and `TestCloudBackend_Anthropic_IdleTimeoutAbortsStalledStream`: pre-header stalls, header-arrival budget reset before the first body chunk, and mid-stream stalls behave deterministically and fail with `"stream idle timeout"` wrapping `context.DeadlineExceeded` on genuine inactivity.
  - `TestIdleGuardTouchDoesNotExpireRightAfterReset`: concurrent `touch()` and timer firing never expire an active guard right after reset.
  - `TestCloudBackend_ParentCancelDoesNotReportIdleTimeout` & `TestCloudBackend_ParentDeadlineDoesNotReportIdleTimeout`: parent `Canceled` / `DeadlineExceeded` preserve parent errors without misreporting idle timeout.
  - `TestCloudBackend_OpenAI_ReasoningContentStreamsToThoughtWriterOnly`, `TestCloudBackend_Anthropic_ThinkingDeltaStreamsToThoughtWriterOnly`, `TestCloudBackend_Anthropic_IgnoresThinkingOverrides`, and `TestCloudBackend_ThinkingThenToolCallsClosesThought`: reasoning routes only to `ThoughtWriter`, Anthropic ignores OpenAI thinking overrides, `resp.Content` and plain writers stay clean, and thought closes at tool boundaries.
  - `TestCloudBackend_WithDisableThinking_SendsAndFallsBackOn400`, `TestCloudBackend_RejectsBothStreamOptionsAndChatTemplateKwargs`, `TestCloudBackend_ThinkingOptionAndContextOverride`, and `TestCloudBackend_EnableThinking4xxFallback`: cover `chat_template_kwargs.enable_thinking`, `WithDisableThinking` overriding `Thinking: &true`, sticky fallback on `chat_template_kwargs` / `enable_thinking`, and combined fallback with `stream_options`.
  - `TestCompactManually_DisablesThinkingOnCloudBackend`: end-to-end compaction sends `chat_template_kwargs.enable_thinking: false`.
  - `TestBuildBackend_AppliesStreamIdleTimeoutAndThinkingOptions`: verifies `ProtocolOpenAI` and `ProtocolCloud` option application including `0` default preservation and `< 0` disabling.
- `internal/agent/color_writer_test.go`:
  - `TestColorWriterCloseCallsCloseThought`: asserts `Close()` closes an open `ThoughtWriter`.
- `internal/console/bridge_test.go`, `thought_test.go`, `activity_test.go`, `model_test.go`:
  - `TestStreamWriterWriteThoughtEmitsOutOfBandThoughtChunks` (including pre-thought buffered answer flush ordering), `TestExtractThought`, `TestParseStreamThought`, `TestLiveTurnNamesOutOfBandThinking`, `TestModelMultiRoundThinkingSpanAccumulates`, and `TestModelOutOfBandThoughtWithLiteralCloseTagStaysOutOfAnswer`.
- `cmd/axis/agent_console_thought_test.go`:
  - `TestAgentMultiRoundOutOfBandThinkingConsoleIntegration`: end-to-end `CloudBackend` -> `Agent.Run` -> `ColorWriter` -> `console.NewStreamWriter` -> `console.Model` across 2 rounds with a tool call, literal `</think>` inside reasoning, collapsed entry + duration badge, and `/thought` expansion.

## Alternatives

- **Raise `http.Client.Timeout` statically (e.g. 600s).** Rejected: a higher fixed timeout still kills streams legitimately exceeding the constant and leaves stalled connections hanging until `timeout` expires instead of failing fast on stream silence.
- **Inject `<think>...</think>` into `StreamChunkMsg` or `ChatStream`'s `io.Writer`.** Rejected: writing `<think>` into `io.Writer` breaks `-p` piped output (`axis agent -p "..." | jq`), and framing `ThoughtChunkMsg` inside `StreamChunkMsg` lets literal `</think>` inside reasoning leak into answers.
- **Disable thinking via server/proxy config only.** Rejected: compaction (`summarizeViaBackend`) and interactive turns share the same endpoint; per-request `WithDisableThinking` with 4xx fallback handles both automatically without proxy reconfiguration.

## Research

- `internal/chat/client.go` already sets `http.Client{Timeout: 0}` so `ctx` bounds Ollama stream duration cleanly; `CloudBackend` diverged by keeping a 120s `Client.Timeout`.
- Open-source Apache-2.0 prior art in [QwenLM/qwen-code](https://github.com/QwenLM/qwen-code) separates per-chunk `streamIdleTimeoutMs` from overall request timeouts and uses `chat_template_kwargs.enable_thinking` for Qwen OpenAI-compatible servers.
- Post-submission findings on [PR #643](https://github.com/toasterbook88/Axis/pull/643) ([`ThoughtWriter` review discussion](https://github.com/toasterbook88/Axis/pull/643#discussion_r4584145844) and [multi-round parser finding](https://github.com/toasterbook88/Axis/pull/643#discussion_r4584145680)) and fresh-context spec/diff reviews identified multi-round duration overwriting, literal `</think>` leakage in `<think>` wrappers, and missing `ProtocolCloud`/`Thinking`/cancel-race guards, all incorporated and tested in this spec.
- Measurement: local `-race` test runs (`internal/agent` 15.3s, `internal/console` 10.0s, `cmd/axis` integration 1.1s) verify deterministic timeout behavior with 25–45ms idle windows without hitting shared endpoints.

## Review

Kind: spec
Reviewer: fresh-context subagent (Antigravity CLI self subagent R2)
Model: gemini
Base: 95b588b7a6c7920533b4604f36ec9e251767adb9
Head: 7fa685d57aed074d01b6e5b496a4e5f1cb3c1bb9
Date: 2026-11-16
Verdict: approve-with-changes

1. [major] Missing standalone TestLiveTurnNamesOutOfBandThinking in internal/console/activity_test.go; fixed in ccfac808 (extracted standalone TestLiveTurnNamesOutOfBandThinking).
2. [minor] Header-arrival idle budget reset and pre-thought buffered answer flush lacked dedicated regression assertions; fixed in ccfac808 (added TestCloudBackend_HeaderResetExtendsFirstChunkBudget in internal/agent/cloud_backend_stream_test.go and pre-thought flush ordering check in internal/console/bridge_test.go).

Questions:
- Does streamAnthropic ignore Thinking and WithDisableThinking? verified: intentional per Non-goals and locked by TestCloudBackend_Anthropic_IgnoresThinkingOverrides in ccfac808.

Kind: diff
Reviewer: fresh-context subagent (Antigravity CLI self subagent A R2)
Model: gemini
Base: 95b588b7a6c7920533b4604f36ec9e251767adb9
Head: 7fa685d57aed074d01b6e5b496a4e5f1cb3c1bb9
Date: 2026-11-16
Verdict: approve-with-changes

1. [blocker] Spec ## Review section is empty; fixed: populated this ## Review section with chronological spec and dual diff review blocks.
2. [minor] TestLiveTurnNamesOutOfBandThinking was folded into TestLiveTurnNamesThinking; fixed in ccfac808 (extracted standalone TestLiveTurnNamesOutOfBandThinking).

Questions:
- Do OpenAI 4xx responses omitting the chat_template_kwargs/enable_thinking field name leave the fallback inactive? open: vendor-specific HTTP 400 error bodies were not probed against live endpoints and are copied to the PR body.
- Does ColorWriter.WriteThought leave a trailing buffered backtick prefix in cw.buf before writing reasoning? open: only occurs if plain output ends on an unbuffered backtick prefix immediately before WriteThought and is copied to the PR body.

Kind: diff
Reviewer: fresh-context subagent (Antigravity CLI self subagent B R2)
Model: gemini
Base: 95b588b7a6c7920533b4604f36ec9e251767adb9
Head: 7fa685d57aed074d01b6e5b496a4e5f1cb3c1bb9
Date: 2026-11-16
Verdict: revise

1. [blocker] Spec ## Review section is empty; fixed: populated this ## Review section with chronological spec and dual diff review blocks.
2. [major] streamIdleGuard.touch() could lose a mutex race against an already-dispatched timer callback and cancel an active stream right after reset (and re-arm after expiry); fixed in ccfac808 (serialized deadline tracking under g.mu with g.onTimer and refused post-expiry touch() re-arming, verified under -race with TestIdleGuardTouchDoesNotExpireRightAfterReset).
3. [minor] TestLiveTurnNamesOutOfBandThinking was missing as a standalone test function; fixed in ccfac808.

Kind: spec
Reviewer: fresh-context subagent (Antigravity CLI self subagent R3)
Model: gemini
Base: 95b588b7a6c7920533b4604f36ec9e251767adb9
Head: ccfac80809d8629d562d9c9fe189642af2d50bdf
Date: 2026-11-16
Verdict: approve

Note: Same-model-family fresh-context spec review against Head ccfac808 passed with zero findings and zero open questions (disclosed in PR body).

Kind: diff
Reviewer: fresh-context subagent (Antigravity CLI self subagent A R3)
Model: gemini
Base: 95b588b7a6c7920533b4604f36ec9e251767adb9
Head: ccfac80809d8629d562d9c9fe189642af2d50bdf
Date: 2026-11-16
Verdict: approve-with-changes

1. [blocker] Spec ## Review section was empty at ccfac808; fixed: populated this ## Review section with approving spec and dual independent diff review blocks and validated with verify-specs.go.

Questions:
- Do live OpenAI-compatible and Anthropic servers accept the chat_template_kwargs.enable_thinking and SSE reasoning fields identically to httptest mocks? open: offline review sandbox prohibits live cluster/remote calls; copied to PR body.
- Were prior R2 reviews unrecorded in ## Review while feature commit f3fffd9b preceded spec commit 99b933bb? verified: full R2 and R3 review history is now recorded above against ccfac808.
- Does actionlint pass in full ./hack/ci-preflight.sh alongside the other gates? verified: ./hack/ci-preflight.sh (which runs actionlint) is executed cleanly after committing this review record.

Kind: diff
Reviewer: fresh-context subagent (Antigravity CLI self subagent B R3)
Model: gemini
Base: 95b588b7a6c7920533b4604f36ec9e251767adb9
Head: ccfac80809d8629d562d9c9fe189642af2d50bdf
Date: 2026-11-16
Verdict: approve-with-changes

1. [major] Spec ## Review section was empty at ccfac808; fixed: populated this ## Review section with approving spec and dual independent diff review blocks and validated with verify-specs.go.

Questions:
- Does end-to-end streaming against live OpenAI-compatible and Anthropic endpoints match httptest.NewServer SSE keepalive, reasoning, and 4xx error payloads? open: offline review sandbox could not probe live endpoints; copied to PR body.

Note: Dual independent diff reviews approved all production and test changes at ccfac808; both only flagged the unpopulated ## Review section resolved by this commit.

Kind: diff
Reviewer: fresh-context subagent (Codex, independent release review)
Model: OpenAI GPT
Base: 03e06095a9fdb9edb9172358fbc936259a295f09
Head: 05c6d42ed20a4bff046b0f2cf780b7bbb208d89b
Date: 2026-11-16
Verdict: approve

No findings or questions. Reviewed the complete diff and specification. The
hermetic race suites for internal/agent, internal/console and cmd/axis passed
with Go 1.26.11. Public-boundary, repository-truth, documentation-facts,
import-tier and specification gates passed. External publication state was not
verified by the offline repository-truth invocation.
