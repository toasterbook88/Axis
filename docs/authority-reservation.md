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
| `Reserve()` | `internal/reservation/ledger.go` | Creates `Entry` in `l.entries`, increments `totalReserved` | Yes (`writeSnapshot`) |
| `Release()` | `internal/reservation/ledger.go` | Deletes `Entry`, increments `totalReleased` | Yes (`writeSnapshot`) |
| `Heartbeat()` | `internal/reservation/ledger.go` | Updates `LastHeartbeat` on `Entry` | Yes (`writeSnapshot`) |
| `Reclaim()` | `internal/reservation/ledger.go` | Deletes stale/expired entries, increments `totalReclaimed` | Yes (`writeSnapshot`) |
| `reclaimInMemoryLocked()` | `internal/reservation/ledger.go` | Internal impl called by `Reclaim()` and `Load()` | Caller persists afterward |
| `Load()` | `internal/reservation/persist.go` | Replaces `l.entries`, then calls `reclaimInMemoryLocked()` | Yes (if reclaim > 0) |
| `SetNodeCapacity()` | `internal/reservation/ledger.go` | Updates `l.nodeRAM[node]` | No |

### 2.2 Callers of ledger mutation in production

- `internal/execution/guarded.go` – `runLocal()` and `runRemote()`:
  - Call `ledger.Reserve(entry)` before execution
  - Call `ledger.Release(execID)` via `defer` after execution
  - Call `heartbeatTask` → `ledger.Heartbeat()` every `executionHeartbeatInterval` during execution
- `internal/daemon/daemon.go` – `New()`:
  - Calls `ledger.Load()` on startup (reclaims stale entries and emits receipts after persistence)
- `internal/daemon/daemon.go` – `doRefresh()`:
  - Reloads the ledger on every refresh using the same reconciliation contract
- `internal/runtimectx/context.go` – `Load()`:
  - Creates a fresh ledger, calls `ledger.Load()` (same persisted reconciliation contract), then `SetNodeCapacity` for each node
- `cmd/axis/task.go` and `cmd/axis/context.go`:
  - Call `state.Load()` (not ledger directly), but the daemon cache applies the ledger overlay

### 2.3 State mutations (`~/.axis/state.json`)

| Function | File | What it mutates | Persist? |
|----------|------|-----------------|----------|
| `Load()` | `internal/state/state.go` | Reads state and applies only pending one-time schema migration | Only if migration is pending |
| `Maintain()` / `MaintainWithReport()` | `internal/state/state.go` | Reclaims stale legacy execution state, normalizes tracking, prunes failures/nodes | No; caller chooses persistence |
| Daemon refresh | `internal/daemon/daemon.go` | Runs maintenance, persists through `state.Update`, then emits receipts | Yes, when changed |
| `reclaimStaleReservation()` | `internal/state/state.go` | Delegates to dead-owner and stale-heartbeat cleanup | Via daemon maintenance transaction |
| `normalizeNodeStateExecTracking()` | `internal/state/state.go` | Reconciles `ActiveTasks`, `ReservedMB`, execution maps | Via daemon maintenance transaction |
| `RecordPlacement()` | `internal/state/state.go` | Appends to `Decisions` slice (capped at 20), calls `Save()` | Yes |
| `RecordObservation()` | `internal/state/observations.go` | Upserts into `Observations` map | No (caller must `Save`) |
| `applyFailureOutcome()` | `internal/execution/guarded.go` | Records failure in `st.Failures` | Yes (via `Save`) |
| `applySuccessOutcome()` | `internal/execution/guarded.go` | Records success in `st.Failures` | Yes (via `Save`) |
| `recordExecutionOutcome()` | `internal/execution/guarded.go` | Calls `RecordObservation`, then `st.Save()` | Yes |

## 3. Dual-Reclamation Problem

Two independent packages reclaim stale reservations, on different triggers, with different rules:

### 3.1 Ledger reclamation (`internal/reservation/`)

- **Trigger**: every `ledger.Load()` (daemon startup, daemon refresh, and direct
  runtime loads) and `ledger.Reclaim()` (explicit call; no separate reclaim ticker).
- **Rules**:
  - `entry.IsStale(now, HeartbeatStaleWindow)` → default **2 minutes**
  - `entry.IsExpired(now)` → hard expiry if `ExpiresAt` set
- **Scope**: Per-entry; checks `LastHeartbeat` on each `Entry`
- **Persistence**: Writes cleaned set back to `ledger.json`

