# CloudBackend stream idle timeout and reasoning support

Date: 2026-11-16. Base: `95b588b7`. Branch: `feat/cloud-stream-idle-and-thinking`.

## Problem

1. **Fixed 120s wall-clock client timeout kills healthy slow streams.** Both `NewCloudBackendWithKey` and `NewOpenAICompatibleBackend` in `internal/agent/cloud_backend.go` construct `http.Client{Timeout: 120 * time.Second}`, whereas `internal/chat/client.go` uses `Timeout: 0` and lets `ctx` bound the turn. Because `cmd/axis/agent.go` defaults `--timeout` to `5m`, a slow prompt prefill plus generation exceeding 120s is aborted mid-stream even while tokens are actively arriving, and stalled connections wait on the caller's full context rather than failing quickly on stream silence.
2. **Separated reasoning deltas are silently dropped.** OpenAI-compatible servers that separate chain-of-thought fields (`delta.reasoning_content`, `delta.reasoning`) and Anthropic `thinking_delta` events are ignored by `openAIChunk` and `anthropicDelta`. During thinking phases in `axis agent`, the interactive console (`internal/console`) sees no bytes, shows no thinking phase or duration, and cannot render a collapsible `ThinkingEntry`.
3. **Compaction cannot request non-thinking summaries.** `summarizeViaBackend` in `internal/agent/compaction.go` invokes `ChatStream` with `tools = nil` and `w = io.Discard`. Backends whose default chat template enables reasoning spend turn latency and token budget inside `reasoning_content` (and can return empty visible `content`) unless `chat_template_kwargs.enable_thinking: false` is sent.
4. **Multi-round thinking spans leak into console answer text.** `cmd/axis/agent_console.go` creates one `console.NewStreamWriter` per operator prompt across multiple `runLocked` model rounds. When a tool-calling turn emits reasoning in round 1 (`<think>...</think>`) and reasons again in round 2 before answering, `extractThought` and `parseStreamThought` in `internal/console/thought.go` only strip the first `<think>...</think>` block, leaving later blocks inside the rendered answer.

## Non-goals

