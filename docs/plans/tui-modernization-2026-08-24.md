---
description: Research and planning for modernizing AXIS CLI with a TUI surface.
---

# AXIS TUI Modernization Plan

**Status:** Research & Planning  
**Created:** 2026-08-24  
**Author:** AXIS Agent  
**Priority:** High (UX enhancement)  
**Timeline:** Multi-phase (weeks to months)

---

## Executive Summary

This document outlines a comprehensive plan to modernize the AXIS CLI (`cmd/axis/`) with a professional Terminal User Interface (TUI) layer, improving operator ergonomics, discoverability, and real-time cluster observability.

**Current State:** AXIS ships with a traditional Cobra CLI + minimal `internal/ui` package (tables, spinners, dropdown selects). All 28 commands are text-in/text-out with no persistent UI state.

**Proposed State:** Add a `--tui` flag to `axis` that launches an interactive, full-screen TUI dashboard with:
- Real-time cluster status streaming
- Interactive command palette
- Placement decision explorer
- Reservation management interface
- Model/backend monitoring
- Daemon/mesh health views
- Keyboard-driven navigation (vim-style or F-key shortcuts)

**Key Constraint:** The CLI surface remains unchanged — TUI is an **alternative renderer** for existing commands, not a replacement. All TUI actions must map to underlying `axis <command>` invocations to preserve scripting/automation compatibility.

---

## Phase 1: Research & Framework Selection

### 1.1 TUI Framework Landscape (Go Ecosystem)

| Framework | Stars | Paradigm | Best For | AXIS Fit |
|-----------|-------|----------|----------|----------|
| **Bubble Tea** (Charm) | ~20k | Elm Architecture (Model-View-Update) | Full-screen apps, complex state, composability | ⭐ **Recommended** |
| **tview** | ~9k | Widget-based (like GTK/Qt) | Form-heavy apps, quick prototypes | Good for forms, less flexible |
| **Ratatui** (Rust) | ~11k | Immediate mode (Rust only) | N/A — wrong language | ❌ |
| **termbox-go** | ~3k | Low-level cell buffer | Custom engines, minimal deps | Too low-level |
| **ftxui** (C++) | ~7k | Component-based (C++ only) | N/A — wrong language | ❌ |
| **OpenTUI** (TS) | Emerging | TypeScript/Deno | N/A — wrong language | ❌ |

**Recommendation:** **Bubble Tea** (`github.com/charmbracelet/bubbletea`)

**Rationale:**
1. **Go-native** — matches AXIS codebase language
2. **Elm Architecture** — clean separation of state (Model), rendering (View), and updates (Update), aligning with AXIS's layered architecture
3. **Charm Ecosystem** — includes `lipgloss` (styling), `bubbles` (prebuilt components: table, spinner, viewport, textinput), `bubbletea` (core framework)
4. **Production Proven** — used by `gh` (GitHub CLI), `k9s` (Kubernetes TUI), `charm` tools, `lazygit`
5. **Composable** — can embed Bubble Tea apps as subviews or run full-screen
6. **Active Maintenance** — ~20k stars, frequent updates, strong community

### 1.2 Precedents in CLI Tools

| Tool | TUI Approach | Notes |
|------|--------------|-------|
| **GitHub CLI (`gh`)** | Bubble Tea for `gh issue create`, `gh pr create` | Interactive forms, not full-screen dashboard |
| **k9s** | Full Bubble Tea app | Kubernetes cluster dashboard — closest precedent to AXIS |
| **lazygit** | Full Bubble Tea app | Git TUI with real-time state updates |
| **charm-cli** | Full Bubble Tea suite | `charm fs`, `charm cloud` — direct inspiration |
| **dagger** | Bubble Tea for progress UI | Build pipeline visualization |
| **trivy** | Optional TUI mode | Security scanner with `--format ui` |

**Key Takeaway:** Successful TUI augmentations preserve CLI scripting while offering interactive surfaces for exploration/debugging.

---

## Phase 2: Architecture & Integration Strategy

