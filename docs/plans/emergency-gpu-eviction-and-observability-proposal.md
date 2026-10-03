# RFC-2026-005: Supervisor-Aware Emergency GPU Preemption & Zero-Bloat Observability Architecture
**Engineering Proposal and Implementation Specification for AXIS**

- **Proposal ID**: `RFC-2026-005`
- **Target Release**: not a release claim. See `internal/buildinfo/version.go`.
- **Authors**: Antigravity Assistant & Operator Pair
- **Status**: Proposal. Partial behavior is on the eviction branch. Not a runbook.

## Implementation status

This file stays a proposal. The clauses below are what the eviction branch implements. Everything else in this document, including the target-release line above, is not shipped behavior.

Landed on the eviction branch:

- Discovery emits one resident per llama-server PID and `gpu_indices` when nvidia-smi or `--main-gpu` says so.
- `--gpu` filters, including when `--all` is also set.
- `force` is a runtime mask plus SIGKILL. `stop` does not mask. Resume of a forced receipt unmasks before start.
- `resume` awaits the port up to `--timeout`.
- Receipt save failure fails the command.
- Before any `systemctl` or `kill`, stop and force refuse a living PID whose comm is not `llama-server`, or whose process start token does not match. An already-exited PID is not a mismatch.
- The success marker is `__AXIS_EVICT_OK__:measured:<delta>`, `:unmeasured`, or `:freeze`. `<delta>` is the signed change in summed nvidia-smi `memory.used`. A PID that is still present does not get a marker.
- The receipt's `reclaimed_vram_mb` is that delta when `vram_observed` is true. An unmeasured stop or force records 0 and the text says `VRAM unmeasured`. Freeze records 0. A node-wide delta is not copied onto every instance.
- `axis model evict` defaults `--live` to true. `--live=false` reads the daemon cache and fails if that cache is missing.

Still planning-only:

- `--drain-timeout`
- Cortex bus, journald, and blackboard
- zero-bloat log rotation and the 4-tier observability pipeline
- `model.eviction.requested` and `model.resume.requested` (the branch emits local `model.evicted` and `model.resumed` only)
- fleet-wide eviction in one command

---

## 1. Executive Summary & Strategic Motivation

This proposal addresses two critical operational vulnerabilities in the AXIS cluster:

1. **The GPU Preemption & Supervisor Resurrection Trap**:
   When sudden high-priority workloads (rendering, fine-tuning, emergency batch jobs) demand immediate access to GPU Video RAM (VRAM), terminating a resident model with standard signals (`kill -9`) or `axis model stop` triggers an immediate resurrection. On nodes where inference servers are managed by `systemd --user` units with `Restart=always` and `RestartSec=5`, systemd interprets the termination as a crash and restarts the process within 5 seconds, causing bus contention, crash loops, and failed preemption. Furthermore, CUDA `SIGSTOP` halts CPU threads without freeing physical GDDR memory.
2. **The "Stealth Mode" Observability Deficit**:
   AXIS currently operates with virtually zero structured logging across its most failure-sensitive packages: `internal/transport` (SSH), `internal/facts` (hardware probes), `internal/placement` (ranking logic), and `internal/modellife` (server lifecycle). Probes and connection drops fail silently.

This specification proposes the following. The status section is the list of what the eviction branch actually does.

- **`axis model evict` and `axis model resume`**: supervisor-aware preemption and one-step resume. On the branch, evict checks the process owner and start token before it stops or kills, and the receipt uses the observed VRAM marker from the status section. `--drain-timeout` is not a flag.
- **The 4-Tier Observability Pipeline**: local event logs, receipts, a cluster event bus, and host `journald`. Not implemented. The branch writes a local receipt and a local event.
- **The Zero-Bloat Storage Budget**: a rotation model with a hard ceiling under 120 MB. Not implemented.

---

## 2. Command Specifications: `axis model evict` & `axis model resume`

### 2.1 `axis model evict`

Preempts resident AI models from GPU hardware, disarms supervising daemons, terminates processes, and verifies physical VRAM reclamation.

```text
axis model evict [<target-spec>] [flags]
```

#### Target Resolution
`<target-spec>` is optional and accepts:
- An instance ID: `mi-node1-llama.cpp-8082`
- A generation ID: `mg-7f8a91`
- A service or model name: `qwen-27b`, `coder-7b`
- Omitted when `--gpu` or `--all` is supplied.

