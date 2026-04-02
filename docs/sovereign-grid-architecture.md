# AXIS: The Intelligence Layer of the Sovereign Grid
**Architectural Evolution Proposal**

## Vision
Axis currently excels at answering: *"What compute do I have right now?"* To orchestrate complex, multi-agent AI workflows across a heterogeneous cluster (the "Sovereign Grid"), Axis must evolve to answer: *"Given the physical realities, active state, and historical performance of this grid, what is the safest and fastest way to execute this specific constellation of tasks?"*

Crucially, this evolution must strictly adhere to the Axis doctrines: **Live Reality Beats Narrative**, **Advisory Before Automatic**, and **Minimalism**. Axis must remain a stateless, fast-executing Go binary that provides flawless empirical context, avoiding the trap of becoming a heavyweight, centralized orchestrator.

The following ten improvements are categorized into three evolutionary phases to deepen the Fact Plane, enhance State Awareness, and enable Advanced Orchestration.

---

## Phase 1: Deepening the Fact Plane (Physics & Reality Checks)
*Moving beyond static specifications to understand the actual physical capacity of the cluster at this exact millisecond.*

### 1. Thermal & Power Reality Probing (The "Physics" Check)
Compute is constrained by thermodynamics. A node with excellent specs is a poor placement target if it is thermal throttling or running on battery power.
*   **Implementation:** Extend the lightweight SSH probe to capture thermal states (e.g., `powermetrics` on macOS, `sensors` on Linux) and power source (AC vs. Battery).
*   **Impact:** Axis actively penalizes hot or off-grid nodes for heavy tasks (like long-context LLM inference), preventing unexpected performance degradation and preserving battery life.

### 2. I/O Contention & Storage Saturation Awareness
Loading large ML models (GGUF, safetensors) is fundamentally an I/O-bound operation. High RAM availability is irrelevant if the disk queue is saturated.
*   **Implementation:** Include a fast check of disk I/O wait states (e.g., `/proc/diskstats`, `iostat`) during `ClusterSnapshot` assembly.
*   **Impact:** Axis routes I/O-heavy initialization tasks away from nodes currently performing heavy compilations or database queries, ensuring predictable startup times for autonomous agents.

### 3. Granular Capability Hashing (Micro-Probes)
In a heterogeneous cluster, knowing `python3` is installed is insufficient. We need to know if it can actually execute the required workload.
*   **Implementation:** Replace binary existence checks with "Micro-Probes." For example, executing a fast script (`python3 -c 'import torch; print(torch.cuda.is_available())'`) or verifying if `ollama` is compiled with specific Metal/ROCm support.
*   **Impact:** Prevents the silent failure of an agent placing a CUDA-dependent task on a node that has Python but lacks hardware-accelerated libraries.

---

## Phase 2: State & Locality Awareness (Data & Cache)
*Understanding what is currently loaded in memory and where the necessary data resides to eliminate redundant transfers.*

### 4. Ephemeral State & "Warm" Cache Awareness
VRAM loading is the highest latency penalty in local AI workflows. Constantly unloading and reloading models across the cluster causes fragmentation and wastes time.
*   **Baseline status:** AXIS already carries basic Ollama warm-state signals (`Listening`, `Running`, loaded `Models`). Phase 2 is about deepening that into stronger cross-backend resident-model locality, not starting from zero.
*   **Implementation:** Extend probes to detect resident models (e.g., querying `ollama ps` or parsing active `mlx` processes) to see exactly what is currently loaded in VRAM.
*   **Impact:** If a task requires `llama3`, Axis drastically boosts the score of a node that already has it loaded. This eliminates load times and makes iterative testing (e.g., of custom models) vastly more efficient.

### 5. Git-Parity and Data Locality Probes
Compute is useless if the node doesn't have the correct, up-to-date context. "Works on my machine" failures often happen because a remote node's repository is stale.
*   **Implementation:** For repository-aware tasks, Axis performs a lightweight SSH check to verify the target node has the required Git repository and that its `HEAD` commit matches the initiator's local state.
*   **Impact:** Acts as a massive quality-of-life safeguard, refusing to place a build or analysis task on a node with stale data.

### 6. Empirical Feedback Loops (The Self-Correcting Grid)
Static heuristics (e.g., guessing a 7B model needs X amount of RAM) will always fail across mixed architectures (Apple Silicon vs. discrete GPUs).
*   **Implementation:** When `axis task run` executes, a lightweight monitor measures *actual* peak RAM/VRAM usage and execution time, saving this footprint to the local `state.json`.
*   **Impact:** The next time a similar task is requested, Axis bases placement on empirical history rather than guessing. The grid becomes self-correcting and highly accurate over time.

---

## Phase 3: Advanced Placement & Orchestration (Constellations & Immune System)
*Providing the connective tissue for autonomous agents to safely leverage the entire cluster without causing gridlock.*

### 7. Network Topology & "Compute Pairs"
A cluster is limited by its interconnects. A Thunderbolt bridge (M1↔M3) offers fundamentally different compute possibilities than a standard Wi-Fi link.
*   **Baseline status:** AXIS already records interface name, subnet, and a heuristic `speed_class`. Phase 3 should treat those as hints, not measured throughput or latency, until stronger link evidence is available.
*   **Implementation:** Introduce link-speed and latency awareness into the `ClusterSnapshot`. Map the network to identify high-speed "Compute Pairs."
*   **Impact:** Axis can intelligently chunk tasks, keeping heavy, data-intensive pipelines on Thunderbolt links while pushing isolated, asynchronous tasks out to the wider mesh.

### 8. Multi-Node Workload Mapping ("Constellations")
Future workflows will involve distributed pipelines (e.g., Node A runs Qdrant, Node B runs the embedding model, Node C runs the LLM).
*   **Implementation:** Allow `axis task place` to accept a structured array or DAG of interconnected tasks, evaluating the cluster to place the *whole constellation* optimally at once.
*   **Impact:** Axis remains advisory, but its advice scales to full system architecture, ensuring optimal use of Compute Pairs while preventing overallocation of any single node.

### 9. "Tombstones" and "Blackouts" (The Grid Immune System)
Pushing the limits of local AI will occasionally cause catastrophic failures (Linux OOM killer, macOS kernel panics).
*   **Implementation:** Implement a "Tombstone" pattern. If a task crashes a node, a local tombstone file is written. Axis reads this during the next sweep. Repeated failures for a specific task type place that node in a temporary "Blackout" for that capability.
*   **Impact:** If an experimental model crashes the M1, Axis ensures no other agent attempts to load it there until operator intervention. This prevents cascading failures across the grid without a centralized database.

### 10. The Universal MCP Context Provider (Execution Leases)
To prevent multiple autonomous agents from blindly colliding and deadlocking the cluster, Axis must become the foundational "traffic cop."
*   **Implementation:** Expand `axis mcp serve` to expose a `request_execution_lease(task_requirements)` tool. Agents *must* ask Axis for a lease before running heavy scripts or compilations.
*   **Impact:** Maintains the local-first architecture while providing a critical safety valve. Axis orchestrates the chaos, ensuring the NixOS GPU or M3 RAM isn't simultaneously overwhelmed by competing agent requests.

---

## Architectural Synergy
By implementing Physics Checks, Warm Caches, Constellation Mapping, and an Immune System, Axis transcends basic hardware discovery. It evolves into a hyper-aware, physics-bound intelligence layer. It provides human operators and autonomous agents with the flawless, empirical context required to operate safely across a complex Sovereign Grid—all while rigorously defending its identity as a fast, simple, and stateless Go binary.
