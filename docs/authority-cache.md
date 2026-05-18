# AUTH-8: Cache Authority

## 1. What Caches Exist

| Cache | Location | Persistence | Purpose |
|-------|----------|-------------|---------|
| **Daemon snapshot cache** | `internal/daemon/daemon.go` (`Daemon.snapshot`) | `~/.axis/snapshot.json` | In-memory copy of the last `ClusterSnapshot` collected by the daemon; served via HTTP API. |
| **Placement / runtime cache** | `internal/runtimectx/context.go` (`Context.Snapshot`) | None (live-build only) | Fresh snapshot built on every `runtimectx.Load()` call for CLI commands that bypass the daemon. |
| **State file** | `internal/state/state.go` (`ClusterState`) | `~/.axis/state.json` | Reservations, failure records, observations, and recent placement decisions. |
| **Skills file** | `internal/skills/skills.go` (`Store`) | `~/.axis/skills.json` | Learned skills and failures from past executions. |
| **Ledger file** | `internal/reservation/ledger.go` (`Ledger`) | `~/.axis/ledger.json` | Double-entry reservation accounting (see AUTH-5). |

The **daemon snapshot cache** is the primary read surface for `axis status --cached`, the HTTP API (`/snapshot`), and the MCP server. The **runtime cache** is the fallback when the daemon is unreachable or when `--cached` is not used.

## 2. Who Owns Each Cache

### 2.1 Daemon Snapshot Cache

| Operation | Owner | Code Path |
|-----------|-------|-----------|
| **Write** | Daemon background refresh goroutine | `Daemon.doRefresh()` (`internal/daemon/daemon.go:477`) |
| **Read** | HTTP handlers (`/snapshot`, `/health`), CLI `axis daemon status`, `axis status --cached` | `Daemon.Snapshot()`, `Daemon.Meta()` |
| **Invalidate** | Daemon config/state/skills watchers, CLI `axis daemon invalidate`, API `POST /invalidate` | `Daemon.Invalidate()` |
| **Refresh trigger** | Interval ticker, config file changes, state file changes, skills file changes, beacon changes, mesh peer events, execution state changes | `Daemon.refreshWithTrigger()` |

The daemon is the **sole writer** of the snapshot cache. No external process mutates `Daemon.snapshot` directly; all mutations go through `doRefresh`, which holds `d.mu.Lock()`.

### 2.2 Placement / Runtime Cache

| Operation | Owner | Code Path |
|-----------|-------|-----------|
| **Write** | `runtimectx.Load()` on every invocation | `internal/runtimectx/context.go:36` |
| **Read** | CLI commands (`axis status`, `axis task place`, `axis task context`, `axis facts`) | Same function returns the built context |
| **Invalidate** | Not applicable — there is no persistent cache; each call rebuilds from discovery + state + skills | — |

This is a **build-on-demand** cache with no persistence and no sharing between invocations.

### 2.3 State File (`~/.axis/state.json`)

| Operation | Owner | Code Path |
|-----------|-------|-----------|
| **Write** | Guarded execution (`recordExecutionOutcome`), placement (`RecordPlacement`), state maintenance | `state.Save()`, `ClusterState.RecordPlacement()` |
| **Read** | Daemon refresh, `runtimectx.Load()`, CLI `axis context show`, placement | `state.Load()` |
| **Invalidate / clear** | CLI `axis context clear` | `os.Remove(state.Path())` |

State is mutated by **execution outcomes** and **load-time maintenance** (reclaiming stale reservations, pruning failures). The daemon watcher (`WatchState`) detects disk changes and triggers a daemon cache refresh.

### 2.4 Skills File (`~/.axis/skills.json`)

| Operation | Owner | Code Path |
|-----------|-------|-----------|
| **Write** | Post-execution success/failure recording | `skills.Store.RecordSuccess()`, `RecordFailure()` |
| **Read** | Daemon refresh (merges warnings into snapshot), `runtimectx.Load()`, placement | `skills.Load()` |
| **Invalidate / clear** | No explicit clear command; file can be deleted manually | — |

Skills are written **only after execution** and are read during snapshot assembly to provide advisory placement context.

## 3. Cache Mutability After Publication

### 3.1 Daemon Snapshot Cache

**Published snapshots are immutable in theory but mutable in practice.**

