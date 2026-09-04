# AXIS TUI Phase 3: Interactive Commands Implementation Plan

**Date:** 2026-08-24  
**Branch:** `feat/tui-interactive-commands`  
**Status:** Planning Complete - Ready for Implementation  

---

## Executive Summary

Phase 3 transforms the AXIS TUI from a **read-only dashboard** into an **interactive control plane** for cluster operations. Users will execute common tasks directly from the TUI without dropping back to CLI.

**Key Capabilities:**
1. Interactive workload placement wizard
2. Model lifecycle controller (start/stop)
3. Reservation management (list/release)
4. SSH session launcher
5. Live event/log stream

---

## Feature 1: Workload Placement Wizard (`p` key)

### User Flow
```
1. User presses 'p' from fleet view
2. Modal overlay appears with text input
3. User types: "run 16GB VRAM model inference"
4. Live scoring breakdown displays (FitScore, locality, thermal penalties)
5. User confirms with Enter, cancels with Escape
6. Task placed via `axis task place` API
```

### Technical Design

**Modal Component:**
```go
// cmd/axis/tui/modal_placement.go
type PlacementModal struct {
    input    textinput.Model
    results  *models.PlacementDecision
    loading  bool
    confirmed bool
}

func (m PlacementModal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    // Handle text input, live scoring via async placement.Place()
}

func (m PlacementModal) View() string {
    // Render modal overlay with scoring breakdown
}
```

**Integration Points:**
- `internal/placement.Place(prompt string)` - Reuse existing placement engine
- `internal/runtimectx.Load()` - Get current snapshot for scoring
- Display: FitScore 0-100, per-node breakdown, reasoning strings

**Keybindings:**
- `p` - Open modal
- `Enter` - Confirm placement
- `Escape` - Cancel
- `Tab` - Cycle through top 3 candidate nodes

---

## Feature 2: Model Lifecycle Controller (`m` key)

### User Flow
```
1. User presses 'm' from fleet view (or Tab 2: Backends)
2. Lists detected weights on named volumes
3. User selects model + target node + port
4. Confirms start command
5. Shows llama-server startup progress
6. Backend appears in Tab 2 with health indicator
```

### Technical Design

**Model Browser Component:**
```go
// cmd/axis/tui/model_lifecycle.go
type ModelBrowser struct {
    weights     []WeightInfo      // From storage scan
    targetNode  int               // Selected node index
    portInput   textinput.Model   // Port number entry
    starting    bool              // Startup in progress
    started     bool              // Successfully started
}

type WeightInfo struct {
    Path      string
    SizeGB    int64
    ModTime   time.Time
    Volume    string
}
```

**Integration Points:**
- `internal/facts/local.go:scanWeights()` - Discover models on named volumes
- `cmd/axis/model.go:modelStartCmd()` - Reuse existing start logic
- `internal/transport/ssh.go` - Execute remote llama-server

**Validation:**
- Port availability check (`lsof -i :PORT`)
- Weights file existence
- Target node reachability
- llama-server binary presence

**Keybindings:**
- `m` - Open model browser
- `j/k` - Select model
- `Tab` - Select target node
- `Enter` - Start model
- `x` - Stop selected model (confirmation required)

---

## Feature 3: Reservation Management (`l` key)

### User Flow
```
1. User presses 'l' from Reservations tab (Tab 4)
2. Lists active reservations with expiry countdown
3. User selects reservation
4. Confirms release
5. Reservation removed, capacity freed
```

### Technical Design

**Reservation List Component:**
```go
// cmd/axis/tui/reservations.go
type ReservationList struct {
    reservations []reservation.Lease
    cursor       int
    releasing    bool        // Confirmation mode
    released     []string    // IDs just released
}

func (r ReservationList) View() string {
    // Table with columns: ID, Node, RAM/VRAM, Expires, Countdown
}
```

