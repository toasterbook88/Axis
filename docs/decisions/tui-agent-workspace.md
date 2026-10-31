# Decision: `axis tui` becomes the agent workspace

**Date:** 2026-10-30
**Scope:** `cmd/axis/tui`, `cmd/axis/tui.go`, `internal/console` (embedded mode), `cmd/axis/agent_console_startup.go`
**Decision:** `axis tui` hosts the `axis agent` console beside a cluster sidebar, with the dashboard one key away. `axis agent` keeps its transcript-first, main-screen shape unchanged.
**Supersedes:** §2.1 of [`agent-surface-transcript-model.md`](agent-surface-transcript-model.md) ("`axis tui` stays observational"). The rest of that record stands.
**User guide:** [`docs/runbooks/tui.md`](../runbooks/tui.md)

## 1. Context

The 2026-08-24 record split the surfaces: `axis tui` an observational
dashboard, the agent a main-screen transcript. It rejected an "Agent Command
Center" that put the agent in a dashboard pane, because that shape caused
every conflict found in review: a single-letter key collision, a second
command grammar beside the slash commands, and pressure on the Truth Rule.

The maintainer asked on 2026-10-30 for `axis tui` to work like OpenCode: a
full-screen app with the conversation in the middle and context around it,
while `axis agent` stays the Claude Code–style transcript. That is the
rejected shape in outline, so this record states how it avoids each
conflict the earlier review found.

## 2. Decision

### 2.1 Surfaces

| Surface | Shape | Purpose |
| --- | --- | --- |
| `axis agent` | Main-screen transcript, dim footer, overlays (unchanged) | Conversational command center that keeps terminal scrollback |
| `axis tui` | Alt-screen workspace: conversation pane, cluster sidebar, status bar; dashboard on `F2` | The same agent with cluster context always on screen |
| `axis tui --dashboard` | Alt-screen dashboard only | Observational fleet view, the old `axis tui` role |

### 2.2 One agent, hosted, not rebuilt

The conversation pane is `internal/console`, the component `axis agent`
runs. `runAgentConsoleStartup` takes an optional program factory
(`agentStartupInputs.program`); `axis tui` supplies one that wraps the
console in the workspace. Background model resolution, MCP connection,
approvals, steering queue, history and slash commands are the same code.
There is no second agent loop and no subprocess.

Rejected alternative: running `axis agent -p` as a subprocess per question.
It was simpler, but each question was a separate one-shot session (no
conversation, no approvals, no `/model`), and it duplicated output parsing.

### 2.3 Embedded console mode

The standalone console prints committed entries to terminal scrollback with
`tea.Println`, which does nothing on the alternate screen. `Options.Embedded`
changes only that path: `commit` appends to a bounded in-memory transcript
(5,000 entries), and `View` draws the transcript tail above the live region,
re-wrapped to the pane width each frame and scrollable. `commit` is the
console's single exit for transcript content, so this is one switch, not a
fork of the renderer. With `Embedded` unset the console behaves exactly as
before (tested).

### 2.4 How the earlier conflicts are avoided

| Conflict (2026-08-24) | Here |
| --- | --- |
| Single-letter keys collide with typing | In the conversation every printable key goes to the prompt. Workspace commands are `ctrl+o` (palette) and `ctrl+x` + key (leader). Single letters act only in the dashboard view, which has no prompt. |
| A second command grammar | The palette and leader keys run existing slash commands through `console.RunCommandMsg`, the path a typed command takes, or press existing dashboard keys. Nothing new is parsed. |
| Truth Rule pressure | The sidebar and dashboard read only the cached snapshot and say `not observed` when a value is absent. Agent output keeps its advisory banner. No new authority path exists. |
| Reads blocking the input loop | Snapshot loads and refreshes run as `tea.Cmd`s; the agent runs off the loop as in `axis agent`. |
| Approvals as view state | Unchanged: the console's approval overlay, fed by `internal/agent`. |

## 3. Consequences

- **Gained:** cluster health is visible while talking to the agent, the agent
  is one key from the dashboard (`a` on a node prefills `@node`), and every
  `axis agent` feature works in the workspace.
- **Lost in `axis tui`:** terminal scrollback and native text selection across
  the transcript (the workspace owns the screen). `axis agent` remains the
  surface for that.
- **Bounded memory:** the embedded transcript drops its oldest entries past
  5,000.
- **Two console layouts to keep working:** the embedded path has its own
  tests (`internal/console/embedded*_test.go`), and the workspace is pinned
  by golden frames (`cmd/axis/tui/testdata/`).

## 4. Non-goals

- Changing `axis agent`'s shape, keys or scrollback behavior.
- New slash commands or verbs reachable only from the workspace.
- Persisting the embedded transcript beyond the existing conversation history.