### 2.1 Integration Models

#### Option A: Subcommand (`axis tui`)
```bash
axis tui              # Launch full-screen dashboard
axis tui --view cluster
axis tui --view reservations
```

**Pros:**
- Clean separation from CLI
- Easy to gate behind build tag if needed
- No risk of breaking existing commands

**Cons:**
- Feels like a separate tool, not integrated
- Can't seamlessly transition from CLI to TUI mid-command

#### Option B: Global Flag (`axis --tui`)
```bash
axis --tui            # Launch dashboard
axis --tui cluster status
axis --tui task place "workload"
```

**Pros:**
- TUI is clearly an alternative renderer for CLI commands
- Preserves mental model: "TUI mode" vs "text mode"
- Can scope TUI to specific commands

**Cons:**
- Requires flag parsing at root level
- Some commands don't benefit from TUI (e.g., `axis version`)

#### Option C: Hybrid (`axis tui` + command-specific `--tui`)
```bash
axis tui              # Full dashboard
axis cluster status --tui   # TUI-rendered single command
```

**Pros:**
- Best of both worlds
- Dashboard for exploration, `--tui` for focused tasks

**Cons:**
- More surface area to maintain

**Recommendation:** **Option C (Hybrid)** — Start with `axis tui` dashboard, add `--tui` flag to high-value commands later.

### 2.2 Package Structure

```
cmd/axis/
  tui/                    # NEW: TUI package
    app.go                # Main Bubble Tea application
    views/
      cluster.go          # Cluster status view
      placement.go        # Placement explorer
      reservations.go     # Reservation manager
      models.go           # AI backend monitor
      daemon.go           # Daemon/mesh health
      help.go             # Help overlay
    components/
      node_table.go       # Reusable node list component
      status_bar.go       # Global status bar
      command_palette.go  # Fuzzy-find command runner
      detail_panel.go     # Contextual detail view
    state.go              # TUI state machine (Model)
    update.go             # Event handlers (Update)
    styles.go             # Lipgloss style definitions

internal/ui/              # EXISTING: Keep for CLI helpers
  table.go                # Still used by text output
  spinner.go              # Still used by CLI
  select.go               # Still used by interactive CLI
  color.go
```

### 2.3 State Management

**Challenge:** TUI needs live cluster state, but `axis cluster status` does SSH fan-out (slow).

**Solution:** Leverage existing daemon cache:
```go
// cmd/tui/state.go
type ClusterState struct {
    Snapshot   *models.ClusterSnapshot
    LastUpdate time.Time
    Stale      bool
}

func (m *Model) Init() (tea.Cmd) {
    return tickCmd()  // Start refresh loop
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tickMsg:
        if m.state.Stale || time.Since(m.state.LastUpdate) > 30*time.Second {
            return m, loadSnapshotCmd()  // Async SSH collect
        }
    case snapshotLoadedMsg:
        m.state.Snapshot = msg.Snapshot
        m.state.LastUpdate = time.Now()
        m.state.Stale = false
    }
    return m, nil
}
```

**Key Invariant:** TUI never blocks on SSH — always async with spinner/loading state.

---

## Phase 3: Feature Prioritization (MVP → v1.0)

### 3.1 MVP (Week 1-2)

**Goal:** Prove TUI viability with minimal viable dashboard.

| Feature | Priority | Complexity | Notes |
|---------|----------|------------|-------|
| Cluster status table | P0 | Low | Reuse `internal/ui/table.go` + Bubble Tea viewport |
| Node detail panel | P0 | Low | Click/hover on node → show specs, GPU, thermal |
| Keyboard navigation | P0 | Medium | j/k scroll, Enter select, q quit, ? help |
| Real-time refresh (30s) | P0 | Medium | Daemon cache or live SSH collect toggle |
| Status bar | P0 | Low | Clock, node count, last refresh time |
| Help overlay | P0 | Low | List all keybindings |

**MVP Success Criteria:**
- Can view cluster status without typing `axis cluster status`
- Feels responsive (<100ms input lag)
- No SSH blocking UI thread
- Quit cleanly with `q`

