# AUTH-9: Observability Authority

## 1. What Observability Outputs Exist

| Output | Layer | Consumers | Code Path |
|--------|-------|-----------|-----------|
| **Structured slog logs** | Daemon, API server, execution, reservation, state maintenance | Operator stdout/stderr, systemd/journald, container runtimes | `log/slog` across `internal/daemon`, `internal/api`, `internal/execution`, `internal/reservation` |
| **Prometheus-style metrics** | HTTP API (`/v2/metrics`) | Monitoring scrapers, `axis doctor` | `internal/api/v2.go:handleMetrics` |
| **Doctor warnings** | CLI `axis doctor` | Operators | `cmd/axis/doctor.go` |
| **CLI status output** | CLI `axis status` | Operators | `cmd/axis/status.go` |
| **Daemon metadata** | HTTP API (`/snapshot/meta`, `/health`) | CLI, MCP, operators | `internal/daemon/daemon.go:Meta()` |
| **Snapshot warnings** | Snapshot assembly | All snapshot consumers | `models.Warning` attached to `ClusterSnapshot` |
| **Repair events** | Subsystems that auto-repair state | Structured slog records, future surfaces | `internal/repairs/types.go` |

### 1.1 Structured Logs (slog)

AXIS uses Go's `log/slog` with structured key-value pairs. Log sites include:

- **Daemon:** refresh errors, mesh peer events, config watch failures (`internal/daemon/daemon.go`)
- **API server:** async refresh failures (`internal/api/server.go`, `internal/daemon/handlers.go`)
- **Reservation ledger:** reserve/release/reclaim events with `id`, `node`, `ram_mb`, `owner` (`internal/reservation/ledger.go`)
- **State maintenance:** no direct logs; failures surface as returned errors or snapshot warnings

There is **no log level configuration** exposed to operators; the default slog level is used.

### 1.2 Prometheus Metrics

The `/v2/metrics` endpoint (`internal/api/v2.go:266-320`) emits:

| Metric | Type | Source |
|--------|------|--------|
| `axis_cache_age_seconds` | gauge | `meta.CacheAgeSec` |
| `axis_cache_refresh_total` | counter | `meta.RefreshCount` |
| `axis_cache_refresh_duration_ms` | gauge | `meta.LastRefreshMs` |
| `axis_cache_max_refresh_latency_ms` | gauge | `meta.MaxRefreshLatencyMs` |
| `axis_cache_stale` | gauge | `meta.Stale` (0/1) |
| `axis_cache_ready` | gauge | `meta.Ready` (0/1) |
| `axis_nodes_total` | gauge | `len(snap.Nodes)` |
| `axis_nodes_healthy` | gauge | Count of `status == "complete"` |

These are **derived from daemon metadata**, not independently collected.

### 1.3 Doctor Warnings

`axis doctor` (`cmd/axis/doctor.go`) performs five checks:

1. **Config load** — core failure if `nodes.yaml` is missing or invalid.
2. **SSH connectivity** — core failure per node if SSH fails.
3. **Daemon cache reachability** — core failure in `--strict` mode, advisory warning otherwise.
4. **Local AI backends** — advisory only (llama-server, mlx); never counts as a core failure.
5. **Binary info** — diagnostic only.

Doctor output uses `internal/ui` colored formatting (`ui.FprintError`, `ui.FprintWarning`, `ui.FprintSuccess`).

### 1.4 CLI Status Output

`axis status` prints:
- Node status table (name, status, RAM free, pressure, tools)
- Resident models table (node, runtime, models, VRAM)
- Snapshot warnings
- Source label (`live`, `daemon-cache`, `live-fallback`) and timestamp

JSON/YAML output includes the full `ClusterSnapshot` plus a `source` field when `--cached` is used.

### 1.5 Snapshot Warnings

`models.Warning` entries are attached to `ClusterSnapshot` during assembly:

| Kind | Source | Example |
|------|--------|---------|
| `state` | `state.Load()` corruption/recovery | `"recovered local AXIS state: ..."` |
| `skills` | `skills.Load()` corruption/recovery | `"recovered learned skills store: ..."` |
| `cache` | `FetchSnapshot()` staleness | `"daemon cache is stale (300s old); ..."` |
| `discovery` | `DiscoveryFreshness` incomplete window | `"results may miss peer nodes"` |

## 2. Operational Telemetry Is Non-Authoritative and Non-Replayable

**Operational telemetry (logs, metrics, doctor output, CLI status text) is non-authoritative and non-replayable.**

- **Logs** are advisory records of subsystem behavior. They are not used for state reconciliation, placement decisions, or execution guardrails.
- **Metrics** are derived from ephemeral daemon metadata and reflect point-in-time cache state. They are not persisted, versioned, or auditable.
- **Doctor warnings** are diagnostic hints for operators. A passing doctor check does not guarantee cluster health; a failing check does not automatically block execution.
- **CLI output** is formatted for human readability and may truncate, colorize, or reorder information for display purposes.

No telemetry output may be presented as cluster truth unless it is backed by a real snapshot or live probe (see AGENTS.md Truth Rule).

## 3. No Subsystem Uses Logs as Source of Truth for State Reconciliation

**There is zero log-to-state reconciliation in AXIS.**

Searches for `slog` consumption in `internal/state`, `internal/reservation`, `internal/daemon`, and `internal/execution` show only **write-side** logging (emitting events). No package reads its own logs—or any other package's logs—to rebuild state, detect failures, or trigger repairs.

