# Decision: AXIS agent surface uses a transcript model, not a dashboard

**Date:** 2026-08-24
**Scope:** `cmd/axis/tui`, `cmd/axis/agent.go`, `internal/agent`, `internal/llmrouter`, docs
**Decision:** Two surfaces. `axis tui` stays observational (panes, alt-screen).
The agent command center is transcript-first with dim footer chrome and
transient overlays. Both render over the same fact plane; neither becomes a
second authority path.

## 1. Context

A proposal argued that `axis tui` should become an "Agent Command Center": a
sticky runtime header, a two-pane split with the agent workspace on the left
and persistent fleet/endpoint panes on the right, an omnibar with its own
command grammar, and single-letter action keys.

Three established agent harnesses were surveyed as prior art (Pi, its OMP
fork, and Hermes). All three independently converged on a different shape:

- **Transcript-first rendering on the main screen.** Fullscreen/alt-screen is
  an opt-in mode toggle, not the architecture. The terminal owns scrollback;
  the renderer updates only changed lines.
- **Persistent chrome is two or three dimmed lines at the bottom.** Working
  directory and session on one line; usage, context, and the active model on
  the next; a single truncated line for subsystem status. The active model
  lives bottom-right, not in a top header. When idle, the status row renders
  blank.
- **Everything else is a transient overlay** — model picker, session picker,
  settings, trust prompts. None is a mode reached by a single letter.
