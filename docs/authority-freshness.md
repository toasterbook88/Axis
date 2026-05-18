# AUTH-3: Freshness Authority

## 1. Where Snapshot Timestamps Are Generated

### Cluster Snapshot Timestamp

`internal/snapshot/snapshot.go:52` sets `ClusterSnapshot.Timestamp` at **build time**:

```go
snap := &models.ClusterSnapshot{
    Timestamp: time.Now().UTC(),
    // ...
}
```

This is a wall-clock UTC timestamp captured the moment `snapshot.Build(nodes)` is called. It is **not** the oldest node collection time, nor the newest; it is the assembly time.

### Per-Node Collection Timestamp

Each `NodeFacts` carries its own `CollectedAt`:

- **Local:** `internal/facts/local.go:48` — `time.Now().UTC()` at the end of local collection.
- **Remote:** `internal/facts/remote.go:41` — `time.Now().UTC()` at the start of the SSH session.

Because nodes are probed concurrently, `CollectedAt` values across the cluster can differ by the duration of the slowest SSH connection.

## 2. Where Freshness Thresholds Come From

### Daemon Cache Staleness Threshold

Hard-coded in `internal/daemon/daemon.go`:

```go
const defaultStaleThreshold = 5 * time.Minute
```

The daemon’s `SetStaleThreshold` method allows runtime override, but there is **no config file key** for this value. The CLI `axis serve --refresh` flag only controls the background refresh interval (default `1m`), not the staleness threshold.

### Discovery Window Freshness

`internal/discovery/discovery.go` computes `DiscoveryFreshness` for UDP beacon windows:

```go
const defaultBeaconInterval = 3 * time.Second
const beaconWaitSlack       = 250 * time.Millisecond
const minBeaconWait         = 1 * time.Second
const maxBeaconWait         = 8 * time.Second
```

The wait duration is derived from `cfg.Discovery.BeaconInterval` (default 3s) plus a fixed 250ms slack, clamped to `[1s, 8s]`. This is entirely hard-coded; operators cannot tune it beyond changing the beacon interval in `nodes.yaml`.

## 3. How Daemon Cache Age Is Determined

`internal/daemon/daemon.go:609-618`:

```go
func (d *Daemon) Meta() Metadata {
    age := time.Duration(0)
    stale := false
    if !d.collectedAt.IsZero() {
        age = time.Since(d.collectedAt)
        stale = age > d.staleThreshold
    }
    // ...
    CacheAgeSec: int(age.Seconds()),
    Stale:       stale,
}
```

- `collectedAt` is set to `time.Now().UTC()` inside `refreshWithTrigger` immediately after the snapshot is persisted.
- `Meta()` is evaluated at **read time** (`time.Since(d.collectedAt)`), so `CacheAgeSec` is a dynamic wall-clock delta.
- `Stale` is `true` when `age > 5 minutes` (or the overridden threshold).

### Cache Age Propagation

`internal/daemon/client.go:72-73` — when `FetchSnapshot` reads from the daemon, it injects a warning if `meta.Stale` is true, including the exact `CacheAgeSec`.

`cmd/axis/status.go` — prints `snap.Timestamp` (the snapshot build time) in the footer, but does **not** display `CacheAgeSec` directly unless a stale warning is present.

## 4. Clock-Dependent Freshness Checks

### Wall-Clock Dependency

All freshness arithmetic uses `time.Now()` and `time.Since`, which are **wall-clock based** (not monotonic). This creates several misbehavior modes:

1. **NTP Jump Forward:** If the system clock jumps forward by >5 minutes, the next `Meta()` call will declare the cache stale even though it may have been refreshed seconds ago. The daemon will emit a stale warning and `axis status --cached` may fall back to live discovery.

2. **NTP Jump Backward:** If the clock jumps backward, `time.Since(d.collectedAt)` can become negative or zero, causing `CacheAgeSec` to under-report or report `0`. The cache may appear fresh when it is actually old.

3. **Daemon Restart:** On restart, `collectedAt` is zero (`time.Time{}`). `Meta()` reports `CacheAgeSec: 0` and `Stale: false` until the first refresh completes. This means a freshly restarted daemon can serve an empty or uninitialized cache without declaring itself stale.

4. **No Monotonic Fallback:** There is no use of `time.Since` with a monotonic anchor (e.g., `ProcessStartTime` + monotonic offset). The code relies entirely on UTC wall-clock timestamps stored in memory.

### Mitigations Present

- `Invalidate()` zeros `collectedAt` and removes the persisted `snapshot.json`, forcing a fresh collection on next read.
- `WatchConfig` and `WatchState` trigger `RefreshNow` when files change, reducing reliance on clock-based expiry alone.
- The `--cached-only` flag in `axis status` lets operators require daemon cache and fail fast if unavailable, avoiding silent stale reads.

### Gaps

- No `SnapshotEpoch` or generation counter exists (see §5).
- No maximum acceptable `CollectedAt` skew is enforced across nodes. A node with a wildly wrong clock will still be included in the snapshot.

## 5. SnapshotEpoch Semantics (Gap)

**There is no `SnapshotEpoch` field in `ClusterSnapshot` today.**

Searches for `SnapshotEpoch`, `snapshot_epoch`, or `epoch` across the repository return no matches in the snapshot/daemon/runtimectx layers. (The only hit is an unrelated `Timestamp` comment in `internal/repairs/types.go`.)

### What Is Missing

A `SnapshotEpoch` would provide:

- **Generation ordering** — independent of wall-clock time, allowing consumers to detect out-of-order cache responses.
- **Cross-layer coherence** — the daemon, HTTP API, MCP server, and CLI could all agree on "which version" of the snapshot they are looking at.
- **Graceful NTP handling** — monotonic epoch increments would survive clock jumps.

### Current Work-Arounds

- The daemon uses `collectedAt` as an implicit epoch boundary: a newer `collectedAt` means a newer snapshot.
- `Timestamp` in `ClusterSnapshot` serves as a coarse version tag, but it is wall-clock derived and can go backwards.
- `Meta().RefreshCount` is a monotonic integer tracking how many refreshes the daemon has performed, but it is **not** embedded into the `ClusterSnapshot` payload seen by consumers.

## Summary Table

| Concept | Source | Mutable by Config? | Clock Dependency |
|---------|--------|--------------------|------------------|
| `ClusterSnapshot.Timestamp` | `snapshot.Build` | No | Wall-clock UTC |
| `NodeFacts.CollectedAt` | Facts collector | No | Wall-clock UTC |
| `Daemon.staleThreshold` | Hard-coded `5m` | No (runtime only) | N/A |
| `Daemon.CacheAgeSec` | `time.Since(collectedAt)` | No | Wall-clock |
| `DiscoveryFreshness` | Hard-coded window math | Partial (interval only) | Wall-clock |
| `SnapshotEpoch` | **Does not exist** | — | — |

**Recommendation:** Introduce a monotonic `SnapshotEpoch` (uint64) in `ClusterSnapshot`, incremented on every `snapshot.Build` call and mirrored in daemon metadata, to give consumers a clock-safe generation identifier.
