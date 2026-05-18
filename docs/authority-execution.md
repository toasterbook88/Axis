# AUTH-5: Execution State Authority

## 1. Who Tracks Running Execution State

Execution state is tracked by **two independent systems** with overlapping concerns:

### 1.1 `internal/state/state.go` — Legacy/Advisory Tracking

`NodeState` within `ClusterState` (persisted to `~/.axis/state.json`) tracks per-execution metadata:

| Field | Type | Purpose |
|-------|------|---------|
| `ActiveExecs` | `[]string` | List of active execution IDs |
| `ExecReservationsMB` | `map[string]int64` | Per-exec RAM reservation |
| `ExecHeartbeatAt` | `map[string]time.Time` | Last heartbeat timestamp per exec |
| `ExecOwnerPID` | `map[string]int` | Owner process ID per exec |
| `ExecOwnerSurface` | `map[string]string` | Owner surface label per exec |
| `ExecOwnerLabel` | `map[string]string` | Owner label per exec |
| `ExecOrigin` | `map[string]models.ExecutionOrigin` | Caller provenance per exec |

These fields are mutated by:
- **Guarded execution** (`internal/execution/guarded.go`) — creates entries during `runLocal`/`runRemote` by reserving through the ledger, but the `state.json` fields are populated indirectly via the legacy reservation path or loaded from disk.
- **State load/maintenance** (`internal/state/state.go`) — `Load()` → `runMaintenance()` normalizes and reclaims stale entries on every read.

### 1.2 `internal/reservation/ledger.go` — Primary Reservation Authority

The `Ledger` (persisted to `~/.axis/ledger.json`) is the **double-entry reservation system** introduced to replace heuristic RAM sharing. It tracks:

| Field | Type | Purpose |
|-------|------|---------|
| `ID` | `string` | Reservation entry ID (same as exec ID) |
| `Node` | `string` | Target node name |
| `OwnerExecID` | `string` | Execution ID |
| `OwnerPID` | `int` | Owner process ID |
| `OwnerOrigin` | `models.ExecutionOrigin` | Caller provenance |
| `RAMMB` | `int64` | Reserved RAM in MB |
| `VRAMMB` | `int64` | Reserved VRAM in MB |
| `CreatedAt` | `time.Time` | Reservation creation time |
| `LastHeartbeat` | `time.Time` | Last liveness timestamp |
| `ExpiresAt` | `time.Time` | Optional hard expiry |

**Who creates ledger entries:**
- `runLocal` (`internal/execution/guarded.go:692-716`) — creates a `reservation.Entry` with `OwnerPID = os.Getpid()` and calls `ledger.Reserve(entry)`.
- `runRemote` (`internal/execution/guarded.go:778-788`) — same pattern for remote executions.
- API server (`internal/api/server.go`) — passes the daemon's ledger into `execution.RunGuarded` via the runtime context.

## 2. Is Execution Liveness Advisory or Authoritative for Lease Validity?

**The ledger heartbeat is authoritative for reservation validity.**

- The ledger's `HeartbeatStaleWindow` defaults to **2 minutes** (`internal/reservation/ledger.go:DefaultLimits`).
- An entry whose `LastHeartbeat` is older than 2 minutes is considered **stale** and will be reclaimed by `Ledger.Reclaim()`.
- Placement consults the ledger for allocatable headroom (`Ledger.AllocatableRAM`), which subtracts only **non-stale** active entries.

The `state.json` heartbeat tracking (`ExecHeartbeatAt`) is **advisory** and exists for:
- Daemon state display and legacy compatibility.
- Cross-checking during `state.Load()` maintenance.
- It does **not** gate placement decisions directly; the ledger does.

## 3. Relationship Between Execution Heartbeats and Ledger Heartbeats

There is **one active heartbeat path** during execution:

```
runLocal / runRemote
  → runWithReservationHeartbeat(ledger, execID, runFunc)
    → goroutine: runs the actual shell command
    → ticker: every 15 seconds (executionHeartbeatInterval)
      → heartbeatTask(ledger, execID)
        → ledger.Heartbeat(execID)
          → updates entry.LastHeartbeat
          → persists ledger.json
```

**Key facts:**
- The heartbeat interval is **15 seconds** (`internal/execution/guarded.go:48`).
- The ledger stale window is **2 minutes** (`internal/reservation/ledger.go:93`).
- There is **no corresponding heartbeat written to `state.json`**. The `ExecHeartbeatAt` map in `state.json` is updated only through legacy paths or during load-time normalization.
- When execution finishes (success or failure), `ledger.Release(execID)` is called via `defer`, removing the ledger entry immediately.