### 3.2 State reclamation (`internal/state/`)

- **Trigger**: explicit `state.Maintain()`. Daemon refresh persists the result;
  CLI context reads may use an in-memory maintained view without writing.
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
4. **Derived view boundary**: a supplied ledger is authoritative including
   zero. State is used only by explicit legacy callers that supply no ledger,
   so disagreement cannot override an available ledger.

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
} else if st != nil && st.Nodes != nil {
    if ns, ok := st.Nodes[node.Name]; ok {
        reserved = ns.ReservedMB
    }
}
```

A supplied ledger is authoritative, including zero. State remains an explicit
compatibility source only for callers that do not supply a ledger.

Load success is part of authority availability. A read or decode failure is not
converted into an empty ledger or a state-derived reservation: runtime loading
and daemon refresh fail closed, preserving the canonical file and any previously
published daemon snapshot for operator recovery.

## 5. Observable Reconciliation

Persisted reclamation emits structured advisory receipts in these paths:

1. **`ledger.Load()` startup reclaim**
   - `internal/reservation/persist.go` calls `l.reclaimInMemoryLocked()` after loading entries.
   - After the cleaned ledger is persisted, one warning-level structured
     `maintenance receipt` is logged per reclaimed entry.
   - A persistence failure logs an error and emits no success receipt.

2. **Daemon state maintenance**
   - `state.Load()` is read-only apart from one-time schema migrations.
   - Daemon refresh calls `MaintainWithReport()` inside `state.Update()` when a
     preview finds cleanup work.
   - After persistence succeeds, it logs one aggregate maintenance receipt per
     changed node record plus one for expired failure-record cleanup.
   - In-memory CLI maintenance previews do not emit a persisted-change receipt.

3. **No ledger file watcher**
   - The daemon watches `state.json` (`WatchState`) and `skills.json` (`WatchSkills`), but **does not watch `ledger.json`**.
   - An external write does not trigger an immediate refresh. The daemon reloads
     the ledger on its next scheduled or event-driven refresh.
   - Ledger mutations via the daemon's own `Reserve`/`Release`/`Heartbeat` calls
     update its in-memory ledger and persist to disk. Other processes receive no
     file event and observe the change when they next load the ledger.

## 6. Grep Invariants (expected single-mutation packages)

```text
# Ledger JSON should only be written from internal/reservation/
grep -rn 'WritePrivateFileAtomic' internal/reservation/ --include='*.go' | grep -v '_test.go'
# → Only internal/reservation/persist.go (persist.LedgerPath path) writes ledger.json

# Ledger Reserve/Release/Heartbeat may only be called from the documented
# surfaces: internal/execution/ (guarded execution) and the advisory lease
# surfaces internal/api/v2.go (/v2/reservations) and internal/mcp/triangle.go
grep -rn '\.Reserve(\|\.Release(\|\.Heartbeat(' internal/ --include='*.go' | grep -v '_test.go' | grep -v 'internal/reservation/'
# → Hits only in internal/execution/guarded.go, internal/api/v2.go, internal/mcp/triangle.go

# State JSON should only be written from internal/state/
grep -rn 'WritePrivateFileAtomic' internal/state/ internal/execution/ --include='*.go' | grep -v '_test.go'
# → Only internal/state/state.go writes state.json (guarded.go writes execution logs, not state)

# Snapshot overlay should be read-only (no RAMReservedMB assignment outside the overlay)
grep -rn 'RAMReservedMB\s*=' internal/ --include='*.go' | grep -v '_test.go' | grep -v 'internal/snapshotview/'
# → Zero hits (only internal/snapshotview/overlay.go assigns node.RAMReservedMB)
```

## 7. Summary

- **Canonical authority**: `internal/reservation/ledger.go` / `~/.axis/ledger.json`
- **Legacy mirror**: `internal/state/state.go` / `~/.axis/state.json` (still actively mutated and loaded)
- **Dual cleanup**: Ledger reconciliation and explicit state maintenance retain different rules because state is a legacy mirror; `state.Load()` itself does not reclaim.
- **Receipts**: Successful persisted cleanup emits structured maintenance receipts; failed persistence cannot emit a success receipt.
- **Risk**: The state mirror can drift from the ledger canonical source. A supplied ledger is authoritative in the derived snapshot, including zero; state is consulted only when no ledger is supplied.