State reconciliation happens through:
- **Explicit file I/O:** `state.Load()`, `ledger.Load()`, `skills.Load()` read their respective JSON files.
- **Daemon refresh:** `doRefresh()` re-probes nodes via SSH and rebuilds the snapshot.
- **Heartbeat checks:** `ledger.Reclaim()` and `state.runMaintenance()` evaluate timestamps and PID liveness, not log entries.

The `internal/repairs/types.go` package defines `RepairEvent` structures for future structured repair emission, but today no repair event is read back for reconciliation.

## 4. Metrics Do Not Influence Placement or Reservation Decisions

**Metrics are purely observational; they never feed into placement or reservation logic.**

- `internal/placement/ranker.go` does not import `internal/api` or reference any metric.
- `internal/execution/guarded.go` does not reference `axis_cache_*` metrics.
- `internal/reservation/ledger.go` tracks internal counters (`totalReserved`, `totalReleased`, `totalReclaimed`, `reserveFailures`), but these are exposed only via `Ledger.Metrics()` and are **not used** by `Reserve()`, `Release()`, or `Reclaim()`.

The only numeric values that influence placement are:
- Snapshot facts (`RAMFreeMB`, `Pressure`, `GPUs`, etc.)
- Reservation state (`ledger.AllocatableRAM()`, `state.NodeState.ReservedMB`)
- Observations (`ExecutionObservation`, advisory only; see AUTH-4)
- Skills (`LearnedSkill`, advisory only)

## 5. Doctor Warnings Are Diagnostic, Not Actionable Guards

**Doctor warnings are diagnostic, not actionable guards.**

- `axis doctor` is a **read-only diagnostic tool**. It never modifies state, triggers repairs, or blocks execution.
- Even in `--strict` mode, doctor only changes the **exit-code classification** of daemon unavailability; it does not prevent `axis task run` or `axis serve` from proceeding.
- The AI backend checks (llama-server, mlx) are explicitly advisory: "not installed" is not a failure, and "installed, not running" is a success check.
- Doctor output includes **hints** (e.g., `cp nodes.example.yaml ~/.axis/nodes.yaml`, `start with: axis serve`), but these are suggestions, not automated actions.

The `/v2/doctor` endpoint (`internal/api/v2.go:336-402`) returns a JSON diagnostic payload with `overall: healthy|degraded|unhealthy`, but this is also advisory and does not gate API behavior.

## 6. Repair Events (`internal/repairs/types.go`)

AXIS v0.11 introduced typed repair-event structures in `internal/repairs/types.go`:

```go
type RepairEvent struct {
    Timestamp       time.Time `json:"timestamp"`
    Severity        Severity  `json:"severity"`          // info | warning | critical
    SourceAuthority string    `json:"source_authority"`  // e.g. "ledger", "snapshot"
    ObjectType      string    `json:"object_type"`       // e.g. "reservation", "node_state"
    ObjectID        string    `json:"object_id,omitempty"`
    OldValue        string    `json:"old_value,omitempty"`
    NewValue        string    `json:"new_value,omitempty"`
    Description     string    `json:"description"`
}
```

### 6.1 Design Intent

Repair events are intended to be emitted as **structured slog records** at the point of automatic repair. The design explicitly avoids:
- Event buses
- Async routing
- Subscriber models
- Persistent event logs

### 6.2 Current State

As of the current codebase, `internal/repairs/types.go` defines the types but **no code emits `RepairEvent` instances yet**. The package comment states:

> "Scope discipline: v0.11 intentionally avoids event buses, async routing, or subscriber models. Events are emitted synchronously at the point of repair."

The repair types exist as a foundation for future authority-aware diagnostics but are not yet wired into state maintenance, ledger reclaim, or snapshot repair paths.

### 6.3 Where Repairs Today Are Silent

| Repair | Location | Current Behavior |
|--------|----------|----------------|
| Corrupt state file recovery | `internal/persist/persist.go` | Quarantines to `.corrupt-*`, logs warning, returns `RecoveryWarning` |
| Corrupt skills file recovery | `internal/skills/skills.go` | Same pattern via `persist.QuarantineCorruptFile` |
| Ledger stale entry reclaim | `internal/reservation/ledger.go` | Logs via `slog.Info`, no structured `RepairEvent` |
| State heartbeat/PID reclaim | `internal/state/state.go` | Silent normalization, no logs or events |
| Snapshot freshness backfill | `internal/daemon/client.go` | Warns via `models.Warning`, no repair event |

When repair events are wired, they will be emitted synchronously at the repair site and surfaced to operators via doctor, metrics, and `--json`/`--ndjson` output.

## Summary Table

| Output | Authoritative? | Drives Placement? | Drives Execution? | Persistent? |
|--------|---------------|-------------------|-------------------|-------------|
| slog logs | No | No | No | No (process-local) |
| Prometheus metrics | No | No | No | No (ephemeral endpoint) |
| Doctor warnings | No | No | No | No (CLI-only) |
| CLI status text | No | No | No | No (human-formatted) |
| Snapshot warnings | No (advisory) | No | No | Yes (in `ClusterSnapshot`) |
| Repair events (future) | No | No | No | No (structured slog only) |
| State file (`state.json`) | **Yes** | Yes | Yes | Yes |
| Ledger (`ledger.json`) | **Yes** | Yes | Yes | Yes |
| Snapshot facts | **Yes** | Yes | Yes | Yes (when cached) |

**Operational telemetry is for operator awareness only. All control decisions are grounded in `state.json`, `ledger.json`, or live snapshot facts.**
