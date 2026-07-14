# AXIS — Line-Anchored Execution Plan (backend fix → TUI UX)

Target: `toasterbook88/Axis` @ `main` HEAD `81dbad1`. All citations live-verified against source at this HEAD (docs untrusted). Surgical, zero speculative refactoring.

TUI render path (shared context for UX claims):
- `axis status` fetches the daemon snapshot and renders tables — watch-mode clears+redraws each tick (`cmd/axis/status.go:53-72,99-107,122-185,248-261`; `internal/ui/table.go:15-26`).
- `axis chat` / `axis agent` start a spinner and block on `ChatStream` (`cmd/axis/chat.go:128-141,204-210,269-274`; `internal/agent/agent.go:313-318`).
- Interactive pickers render via `ui.Select` (`internal/ui/select.go:62-79,139-145`); spinner lifecycle `internal/ui/spinner.go:23-30,56-80`.

---

## 1. Layering inversion (L4 → L5)

- **Problem:** L4 safety gate + executor import upward into L5 `knowledge`/`events`, coupling the execution path to advisory aggregates. Structural (compile/coupling) — not a direct render stall, but it forces `knowledge`/`events` (and their transitive `snapshotview`/`state`/`cortex`) to initialize on the hot execution path the TUI drives.
- **Target Citations (live-verified):**
  - `internal/safety/blocker.go:3-8` (imports `internal/knowledge`), `internal/safety/blocker.go:19-20` — `func Check(k *knowledge.ClusterKnowledge, desc string, isKnownBad func(string) bool) BlockResult`.
  - `internal/execution/guarded.go:20-24` (imports `internal/events` + `internal/knowledge`); call sites `guarded.go:378-380,421-424,485-488` (`events.EmitToBuffer`), `:510` (`knowledge.ExecutionContextJSON`), `:1200-1210` (`events.EmitToBuffer`).
  - `go list` confirms: safety→knowledge; execution→events,knowledge.