**Integration Points:**
- `internal/reservation.ListActive()` - Get current leases
- `internal/reservation.Release(id string)` - Free capacity
- Countdown timer: `time.Until(lease.ExpiresAt)` with live updates

**Display Format:**
```
ID      NODE        RAM      VRAM     EXPIRES        COUNTDOWN
abc123  node-a      16 GB    8 GB     2h 15m         [████████░░] 78%
def456  node-b      32 GB    --       45m            [███░░░░░░░] 33%
```

**Keybindings:**
- `l` - Open reservation list (from Tab 4)
- `r` - Release selected (with confirmation)
- `d` - Doctor view (find stale entries)
- `f` - Force release stale (requires confirmation)

---

## Feature 4: SSH Session Launcher (`Enter` key)

### User Flow
```
1. User selects node in fleet table
2. Presses Enter
3. Confirmation prompt: "SSH to node-a (192.168.1.100)?"
4. On confirm: suspends TUI, launches SSH session
5. On SSH exit: resumes TUI, refreshes snapshot
```

### Technical Design

**SSH Launcher:**
```go
// cmd/axis/tui/ssh_session.go
func launchSSHSession(node models.NodeFacts) tea.Cmd {
    return func() tea.Msg {
        // 1. Suspend TUI (exit alt-screen)
        fmt.Print("\x1b[?1049l") // Exit alt-screen
        
        // 2. Build SSH command
        target := node.SSHTarget
        if node.ResolvedDialTarget != "" {
            target = node.ResolvedDialTarget
        }
        
        // 3. Execute SSH (blocking)
        cmd := exec.Command("ssh", "-t", target)
        cmd.Stdin = os.Stdin
        cmd.Stdout = os.Stdout
        cmd.Stderr = os.Stderr
        err := cmd.Run()
        
        // 4. Resume TUI (enter alt-screen)
        fmt.Print("\x1b[?1049h")
        
        // 5. Return refresh command
        if err != nil {
            return loadErrMsg{Err: err}
        }
        return loadSnapshotCmd()
    }
}
```

**Integration Points:**
- `models.NodeFacts.SSHTarget` - Configured SSH hostname
- `models.NodeFacts.ResolvedDialTarget` - Fastest discovered IP
- TUI suspend/resume via terminal escape codes

**Safety:**
- Confirmation prompt before connecting
- Display target IP/hostname
- Graceful error handling (node unreachable, auth failed)

**Keybindings:**
- `Enter` - SSH to selected node (with confirmation)
- `s` - Alternative SSH keybinding

---

## Feature 5: Live Event Stream (Bottom Drawer)

### User Flow
```
1. User presses 'e' to toggle event drawer
2. Bottom 1/3 of screen shows live event feed
3. Events scroll in real-time:
   - Node status changes (complete → partial)
   - Reservation created/released
   - Model started/stopped
   - Mesh peer join/leave
4. Press 'e' to collapse drawer
```

### Technical Design

**Event Stream Component:**
```go
// cmd/axis/tui/event_drawer.go
type EventDrawer struct {
    events     []events.LifecycleEvent
    viewport   viewport.Model
    subscribed bool
    paused     bool
}

func (e EventDrawer) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case events.LifecycleEvent:
        if !e.paused {
            e.events = append(e.events, msg)
            e.viewport.SetContent(renderEvents(e.events))
        }
        return e, nil
    }
}
```

**Integration Points:**
- `internal/events.Subscribe()` - Listen to lifecycle events
- Event types: `NodeFactsUpdated`, `ReservationCreated`, `ReservationReleased`, `ModelStarted`, `ModelStopped`, `MeshPeerJoined`
- Event persistence: `~/.axis/events*.jsonl`

**Display Format:**
```
[14:32:05] node-a: status changed (complete → partial) - thermal throttling detected
[14:31:48] reservation abc123 created on node-b (16 GB RAM, 8 GB VRAM)
[14:30:12] model qwen-demo started on node-a:8082
[14:28:55] mesh peer joined: node-c (latency 2.3ms)
```