### 3.2 v0.1 (Week 3-4)

**Goal:** Add interactive command execution.

| Feature | Priority | Complexity | Notes |
|---------|----------|------------|-------|
| Command palette (Ctrl+P) | P0 | High | Fuzzy-find `axis <command>` |
| Placement explorer | P1 | Medium | `axis placement explain` with visual scoring breakdown |
| Reservation list | P1 | Low | `axis reservations list` as table |
| Backend monitor | P1 | Medium | `axis ai backends` with health indicators |
| Theme support | P2 | Low | Dark/light via `lipgloss` |

### 3.3 v1.0 (Month 2-3)

**Goal:** Full operator workflow coverage.

| Feature | Priority | Complexity | Notes |
|---------|----------|------------|-------|
| Task placement wizard | P0 | High | Interactive prompt → `axis task place` |
| Reservation release | P0 | Medium | Select + confirm → `axis reservations release` |
| Model start/stop | P1 | Medium | `axis model start/stop` with port picker |
| Mesh peer graph | P1 | High | ASCII/node-link diagram of gossip peers |
| Daemon logs stream | P2 | Medium | Tail `~/.axis/logs/daemon.log` in viewport |
| Export/screenshot | P2 | Low | Save current view as PNG/text |

---

## Phase 4: Technical Debt & Risks

### 4.1 Known Risks

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| SSH fan-out blocks UI | Medium | High | Always async, use daemon cache, show loading spinner |
| Terminal compatibility | Low | Medium | Test on xterm, tmux, screen, Windows Terminal, iTerm2 |
| Bubble Tea learning curve | High | Low | Pair with existing Bubble Tea examples (`k9s`, `lazygit` code) |
| Feature creep | High | Medium | Strict MVP scope, defer "nice-to-haves" to v1.0 |
| Maintenance burden | Medium | Medium | Keep TUI as thin renderer over existing commands |

### 4.2 Performance Budgets

| Metric | Target | Measurement |
|--------|--------|-------------|
| Input latency | <100ms | `tea.Println` timing |
| Frame rate | 30-60 FPS | Bubble Tea default |
| SSH collect timeout | 10s per node | `nodes.yaml` timeout_sec |
| Memory usage | <50MB | `ps aux` during idle |
| Startup time | <2s | `time axis tui` |

---

## Phase 5: Implementation Roadmap

### Sprint 1 (Days 1-7): Foundation
- [ ] Add Bubble Tea + lipgloss + bubbles to `go.mod`
- [ ] Create `cmd/axis/tui/` skeleton
- [ ] Implement `app.go` with basic Model-View-Update
- [ ] Build cluster status table view
- [ ] Add keyboard navigation (j/k/q/?)
- [ ] Test on Linux/macOS terminals

### Sprint 2 (Days 8-14): State & Refresh
- [ ] Wire daemon cache integration
- [ ] Implement async snapshot loading
- [ ] Add 30s auto-refresh tick
- [ ] Build node detail panel
- [ ] Add status bar (clock, node count)
- [ ] Help overlay with keybindings

### Sprint 3 (Days 15-21): Commands
- [ ] Command palette (Ctrl+P)
- [ ] Fuzzy-find `axis <command>`
- [ ] Execute commands via `terminal()` tool
- [ ] Capture and display output
- [ ] Placement explorer view
- [ ] Reservation list view

### Sprint 4 (Days 22-28): Polish & Release
- [ ] Theme support (dark/light)
- [ ] Error handling (SSH failures, timeouts)
- [ ] Performance profiling
- [ ] Windows Terminal testing
- [ ] Documentation (`docs/tui.md`)
- [ ] Release under `axis tui` (experimental flag)

---

## Phase 6: Success Metrics

