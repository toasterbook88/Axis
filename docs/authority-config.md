# AUTH-6: Configuration Authority

## 1. Where Config Is Loaded From

AXIS configuration lives in a single file:

```
~/.axis/nodes.yaml
```

Loaded by `internal/config/config.go` via `config.Load(path)`. The default path is returned by `config.DefaultConfigPath()`.

## 2. Is Config Mutable at Runtime?

**Yes, via file-watch-driven reload.**

The daemon (`internal/daemon/daemon.go`) polls the config file every **500 ms** (`watchConfigPollInterval`) and compares a SHA-256 fingerprint. When the file changes, disappears, or reappears:

1. `Daemon.Invalidate()` clears the in-memory snapshot.
2. `Daemon.refreshWithTrigger(ctx, RefreshTriggerConfigChange)` re-collects the cluster state from the new config.
3. `WatchDiscovery` tears down and restarts the UDP beacon listener if `discovery.enabled` changed.

There is **no SIGHUP or API endpoint** for explicit reload; mutation is purely filesystem-driven.

## 3. Field Mutability Classification

### Startup-Only (reloadable but identity-sensitive)

Changing these fields alters cluster identity and can orphan reservations or decisions:

| Field | Impact if changed |
|-------|-------------------|
| `nodes[].name` | Orphans reservation ledger entries keyed by the old name. Daemon refresh picks up the new name, but existing execs still reference the old name in `state.json`. |
| `nodes[].hostname` | Next SSH probe uses the new address; may cause temporary `unreachable` status if DNS has not propagated. |
| `nodes[].stable_id` | Locality matching changes; deduplication key changes. |
| `nodes[].ssh_user` | Used for the next SSH connection only. No retroactive re-auth. |
| `nodes[].ssh_port` | Used for the next SSH connection only. |
| `nodes[].role` | Placement scoring changes on next snapshot build. |

### Hot-Reloadable

| Field | Effect on Reload |
|-------|------------------|
| `discovery.enabled` | Starts or stops the UDP beacon listener and broadcaster. The mesh gossip layer (`WatchMesh`) is **not** restarted on config change; it is created once at daemon startup. |
| `discovery.udp_port` | Requires daemon restart because the UDP socket is bound at startup. Config change alone does not rebind. |
| `discovery.beacon_interval_sec` | Picked up when the beacon broadcaster goroutine restarts (next config-driven `applyWatcher` cycle). |
| `discovery.secret` | Picked up when the beacon listener restarts. Mesh gossip does **not** consume this secret; mesh uses its own empty default. |
| `chat.default_model` | Used on the next `axis chat` invocation. |
| `ai_providers[].*` | Used on the next inference routing decision. |
| `inference.*` | Used on the next inference routing decision. |
| `nodes[].timeout_sec` | Used for the next SSH probe only. |

### Runtime-Derived (not in config)

| Property | Source |
|----------|--------|
| `stale_threshold` | Daemon constant `defaultStaleThreshold` (5 min), overridable via `Daemon.SetStaleThreshold()`. Not in `nodes.yaml`. |
| `refresh_interval` | `axis serve --refresh` flag or `daemon.NewDefault(interval)`. Not in `nodes.yaml`. |
| `snapshot_path` | `~/.axis/snapshot.json`, derived in `daemon.DefaultSnapshotPath()`. |
| `state_path` | `~/.axis/state.json`, derived in `state.Path()`. |
| `ledger_path` | `~/.axis/ledger.json`, derived in `reservation`. |
| `api_token` | `~/.axis/token` or `AXIS_API_TOKEN` env var. |

### Immutable

There are **no truly immutable** fields in `nodes.yaml`. Even `name` can be edited, but the operator must understand that historical ledger entries and `state.json` decisions remain keyed by the old name.

## 4. Who Validates Config

Validation is **three-layer**:

1. **Strict YAML parsing** (`internal/config/config.go:169-184`)
   - `yaml.NewDecoder(bytes.NewReader(data)).KnownFields(true)`
   - Rejects unknown keys at **any nesting level** (top-level, node, discovery, chat, ai_providers, models, inference).
   - Rejects multi-document YAML.

2. **Programmatic validation** (`Config.Validate()`)
   - At least one node must be defined.
   - Each node must have `name`, `hostname`, and `ssh_user`.
   - `stable_id` is normalized (lowercase, trimmed) but uniqueness is **not enforced**.

3. **Daemon defensive reload**
   - If `config.Load()` fails during a refresh, the daemon keeps the previous snapshot and records the error in `Metadata.LastError`.
   - `WatchDiscovery` skips starting the beacon listener if the reloaded config is invalid or `discovery.enabled` is false.

## 5. Mesh Enable/Disable Runtime Configurability

Mesh gossip and UDP discovery are **separately controlled**:

| Layer | Config Key | Runtime Mutable? | Notes |
|-------|------------|------------------|-------|
| UDP discovery | `discovery.enabled` | **Yes** | `WatchDiscovery` polls config and starts/stops the beacon listener/broadcaster. |
| Mesh gossip | `discovery.enabled` (via `IsMeshEnabled()`) | **Partially** | The mesh is created once in `daemon.NewDefault()` and started in `WatchMesh()`. Changing `discovery.enabled` after startup does **not** start or stop the mesh; a daemon restart is required. |

Backward compatibility: when `discovery` is **absent** from `nodes.yaml`, `IsMeshEnabled()` returns `true`.

## 6. Config Defaults That Affect Authority

| Default | Value | Authority Impact |
|---------|-------|------------------|
| `ssh_port` | 22 | All SSH probes use port 22 unless overridden. |
| `timeout_sec` | 10 | SSH probe and UDP beacon wait window default. |
| `discovery.udp_port` | 42424 | Beacon broadcast/listen port. |
| `discovery.beacon_interval_sec` | 3 | How often beacons are emitted. |
| `daemon.defaultStaleThreshold` | 5 min | Cached snapshot older than this is flagged `stale` in metadata. |
| `daemon.defaultRefreshInterval` | 1 min | How often the daemon re-probes the cluster. |
| `daemon.ShutdownDrainTimeout` | 10 sec | Max wait for in-flight refresh on daemon stop. |
| `watchConfigPollInterval` | 500 ms | How quickly the daemon notices a config edit. |

## Summary Table

| Concern | Source of Truth | Mutable at Runtime? |
|---------|-----------------|---------------------|
| Cluster seed (nodes) | `~/.axis/nodes.yaml` | Yes (file edit) |
| Strict schema validation | `internal/config/config.go` | N/A (load-time only) |
| Daemon refresh interval | `--refresh` flag / `NewDefault(interval)` | No (restart required) |
| Stale threshold | `Daemon.SetStaleThreshold()` / default 5 min | Yes (API / internal) |
| Mesh gossip on/off | `discovery.enabled` at daemon startup | Partially (restart required for mesh) |
| UDP discovery on/off | `discovery.enabled` polled live | Yes (file edit) |
| API token | `~/.axis/token` or `AXIS_API_TOKEN` | Yes (manual rotation) |
