# AXIS Hybrid Multi-Agent Orchestration & Inference Specification — Annotated & Compared

**Status**: Annotated evaluation version (2026-05-28)  
**Base Document**: `docs/future/hybrid-orchestration-spec.md` (original proposal)  
**Annotation Methodology**: Rigorous double-check against live codebase (v0.10.7), source files, `git` history, GitHub PRs, and canonical docs (doctrine.md, AGENTS.md, future-roadmap.md, sovereign-grid-architecture.md, lifecycle.md, current-state.md, docs-evaluation.md, triangle-orchestration.md).  

**Legend for Annotations**:
- **[VERIFIED]** — Claim matches current code, live behavior, or documented reality.
- **[PARTIAL]** — Directionally aligned but overstated or incomplete in current implementation.
- **[DISCREPANCY]** — Proposed code/symbol/architecture does not exist as described.
- **[DOCTRINE RISK]** — Conflicts with or strains core Axis principles (Truth Rule, Advisory subordination, "no heavyweight orchestrator", single-binary minimalism).
- **[SUGGESTED]** — Recommendation for revision based on evidence.
- **[CONTEXT FROM TRIANGLE]** — Cross-reference to the more narrowly scoped `triangle-orchestration.md`.

**Important Reading Note**: Per `AGENTS.md` and `docs/README.md`, this (and the original spec) are design material. Trust live code + `current-state.md` + `doctrine.md` first.

---

## Executive Summary of This Annotated Evaluation

The original Hybrid spec proposes an ambitious integration of **Triangle** concepts with a full event-driven task/execution system built around Cortex.