| Metric | Baseline | Target (v1.0) | Measurement |
|--------|----------|---------------|-------------|
| Daily TUI users | 0 | 20% of CLI users | `axis --source tui` telemetry (opt-in) |
| Command discovery | N/A | 2x more commands used/session | Session analytics |
| Operator satisfaction | N/A | 4.5/5 stars | Post-session survey (`/feedback`) |
| Time-to-insight | ~5s (CLI) | <2s (TUI) | `time axis cluster status` vs TUI load |
| Support tickets (UX) | Baseline | -30% | GitHub issues tagged `ux` |

---

## Appendix A: Bubble Tea Crash Course

### The Elm Architecture (Model-View-Update)

```go
// Model: application state
type Model struct {
    nodes     []models.NodeFacts
    selected  int
    loading   bool
    err       error
}

// View: render state to string
func (m Model) View() string {
    if m.loading {
        return "Loading..."
    }
    if m.err != nil {
        return fmt.Sprintf("Error: %v", m.err)
    }
    // Render node table
    var s strings.Builder
    for i, node := range m.nodes {
        prefix := "  "
        if i == m.selected {
            prefix = "> "
        }
        s.WriteString(fmt.Sprintf("%s%s (%s)\n", prefix, node.Name, node.Status))
    }
    return s.String()
}

// Update: handle events, return new state + commands
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.KeyMsg:
        switch msg.String() {
        case "q", "ctrl+c":
            return m, tea.Quit
        case "j", "down":
            m.selected++
        case "k", "up":
            m.selected--
        }
    case nodesLoadedMsg:
        m.nodes = msg.Nodes
        m.loading = false
    }
    return m, nil
}

// Main entry point
func main() {
    p := tea.NewProgram(initialModel())
    if _, err := p.Run(); err != nil {
        fmt.Printf("Error: %v", err)
        os.Exit(1)
    }
}
```

### Key Bubble Tea Concepts

1. **`tea.Model` interface:**
   ```go
   type Model interface {
       Init() Cmd
       Update(Msg) (Model, Cmd)
       View() string
   }
   ```

2. **`tea.Cmd` (Command):**
   - Side-effectful operation (HTTP request, file read, timer)
   - Returns `tea.Msg` when complete
   - Example: `tea.Quit`, `tickCmd()`, `loadSnapshotCmd()`

3. **`tea.Msg` (Message):**
   - Event or result (key press, HTTP response, timer tick)
   - Passed to `Update()` for handling

4. **Composability:**
   - Embed sub-models (e.g., `viewport.Model`, `table.Model`)
   - Delegate `Update()`/`View()` to children

---

## Appendix B: Precedent Codebases to Study

| Repo | File | Why Study |
|------|------|-----------|
| **k9s** | `internal/view/pod.go` | Kubernetes node/pod table — closest to AXIS cluster view |
| **lazygit** | `pkg/gui/controllers.go` | Git branch/commit navigation — similar to node selection |
| **charm-cli** | `pkg/cmd/fs/fs.go` | Filesystem TUI — real-time state updates |
| **gh** | `pkg/cmd/issue/create/create.go` | Interactive forms — model for `axis task place` wizard |

---

## Appendix C: Dependencies to Add

```go
// go.mod
require (
    github.com/charmbracelet/bubbletea v0.26.0
    github.com/charmbracelet/lipgloss v0.12.0
    github.com/charmbracelet/bubbles v0.18.0
)
```

**Total added:** ~500KB binary size (acceptable for UX gain)

---

## Next Steps

1. **Stakeholder Review:** Share this plan with AXIS maintainers for feedback
2. **Spike Prototype:** Build MVP (Sprint 1) in a feature branch
3. **User Testing:** Demo to 2-3 operators, gather feedback
4. **Iterate:** Refine based on feedback, proceed to Sprint 2

**Decision Point:** After Sprint 2, decide whether to:
- Continue to v1.0 (full feature set)
- Ship MVP as "experimental" and iterate in production
- Pivot to alternative approach (e.g., web dashboard)

---

**Truth Rule:** This plan describes proposed behavior — no TUI exists yet. All claims about Bubble Tea capabilities are based on external documentation and precedent codebases, not live AXIS implementation.
