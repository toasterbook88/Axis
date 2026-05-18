# AUTH-1: Reservation Authority Audit

## 1. Canonical Owner

`internal/reservation/ledger.go` is the canonical reservation authority.

- Its package docstring declares: *"The ledger is the single source of truth for 'what is reserved where'"*.
- It is the **only** production path that writes to `~/.axis/ledger.json`.
- `internal/reservation/persist.go` exists only as a thin persistence layer for the ledger; it does not own semantics.

## 2. All Mutation Points

### 2.1 Ledger mutations (`~/.axis/ledger.json`)

| Function | File | What it mutates | Persist? |
|----------|------|-----------------|----------|
| `Reserve()` | `internal/reservation/ledger.go` | Creates `Entry` in `l.entries`, increments `totalReserved` | Yes (`saveLocked`) |
| `Release()` | `internal/reservation/ledger.go` | Deletes `Entry`, increments `totalReleased` | Yes (`saveLocked`) |
| `Heartbeat()` | `internal/reservation/ledger.go` | Updates `LastHeartbeat` on `Entry` | Yes (`saveLocked`) |
| `Reclaim()` | `internal/reservation/ledger.go` | Deletes stale/expired entries, increments `totalReclaimed` | Yes (`saveLocked`) |
| `reclaimLocked()` | `internal/reservation/ledger.go` | Internal impl called by `Reclaim()` and `Load()` | Yes (`saveLocked`) |
| `Load()` | `internal/reservation/persist.go` | Replaces `l.entries`, **then calls `reclaimLocked()`** | Yes (if reclaim > 0) |
| `SetNodeCapacity()` | `internal/reservation/ledger.go` | Updates `l.nodeRAM[node]` | No |

### 2.2 Callers of ledger mutation in production

- `internal/execution/guarded.go` – `runLocal()` and `runRemote()`:
  - Call `ledger.Reserve(entry)` before execution
  - Call `ledger.Release(execID)` via `defer` after execution
  - Call `heartbeatTask` → `ledger.Heartbeat()` every `executionHeartbeatInterval` during execution
- `internal/daemon/daemon.go` – `New()`:
  - Calls `ledger.Load()` on startup (triggers silent reclaim)
- `internal/runtimectx/context.go` – `Load()`:
  - Creates a fresh ledger, calls `ledger.Load()` (triggers silent reclaim), then `SetNodeCapacity` for each node
- `cmd/axis/task.go` and `cmd/axis/context.go`:
  - Call `state.Load()` (not ledger directly), but the daemon cache applies the ledger overlay

### 2.3 State mutations (`~/.axis/state.json`)

