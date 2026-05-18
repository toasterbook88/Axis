# Reservation Semantics

> Operator-facing guide to how AXIS tracks intent to consume RAM on cluster nodes.

## 1. What a reservation is

A **reservation** is an explicit declaration of intent to use a specific amount of RAM (and optionally VRAM) on a named node. It does **not** start a process, allocate kernel memory, or change cgroup limits. It is purely bookkeeping so that placement and status queries can report accurate allocatable headroom.

Reservations are created before execution and removed after execution finishes, succeeds, or fails. While a reservation is alive, the reserved RAM is subtracted from the node's reported allocatable capacity.

## 2. How reservations are created

### `axis task run`
When you run a task via the CLI, the execution path (`internal/execution/guarded.go`) automatically reserves RAM before starting the process:

1. Placement selects a node and estimates required RAM (`ReservationMBForRequirements`).
2. The runner calls `ledger.Reserve(entry)` with:
   - `ID`: execution ID
   - `Node`: chosen node name
   - `RAMMB`: estimated reservation
   - `OwnerExecID`, `OwnerPID`, `OwnerSurface`, `OwnerOrigin`: provenance
3. The process starts.
4. A `defer` ensures `ledger.Release(execID)` is called when the process exits.

### HTTP API
`POST /v2/reservations` is scaffolded but **not yet implemented**. The handler currently returns `501 Not Implemented`. When wired, it will allow operators to create reservations manually via the ledger API.

## 3. How reservations expire

Reservations are liveness-sensitive. Each reservation carries a `LastHeartbeat` timestamp. The runner sends heartbeats every **15 seconds** (`executionHeartbeatInterval` in `internal/execution/guarded.go`).

If a reservation misses heartbeats for longer than the **stale window** (default **2 minutes**, `HeartbeatStaleWindow` in `internal/reservation/ledger.go`), it is considered stale and eligible for reclamation.

Reservations may also carry an optional hard `ExpiresAt`. When that time passes, the reservation is unconditionally expired regardless of heartbeats.

## 4. Reservations vs. execution

| Concern | Reservation | Execution |
|---------|-------------|-----------|
| What it is | Bookkeeping entry in the ledger | Actual OS process (local or remote) |
| Lifetime | Created before start, released after finish | Starts after reserve, ends on exit |
| Failure mode | Stale reservations are reclaimed | Crashes are recorded in the failure immune system |
| Authority | `~/.axis/ledger.json` | Observed process state + `~/.axis/state.json` |

A reservation can exist without a running process (orphan), and a process can in theory run without a reservation if the ledger is bypassed. The guarded execution path keeps the two tightly coupled.

## 5. Canonical authority: `~/.axis/ledger.json`

The **ledger** (`internal/reservation/ledger.go`) is the single source of truth for "what is reserved where." It is the only production path that writes to `~/.axis/ledger.json`.

Key properties:
- Mutex-protected in-memory map of `Entry` values.
- Atomic disk writes via `persist.WriteFileAtomic`.
- Startup reconciliation pass (`Load()` calls `reclaimLocked()` silently).
- Tracks totals: `totalReserved`, `totalReleased`, `totalReclaimed`, `reserveFailures`.

## 6. Ledger vs. `state.json` (`NodeState.ReservedMB`)

AXIS maintains **two** persisted views of reservation state:

| File | Package | Role |
|------|---------|------|
| `~/.axis/ledger.json` | `internal/reservation/` | **Canonical authority** |
| `~/.axis/state.json` | `internal/state/` | **Legacy mirror** |

`state.json` stores `NodeState.ReservedMB`, `ActiveExecs`, `ExecReservationsMB`, `ExecHeartbeatAt`, and `ExecOwnerPID`. It was created before the ledger existed and is still loaded by the daemon refresh path, CLI reads, and API handlers. The mirror is maintained independently; it is not driven by ledger events.

## 7. Inspecting active reservations

### HTTP API (daemon must be running)
```bash
curl -H "Authorization: Bearer $AXIS_TOKEN" \
  http://localhost:8080/v2/reservations
```
Returns:
- `cluster`: per-node and cluster-wide summaries
- `reservations`: full list of `Entry` objects with IDs, nodes, RAM, heartbeats, and owners

### CLI (indirect)
There is **no dedicated CLI subcommand** yet. Reservations are visible indirectly:
- `axis status --cached` shows `RAMReservedMB` and `RAMAllocatableMB` per node.
- `axis task place` reports allocatable headroom after subtracting reservations.

## 8. How stale reservations are reclaimed

### Ledger reclamation
- **Trigger**: `ledger.Load()` on daemon startup, or explicit `ledger.Reclaim()`.
- **Rule**: `entry.IsStale(now, 2m)` or `entry.IsExpired(now)`.
- **Scope**: Per-entry.
- **Persist?** Yes — writes cleaned set back to `ledger.json`.

### State reclamation
- **Trigger**: `state.Load()` on **every** load (daemon refresh, CLI reads, API handlers).
- **Rules**:
  1. `reclaimDeadOwnerExecutions()` — drops execs whose owner PID is no longer alive.
  2. `reclaimHeartbeatStaleExecutions()` — drops execs whose last heartbeat is older than **2 minutes**.
  3. Legacy fallback — caps `ReservedMB` after **45 minutes** if the node uses the pre-heartbeat tracking mode.
  4. `shouldDropAncientNodeState()` — drops the entire `NodeState` after **45 minutes** (no execs) or **24 hours** (legacy).
- **Scope**: Per-node `NodeState`.
- **Persist?** Yes — writes cleaned set back to `state.json`.

## 9. The dual-reclamation problem

Both `ledger.json` and `state.json` independently prune stale reservations, but they do so on different triggers with different rules:

| Aspect | Ledger | State |
|--------|--------|-------|
| Trigger | Startup `Load()`, explicit `Reclaim()` | Every `Load()` (frequent) |
| Stale window | 2 minutes | 2 minutes (heartbeat mode), 45 minutes (legacy) |
| Extra checks | Hard expiry only | PID death, ancient node drop |
| Cross-checks | None — does not read `state.json` | None — does not read `ledger.json` |

This means the two files can drift:
- A reservation may be alive in the ledger but already purged from state.
- A reservation may be purged from the ledger but still counted in state.

The derived snapshot view (`internal/snapshotview/overlay.go`) resolves this at read time with precedence:

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

**Ledger wins; state is a fallback for zero.** This means state under-reporting is invisible to the operator, while ledger under-reporting would be visible because the fallback would not fire.

Neither reclamation path emits events or warnings. A daemon restart can silently drop missed-heartbeat reservations, and every CLI invocation that calls `state.Load()` can rewrite `state.json` without notice.

## 10. Future: reservation CLI commands

No dedicated reservation CLI commands exist today. The following are planned but not scheduled:

- `axis reservation list` — show active ledger entries
- `axis reservation show <id>` — detail a single reservation
- `axis reservation create --node <name> --ram <mb>` — manual reservation
- `axis reservation release <id>` — manual release
- `axis reservation reclaim` — trigger orphan sweep

Until then, use the HTTP `/v2/reservations` endpoint or inspect reservations indirectly via `axis status` and `axis task place`.