- **The agent core is headless behind a protocol; the UI is a client.**
  This is the same constraint already recorded in
  `docs/plans/tui-modernization-2026-08-24.md` ("TUI is an alternative
  renderer for existing commands, not a replacement").
- **Approvals are protocol requests, not view state.** Choices are computed by
  the authority, the payload is redacted before it reaches the UI, and a
  reconnecting client can re-read the pending approval.
- **Steering queues replace bespoke interruption rules.** A message submitted
  mid-turn is queued for delivery at a turn boundary; a separate binding
  queues until all work completes; abort restores the queue to the editor.
- **Reads never block the input loop.** Picker payload construction, backend
  probes, and periodic status refresh all run off the loop. In the surveyed
  implementations, violating this produced frozen UIs where approvals and
  interrupts sat unread behind a slow read.

The proposal's shape — one surface, dashboard-shaped, with the agent as a pane
— is the combination none of the three chose, and it is the source of every
conflict identified in review: the `m`-key collision with
`docs/plans/tui-phase3-plan-2026-08-24.md`, the duplicate command grammar
against `handleREPLSlashCommand`, and the layering pressure against the
Truth Rule.

## 2. Decision

### 2.1 Two surfaces

| Surface | Shape | Purpose |
| --- | --- | --- |
| `axis tui` | Alt-screen, panes, fleet table + inspector | Observational fleet dashboard. Read-only plus advisory modals. |
| Agent surface | Main-screen transcript, dim footer, overlays | Conversational command center. Actions route through guarded execution. |

The existing `cmd/axis/tui` fleet table, snapshot loader, and placement modal
are **retained unchanged**. They are not obsolete; they are the observational
surface. Phase 3 keybindings (`m` model lifecycle, `p` placement, `l`
reservations, `e` events) stay owned by `axis tui`.

The agent surface has no single-letter action keys, so it has no key namespace
to contest. Model switching is `/model`. Model lifecycle is `/model start`.

### 2.2 The agent surface is a renderer, not an authority

Every action reachable from the agent surface resolves to an existing
`axis` code path:

- Read verbs load through `internal/runtimectx`.
- Execution verbs go through `internal/execution` (safety -> reserve -> run ->
  release). The surface never calls `internal/transport` directly.
- Approval choices come from `internal/safety` + `internal/agent`, not from
  the view.

## 3. Non-goals

- Replacing the Cobra CLI or making the TUI the only way to reach a verb.
- A second command parser. The agent surface reuses
  `handleREPLSlashCommand` (`cmd/axis/agent.go`).
- Port scanning peers to find inference endpoints.
- Merging the fleet dashboard into the transcript surface.
- Learned/auto approvals. Session-scoped approval tiers only.

## 4. Primary screen wireframe

```text
  AXIS 5 nodes - /help for commands
  Loaded: AGENTS.md - 3 skills - mcp: cortex

  > which node has the most free VRAM?

  * node-a - 28 GB free of 48 GB (snapshot 12s old)
    node-b has 16 GB free. Worker nodes report no GPU.

  > start qwen-32b there

  * axis model start --node node-a --weights qwen-32b --port 8000
    +- approve ---------------------------------------------+
    | node-a - 24 GB VRAM est - 28 GB available             |
    | safety 35/100                                         |
    | (o)nce  (s)ession  (a)lways  (d)eny                   |
    +-------------------------------------------------------+
  = approved - started - qwen-32b @ node-a:8000 (4.2s)

+--------------------------------------------------------------+
| > ask or /command                                            |
+--------------------------------------------------------------+
~/axis (main) - cluster-work
^12k v3k  38.1%/128k (auto)      (llama.cpp) qwen-32b @ node-a:8000
fleet 5/5 ok - 2 gpu free - snap 12s - 1 lease
```

Rules:

- Footer lines are dimmed. No borders, no logo, no clock, no live latency.
- Footer line 3 is a **status line**, keyed by subsystem, sorted by key,
  joined, truncated to one line. `fleet`, `mesh`, and `leases` each own a key.
  New subsystems add a key; they do not negotiate for pane area.
- The status/working row renders blank when nothing is in flight.
- Transcript entries carry their own timestamp and freshness. A fact printed
  into the transcript scrolls away; it never masquerades as live.

## 5. Overlays

### 5.1 Model picker (`/model`, or the picker hotkey)

```text
+- select backend ----------------------------------------------+
| filter: _                                                     |
+---------------------------------------------------------------+
| PROVIDER   MODEL              TARGET            HEALTH   AGE  |
| llama.cpp  qwen-32b-coder     node-a:8000       ok  12ms  3s  |
| ollama     llama-3.3-70b      node-b:11434      ok  24ms  3s  |
| mlx        phi-4-14b          node-c:8080       degraded  9s  |
| vllm       mistral-small-24b  node-a:8001       disabled  --  |
| anthropic  claude-*           remote            ok   --   3s  |
+---------------------------------------------------------------+
| enter switch  j/k move  / filter  a re-probe  esc cancel      |
+---------------------------------------------------------------+
```

- `* ` marks the currently selected target.
- Health is `ok | degraded | unreachable | disabled`, derived from
  `llmrouter.BackendProbe` (`OK`, `Enabled`, `Message`). `ACTIVE`, `READY`,
  `STANDBY`, and `ONLINE` are **not** used: selection and health are
  orthogonal axes and are rendered separately.
- `AGE` is probe age. Latency is only shown alongside its age, never alone.
- Endpoints render as `loopback | private | remote` in any non-interactive
  output. Raw URLs stay inside the interactive overlay.
- Payload construction is asynchronous. The overlay opens immediately with
  cached probe results and repaints as fresh ones land.

Switch semantics:

1. The switch is queued against the next turn boundary. An in-flight request
   is never interrupted.
2. The new target must pass a health probe before it becomes selected.
3. On failure the previous target stays selected and the failure is written
   to the transcript, naming the unchanged target.

### 5.2 Approval

```text
+- approve -------------------------------------------------+
| tool     bash                                             |
| command  <redacted-safe rendering>                        |
| node     node-a                                           |
| est      24 GB VRAM, 32 GB RAM                            |
| avail    28 GB VRAM (snapshot 12s old)                    |
| safety   35/100                                           |
|                                                           |
| (o)nce  (s)ession  (a)lways  (d)eny  (e)xplain            |
+-----------------------------------------------------------+
```

- Choices map 1:1 to `agent.ConfirmResult`: `once` -> `ConfirmYes`,
  `deny` -> `ConfirmNo`, `session`/`always` -> `ConfirmAlways`,
  and a denial tier maps to `ConfirmNever`.
- Choices are computed by the authority, not the view. When
  `safety.BlockResult.Blocked` is true (score >= 80), the only choices offered
  are `deny` and `explain`; there is no approve path past a hard block.
- The score is always rendered numerically with its reason. `PASS`/`FAIL` is
  never rendered: the 70-79 risky-but-allowed band must remain visible.
- The command string is redacted through the shared secrets seam before it
  reaches the view. The view is an egress transport.
- Pending approvals live in the execution layer, not the model. The surface
  re-reads the oldest unresolved approval on attach.

## 6. Discovery flow

`a` re-probes **declared** backends. It does not scan.

```text
  > /discover
  * probing 5 declared backends...
  = node-a:8000    ok  12ms   qwen-32b-coder      ai.yaml
  = node-b:11434   ok  24ms   llama-3.3-70b       ai.yaml
  = node-c:8080    unreachable: dial timeout      ai.yaml
  = node-a         ok         3 resident models   node facts
  = remote         ok         catalog only        ai.yaml
    4 of 5 reachable - 2 endpoints changed since last probe
```

Sources, in provenance order:

| Source | Origin |
| --- | --- |
| `ai.yaml` | Declared backend in `~/.axis/ai.yaml` |
| `node facts` | `Ollama.Port` / `ResidentModels[].Port` from collected facts |
| `nodes.yaml` | Backend bound to a configured node |

An endpoint that appears only in node facts is **listed but not selectable**
until it is declared. No probe result auto-registers a backend.

`BackendProbe` gains a `ProbedAt time.Time` field so age is derivable rather
than assumed.

## 7. Omnibar grammar

One parser, shared with the agent REPL (`handleREPLSlashCommand`).

| Class | Form | Routing |
| --- | --- | --- |
| Natural language | `which node has free VRAM?` | agent turn |
| Slash command | `/status`, `/facts node-a`, `/model`, `/reservations` | existing REPL handler |
| Bang | `!cmd` runs and feeds output to the agent; `!!cmd` runs without | guarded execution |
| Approval | `o` / `s` / `a` / `d` while an approval is pending | execution layer |

Explicitly **not** added: `@node <verb>` targeting and `/place <spec>`. Both
would be new grammar with no CLI equivalent. Node targeting is an argument to
an existing verb (`/facts node-a`), and placement is `/task place`.

Editor behavior:

- `Enter` submits, or queues a steering message at the next turn boundary when
  a turn is in flight.
- `Alt+Enter` queues a follow-up delivered after all work completes.
- `Escape` aborts the turn and restores queued messages to the editor.
- History recall and completion come from the existing readline completer
  item set, not a second implementation.

## 8. State and event taxonomy

Presentation classes map onto `internal/events`; they do not replace it.

| Transcript class | Source |
| --- | --- |
| `user` | operator input |
| `agent` | assistant message |
| `tool` | tool call + result |
| `exec` | `task.execution.{pre,reserved,started,post,finished}` |
| `lease` | `reservation.{requested,granted,released}` |
| `snapshot` | `snapshot.collected`, `daemon.refresh.{pre,post}` |
| `error` | any of the above in a failed terminal state |

Node health renders from `models.NodeStatus` directly:

| `NodeStatus` | Render |
| --- | --- |
| `complete` | `ok` |
| `partial` | `partial` |
| `unreachable` | `unreachable` |
| `error` | `error` |

Plus one derived state, `stale`, when snapshot age exceeds the daemon refresh
interval by more than 2x. `WARN` is not used; `partial` is a real observed
state and is named as such.

## 9. Keyboard and focus model

- Focus is the editor by default. An overlay captures focus while visible and
  returns it on dismiss.
- No single-letter global actions. Chords only, plus `/commands`.
- `Ctrl+C` clears the editor; twice quits. `Escape` aborts the turn; the
  overlay `Escape` dismisses without acting.
- Approval single-key answers (`o`/`s`/`a`/`d`) are live only while an
  approval overlay holds focus.

## 10. Narrow-terminal fallback

Truncation is layered and explicit, matching the footer's existing behavior:

1. Below 100 columns, drop the provider prefix from the model label.
2. Below 80, drop cache/cost usage parts; keep context percent and model.
3. Below 60, the status line keeps `fleet` and `snap` and drops other keys.
4. Below 40, the footer collapses to the model label alone.

Overlays take a `visible(width, height)` predicate evaluated per frame; the
model picker drops the `TARGET` column before the `HEALTH` column.

Non-TTY continues to error, as `runTUI` already does.

## 11. Data-source contract

This section is the Truth Rule gate. Every rendered cell cites a field, or it
does not ship.

| Cell | Source | Absent |
| --- | --- | --- |
| node health | `models.NodeFacts.Status` | required; never defaulted |
| RAM | `Resources.RAMAllocatableMB` of `RAMTotalMB` | `--` |
| memory pressure | `Resources.Pressure` (`none/low/medium/high`) | `--` |
| load | `Resources.Load1M` | `--` |
| GPU free | `Resources.GPUs[].VRAMMB` less resident model VRAM | `--` |
| GPU util | `Resources.GPUUtilPercent` (pointer; nil = unknown) | `--` |
| thermal | `Resources.ThermalState` | omitted when nominal |
| snapshot age | `ClusterSnapshot.Timestamp` | `unknown` |
| model | `agent.ModelTarget.Model` | `no-model` |
| endpoint | `agent.ModelTarget.Endpoint` | `local` |
| provider | `ModelTarget.ProviderName` / `ProviderKind` | omitted |
| backend health | `llmrouter.BackendProbe.OK` + `Enabled` + `Message` | `unknown` |
| probe latency | `BackendProbe.Latency`, only with `ProbedAt` age | `--` |
| safety | `safety.BlockResult.Score` + `Reason` | required |

Explicitly **not rendered**, because no fact backs them:

- A CPU utilization percentage. AXIS collects cores, load averages, and a
  pressure class. Load and pressure are rendered as themselves.
- Free-vs-total RAM as the primary figure. Allocatable is what placement and
  safety reason about; rendering free RAM would disagree with
  `axis task place`.
- A live latency figure. Probe RTT is rendered with its age or not at all.

Degraded states that must have a rendering before implementation starts:
no daemon; snapshot older than 2x refresh; no `ai.yaml`; zero reachable
backends; a node in `partial`; an unreachable node; an empty session.

## 12. Concurrency invariant

**The input loop performs no I/O.**

Backend probes, picker payload construction, snapshot refresh, completion, and
history load all run off-loop with their own timeouts and deliver results as
messages. Approval responses and turn interrupts are never queued behind a
read.

Concretely: `llmrouter.ProbeBackend` has a 3s default timeout per backend, so
opening a picker over five backends must never be able to stall input for
15 seconds. The picker opens on cached results and repaints.

## 13. Test strategy

- **Golden transcripts.** A recorded session replays through the production
  transcript pipeline and is compared line-for-line. This is the primary gate.
- **Renderer gallery.** Every transcript entry type renders in each state —
  streaming, in-progress, success, failure, truncated — as a fixture the
  tests walk. Adding an entry type without a gallery fixture fails.
- **Headless terminal.** Width/resize/truncation behavior is asserted against
  a virtual terminal at fixed widths (40, 60, 80, 100, 120).
- **Message-level update tests.** Existing `cmd/axis/tui` update tests keep
  their pattern: construct model, feed message, assert state.
- Coverage gates in `hack/coverage-check.sh` apply unchanged.

## 14. Public boundary

No hostnames, tailnet addresses, or live inventory. Examples use `node-a`,
`node-b`, `node-c` and `loopback | private | remote`. Endpoint URLs appear
only inside the interactive overlay, never in machine output or docs.

## 15. Supersedes

- `docs/plans/tui-modernization-2026-08-24.md` — its "alternative renderer"
  constraint is **kept** and extended to the agent surface. Its `--tui` flag
  framing is superseded by the two-surface split.
- `docs/plans/tui-phase3-plan-2026-08-24.md` — Sprints 3B (model lifecycle
  browser) and 3E (event drawer) are withdrawn from `axis tui` and reappear on
  the agent surface as `/model start` and transcript entries. Sprints 3A
  (shipped), 3C (reservations), and 3D (SSH) stay with `axis tui`.

Follow-up doc work when implementation lands: `docs/current-state.md` has no
`tui` entry today while `AGENTS.md` lists the command; that gap closes in the
same change.