#### Flag Matrix
| Flag | Type | Default | Description |
| :--- | :--- | :--- | :--- |
| `--node` | `string` | local | Cluster node to target. |
| `--gpu` | `int` | `-1` | Target only models occupying a specific GPU index (e.g. `--gpu 0`). |
| `--all` | `bool` | `false` | Evict all resident models across all GPUs on the target node. |
| `--mode` | `string` | `stop` | `stop` (supervisor stop), `freeze` (cgroup freeze, no kill), or `force` (runtime mask plus SIGKILL). |
| `--live` | `bool` | `true` | Select targets from a fresh snapshot. `--live=false` uses the daemon cache and fails if that cache is missing. |
| `--format` | `string` | `text` | Output format: `text`, `json`, or `yaml`. |

`--drain-timeout` is not implemented.

#### Example Invocation & Output
```bash
$ axis model evict --node node1 --gpu 0
evicted qwen-27b on node1 (reclaimed 800 MiB observed in 84ms) receipt mo-8f92a1
```

`800` is an observed `memory.used` delta, not a model weight. An unmeasured stop prints `VRAM unmeasured` and records 0. Freeze prints `reclaimed 0 MiB`.

---

### 2.2 `axis model resume`

Restores previously evicted models to active serving status using receipt evidence or unit names, monitoring weight transfer and polling health endpoints until ready.

```text
axis model resume [<target-spec>] [flags]
```

#### Flags
| Flag | Type | Default | Description |
| :--- | :--- | :--- | :--- |
| `--receipt` | `string` | `""` | Restore all models recorded in an eviction receipt ID (e.g. `mo-8f92a1`). |
| `--timeout` | `duration` | `60s` | Maximum time to wait for model readiness probe. |
| `--node` | `string` | local | Target cluster node (when targeting by name). |
| `--format` | `string` | `text` | Output format: `text`, `json`, or `yaml`. |

#### Example Invocation & Output
```bash
$ axis model resume --receipt mo-8f92a1
resumed on node1 in 1840ms operation mo-8f92a1
```

That line is the branch's text format. The branch also awaits each receipt port up to `--timeout`.

---

## 3. The 4-Tier Observability & Audit Pipeline

Not implemented on the eviction branch. A successful evict or resume writes a local receipt and emits `model.evicted` or `model.resumed` on the local event log. It does not publish to Cortex, journald, or a blackboard. `model.eviction.requested` and `model.resume.requested` are unused.

The diagram below is the proposal, not current behavior.

Every eviction and resumption action is committed to four coordinated planes:

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│                          axis model evict / resume                          │
└──────────────────────────────────────┬──────────────────────────────────────┘
                                       │
         ┌──────────────────┬──────────┴──────────┬──────────────────┐
         ▼                  ▼                     ▼                  ▼