**Key Findings from Double-Check**:
- Many specific proposed interfaces (`DynamicEngine`, `SpeculativeAcquire`, `ApplyObservationModifiers`, `WorkerDaemon`) **do not exist** in the referenced packages.
- Cortex is currently a thin client for memory + limited CI/CD events + locking — not a general execution queue.
- The proposal sits closer to the rejected "Path 8: Full Orchestrator" than the carefully scoped Triangle proposal.
- Recent development (observations #139, MCP unification #140, Cortex client work) strengthens empirical and MCP foundations but does not implement the orchestration engine described here.
- Stronger alignment is possible if the spec is narrowed significantly to match the "MCP-native context substrate" direction recommended in `future-roadmap.md`.

This annotated version interleaves the original text with evidence-based commentary.

---

## 1. Executive Summary (Original)

This document specifies the integrated architecture for **Triangle Multi-Agent Orchestration** combined with an **Event-Driven LLM & Task Inference Queue** (referred to as the *Hybrid Architecture*). 

The goal of this design is to allow independent, resource-aware agents (e.g., Grok, Hermes) to safely cooperate in "crews" on the **AXIS Sovereign Grid** without overloading thin client nodes, saturating low-speed networks, or violating the project's core **Truth Rule** (advisory layers must never present speculative generation as cluster truth).

This specification is explicitly designed to be **portable and config-driven**, containing no hardcoded node names, private IP addresses, or rigid hardware assumptions.

**[ANNOTATION — PARTIAL + DOCTRINE RISK]**
- Goal of safe crews on heterogeneous grid is reasonable and echoes `sovereign-grid-architecture.md` Phase 3 and the verified Triangle proposal.
- However, framing Cortex as the "Execution & Concurrency Engine" with task delivery and a `WorkerDaemon` introduces a more centralized orchestration runtime than the "advisory substrate" model in `triangle-orchestration.md:23` and the explicit rejection of general orchestrators in `doctrine.md:38` and `future-roadmap.md:281`.
- Recent PRs (#139 observations, #140 MCP client) show investment in empirical + MCP foundations, but nothing approaching the proposed execution queue.

**[SUGGESTED]** Re-title or heavily qualify as "Exploratory Direction" rather than near-term v0.11 spec. Cross-reference the narrower Triangle document.

---

## 2. Integrated Architecture Overview (Original)

The system divides responsibilities between two core Advisory layers:

1.  **Triangle (Context & Routing Brain):** Defines the crew session, specialized agent roles (Orchestrator, Muscle, Scout), and required task constraints.
2.  **Cortex Event Queue (Execution & Concurrency Engine):** Manages async task delivery, distributed locks, safety validation, and local execution.

[Diagram showing Triangle → llmrouter.DynamicEngine → reservation.Ledger → Cortex Event Queue → execution.WorkerDaemon]

**[ANNOTATION — DISCREPANCY + DOCTRINE RISK]**
- The 5-layer model (Fact → Snapshot → Placement → Execution → Advisory) is correctly referenced in the Triangle doc and `architecture.md`.
- However, elevating Cortex to "Execution & Concurrency Engine" with task dispatch and introducing `execution.WorkerDaemon` creates a new central control plane. This conflicts with:
  - `doctrine.md`: "not a general-purpose orchestrator", "Optional Execution Surfaces must stay subordinate".
  - `future-roadmap.md:262-281`: Explicit "Path 8" rejection of Nomad/Ray/Ansible-style systems.
  - `AGENTS.md` and `architecture.md`: Advisory surfaces "never override the fact plane".
- No `WorkerDaemon` exists in `internal/execution/` (confirmed via directory scan and `doc.go`: "guarded execution path").

**[CONTEXT FROM TRIANGLE]** Triangle explicitly says it "lives strictly in the Advisory layer" and uses the *existing* MCP server as primary interface (no new central event engine).

**[SUGGESTED]** Remove or heavily qualify the "Cortex Event Queue as execution engine" framing. Keep Cortex as coordination/memory substrate only.

---

## 3. Portability & Generalization (Config-Driven Design) (Original)

... [Role & Label Tagging example in nodes.yaml with rich `roles` array and `labels` including `gpu_backend`, `low_power`, etc.]

**[ANNOTATION — PARTIAL]**
- Basic `role` field exists in `internal/config/config.go:26`.
- Rich `roles` + `labels` map with `gpu_backend` etc. as shown in the example is **not yet implemented** in the config loader or heavily used in placement/facts (grep showed limited hits).
- ResidentModels **does exist** (`internal/models/types.go`, `internal/facts/tools.go`, surfaced in `axis status` and doctor). Good grounding here.
- Dynamic model residency and filtering in placement has partial support via empirical and resident model work (recent commits #66, #67, #68, #69).

**[VERIFIED]** Graceful fallback tiers and portable design intent align with current heterogeneous cluster reality (documented in `docs-evaluation.md` and live `axis status`).

---

## 4. Go Interface & Data Struct Definitions (Original)

### 4.1 Dynamic LLM Routing (`internal/llmrouter/engine.go`)

[Full proposed `DynamicEngine` + `SelectBestEndpoint` code that queries snapshot for resident models + lowest pressure and returns HTTP endpoint]

**[ANNOTATION — DISCREPANCY]**
- The actual `internal/llmrouter/engine.go` (full read) contains `Engine` with `Classify(ctx, prompt, extraContext)` for workload classification using a local Ollama model + reflex fallback (`workload.Match`).
- No `DynamicEngine` struct.
- No `SelectBestEndpoint` method.
- The package is explicitly "EXPERIMENTAL — semantic workload classification and LLM routing" and is used for `axis llm` and placement inference, **not** dynamic endpoint selection for crews.
- Recent hybrid-ai-router PRs (#84–87) added the classification + cloud providers, but not the snapshot-driven router described here.

**[SUGGESTED]** Either implement the proposed router (as new functionality) or revise the spec to describe using the existing `llmrouter.Engine.Classify` + current placement empirical logic.

### 4.2 Speculative Pipeline Reservations (`internal/reservation/ledger.go`)

[Proposed `PipelineStep`, `SpeculativePipeline`, `SpeculativeAcquire` for atomic multi-node reservations]

**[ANNOTATION — DISCREPANCY]**
- `internal/reservation/ledger.go` header: "**SCAFFOLDED** — double-entry reservation ledger for cluster RAM and VRAM. Not wired into the stable operator path."
- Current API: Basic `Reserve`, `Release`, `Heartbeat`, `Reclaim`, `SetNodeCapacity`, `IsStale`.
- No `PipelineStep`, `SpeculativePipeline`, or atomic `SpeculativeAcquire` with rollback.
- `docs/lifecycle.md` and `current-state.md` confirm this status. Related PR #82 added accounting, but not speculative pipelines.
- "axis reservation doctor" referenced in failure matrix exists only as a design sketch in `docs/future/reservation-doctor.md`.

### 4.3 Observation-Based Placement (`internal/placement/empirical.go`)

[Proposed `ApplyObservationModifiers` using historical durations/failure rates]

**[ANNOTATION — PARTIAL]**
- Real empirical support exists and is actively improving (recent commits #69, #71, #139 for per-model observations, PeakRAMMB hard filters, etc.).
- `empirical.go` has `ObservationScopeForRequirements`, freshness checks, and preference comparison logic used in ranking.
- The exact `ApplyObservationModifiers` function and `GetObservationHistory` API as written do not exist in the file.

---

## 5. Formal JSON-RPC 2.0 MCP Tool Schemas (Original)

[Proposed `triangle_request_lease` and `triangle_delegate_task` — `internal/mcp/server.go`: Now includes 13 tools, having implemented the 3 advisory lease primitives (`triangle_request_lease`, `triangle_release_lease`, `triangle_heartbeat_lease`). There is still no `triangle_delegate_task` or any execution routing engine.]

**[ANNOTATION — VERIFIED (v0.10.8)]**
- `internal/mcp/server.go` registers 13 tools, including the 3 new advisory lease tools (`triangle_request_lease`, `triangle_release_lease`, `triangle_heartbeat_lease`).
- These tools interact with the local `reservation.Ledger` and save directly to `~/.axis/ledger.json`.
- The instructions were updated to: "AXIS exposes read-only cluster state, diagnostics, and advisory resource leases. Do not assume any execution authority."

---

## 6. Failure Mode & Mitigation Matrix (Original)

**[ANNOTATION — MIXED]**
- Some rows (NTP skew, cold VRAM, safety sandboxing) are reasonable engineering concerns.
- "axis reservation doctor" and worker heartbeats to Cortex assume capabilities that are not yet present (see above).
- "Arbitrary Execution Vulnerability" mitigation references `safety.Evaluator` (real but experimental per lifecycle.md) and a non-existent `axis-worker` group + WorkerDaemon.

---

## Deep Comparison: Hybrid Spec vs. Triangle Spec vs. Current Reality & Doctrine

### 1. Scope Philosophy
- **Triangle** (verified version): Explicitly "not a general-purpose scheduler or execution runtime". Stays in Advisory layer, MCP-mediated substrate. Includes its own verification appendix.
- **Hybrid**: More integrated "engine" with task queue, worker daemons, speculative pipelines. Closer to the rejected Path 8 model.
- **Doctrine / future-roadmap**: Strongly prefers "cluster truth and tool surface that other agents use" over becoming the runtime.

**Risk Assessment**: Hybrid as written increases the chance of creating hidden authority paths (one of the explicit out-of-bounds items in `doctrine.md`).

### 2. Use of Existing Primitives
- Triangle carefully qualifies Cortex (EXPERIMENTAL), reservation (scaffolded), and builds on MCP + placement + empirical.
- Hybrid assumes more advanced versions of these primitives than currently exist (per code reads + lifecycle.md).

### 3. Recent Development Trajectory (from git + gh PRs)
- Heavy investment in **empirical observations** (exactly the direction both specs want).
- **MCP client unification** (#140) and server improvements — strengthens the substrate approach favored by Triangle.
- Cortex work remains focused on memory + limited events (consistent with current client.go, not a full execution bus).
- No commits or PRs for the new orchestration machinery proposed in the Hybrid spec. The file itself has no git history in the future/ dir (consistent with other design sketches).

### 4. Alignment with Sovereign Grid Vision
- Both specs correctly draw from `sovereign-grid-architecture.md` (Constellations, Execution Leases, Universal MCP Context Provider).
- Hybrid's "request_execution_lease" + delegation is a reasonable evolution of the lease idea in that document.
- However, the centralized event queue + WorkerDaemon risks violating the "stateless, fast-executing Go binary" and "Advisory Before Automatic" constraints repeated throughout sovereign-grid and doctrine.

---

## Recommendations (for the Hybrid Spec)

1. **Narrow the scope** to match Triangle's "advisory substrate" model. Remove or defer the WorkerDaemon and full Cortex-as-execution-queue concepts.
2. **Add a verification appendix** like the one in the post-double-check Triangle document.
3. **Qualify maturity** using language from `lifecycle.md` (many proposed pieces are experimental or scaffolded).
4. **Cross-reference** the verified Triangle proposal and explicitly address how Hybrid differs without creating conflicting visions.
5. **Align with future-roadmap** strategic direction (MCP-native context second, explicit execution only when trust is high).

---

**End of Annotated & Compared Document**

*This file was generated as part of a rigorous evidence-based evaluation on 2026-05-28. It is itself design/evaluation material and sits low in the truth hierarchy defined in `docs/README.md`.*