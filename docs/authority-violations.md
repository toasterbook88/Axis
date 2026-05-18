# AUTH-11: Authority Inversion Detection

## 1. Definition

**Authority inversion** occurs when a derived or secondary system influences the canonical authority without using an approved write path.

In AXIS, the canonical authorities are:

- **Reservation authority**: `internal/reservation/ledger.go` / `~/.axis/ledger.json`
- **Snapshot authority**: `internal/snapshot/` (built from live probes)
- **Freshness authority**: `internal/daemon/daemon.go` (wall-clock + discovery window)
- **State authority**: `internal/state/state.go` / `~/.axis/state.json` (placement memory, failures, observations)

Inversion happens when a mirror, cache, test, CLI heuristic, or observability surface mutates canonical state, backfills canonical fields from derived sources, or reclaims resources outside the ledger.

---

## 2. Prohibited Patterns

### 2.1 Bypass Writes

Writing reservation or state data to `~/.axis/state.json` when the canonical write path is `~/.axis/ledger.json`.

**Concrete AXIS example:**

`internal/state/state.go:108-118` calls `runMaintenance()` inside `Load()`, which reclaims stale reservations and then calls `Save()`:

```go
// Maintenance: prune and reclaim stale state on every load.
if maintained := runMaintenance(&s); maintained {
    mutated = true
}

if mutated {
    if err := s.Save(); err != nil {
        return nil, err
    }
}
```

This writes reclaimed state back to `state.json`, bypassing the ledger which is the canonical reservation authority.

### 2.2 Heuristic Freshness Overrides

Guessing or overriding freshness metadata instead of using the daemon's canonical evaluator.

**Concrete AXIS example:**

`internal/daemon/client.go:75-78` backfills `snap.Freshness` from `meta.Freshness` when the snapshot body lacks one:

```go
if snap.Freshness == nil && meta.Freshness != nil {
    freshness := *meta.Freshness
    snap.Freshness = &freshness
}
```

The snapshot body and the metadata endpoint are separate reads. Injecting metadata freshness into an older snapshot body creates a mixed-epoch view where the freshness claim is newer than the node facts it describes.

### 2.3 Repair-on-Read Mutation

Mutating persisted state during a `Load()` call before returning it to the caller.

**Concrete AXIS example:**

`internal/state/state.go:72-119` — `Load()` not only reads `state.json` but also runs migrations, reclaims dead/stale executions, normalizes `ActiveTasks`/`ReservedMB`, deletes ancient nodes, and writes the result back to disk before returning:

```go
func Load() (*ClusterState, error) {
    // ... read file ...
    // One-time migrations
    if s.Version < currentStateVersion {
        if migrated := runMigrations(&s); migrated {
            mutated = true
        }
    }
    // Maintenance: prune and reclaim stale state on every load.
    if maintained := runMaintenance(&s); maintained {
        mutated = true
    }
    if mutated {
        if err := s.Save(); err != nil {
            return nil, err
        }
    }
    return &s, nil
}
```

Every CLI invocation that touches state (`axis task context`, `axis task place`, daemon refresh) can trigger a repair-on-read rewrite of `state.json`.

### 2.4 Mirror-to-Canonical Reconciliation Without Event Emission

Reconciling a mirror back into canonical form without emitting events or warnings that operators can observe.

**Concrete AXIS examples:**

1. `internal/state/state.go:187-191` — `reclaimStaleReservation()` is called inside `runMaintenance()`, silently zeroing `ReservedMB` and deleting `ActiveExecs`. No event is emitted; the daemon's `WatchState` may eventually notice the disk write and refresh, but the reclaim itself is invisible.

```go
reclaimed, reclaimedAny := reclaimStaleReservation(now, ns, legacyHeartbeatMode)
if reclaimedAny {
    ns = reclaimed
    mutated = true
}
```

2. `internal/reservation/persist.go:50-52` — `ledger.Load()` silently reclaims stale entries on startup:

```go
reclaimed := l.reclaimLocked()
if reclaimed > 0 {
    l.logger.Info("startup reconciliation complete", "reclaimed", reclaimed)
}
```

A daemon restart can silently drop reservations that missed their heartbeat window while the daemon was down. No warning is appended to the snapshot, no error is returned, and no `StateChange*` event fires.

### 2.5 Observability-Derived State Mutation

Using metrics, metadata, or derived summaries to trigger or influence reservation changes.

**Concrete AXIS example:**

`internal/daemon/daemon.go:653-664` — `Meta()` falls back to `state.Load()` for `ReservedMB` when the ledger is unavailable:

```go
if d.ledger != nil {
    meta.ReservedMB = d.ledger.Summary().TotalReservedMB
} else if st, err := state.Load(); st != nil {
    for _, ns := range st.Nodes {
        meta.ReservedMB += ns.ReservedMB
    }
    ...
}
```

The metrics endpoint (`/v2/metrics`) and the doctor endpoint (`/v2/doctor`) consume `Meta()`, so a missing ledger causes metrics to derive `ReservedMB` from the state mirror. If an operator or external system uses those metrics to make placement or scaling decisions, the decision is based on inverted authority.

---

## 3. Current Violations Found in Code

### 3.1 `state.go` reclaiming stale reservations (should be ledger-only)

```go
internal/state/state.go:388-416   reclaimStaleReservation()
internal/state/state.go:425-510   reclaimHeartbeatStaleExecutions()
internal/state/state.go:512-606   reclaimDeadOwnerExecutions()
```

These functions independently prune reservations using rules (45 min legacy, 2 min heartbeat, PID death detection) that differ from the ledger's 2-minute heartbeat window. The state file should not reclaim reservations; the ledger owns all reclamation.