┌─────────────────┐ ┌───────────────┐   ┌──────────────────┐ ┌───────────────┐
│ 1. Local Events │ │ 2. Receipts   │   │ 3. Cluster Bus   │ │ 4. Blackboard │
│ ~/.axis/        │ │ ~/.axis/      │   │ Coordination Bus │ │  System Logs  │
│   events.jsonl  │ │   receipts/   │   │   (Cortex MCP)   │ │   journald    │
└─────────────────┘ └───────────────┘   └──────────────────┘ └───────────────┘
```

1. **Local Node Events (`~/.axis/events.jsonl`)**:
   Appends structured, sequence-ordered events (`model.evicted`, `model.resumed`) into the existing AXIS event ledger for local auditability.
2. **Immutable Receipts Store (`~/.axis/receipts/evict-<id>.json`)**:
   Saves full `axis.eviction-receipt/v1` documents (permissions `0600`). This file acts as the state token that allows `axis model resume --receipt <id>` to re-arm the exact supervisor units without scanning logs.
3. **Cluster Event Bus (Cortex MCP)**:
   Emits `model.evicted` to the coordination event bus. Other nodes and agents immediately learn that the GPU is preempted, preventing upstream routers and placement engines from routing queries to an offline port.
4. **Human & System Logs (`journald` & Blackboard)**:
   - Issues `logger -t axis-model-evict "..."` for OS-level tracking in `journalctl`.
   - Records structured notes for operator and agent pair-programming visibility.

---

## 4. Zero-Bloat Log Rotation & Storage Budget

Not implemented. This branch does not install a rotation budget.

To accommodate rich structured logging without allowing disk usage to grow over time, AXIS adopts a mathematically bounded storage model:

### 4.1 Storage Budget Allocation

| Log Stream | Mechanism | Retention Policy | Maximum Storage Footprint |
| :--- | :--- | :--- | :--- |
| **Eviction Receipts** | JSON Documents | Retain last 100 receipts | **< 200 KB** |
| **Events Ledger** | Ring Buffer JSONL | 10 files × 10 MB (existing) | **100 MB** |
| **Debug Logs (`axis.log`)** | 3-Stage Gzip Ring | 10 MB active + 3 × 1.1 MB `.gz` | **~13.3 MB** |
| **Task Output Logs** | TTL Pruner | Prune logs older than 7 days | **< 1.0 MB** |
| **STATE & SNAPSHOTS** | Atomic Replace | 1 snapshot + 1 state file | **< 500 KB** |
| **TOTAL DISK CEILING** | **Hard Bounded** | **Deterministic Maximum** | **< 120 MB FOREVER** |

### 4.2 Guardrail Implementations

1. **The 3-Stage Gzip Log Ring**:
   - `~/.axis/logs/axis.log` appends until reaching 10 MB.
   - Upon reaching 10 MB, it compresses to `axis.1.log.gz` (~9:1 compression ratio, reducing 10 MB of text to ~1.1 MB).
   - Keeps at most `axis.1.log.gz`, `axis.2.log.gz`, and `axis.3.log.gz`. Older archives are purged.
2. **In-Memory Ring Buffer for CLI Commands**:
   - For standard runs (`axis status`, `axis task place`), debug statements are kept in a 1,000-line circular memory buffer.
   - If the command exits `0`, the buffer is freed (zero disk I/O).
   - If the command fails, the buffer flushes to `axis.log` to preserve forensic details.
3. **Reaper Engine for Task Logs**:
   - During `axis daemon` startup and daily maintenance sweeps, any `task-*.log` older than 7 days is unlinked.

---

## 5. Architectural Tiering & Package Layout

```text
Layer 5: CLI Surface
  ├── cmd/axis/model.go            (Add 'evict' and 'resume' Cobra subcommands)
  └── cmd/axis/root.go             (Add global '--debug' / '-v' logging flag)

Layer 4: Execution & Lifecycle
  ├── internal/modellife/evict.go  (EvictPlan, EvictTarget, EvictionReceipt)
  ├── internal/modellife/resume.go (ResumePlan, AwaitReadiness)
  └── internal/modellife/stop.go   (Extend StopTarget with supervisor awareness)

Layer 2: Snapshot & State
  ├── internal/models/types.go     (Add SupervisorType, SupervisorUnit, GPUIndices)
  └── internal/models/model_instance.go (Map supervisor metadata to ModelInstance)

Layer 1: Fact Plane & Probing
  ├── internal/facts/tools.go      (Update LlamaServerDiscoveryScript with cgroup probe)
  └── internal/transport/ssh.go    (Add structured slog.Debug instrumentation)