- `Daemon.Snapshot()` returns a **deep clone** via `snapshotview.Clone()` (`internal/daemon/daemon.go:597`). This prevents callers from mutating the internal cache.
- However, the daemon's internal `snapshot` pointer is **overwritten** on every `doRefresh`. There is no versioning or copy-on-write; the old snapshot is simply replaced.
- `snapshot.json` on disk is overwritten via `os.WriteFile` after each refresh. There is no atomic rename or double-buffering.

### 3.2 Runtime Cache

The `ClusterSnapshot` returned by `runtimectx.Load()` is a fresh build. It is **not cloned** before being handed to callers, so the caller's copy is the only copy. Subsequent calls produce entirely new objects.

### 3.3 State and Skills Files

Both are **mutable** after load. `state.Load()` runs maintenance (reclaim, prune, normalize) and may **mutate and re-save** the file before returning it to the caller. This means the returned `ClusterState` may already differ from what was on disk seconds earlier.

## 4. Generation IDs and Epoch Semantics

**There are no generation IDs or epoch semantics in any AXIS cache.**

Searches for `SnapshotEpoch`, `snapshot_epoch`, `epoch`, `generation`, or `gen` in the cache-relevant packages (`internal/daemon`, `internal/snapshotview`, `internal/state`, `internal/skills`, `internal/runtimectx`) return no matches.

### What Is Missing

- `ClusterSnapshot` has a `Timestamp` (wall-clock UTC) but no monotonic generation counter.
- `Daemon.Meta()` exposes `RefreshCount` (a monotonic integer), but this is **not embedded into the snapshot payload** returned by `Snapshot()`.
- There is no way for a consumer to ask "is this snapshot newer than the one I already have?" except by comparing `Timestamp` (vulnerable to clock jumps) or `RefreshCount` (only available via the separate `/snapshot/meta` endpoint).

## 5. Double-Buffering and Atomic Publication

**Neither double-buffering nor atomic publication is used.**

### 5.1 Daemon Cache Publication

```go
// internal/daemon/daemon.go:535
d.snapshot = snapshotview.Clone(snap)
// ...
if err := persistSnapshot(d.snapshotPath, d.snapshot); err != nil {
    d.lastError = err.Error()
    return err
}
```

The sequence is:
1. Lock `d.mu`
2. Assign `d.snapshot = clone(snap)`
3. Unlock `d.mu`
4. Write `snapshot.json` to disk (non-atomic)

There is a window between step 3 and step 4 where the in-memory cache is newer than the disk file. Conversely, if the daemon crashes between step 2 and step 4, the in-memory state is lost.

### 5.2 State File Persistence

`state.Save()` uses `persist.WriteFileAtomic()`, which **does** perform an atomic write (write to temp, rename). However, the read-modify-write cycle in `state.Load()` is not atomic relative to other writers.

### 5.3 Skills File Persistence

`skills.Store.Save()` also uses `persist.WriteFileAtomic()`.

## 6. Cache Invalidation: Event-Driven vs Time-Driven

### 6.1 Time-Driven Triggers

| Cache | Trigger | Interval / Threshold |
|-------|---------|----------------------|
| Daemon snapshot | Background ticker | `defaultRefreshInterval = 1 minute` (`internal/daemon/daemon.go:35`) |
| Daemon staleness | Wall-clock age vs threshold | `defaultStaleThreshold = 5 minutes` (`internal/daemon/daemon.go:37`) |
| Ledger reclaim | Heartbeat stale window | `HeartbeatStaleWindow = 2 minutes` (`internal/reservation/ledger.go:93`) |
| State maintenance | Load-time reclaim | `staleReservationReclaimAfter = 45 minutes`, `execHeartbeatStaleAfter = 2 minutes` (`internal/state/state.go:62-64`) |

### 6.2 Event-Driven Triggers

| Event | Cache | Action |
|-------|-------|--------|
| Config file change | Daemon snapshot | `Invalidate()` + `RefreshNow()` (`WatchConfig`) |
| State file change | Daemon snapshot | `Invalidate()` + `RefreshNow()` (`WatchState`) |
| Skills file change | Daemon snapshot | `Invalidate()` + `RefreshNow()` (`WatchSkills`) |
| Beacon registry change | Daemon snapshot | `scheduleRefresh()` (`WatchDiscovery`) |
| Mesh peer join/leave | Daemon snapshot | `RefreshWithTrigger()` (`WatchMesh`) |
| Execution reserve/finish | Daemon snapshot | `scheduleCacheRefresh()` (`api/server.go`, `daemon/handlers.go`) |
| Manual CLI request | Daemon snapshot | `axis daemon refresh`, `axis daemon invalidate` |