**Keybindings:**
- `e` - Toggle event drawer (expand/collapse)
- `p` - Pause/resume scrolling
- `c` - Clear events
- `Up/Down` - Scroll history

---

## Implementation Sequence

### Sprint 3A: Placement Wizard (Days 1-3)
- [ ] Create `modal_placement.go`
- [ ] Wire `textinput.Model` for prompt entry
- [ ] Async placement scoring via `internal/placement.Place()`
- [ ] Render scoring breakdown (FitScore, per-node details)
- [ ] Confirmation flow with Enter/Escape
- [ ] Integration test: place task from TUI

### Sprint 3B: Model Lifecycle (Days 4-6)
- [ ] Create `model_lifecycle.go`
- [ ] Weight scanning on named volumes
- [ ] Port availability check
- [ ] SSH execution for remote llama-server start
- [ ] Health monitoring post-start
- [ ] Stop command with confirmation

### Sprint 3C: Reservations (Days 7-8)
- [ ] Enhance Tab 4 with live reservation list
- [ ] Countdown timer rendering
- [ ] Release command integration
- [ ] Doctor view for stale entries
- [ ] Force release with confirmation

### Sprint 3D: SSH Launcher (Day 9)
- [ ] SSH session suspension/resumption
- [ ] Confirmation prompt
- [ ] Error handling (unreachable, auth failed)
- [ ] Post-SSH snapshot refresh

### Sprint 3E: Event Drawer (Days 10-12)
- [ ] Create `event_drawer.go`
- [ ] Subscribe to lifecycle events
- [ ] Real-time rendering with viewport
- [ ] Pause/clear/scroll controls
- [ ] Toggle with `e` key

---

## Testing Strategy

### Unit Tests
```go
// cmd/axis/tui/modal_placement_test.go
func TestPlacementModal_Update(t *testing.T) {
    // Test text input, scoring updates, confirmation flow
}

// cmd/axis/tui/model_lifecycle_test.go
func TestModelBrowser_ValidatePort(t *testing.T) {
    // Test port availability checks
}
```

### Integration Tests
```bash
# Manual testing script
./axis daemon start
./axis tui <<EOF
p
run 16GB VRAM model
Enter
m
j
Enter
8082
Enter
l
r
Enter
e
q
EOF
```

### Acceptance Criteria
- [ ] Placement wizard completes task placement end-to-end
- [ ] Model starts on remote node and appears in Tab 2
- [ ] Reservation release frees capacity (verified via `axis reservations list`)
- [ ] SSH session launches and returns cleanly
- [ ] Event drawer shows real-time updates

---

## Risk Mitigation

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| SSH session corrupts TUI state | Medium | High | Test suspend/resume extensively, fallback to full restart |
| Model start fails silently | Medium | Medium | Add health check loop post-start, show error modal |
| Placement scoring slow (>2s) | Low | Medium | Show loading spinner, async scoring with progress |
| Reservation release conflicts | Low | High | Optimistic locking, retry on conflict, show error |
| Event stream floods UI | Low | Low | Rate-limit to 1 event/sec, buffer overflow protection |

---

## Success Metrics

| Metric | Target | Measurement |
|--------|--------|-------------|
| Time-to-place-task | <15s | From `p` press to confirmation |
| Model start success rate | >95% | Successful starts / attempts |
| SSH session clean return | 100% | No TUI corruption after SSH exit |
| Event latency | <1s | Event fired → displayed |
| User satisfaction | 4.5/5 | Post-demo survey |

---

## Next Steps

1. **Review this plan** with AXIS maintainers
2. **Start Sprint 3A** (Placement Wizard)
3. **Daily standups** to demo progress
4. **End-of-sprint testing** for each feature

**Estimated completion:** 12 days (2.5 weeks)

---

**Ready to implement?** Start with Sprint 3A: Placement Wizard modal.
