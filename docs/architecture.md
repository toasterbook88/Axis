# AXIS Architecture Reference

> This document mixes shipped architecture with proposed v0.10.0 extensions.
> When it disagrees with the live code, trust the code and
> [docs/current-state.md](current-state.md) first.

## Design Principles

1. **Snapshot-first.** All cluster intelligence derives from observed hardware
   facts collected via SSH. No generated, cached, or gossipped data may
   present itself as cluster truth.

2. **Deterministic placement.** Given the same ClusterSnapshot and
   TaskRequirements, placement always produces the same result. No
   randomness, no LLM influence on the placement decision.

3. **Single binary.** No required daemon, database, or background process.
   The daemon is optional and provides caching, not truth.

4. **Layers are subordinate.** Each layer in the stack depends only on
   layers below it. Advisory surfaces (chat, agent, MCP) never override
   the fact plane.

## 5-Layer Stack

### Layer 1: Fact Plane

**Packages:** `internal/facts`, `internal/discovery`, `internal/mesh`, `internal/transport`

The fact plane collects hardware and software facts from cluster nodes.

| Component | Role |
|-----------|------|
| `LocalCollector` | Probes the local machine (no SSH) |
| `RemoteCollector` | SSH into remote nodes via `transport.SSHExecutor` |
| `Discovery` | Fan-out probes (maxParallel=10) with semaphore |
| `UDP Beacons` | HMAC-SHA256 authenticated node announcements |
| `Mesh Gossip` | Proposed peer discovery with 5-state lifecycle (v0.10.0) |

**Facts collected per node:**
- OS, architecture, hostname, kernel version
- RAM total/free, memory pressure, memory topology (unified vs standard)
- GPU vendor, model, VRAM, capabilities (metal, cuda, rocm, vulkan)
- Installed tools with versions (ollama, docker, git, go, etc.)
- Battery level, thermal state, storage class
- Resident AI models (ollama, llama.cpp, MLX)

**Output:** `[]models.NodeFacts` with typed `NodeStatus` (complete/partial/unreachable/error)

### Layer 2: Snapshot Plane

**Packages:** `internal/snapshot`, `internal/daemon`, `internal/snapshotview`

Assembles individual NodeFacts into a `ClusterSnapshot` — the canonical
representation of cluster state at a point in time.

| Component | Role |
|-----------|------|
| `snapshot.Build()` | Assembles ClusterSnapshot from []NodeFacts |
| `daemon.Cache` | In-memory snapshot cache with staleness detection |
| `snapshotview` | Rendering for CLI and API consumers |

**Daemon cache triggers (7):**
1. Timer-based refresh (default: 60s interval)
2. `nodes.yaml` content-aware file watch
3. `state.json` semantic file watch (filters heartbeat churn)
4. `skills.json` file watch
5. UDP beacon arrival
6. Execution state change events
7. Manual `axis daemon refresh` / `POST /refresh`

**Staleness:** Cache older than threshold (default: 5 minutes) is flagged stale.
Consumers see `Stale: true` in metadata and can request fresh data.

### Layer 3: Placement Plane

**Packages:** `internal/placement`, `internal/workload`

Deterministic **Filter → Rank → Select** pipeline.

```
TaskRequirements
      │
      ▼
┌─────────────┐    Nodes that fail any hard
│   FILTER    │──▶ requirement are excluded
└─────┬───────┘    with per-node reasoning
      │
      ▼
┌─────────────┐    10-level deterministic sort
│    RANK     │──▶ with stable name tiebreak
└─────┬───────┘
      │
      ▼
┌─────────────┐    Best node + FitScore 0-100
│   SELECT    │──▶ with full diagnostic reasoning
└─────────────┘
```

**FitScore factors:**
- GPU match: +25 pts
- Local node: +10 pts
- Unified memory bonus for matching workloads
- Reservation ratio factor (less loaded → higher score)

**Key innovations:**
- `empirical.go` — Per-model observation scopes with wall time + RAM/VRAM peaks
- `modelname.go` — Canonical model name extraction from any format
- Resident model locality — prefers nodes already running the target model
- Reservation-aware headroom — factors active reservations into allocatable RAM

### Layer 4: Execution Plane

**Packages:** `internal/execution`, `internal/safety`, `internal/reservation`, `internal/scripts`, `internal/skills`

Guarded task execution with pre-flight safety checks and resource reservation.

```
Description → Intent Parse → Safety Gate → Placement → Reserve → Execute → Release
```

**Proposed safety evaluation (v0.10.0):**
- Parsed command analysis (program + args, not substring matching)
- 7 risk categories: safe, read-only, modify, destructive, network-mutating,
  privilege-escalate, system-critical
- 3 verdicts: allow, deny, prompt (ask operator)
- Learned overrides from operator approvals

**Reservation lifecycle:**
1. `Reserve()` — claim RAM on target node
2. `Heartbeat()` — 15s interval liveness signal
3. `Release()` — free resources on completion
4. `Reclaim()` — automatic cleanup of stale entries (>2min without heartbeat)

**Execution modes:**
- `script` — matched against built-in scripts/skills catalog
- `exec` — explicit raw command (requires `confirm=YES`)

**Streaming:** NDJSON wire format with state-change events:
`reserved → ready → output → final-result → finished`

### Layer 5: Advisory Plane

**Packages:** `internal/chat`, `internal/agent`, `internal/mcp`, `internal/api`, `internal/llmrouter`, `internal/cortex`

Advisory surfaces subordinate to observed state. These surfaces may suggest
actions but never present generated output as cluster truth.

| Surface | Protocol | Role |
|---------|----------|------|
| `axis chat` | Ollama /api/chat | Interactive advisory with rolling context |
| `axis agent` | Tool-calling loop | Read-only tools + safety-gated shell |
| `axis mcp serve` | MCP over stdio | Read-only cluster diagnostics for LLM clients |
| `axis serve` | HTTP (Unix socket) | Programmatic API for integrations |
| `cortex` | Internal | Distributed vector memory + event bus |
| `llmrouter` | Internal | Model routing and selection |

## State Persistence

All state lives under `~/.axis/`:

| File | Purpose | Watches |
|------|---------|---------|
| `nodes.yaml` | Cluster node configuration | Content-hash watch |
| `state.json` | Execution history, reservations | Semantic watch |
| `skills.json` | Learned execution skills | File watch |
| `axis.sock` | Unix domain socket (API) | — |

**Corruption recovery:** Both `state.json` and `skills.json` use atomic
write with rename and automatic recovery from corrupt files.

## Network Ports

| Port | Protocol | Purpose |
|------|----------|---------|
| 42425 | TCP | Daemon HTTP API (localhost only) |
| 42426 | UDP | Mesh gossip + beacon discovery |
| `~/.axis/axis.sock` | Unix | Primary API socket |
