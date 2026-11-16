# CloudBackend Stream Idle Timeout & Out-of-Band Thinking Architecture

**Date:** 2026-11-16
**Scope:** `internal/agent` (`cloud_backend.go`, `color_writer.go`, `compaction.go`, `model_target.go`), `internal/console` (`activity.go`, `bridge.go`, `model.go`, `thought.go`)
**Spec:** [`docs/superpowers/specs/2026-11-16-cloud-stream-idle-and-thinking-design.md`](../superpowers/specs/2026-11-16-cloud-stream-idle-and-thinking-design.md)
**Layer:** Layer 5 (Advisory) — subordinate to observed cluster state

This document is the contributor guide and architecture reference for `CloudBackend` idle stream protection, out-of-band reasoning transport, multi-round console thought rendering, and per-call thinking opt-out.

---

## 1. Explanation: Why Streaming Needed Separate Idle & Thinking Channels

### 1.1 Stream Idle Timeouts vs. `http.Client.Timeout`

Before this change, `NewCloudBackendWithKey` and `NewOpenAICompatibleBackend` set `http.Client{Timeout: 120 * time.Second}`. Go's `http.Client.Timeout` bounds the **entire HTTP exchange from request dial through final response-body EOF**:

- A healthy multi-minute streaming turn (for example slow local decoding or long-context prefill within the caller's `--timeout`) was aborted at 120s even while SSE tokens arrived steadily.
- Conversely, removing `Client.Timeout` without an activity watchdog would let half-open sockets hang silently until `--timeout` (default `5m`) expired.

`CloudBackend` now matches `internal/chat.Client` by using `http.Client{Timeout: 0}` and enforcing inactivity through `streamIdleGuard`, resetting a `120s` default timer when headers arrive and on every positive read from `resp.Body`. Callers still control total wall-clock budget with `ctx` (`--timeout`).

### 1.2 Out-of-Band `ThoughtChunkMsg` vs. Inline `<think>` Tag Injection

OpenAI-compatible reasoning servers (`delta.reasoning_content`, `delta.reasoning`) and Anthropic streams (`thinking_delta`) separate reasoning from visible assistant output:

- Writing raw reasoning to `io.Writer` or wrapping it synthetically in `<think>...</think>` inside `StreamChunkMsg` breaks piped stdout (`axis agent -p "..."`) and fails if reasoning text mentions literal `</think>`.
- Routing reasoning through the optional `agent.ThoughtWriter` interface keeps `resp.Content` and plain `io.Writer` sinks clean, while `*console.streamWriter` forwards chunks out of band as `console.ThoughtChunkMsg`.
- Models that still emit inline `<think>...</think>` inside normal content continue to work through `splitThoughtSpans` in `internal/console/thought.go`, which supports multiple rounds per turn cleanly.

---

## 2. Reference: Architecture & Component Contracts

```text
CloudBackend.ChatStream(ctx, msgs, tools, w)
  ├─► openOpenAIStream / doStreamRequest(ctx)
  │     └─ streamIdleGuard (120s default idle timer, deadline serialized under g.mu)
  │          ├─ header arrival ──────────────────────► guard.touch()
  │          └─ idleReadCloser.Read(p) n > 0 ────────► guard.touch()
  │
  ├─► Reasoning delta (reasoning_content / reasoning / thinking_delta)
  │     └─ writeThought(w) ─► *ColorWriter.WriteThought ─► *streamWriter.WriteThought
  │                                                          ├─ flush pending answer runes first
  │                                                          └─ send ThoughtChunkMsg{Turn, Text}
  │     └─ boundary (content / tool call / stream EOF) ─► closeThought(w) ─► ThoughtChunkMsg{Turn, Done: true}
  │
  ├─► Visible delta (delta.content / text_delta)
  │     └─ w.Write(...) ─► *ColorWriter.Write ─► *streamWriter.Write ─► closeThoughtLocked + coalesce StreamChunkMsg
  │                                                                     (resp.Content excludes reasoning)
  └─► console.Model.Update(...)
        ├─ ThoughtChunkMsg  ─► thoughtBuf (+ "\n\n" between rounds via oobThoughtSep), inOutOfBandThought, stampThinkingSpan
        ├─ StreamChunkMsg   ─► stream, splitThoughtSpans (legacy inline <think>), stampThinkingSpan
        └─ TurnDoneMsg      ─► joinNonEmptyThoughts(oobThought, inlineThought) ─► NewThinkingEntry (Elapsed) + NewAgentEntry
```

### 2.1 `CloudBackendOptions` & Constructors (`internal/agent`)

| Type / Function | Field / Parameter | Contract |
| --- | --- | --- |
| `NewCloudBackendWithKey`, `NewOpenAICompatibleBackend` | `client.Timeout`, `streamIdleTimeout` | Sets `client.Timeout = 0` and initializes `streamIdleTimeout = defaultStreamIdleTimeout` (`120s`). |
| `CloudBackendOptions` (applied via `BuildBackend` for `ProtocolOpenAI` & `ProtocolCloud`) | `StreamIdleTimeout` | `0` keeps the `120s` default; `> 0` overrides the idle window; `< 0` sets `streamIdleTimeout = 0` (disables the idle watchdog). |
| `CloudBackendOptions` | `Thinking` (`*bool`) | `nil` leaves server defaults untouched (omits `chat_template_kwargs`); `&true` / `&false` sends `chat_template_kwargs.enable_thinking` on OpenAI-compatible streams. |
| `WithDisableThinking(ctx)` | `context.Context` | Highest precedence (`resolveThinking`): forces `enable_thinking: false` on OpenAI-compatible requests regardless of `CloudBackendOptions.Thinking` (`&true`, `&false`, or `nil`). Used by `summarizeViaBackend` in `internal/agent/compaction.go`. |
| Anthropic (`providerKind == "anthropic"`) | `Thinking`, `WithDisableThinking` | Intentionally ignored by `streamAnthropic` (`thinking_delta` streaming and `streamIdleGuard` still apply). |

### 2.2 `streamIdleGuard` Deadline & Error Semantics (`internal/agent/cloud_backend.go`)

1. **Initialization:** If `timeout > 0`, `newStreamIdleGuard` acquires `g.mu`, records `g.deadline = time.Now().Add(timeout)`, and arms `time.AfterFunc(timeout, g.onTimer)`. Calling with `parent == nil` defaults safely to `context.Background()`.
2. **Activity reset (`touch()`):** Under `g.mu`, if `!g.stopped && !g.expired.Load() && g.timer != nil`, `touch()` advances `g.deadline = time.Now().Add(g.timeout)` and resets `g.timer`. Post-expiry and post-`Close()` calls are no-ops.
3. **Serialized timer firing (`onTimer()`):** Under `g.mu`, if `g.stopped || g.expired.Load()`, `onTimer` returns immediately. If `touch()` extended `g.deadline` while an earlier timer callback was queued waiting on `g.mu`, `rem := time.Until(g.deadline) > 0` reschedules `g.timer.Reset(rem)` without canceling. Otherwise `g.expired.Store(true)` is set and `g.cancel()` fires outside the lock.
4. **Error attribution (`wrapErr`):** Returns `fmt.Errorf("stream idle timeout after %s: %w", g.timeout, context.DeadlineExceeded)` **only** when `g.expired.Load()`, `errors.Is(err, context.Canceled)`, and `parent == nil || parent.Err() == nil`. Parent cancellations and deadlines always surface the parent error instead of misreporting an idle timeout.

### 2.3 OpenAI Compatibility 4xx Fallbacks (`openOpenAIStream`)

Up to `maxAttempts = 3` tries per stream open:
- Sends `stream_options: {"include_usage": true}` unless `b.noStreamUsage` is already set.
- Sends `chat_template_kwargs: {"enable_thinking": *thinking}` when `resolveThinking(ctx) != nil` and `b.noChatTemplateKwargs` is unset.
- On an HTTP 4xx response (`rejectsRequestField`, case-insensitive substring check):
  - Response body naming `stream_options` -> sets sticky `b.noStreamUsage.Store(true)` and retries.
  - Response body naming `chat_template_kwargs` or `enable_thinking` -> sets sticky `b.noChatTemplateKwargs.Store(true)` and retries.
  - Any other HTTP status/body -> fails immediately on that attempt without flipping sticky flags.

### 2.4 `ThoughtWriter`, `ColorWriter`, and `console.streamWriter` Boundaries

- **`agent.ThoughtWriter`** (`WriteThought(string)`, `CloseThought()`): implemented by `*agent.ColorWriter` (delegates to underlying writer via type assertion, without flushing `cw.buf` pending backtick prefixes) and `*console.streamWriter`.
- **Plain writers (`io.Writer` only):** `writeThought` / `closeThought` check `w.(ThoughtWriter)` and become no-ops when the sink does not implement it, keeping `-p` non-interactive output free of reasoning tokens.
- **`*console.streamWriter` (`internal/console/bridge.go`):**
  - `WriteThought`: ignores empty strings and closed writers; calls `w.flushLocked(true)` for any buffered complete answer runes before setting `w.inThought = true` and sending `ThoughtChunkMsg{Turn: w.turn, Text: thought}`. Trailing incomplete UTF-8 sequences wait for their continuation bytes or `Close()`.
  - `Write`: closes an active out-of-band thought (`closeThoughtLocked()`) before enqueueing answer bytes.
  - `CloseThought` & `Close`: send `ThoughtChunkMsg{Turn: w.turn, Done: true}` once per open thought span; calls after `Close()` or outside `inThought` do not emit duplicate `Done` signals.

### 2.5 `console.Model` Thinking State & Multi-Round Reconciliation

- **`stampThinkingSpan()`:** Starts `thoughtStart` when entering either out-of-band (`inOutOfBandThought`) or inline `<think>` (`parseStreamThought`) activity, and accumulates `thoughtElapsed += now.Sub(thoughtStart)` on each transition out of thinking so multi-round tool turns sum reasoning durations instead of overwriting earlier rounds.
- **`liveActivity()`:** Renders `<spinner> thinking` during `m.inOutOfBandThought || inlineInThought || strings.TrimSpace(answer) == ""` unless a pending tool card, overlay, or `turnCancelling` state takes priority.
- **`finishTurn(err)` & `resetThoughtState()`:** Combines `oobThought` (`thoughtBuf`) and `inlineThought` (`extractThought(stream)` via `splitThoughtSpans`) using `joinNonEmptyThoughts`, emits `ThinkingEntry` with accumulated `Elapsed`, retains the combined thought in `m.lastThought` for `/thought`, emits `AgentEntry` for any answer, and resets all buffers and timers before draining queued steering prompts.
- **Turn Attribution (`TurnID`):** `ThoughtChunkMsg` passes through `Model.stale(msg.Turn)` just like `StreamChunkMsg`: non-zero mismatched or retired turns are dropped cleanly (while `TurnID == 0` stays turn-independent).

---

## 3. How-To Recipes for Contributors

### 3.1 Build a `CloudBackend` with Custom Idle Timeout or Thinking Default

```go
disableThinking := false
backend, err := agent.BuildBackend(agent.ModelTarget{
    Model:    "local-model",
    Protocol: agent.ProtocolOpenAI,
    Endpoint: "http://127.0.0.1:8080",
}, agent.CloudBackendOptions{
    StreamIdleTimeout: 30 * time.Second, // 0 keeps 120s; <0 disables
    Thinking:          &disableThinking,
})
```

### 3.2 Force Non-Thinking Execution on an Internal Helper Turn

Wrap the call context with `agent.WithDisableThinking(ctx)` as `summarizeViaBackend` in `internal/agent/compaction.go` does:

```go
resp, err := backend.ChatStream(agent.WithDisableThinking(ctx), msgs, nil, io.Discard)
```

Even if the backend was configured with `Thinking: &true`, `WithDisableThinking(ctx)` overrides it for that request and automatically falls back once if the target server rejects `chat_template_kwargs` / `enable_thinking` with a 4xx mentioning either field.

### 3.3 Write Deterministic Hermetic Tests Without Live Cluster Endpoints

1. Use `net/http/httptest.NewServer` on loopback (`127.0.0.1`) with small `b.streamIdleTimeout` (`25ms–50ms`) so suites finish cleanly under `-race`.
2. Ensure stalled test handlers select on `<-r.Context().Done()` and unblock a `done` channel before `defer server.Close()` so `httptest.Server.Close()` never hangs waiting on open handler goroutines.
3. For timer-race coverage (`streamIdleGuard`), test `touch()` + `onTimer()` directly rather than relying on tight microsecond `time.Sleep` margins that can oversleep under `-race` scheduler jitter.
4. Keep test fixtures free of private cluster identifiers and high-entropy token lookalikes (`leakguard` / `./hack/verify-public-boundary.sh`).
5. Validate after edits with:
   - `./hack/hermetic-go-test.sh ./internal/agent ./internal/console ./cmd/axis -race -count=1`
   - `./hack/ci-preflight.sh` (or inside a linked git worktree with a matching cached Go toolchain: `GOTOOLCHAIN=local GOFLAGS=-buildvcs=false ./hack/ci-preflight.sh`).