- Parsing or suppressing inline `<think>...</think>` tags inside `delta.content` within `CloudBackend` (the console's `thought.go` parser already owns inline tag extraction).
- Changing `internal/chat.Client` (`/api/chat` Ollama client) behavior, `~/.axis/ai.yaml` config schema, or `cmd/axis` CLI flags (no new flags or config keys are added here; `CloudBackendOptions` exposes programmatic fields for future wiring).
- Adding a hard-coded provider/model name allowlist for `chat_template_kwargs`: capability detection uses explicit opt-in (`WithDisableThinking` / `CloudBackendOptions.Thinking`) plus reactive 4xx fallback, matching `stream_options`.
- Modifying `consult` or sibling WIP branches not on `main`.

## Design

1. **Client timeout removal and shared idle stream guard (`internal/agent/cloud_backend.go`).**
   - Set `client: &http.Client{Timeout: 0}` on both `NewCloudBackendWithKey` and `NewOpenAICompatibleBackend` so caller `ctx` bounds total request lifetime, and set `streamIdleTimeout: defaultStreamIdleTimeout` (`120 * time.Second`).
   - Route both `streamOpenAI` and `streamAnthropic` HTTP calls through one shared helper `doStreamRequest` backed by `streamIdleGuard`:
     - Derives `reqCtx, cancel := context.WithCancel(parent)` and, when `timeout > 0`, arms `time.AfterFunc(timeout, func() { expired.Store(true); cancel() })`.
     - Resets the timer under a mutex when HTTP response headers arrive and wraps `resp.Body` in `idleReadCloser`, resetting the timer on every `Read` returning `n > 0` bytes.
     - On `Do` or `scanner.Err()` failure, `guard.wrapErr` returns `fmt.Errorf("stream idle timeout after %s: %w", g.timeout, context.DeadlineExceeded)` **only** when `g.expired.Load()` is true and `parent.Err() == nil`. Parent cancellation or deadline expiry preserves the parent context error without misreporting an idle timeout.
     - Non-positive `StreamIdleTimeout` (`< 0` on `CloudBackendOptions`) disables the idle watchdog (`streamIdleTimeout = 0`); `0` retains the 120s default.

2. **Out-of-band reasoning stream (`internal/agent/color_writer.go`, `internal/agent/cloud_backend.go`, `internal/console/bridge.go`).**
   - Define `agent.ThoughtWriter` (`WriteThought(thought string)`, `CloseThought()`). `*ColorWriter` implements `ThoughtWriter` and forwards to its wrapped sink if that sink implements `ThoughtWriter`; plain writers (`os.Stdout` in `-p` piped mode, `bytes.Buffer`) receive nothing, keeping piped stdout and conversation history (`resp.Content`) free of raw thinking text. `ColorWriter.Close()` calls `CloseThought()` before closing/flushing.
   - `*console.streamWriter` implements `agent.ThoughtWriter` (checked with `var _ agent.ThoughtWriter = (*streamWriter)(nil)`), buffering via a single `enqueueLocked` path: `WriteThought` opens `<think>` if not currently inside a span, `Write` closes `</think>` before ordinary answer bytes, `CloseThought` closes an open span at tool boundaries or end of round, and `Close` closes any remaining open span before final flush.
   - `streamOpenAI` reads `delta.ReasoningContent` (falling back to `delta.Reasoning`) before `delta.Content`, emitting via `writeThought(w, ...)` and closing via `closeThought(w)` when `delta.Content != ""`, `len(delta.ToolCalls) > 0`, or at stream end. `streamAnthropic` handles `event.Delta.Type == "thinking_delta"` identically, closing on `text_delta`, `tool_use` `content_block_start`, `input_json_delta`, or stream end.

3. **Thinking toggle and compaction wiring (`internal/agent/cloud_backend.go`, `internal/agent/compaction.go`, `internal/agent/model_target.go`).**
   - Add `WithDisableThinking(ctx context.Context) context.Context` and `CloudBackendOptions.StreamIdleTimeout` / `CloudBackendOptions.Thinking` (`*bool`). `BuildBackend` applies `CloudBackendOptions` to OpenAI-compatible and Cloud backends. Context-scoped disabling overrides a backend's default `thinking` field cleanly for internal turns.
   - Update `summarizeViaBackend` in `internal/agent/compaction.go` to pass `WithDisableThinking(ctx)`.
   - Extract `openOpenAIStream(ctx, reqBody, thinking)` in `internal/agent/cloud_backend.go` using shared `rejectsRequestField(status, body, field)` across up to 3 attempts so servers rejecting `stream_options` and/or `chat_template_kwargs` via HTTP 4xx strip the unsupported field cleanly, record sticky `noStreamUsage` / `noChatTemplateKwargs` flags per backend, and retry.

4. **Multi-round thinking span extraction (`internal/console/thought.go`).**
   - Share one `splitThoughtSpans(s)` scanner between `extractThought` and `parseStreamThought` so every `<think>...</think>` block across multiple model rounds on a single `streamWriter` is collected and joined with `"\n\n"` in order.
   - Completed parsing (`extractThought`) preserves all text outside `<think>...</think>` tags in `answer` (`strings.TrimSpace`) when any `<think>` tag was present, and leaves non-tag strings untouched.
   - In-flight parsing (`parseStreamThought`) preserves `answer == ""` whenever the final `<think>` span is unclosed (`inThought == true`, maintaining existing reversed-tag contract `Preceding </think> then <think>in progress`), and returns joined thoughts plus trimmed answer once all open spans close.

## Tests

- `internal/agent/cloud_backend_stream_test.go`:
  - `TestCloudBackend_DefaultsUseIdleTimeoutNotClientTimeout`: asserts `client.Timeout == 0` and `streamIdleTimeout == 120s` for both constructors.
  - `TestCloudBackend_HealthySlowStreamSurvivesBeyondIdleWindow`: active SSE stream taking >2x the test `streamIdleTimeout` succeeds when chunk gaps remain under the idle threshold.
  - `TestCloudBackend_OpenAI_IdleTimeoutAbortsStalledStream` and `TestCloudBackend_Anthropic_IdleTimeoutAbortsStalledStream`: stalled OpenAI and Anthropic streams abort with `"stream idle timeout"` wrapping `context.DeadlineExceeded`.
  - `TestCloudBackend_ParentCancelDoesNotReportIdleTimeout`: parent `ctx` cancellation during blocked request returns `context.Canceled` without misreporting idle timeout.
  - `TestCloudBackend_OpenAI_ReasoningContentStreamsToThoughtWriterOnly` and `TestCloudBackend_Anthropic_ThinkingDeltaStreamsToThoughtWriterOnly`: reasoning chunks route strictly to `ThoughtWriter` and never enter `resp.Content` or plain writers.
  - `TestCloudBackend_WithDisableThinking_SendsAndFallsBackOn400` and `TestCloudBackend_RejectsBothStreamOptionsAndChatTemplateKwargs`: verifies `chat_template_kwargs.enable_thinking: false`, sticky 4xx fallback, and combined fallback with `stream_options`.
  - `TestCompactManually_DisablesThinkingOnCloudBackend`: end-to-end compaction sends `chat_template_kwargs.enable_thinking: false`.
  - `TestBuildBackend_AppliesStreamIdleTimeoutAndThinkingOptions`: verifies positive/negative `StreamIdleTimeout` and `Thinking` propagation through `BuildBackend`.
- `internal/console/bridge_test.go` and `internal/console/thought_test.go`:
  - `TestStreamWriterWriteThoughtWrapsAndClosesThinkTags`: covers `WriteThought` + `Write`, `CloseThought` without trailing answer, and multi-round `WriteThought` → `CloseThought` → `WriteThought` → `Write` on one writer.
  - `TestExtractThought` and `TestParseStreamThought`: multi-span extraction, interleaved answer preservation, and in-flight multi-span streaming (`inThought == true` and closed).

## Alternatives

- **Raise `http.Client.Timeout` statically (e.g. 600s).** Rejected: a higher fixed timeout still kills streams legitimately exceeding the constant and leaves stalled connections hanging until `timeout` expires instead of failing fast on stream silence.
- **Inject `<think>...</think>` directly into `ChatStream`'s `io.Writer` for all callers.** Rejected: `-p` piped runs (`axis agent -p "..." | jq`) write `stdout` through `ColorWriter` and expect assistant output without injected `<think>` tags when a server already separated `reasoning_content`.
- **Disable thinking via server/proxy config only.** Rejected: compaction (`summarizeViaBackend`) and interactive turns share the same endpoint; per-request `WithDisableThinking` with 4xx fallback handles both automatically without proxy reconfiguration.

## Research

- `internal/chat/client.go` already sets `http.Client{Timeout: 0}` so `ctx` bounds Ollama stream duration cleanly; `CloudBackend` diverged by keeping a 120s `Client.Timeout`.
- Open-source Apache-2.0 CLI agents (`QwenLM/qwen-code`, `docs/configuration/model-providers.md`) separate `streamIdleTimeoutMs` from total request timeout and pass `chat_template_kwargs.enable_thinking` for `qwen-chat-template` OpenAI-compatible servers.
- Post-submission PR review on PR #643 (`internal/console/thought.go`) surfaced multi-round `<think>` truncation in `extractThought` and `parseStreamThought` when a single `streamWriter` spans multiple tool rounds; the spec and regression suite cover that fix directly.
- Measurement: hermetic race tests on the local test suite (`cmd/axis`, `internal/agent`, `internal/console`) verify timing behavior deterministically with 30–45ms idle windows without modifying cluster endpoints.

## Review