| Function | File | What it mutates | Persist? |
|----------|------|-----------------|----------|
| `Load()` | `internal/state/state.go` | Loads from disk, then calls `runMaintenance()` | Yes (if maintenance mutates) |
| `runMaintenance()` | `internal/state/state.go` | Calls `reclaimStaleReservation()`, `normalizeNodeStateExecTracking()`, deletes ancient nodes | Yes (via `Load`'s save) |
| `reclaimStaleReservation()` | `internal/state/state.go` | Delegates to `reclaimDeadOwnerExecutions()` and `reclaimHeartbeatStaleExecutions()` | Yes (via above) |
| `normalizeNodeStateExecTracking()` | `internal/state/state.go` | Reconciles `ActiveTasks`, `ReservedMB`, `ExecReservationsMB`, `ExecHeartbeatAt`, etc. | Yes (via above) |
| `RecordPlacement()` | `internal/state/state.go` | Appends to `Decisions` slice (capped at 20), calls `Save()` | Yes |
| `RecordObservation()` | `internal/state/observations.go` | Upserts into `Observations` map | No (caller must `Save`) |
| `applyFailureOutcome()` | `internal/execution/guarded.go` | Records failure in `st.Failures` | Yes (via `Save`) |
| `applySuccessOutcome()` | `internal/execution/guarded.go` | Records success in `st.Failures` | Yes (via `Save`) |
| `recordExecutionOutcome()` | `internal/execution/guarded.go` | Calls `RecordObservation`, then `st.Save()` | Yes |

## 3. Dual-Reclamation Problem

Two independent packages reclaim stale reservations, on different triggers, with different rules:

### 3.1 Ledger reclamation (`internal/reservation/`)

- **Trigger**: `ledger.Load()` (startup only) and `ledger.Reclaim()` (explicit call; no periodic background ticker in daemon).
- **Rules**:
  - `entry.IsStale(now, HeartbeatStaleWindow)` → default **2 minutes**
  - `entry.IsExpired(now)` → hard expiry if `ExpiresAt` set
- **Scope**: Per-entry; checks `LastHeartbeat` on each `Entry`
- **Persistence**: Writes cleaned set back to `ledger.json`

### 3.2 State reclamation (`internal/state/`)

- **Trigger**: `state.Load()` on **every** load (called by daemon refresh, CLI reads, API handlers, etc.).
- **Rules**:
  - `reclaimDeadOwnerExecutions()` → checks if owner PID is alive via `processAlive()`
  - `reclaimHeartbeatStaleExecutions()` → `now.Sub(hb) > execHeartbeatStaleAfter` → **2 minutes**
  - Legacy fallback: `now.Sub(LastPlacedAt) > staleReservationReclaimAfter` → **45 minutes**
  - `shouldDropAncientNodeState()` → drops node state after 45 min (no execs) or 24 h (legacy)
- **Scope**: Per-node `NodeState`; checks `ExecHeartbeatAt`, `ExecOwnerPID`, `LastPlacedAt`
- **Persistence**: Writes cleaned set back to `state.json`

### 3.3 Why this is a problem

1. **Different files, same concept**: Both prune "stale reservations" but on separate JSON files (`ledger.json` vs `state.json`). A reservation can be alive in the ledger but already purged from state, or vice versa.
2. **Different windows**: Ledger uses 2 min stale window universally. State uses 2 min for heartbeat-aware execs, 45 min for legacy mode, plus PID-based death detection.
3. **No cross-file reconciliation**: Neither loader reads the other file. `ledger.Load()` does not consult `state.json`, and `state.Load()` does not consult `ledger.json`.
4. **Derived view ambiguity**: `snapshotview.ApplyReservationView()` prefers ledger, falls back to state. If the two disagree, the snapshot silently reflects whichever has a non-zero value, masking drift.

## 4. Path Classification

| Path | Role | Writes? | Consumers |
|------|------|---------|-----------|
| `internal/reservation/ledger.go` → `~/.axis/ledger.json` | **Canonical** | Yes (exclusive) | Daemon, execution, placement overlay, API `/v2/reservations` |
| `internal/state/state.go` → `~/.axis/state.json` | **Mirror / legacy** | Yes | Daemon refresh, CLI `task place/context`, empirical placement observations, failure immune system |
| `internal/snapshotview/overlay.go` → `ClusterSnapshot.Nodes[].RAMReservedMB` | **Derived / read-only** | No | `axis status`, `axis task place`, HTTP API, MCP tools |

### 4.1 Overlay precedence (from `snapshotview/overlay.go`)

```go
reserved := int64(0)
if ledger != nil {
    reserved = ledger.NodeSummaryFor(node.Name).ReservedRAMMB
}
if reserved <= 0 && st != nil && st.Nodes != nil {
    if ns, ok := st.Nodes[node.Name]; ok {
        reserved = ns.ReservedMB
    }
}
```

Ledger wins; state is a fallback for zero. This means state can under-report without the operator noticing.

## 5. Silent Reconciliation

Reclamation happens without event emission or caller-visible warning in the following paths:

1. **`ledger.Load()` startup reclaim**
   - `internal/reservation/persist.go:50` calls `l.reclaimLocked()` after loading entries.
   - Reclaimed entries are logged (`l.logger.Info`) but no error is returned, no warning is appended to any snapshot, and no `StateChange*` event fires.
   - **Impact**: A daemon restart can silently drop reservations that missed their heartbeat window while the daemon was down.

2. **`state.Load()` maintenance reclaim**
   - `internal/state/state.go:108` calls `runMaintenance()` on every load.
   - `reclaimStaleReservation()` can delete `ActiveExecs`, zero `ReservedMB`, drop entire `NodeState` entries.
   - Maintenance saves back to disk inside `Load()`, then returns a clean `*ClusterState`.
   - No event is emitted. The daemon's `WatchState` may later detect the write and trigger a refresh, but the reclaim itself is silent.
   - **Impact**: Every CLI invocation that calls `state.Load()` (e.g., `axis task context`) can mutate and rewrite `state.json` without the operator knowing.

3. **No ledger file watcher**
   - The daemon watches `state.json` (`WatchState`) and `skills.json` (`WatchSkills`), but **does not watch `ledger.json`**.
   - If an external process modifies `ledger.json`, the in-memory daemon ledger stays stale until restart.
   - Conversely, ledger mutations via `Reserve/Release/Heartbeat` are in-memory only (auto-persisted to disk), but no file watcher signals other processes.

## 6. Grep Invariants (expected single-mutation packages)

```text
# Ledger JSON should only be written from internal/reservation/
grep -rn 'os.WriteFile.*ledger\|persist.WriteFileAtomic.*ledger' internal/
# → Only hits in internal/reservation/persist.go (ledger.saveLocked)

# Ledger Reserve/Release/Heartbeat should only be called from internal/execution/ in prod
grep -rn 'ledger\.Reserve\|ledger\.Release\|ledger\.Heartbeat' internal/ --include='*.go' | grep -v '_test.go'
# → Hits in internal/execution/guarded.go only

# State JSON should only be written from internal/state/ (and execution outcomes)
grep -rn 'os.WriteFile.*state\|persist.WriteFileAtomic.*state' internal/
# → Only hits in internal/state/state.go (ClusterState.Save)

# Snapshot overlay should be read-only
grep -rn 'snap\.Nodes\[.*\]\.RAMReservedMB.*=' internal/ --include='*.go' | grep -v '_test.go'
# → Only hit in internal/snapshotview/overlay.go (assignment is local to the overlay function)
```

## 7. Summary

- **Canonical authority**: `internal/reservation/ledger.go` / `~/.axis/ledger.json`
- **Legacy mirror**: `internal/state/state.go` / `~/.axis/state.json` (still actively mutated and loaded)
- **Dual-reclamation**: Both `ledger.Load()` and `state.Load()` independently prune stale reservations with different rules and windows.
- **Silent ops**: Startup ledger reclaim and per-load state maintenance both mutate persisted state without emitting events or returning warnings.
- **Risk**: The state mirror can drift from the ledger canonical source. The derived snapshot overlay prefers ledger, so drift in state is masked; drift in ledger would be visible but only if the ledger value is non-zero.