**The daemon snapshot cache is both event-driven and time-driven.** The background ticker provides a baseline refresh, while file watchers and execution callbacks provide reactive invalidation. The runtime cache is purely on-demand (no background refresh).

## 7. Risk of Mixed-Epoch Reads

### 7.1 What Can Go Wrong

Because there is **no epoch or generation ID**, consumers can assemble a logically inconsistent view from multiple cache layers:

| Scenario | Layers Involved | Risk |
|----------|-----------------|------|
| Snapshot N + overlay N+1 | Daemon snapshot (older) + state file (newer) | `runtimectx.Load()` and `Daemon.doRefresh()` both load `state.json` **after** building the snapshot. If the state changed since the snapshot was taken, the reservation overlay reflects a newer world than the node facts. |
| Freshness N-1 + snapshot N | `FetchSnapshot` backfills `Freshness` from `Meta()` into an older snapshot | `internal/daemon/client.go:75-78` copies `meta.Freshness` into the snapshot if the snapshot lacks one. The freshness metadata may be newer than the snapshot body. |
| Ledger in-memory + snapshot disk | Daemon crashes between `d.snapshot = clone(snap)` and `persistSnapshot()` | On restart, the daemon loads an older `snapshot.json` but may load a current `ledger.json`, creating a reservation view that overestimates reserved RAM relative to the snapshot. |
| Skills file + snapshot | Skills file changes between snapshot build and skill read | `Daemon.doRefresh()` loads skills **after** cloning the snapshot. A concurrent skill write could mean the warning attached to the snapshot references skill state newer than the node facts. |

### 7.2 Specific Mixed-Epoch Example

```
T0: Daemon builds snapshot N from nodes A, B, C
T1: State file is updated by a remote execution on node B (new reservation)
T2: Daemon loads state.json and applies reservation overlay to snapshot N
    → snapshot N now shows node B with ReservedMB from T1, but node B's
      RAMFreeMB is from T0. If RAM was consumed between T0 and T1, the
      allocatable math is silently wrong.
T3: CLI `axis status --cached` reads snapshot N + overlay N
    → Operator sees allocatable RAM that may exceed actual free RAM.
```

This is mitigated only by:
- The **5-minute staleness threshold** prompting operators to refresh.
- The **reservation overlay** subtracting reserved RAM from allocatable, which at least accounts for known reservations even if the base snapshot is stale.
- There is **no automatic detection** of the mismatch between snapshot age and overlay age.

### 7.3 Gaps

| Gap | Impact |
|-----|--------|
| No `SnapshotEpoch` | Consumers cannot detect out-of-order or mixed-layer reads. |
| No atomic snapshot+state+ledger read | Each layer is read independently, creating windows of inconsistency. |
| No monotonic clock in `Timestamp` | `ClusterSnapshot.Timestamp` is wall-clock UTC and can go backwards after NTP jumps. |
| `snapshot.json` is not written atomically | Crash between memory update and disk write leaves stale disk state. |

## Summary Table

| Property | Daemon Cache | Runtime Cache | State File | Skills File |
|----------|--------------|---------------|------------|-------------|
| Persistence | `~/.axis/snapshot.json` | None | `~/.axis/state.json` | `~/.axis/skills.json` |
| Writer | Daemon (single goroutine) | `runtimectx.Load()` | Execution + load maintenance | Post-execution recording |
| Reader | HTTP API, CLI, MCP | CLI commands | Daemon, placement, CLI | Daemon, placement, CLI |
| Invalidation | `Invalidate()`, file watchers, execution events | N/A (rebuilt each time) | `context clear`, load maintenance | Manual file delete |
| Mutable after publish | Overwritten on refresh | Fresh build each time | Mutated during load | Append-only |
| Generation ID | **None** | **None** | `Version` field (schema) | **None** |
| Atomic publication | **No** | N/A | Yes (`WriteFileAtomic`) | Yes (`WriteFileAtomic`) |
| Double-buffering | **No** | N/A | No | No |
| Event-driven invalidation | Yes | N/A | No | No |
| Time-driven invalidation | Yes (1m ticker, 5m stale) | N/A | Yes (load-time reclaim) | N/A |