```

---

## 6. Implementation Specifications

### 6.1 Data Types (`internal/models/types.go`)

```go
type ResidentModel struct {
    Name              string    `json:"name" yaml:"name"`
    Runtime           string    `json:"runtime,omitempty" yaml:"runtime,omitempty"`
    Processor         string    `json:"processor,omitempty" yaml:"processor,omitempty"`
    Source            string    `json:"source,omitempty" yaml:"source,omitempty"`
    Port              int       `json:"port,omitempty" yaml:"port,omitempty"`
    WeightSizeMB      int64     `json:"weight_size_mb,omitempty" yaml:"weight_size_mb,omitempty"`
    SizeRAMMB         int64     `json:"size_ram_mb,omitempty" yaml:"size_ram_mb,omitempty"`
    SizeVRAMMB        int64     `json:"size_vram_mb,omitempty" yaml:"size_vram_mb,omitempty"`
    PID               int       `json:"pid,omitempty" yaml:"pid,omitempty"`
    Executable        string    `json:"executable,omitempty" yaml:"executable,omitempty"`
    ProcessOwner      string    `json:"process_owner,omitempty" yaml:"process_owner,omitempty"`
    ProcessStartToken string    `json:"process_start_token,omitempty" yaml:"process_start_token,omitempty"`
    ExpiresAt         time.Time `json:"expires_at,omitempty" yaml:"expires_at,omitempty"`
    WarmthScore       float64   `json:"warmth_score,omitempty" yaml:"warmth_score,omitempty"`

    // Additive Supervisor & Hardware Provenance
    SupervisorType    string    `json:"supervisor_type,omitempty" yaml:"supervisor_type,omitempty"` // "systemd-user", "systemd-system", "none"
    SupervisorUnit    string    `json:"supervisor_unit,omitempty" yaml:"supervisor_unit,omitempty"` // e.g. "bonsai2-27b.service"
    GPUIndices        []int     `json:"gpu_indices,omitempty" yaml:"gpu_indices,omitempty"`         // Physical GPU index allocations
}
```

### 6.2 Eviction Receipt Schema (`axis.eviction-receipt/v1`)

The struct below is the original proposal. It is not the receipt the branch writes. See `EvictionReceipt` in `internal/modellife/evict.go`, which includes `mode` and `vram_observed` and does not include `residual_gpu_vram_mb`.

```go
type EvictionReceipt struct {
    Schema            string                   `json:"schema"` // "axis.eviction-receipt/v1"
    ID                string                   `json:"id"`     // "mo-<uuid>"
    Node              string                   `json:"node"`
    Action            string                   `json:"action"` // "evict" | "resume"
    Status            models.ModelOperationStatus `json:"status"`
    ReclaimedVRAMMB   int64                    `json:"reclaimed_vram_mb"`
    DurationMS        int64                    `json:"duration_ms"`
    EvictedInstances  []EvictedInstanceReceipt `json:"evicted_instances"`
    ResidualGPUVRAMMB []int64                  `json:"residual_gpu_vram_mb"`
    ResumeCommand     string                   `json:"resume_command"`
    SnapshotSource    string                   `json:"snapshot_source"`
    PublicationID     string                   `json:"publication_id"`
    StartedAt         time.Time                `json:"started_at"`
    CompletedAt       time.Time                `json:"completed_at"`
    Error             string                   `json:"error,omitempty"`
}
```

### 6.3 Remote preemption result marker

`BuildEvictShellScript` in `internal/modellife/evict.go` is the script the branch runs. The old template in this section (`set -e`, `sudo systemctl stop`, and `__AXIS_EVICT_OK__:$VRAM_PRE:$VRAM_POST`) is not that script.

The script checks process comm and the process start token before any `systemctl` or `kill`. Stop and force then print one line:

- `__AXIS_EVICT_OK__:measured:<delta>` when nvidia-smi reports `memory.used` before and after, and the targeted PIDs are gone from compute-apps. `<delta>` is the signed sum of that change, in MiB.
- `__AXIS_EVICT_OK__:unmeasured` when nvidia-smi is absent and `kill -0` shows the targeted PIDs are gone.
- `__AXIS_EVICT_OK__:freeze` for freeze. Freeze does not kill and does not sample VRAM.

A targeted PID that is still present does not get a success marker. A bare `__AXIS_EVICT_OK__` is not a successful evict. Resume still prints the bare marker.

---

## 7. Phased Implementation Roadmap & Quality Gates

### Phase 1: Fact Plane & Data Models
- Update `models.ResidentModel` and `models.ModelInstance` with supervisor fields.
- Update `LlamaServerDiscoveryScript` in `internal/facts/tools.go` to extract unit names from `/proc/$PID/cgroup`.
- Expand unit tests in `internal/facts/resident_models_test.go`.

### Phase 2: Execution Engine (`internal/modellife`)
- Implement `internal/modellife/evict.go` and `internal/modellife/resume.go`.
- Implement `PlanEvict()`, shell builder, VRAM polling probe, and receipt serialization.
- Add unit tests with mock remote executors.

### Phase 3: Zero-Bloat Structured Logging Engine
- Add 3-stage gzipped rotating file handler in `internal/events/logger.go`.
- Instrument `internal/transport/ssh.go` and `internal/facts/remote.go` with `slog.Debug`.
- Implement task log TTL pruner in `internal/daemon`.

### Phase 4: CLI Surface & CI Verification
- Wire `axis model evict` and `axis model resume` into `cmd/axis/model.go`.
- Verify coverage passes `hack/coverage-check.sh`.
- Verify doc consistency via `hack/verify-doc-facts.sh` and public boundaries via `hack/verify-public-boundary.sh`.