- **Surgical Fix Plan:** Define a consumer-side interface in L4 — `safety.Check` takes `type KnownBad interface{ IsKnownBad(string) bool }` (or just the two scalar fields it reads) instead of `*knowledge.ClusterKnowledge`; caller (agent/api/daemon) adapts `knowledge` to it. For `execution`, hoist the `knowledge.ExecutionContextJSON` build + `events.EmitToBuffer` emission to the L5 caller and pass results/an `Emitter` interface down (see #2). Pure dependency inversion; no behavior change.
- **TUI UX Impact:** Removes L5 init from the guarded-exec path the agent/chat TUI triggers → lower first-tool-call latency in `axis agent` (fewer packages constructed before the spinner can stop at `cmd/axis/agent.go:416-431`). Enables #2/#3 to bound stalls without dragging the advisory graph along.

## 2. `events` global bus + unbounded goroutines

- **Problem:** Process-global bus; every listener + webhook dispatch spawns an unbounded goroutine; webhook retry blocks on a bare `time.Sleep`. Under event bursts (a placement/exec run emits several), the emitter — called on the agent/exec render path — competes for CPU/locks and swallows panics.
- **Target Citations (live-verified):**
  - Globals: `internal/events/events.go:17-20` (cortex client+mutex), `:124-128` (listeners), `:194-198` (ring buffer, `bufferSize=100`), `:204-207` (interests), `:250-256` (queue + `sync.Once` + flush chans).
  - Unbounded fan-out: `events.go:163-168` (`go func(fn Listener, e Event)`), `:272-273` (`go eventWorker()`), `:310-320` (`go func(ev Event)` cortex publish).
  - Webhooks: `internal/events/webhooks.go:15-20` (globals), `:54-60` (`go func(targetURL, body)`), `:69` (`http.NewRequest`, no ctx), `:88-89` (`time.Sleep(backoff)`).
  - Global mutation from L5: `internal/mcp/server.go:113-129` (`events.RegisterListener`).
- **Surgical Fix Plan:** (a) Bound dispatch: replace per-callback `go` at `events.go:163-168` and `webhooks.go:54-60` with a fixed worker pool draining a buffered channel (drop-oldest on full — bus must never block the emitter). (b) Make retry cancellable: `select { case <-time.After(backoff): case <-ctx.Done(): }` at `webhooks.go:88-89`, threading ctx into `http.NewRequestWithContext` at `:69`. (c) Recover+log in worker bodies (no silent panic). Minimal move: keep the package-global singleton; only change dispatch mechanics.
- **TUI UX Impact:** Emitter becomes O(enqueue) and never blocks → `axis agent` streaming writes (`internal/agent/agent.go:313-318`) don't stutter when a tool emits events mid-stream; eliminates goroutine pile-up that can starve the render goroutine during multi-event runs. Cancellable retry means Ctrl-C in the TUI returns promptly instead of waiting out a webhook backoff sleep.

## 3. Daemon lock SPOF (highest direct TUI impact)

- **Problem:** One `sync.RWMutex` guards both the refresh writer and every reader. `RefreshNow` collects facts over SSH then takes the write lock; concurrent `Snapshot()`/`Meta()` readers (what `axis status` calls) block for the refresh's critical section. In `status --watch`, ticks landing during a refresh stall → visible frame hitch / stale redraw.
- **Target Citations (live-verified):**
  - `internal/daemon/daemon.go:79-90` — `mu sync.RWMutex` guarding snapshot state.
  - Refresh writer: `daemon.go:147-149` (`RefreshNow`→`RefreshWithTrigger`), `:646-655`, `:660-672`, `:712`, `:720-724`, `:739-741` (all `d.mu.Lock()/Unlock()` around state swap + persist).
  - Readers on the same lock: `daemon.go:866-873` (`Snapshot()` `RLock`+clone), `:888-905` (`Meta()` `RLock`), `:876-881` (`Invalidate()`).
  - TUI consumer: `cmd/axis/status.go:53-63,82-93` (`fetchStatusSnapshot`→`daemon.FetchSnapshot`), watch redraw `:65-72`.
- **Surgical Fix Plan:** Store the published snapshot in an `atomic.Pointer[models.ClusterSnapshot]`. Refresh builds the new snapshot off-lock and does one `Store()` (atomic swap) at the `:660-672` update site; `Snapshot()`/`Meta()` become lock-free `Load()` (`:866-905`). Keep `mu` only to serialize concurrent *refreshes* (not readers). No API/signature change.
- **TUI UX Impact:** `axis status` (and `--watch`) reads never block on a refresh → removes render stalls lasting up to the full refresh duration (SSH fact-collect = seconds on a multi-node cluster) from the status/watch redraw path. Watch-mode redraw cadence (`status.go:65-72`) stays smooth regardless of background refresh; eliminates the "frozen table" window.

## 4. Full-file JSON read-modify-write under lock

- **Problem:** Each mutation holds the in-memory/file lock across read→decode→mutate→full-file atomic rewrite → O(state-size) write amplification and serialized mutations. Reservation/state writes sit behind guarded-exec, which the agent/chat TUI drives; a large ledger makes every reserve pay full-file marshal+fsync while holding the lock.
- **Target Citations (live-verified):**
  - `internal/state/state.go:114-131` (`Update` `Flock(LOCK_EX)`), `:133-147` (locked read/migrate/mutate/`saveTo`), `:150-166` (`loadStateFile` full `ReadFile`+`Unmarshal`), `:741-766` (`saveTo` full marshal + `persist.WriteFileAtomic`).
  - `internal/reservation/persist.go:90-102` (`Load` holds `l.mu` across file-lock), `:104-124` (full unmarshal under `l.mu`), `:135-150` (`Save` holds `l.mu` across lock), `:153-169` (`saveLocked` marshal-all + `WriteFileAtomic`).
  - `internal/skills/skills.go:71-74` (`Save` full-store `MarshalIndent`+`WriteFileAtomic`).
- **Surgical Fix Plan:** Narrow the critical section — mutate the in-memory struct under `l.mu`, snapshot it, release `l.mu`, then marshal + `WriteFileAtomic` under the *file* lock only (`reservation/persist.go:135-169`; mirror in `state.go` by doing decode/mutate under flock but not blocking unrelated in-mem readers). Durability preserved (`WriteFileAtomic` already temp+fsync+rename). Optional: dirty-flag debounce for `skills.Save`.
- **TUI UX Impact:** `axis agent`/tool reservations don't serialize behind a full-file rewrite → steadier per-tool latency as ledger/state grow; the spinner around `ChatStream`/tool calls stops closer to actual work-done rather than lagging on disk I/O held under lock. Prevents growing state files from adding creeping input-to-response delay in the agent REPL.

## 5. Missing hardening (timeouts, ctx, panics, SSRF)

- **Problem:** (a) No-timeout HTTP clients on the chat/LLM path → a stalled model hangs the spinner indefinitely with no way out but kill. (b) `ui.Select` re-panics a recovered input-handler panic → a transient terminal read error crashes the whole TUI. (c) `MustRegister` panics. (d) Contextless local `exec.Command` in fact collection → uncancellable hangs feeding `status`. (e) Unvalidated webhook/MCP URLs → SSRF.
- **Target Citations (live-verified):**
  - No-timeout clients: `internal/chat/client.go:38-43`, `internal/chat/ollama.go:22-27`, `internal/llmrouter/engine.go:117-123` (`&http.Client{}`).
  - TUI crash: `internal/ui/select.go:185-190` (`panic(r)` re-raise). Registry: `internal/llmrouter/registry.go:47-53` (`panic(err)`).
  - Contextless exec (feeds `status`): `internal/facts/local.go` — `:322,328,422,456,459,463,469,488,506,511,519,621,662,669,688,720,800,884,916,1201,1220,1262,1294,1435,1582,1624,1675,1790` (all `exec.Command`, not `CommandContext`); plus `internal/execution/guarded.go:1415` (`context.Background()` git stash pop).
  - SSRF: `internal/events/webhooks.go:22-28` (`SetWebhooks` stores unvalidated URL), `:64-75` (URL → `http.NewRequest`/`Do`); `internal/mcpclient/connection.go:173-189` (`cfg.URL`→`NewStreamableHttpClient`).
- **Surgical Fix Plan:** (a) Set explicit `Timeout` on the three `http.Client{}` constructions. (b) At `ui/select.go:185-190`, convert the recovered panic into a returned error (graceful terminal restore) instead of `panic(r)`. (c) `MustRegister`→return error where callable, keep panic only in package init. (d) Thread the collector ctx into `exec.CommandContext` across `facts/local.go` (and drop the `context.Background()` at `guarded.go:1415`). (e) Add one `validateOutboundURL()` (require http/https; reject loopback/link-local/`169.254.169.254`/metadata unless allowlisted) called before webhook (`webhooks.go:64`) and MCP (`connection.go:173`) dispatch. Independent, low-blast-radius.
- **TUI UX Impact:** (a) Bounded HTTP timeout → the chat/agent spinner (`cmd/axis/chat.go:128-141`) fails fast with an error instead of hanging forever on a dead model, keeping the prompt responsive. (b) `ui.Select` no longer takes the process down on a transient input glitch → menu-driven flows (`axis init`/model picker `cmd/axis/agent.go:226-231`) recover gracefully with the terminal restored. (d) Cancellable fact probes → `axis status` honors its 60s ctx (`status.go:82-93`) and Ctrl-C instead of blocking on a wedged `nvidia-smi`/`system_profiler`.

---

### Priority order (by direct TUI UX gain, simplest first)
1. **#3 daemon atomic.Pointer** — biggest, most localized win; kills status/watch render stalls.
2. **#5a/#5b/#5d hardening** — small, independent; removes hangs + TUI crashes.
3. **#2 bounded event dispatch** — steadies agent streaming under event bursts.
4. **#4 narrow lock scope** — steadier agent/tool latency as state grows.
5. **#1 dependency inversion** — structural; unblocks clean interfaces for #2/#3.

<!-- verify-plan-progress: matrix -->
## Progress Tracker

| Item | Fix | Status | Tests |
| --- | --- | --- | --- |
| #1 | Decouple safety from knowledge | [ ] | internal/safety/blocker_iface_test.go::TestCheckDecoupledFromKnowledge |
| #2 | Bound event dispatch fan-out | [x] | internal/events/bounded_test.go::TestEventBusBoundedDispatch |
| #3 | Make daemon snapshot reads lock-free | [x] | internal/daemon/lockfree_test.go::TestDaemonReadsLockFreeDuringRefresh, internal/daemon/lockfree_test.go::BenchmarkSnapshotUnderRefresh |
| #4 | Narrow persistence lock scope | [x] | internal/reservation/persist_lock_test.go::TestSaveDoesNotHoldMutexDuringIO |
| #5 | Add missing timeout/input/URL hardening | [x] | internal/ui/select_panic_test.go::TestSelectRecoversInputPanic |