## 4. Source of Truth for Execution Records

| Concern | Source of Truth | File |
|---------|-----------------|------|
| **Active reservations** | `ledger.json` | `~/.axis/ledger.json` |
| **Reservation accounting** | `Ledger` in daemon | `internal/reservation/ledger.go` |
| **Historical decisions** | `state.json` `Decisions` | `~/.axis/state.json` (last 20) |
| **Failure immune system** | `state.json` `Failures` | `~/.axis/state.json` |
| **Empirical observations** | `state.json` `Observations` | `~/.axis/state.json` |
| **Legacy exec tracking** | `state.json` `NodeState` | `~/.axis/state.json` |

**`ledger.json` is the source of truth for "what is currently reserved where."**

`state.json` retains legacy `NodeState` exec tracking fields for backward compatibility and diagnostic purposes, but placement and execution guardrails now primarily consult the ledger. The daemon holds the canonical ledger instance (`internal/daemon/daemon.go:164`).

## 5. Paths Where Execution Probing Independently Reclaims Reservations

There are **two independent reclamation paths** that can release reservations without the executing process explicitly calling `ledger.Release`:

### 5.1 Ledger Reclaim (`internal/reservation/ledger.go:242-276`)
- **Trigger:** Called explicitly by daemon refresh, API handlers, or startup (`Ledger.Reconcile()` alias).
- **Rule:** Removes entries where `IsStale(now, HeartbeatStaleWindow)` or `IsExpired(now)`.
- **Scope:** All entries in `ledger.json`.
- **Persistence:** Re-saves `ledger.json` after reclaim.

### 5.2 State.json Reclaim (`internal/state/state.go:157-203`)
- **Trigger:** `state.Load()` on every load (CLI reads, daemon refresh, API handlers).
- **Rules:**
  - `reclaimDeadOwnerExecutions()` — checks if owner PID is alive via `processAlive()` (OS signal 0 on Unix, always-true stub on other platforms).
  - `reclaimHeartbeatStaleExecutions()` — `now.Sub(hb) > execHeartbeatStaleAfter` (2 minutes).
  - Legacy fallback: `now.Sub(LastPlacedAt) > staleReservationReclaimAfter` (45 minutes).
  - `shouldDropAncientNodeState()` — drops node state after 45 min (no execs) or 24 h (legacy).
- **Scope:** Per-node `NodeState`; checks `ExecHeartbeatAt`, `ExecOwnerPID`, `LastPlacedAt`.
- **Persistence:** Writes cleaned set back to `state.json`.

### 5.3 Why This Is a Problem

These two reclaimers operate on **different files**, with **different triggers**, and **different PID semantics**:

| Reclaimer | File | Trigger | PID Check | Heartbeat Window |
|-----------|------|---------|-----------|------------------|
| Ledger | `ledger.json` | Explicit `Reclaim()` / `Reconcile()` | None | 2 min |
| State maintenance | `state.json` | Every `Load()` | `processAlive()` signal 0 | 2 min |

**Risk:** A process that crashes without calling `ledger.Release` will have its ledger entry reclaimed after 2 minutes of missing heartbeats. The corresponding `state.json` entry may also be reclaimed via PID check or heartbeat stale detection during the next `Load()`. However, because they operate independently, there are windows where the two views diverge:
- Ledger may reclaim while `state.json` still shows the exec as active.
- `state.json` may reclaim based on PID death while the ledger entry is still valid (if heartbeats continued from a different process or the PID was reused).

There is **no cross-file synchronization** between the ledger and `state.json` reclamation.

## Summary Table

| Property | Value |
|----------|-------|
| Primary reservation source of truth | `ledger.json` (`internal/reservation/ledger.go`) |
| Legacy exec tracking | `state.json` `NodeState` fields |
| Heartbeat writer | `internal/execution/guarded.go` (`runWithReservationHeartbeat`) |
| Heartbeat interval | 15 seconds |
| Ledger stale threshold | 2 minutes |
| State heartbeat stale threshold | 2 minutes (`execHeartbeatStaleAfter`) |
| PID-based reclaim | `internal/state/state.go` (`reclaimDeadOwnerExecutions`) |
| Independent reclaim paths | 2 (ledger + state.json) |