### 3.2 `state.go` mutating during `Load()` (repair-on-read)

```go
internal/state/state.go:72-119    Load() → runMaintenance() → Save()
internal/state/state.go:155-203   runMaintenance()
internal/state/state.go:227-386   normalizeNodeStateExecTracking()
```

`Load()` is a read surface. It should return what is on disk. Instead it mutates `ActiveTasks`, `ReservedMB`, `ExecReservationsMB`, deletes nodes, and writes back to disk before returning.

### 3.3 Direct `ReservedMB` writes outside `internal/reservation/`

**Writes to `NodeState.ReservedMB` (state authority, not ledger):**

```go
internal/state/state.go:381     ns.ReservedMB = reservedSum
internal/state/state.go:414     ns.ReservedMB = capMB
internal/state/state.go:482     ns.ReservedMB = 0
internal/state/state.go:508     ns.ReservedMB = sumExecReservations(reservations)
internal/state/state.go:578     ns.ReservedMB = 0
internal/state/state.go:604     ns.ReservedMB = sumExecReservations(reservations)
```

**Test-only writes (acceptable in tests, but documented for completeness):**

```go
internal/daemon/daemon_test.go:502     ns.ReservedMB = 512
```

**Derived-view writes (overlaying snapshot, not canonical):**

```go
internal/snapshotview/overlay.go:88     node.RAMReservedMB = reserved
internal/runtimectx/context_test.go:46     nodes[0].RAMReservedMB = 512
internal/daemon/daemon_test.go:835     snap.Nodes[0].RAMReservedMB = 2048
internal/api/server_test.go:1487     summary.TotalReservedMB += node.RAMReservedMB
internal/placement/placement_test.go:*  numerous RAMReservedMB assignments in test data
```

The overlay assignment in `snapshotview/overlay.go:88` is expected behavior for a derived read surface, but it is included here to show the complete boundary of where `RAMReservedMB` is assigned.

---

## 4. Detection Invariants (Grep Commands Operators Can Run)

```bash
# 1. Detect state-file reclamation (should not happen; ledger should own it)
grep -rn 'reclaimStaleReservation\|reclaimDeadOwnerExecutions\|reclaimHeartbeatStaleExecutions' internal/ --include='*.go' | grep -v '_test.go'

# 2. Detect repair-on-read inside Load()
grep -n 'func Load()' internal/state/state.go && grep -n 'runMaintenance\|Save()' internal/state/state.go

# 3. Detect direct ReservedMB writes outside internal/reservation/ (production)
grep -rn '\.ReservedMB\s*=' internal/ --include='*.go' | grep -v '_test.go' | grep -v 'internal/reservation/'

# 4. Detect direct RAMReservedMB writes outside internal/snapshotview/ (production)
grep -rn '\.RAMReservedMB\s*=' internal/ --include='*.go' | grep -v '_test.go' | grep -v 'internal/snapshotview/'

# 5. Detect state.Save() calls outside internal/state/ and internal/execution/
grep -rn '\.Save()' internal/ --include='*.go' | grep -v '_test.go' | grep -v 'internal/state/' | grep -v 'internal/execution/' | grep -v 'ledger\|skills\|config\|persist'

# 6. Detect freshness backfill from metadata into snapshot
grep -rn 'meta\.Freshness.*snap\.Freshness\|snap\.Freshness.*meta\.Freshness' internal/ --include='*.go' | grep -v '_test.go'

# 7. Detect ledger fallback in metadata / metrics
grep -rn 'state\.Load().*ReservedMB\|ReservedMB.*state\.Load' internal/ --include='*.go' | grep -v '_test.go'
```

---

## 5. Remediation Priority

| Priority | Violation | Location | Impact | Suggested Fix |
|----------|-----------|----------|--------|---------------|
| **P0** | Repair-on-read mutation | `internal/state/state.go:72-119` | Every CLI invocation mutates `state.json` silently | Remove `runMaintenance()` from `Load()`. Move maintenance to an explicit `Maintain()` called only by the daemon or CLI `axis doctor` |
| **P0** | Dual reclamation | `internal/state/state.go:388-606` | State and ledger prune independently with different rules | Delete state-file reclamation entirely. Rely on ledger `Reclaim()` as the single source of truth |
| **P1** | Heuristic freshness override | `internal/daemon/client.go:75-78` | Mixed-epoch freshness backfill | Remove backfill. Return snapshot as-is; let callers request `/snapshot/meta` separately if they need freshness |
| **P1** | Ledger fallback in metadata | `internal/daemon/daemon.go:653-664` | Metrics derive from state mirror when ledger is nil | Remove `state.Load()` fallback from `Meta()`. If ledger is nil, report `ReservedMB: -1` or omit the field |
| **P2** | Test-only ReservedMB writes | `internal/daemon/daemon_test.go:502`, etc. | Tests build invalid state | Ensure test helpers use ledger APIs or document that they intentionally simulate legacy state |
| **P2** | Normalization during Load | `internal/state/state.go:227-386` | `normalizeNodeStateExecTracking` rewrites exec maps on read | Move normalization to explicit maintenance or deprecation path |

---

## 6. Summary

- **Canonical reservation authority** is `internal/reservation/ledger.go`.
- **State file** (`~/.axis/state.json`) acts as a legacy mirror that still reclaims and normalizes reservations independently.
- **Repair-on-read** in `state.Load()` means every CLI invocation can silently rewrite persisted state.
- **Freshness backfill** in `daemon/client.go` mixes metadata epoch into snapshot body.
- **Ledger fallback** in `daemon.Meta()` causes metrics to reflect mirror state when canonical is unavailable.
- **Remediation** should prioritize removing mutation from read paths and unifying reclamation under the ledger.
