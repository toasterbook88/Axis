# AXIS Substrate-First Architecture Roadmap

This document captures the next-layer architecture for AXIS after the placement-contract unification work. It exists to keep the project substrate-first as it grows.

## Authority Hierarchy

AXIS must maintain a strict authority order:

1. **Truth plane**
   - live facts
   - snapshot assembly
   - node identity
   - cache provenance
   - degraded-state reporting
2. **Decision plane**
   - workload classification
   - placement requirements
   - fit scoring
   - failure-memory influence
   - canonical `PlacementDecision`
3. **Orchestration plane**
   - multi-unit work planning
   - per-unit node assignment
   - dependency ordering
   - guarded execution policies
4. **Interaction plane**
   - CLI
   - TUI
   - HTTP
   - MCP
   - chat
   - agent

Interaction surfaces are clients of the substrate. They must not redefine truth or placement semantics.

---

## 1. Workload Profile System

### Goal
Replace free-text heuristic sprawl with a deterministic workload profile layer that feeds the placement engine.

### Core flow

`task string -> workload profile match -> TaskRequirements -> PlacementDecision`

### Model additions

Add to `internal/models`:

- `WorkloadClass string`
- `WorkloadProfile struct`
- `WorkloadProfileMatch struct`

Suggested initial classes:

- `repo-analysis`
- `go-build`
- `docker-build`
- `local-llm-inference`
- `long-context-inference`
- `indexing-io`
- `batch-script`

### New package

`internal/workload/`

Suggested files:
- `profiles.go`
- `parse.go`
- `requirements.go`
- `match.go`

### Matching rules

Use deterministic phrase groups and precedence.

Examples:
- `analyze repo`, `review codebase` -> `repo-analysis`
- `go build`, `run go tests` -> `go-build`
- `docker build`, `build image` -> `docker-build`
- `run ollama`, `7b`, `llama.cpp`, `mlx` -> `local-llm-inference`
- `128k`, `long context`, `million token` -> `long-context-inference`
- `index`, `embed`, `vectorize`, `scan filesystem` -> `indexing-io`

Precedence:
- `long-context-inference` overrides `local-llm-inference` when both match
- backend keywords add preferences without replacing the profile unnecessarily
- one primary class only; ambiguity becomes notes, not unions

### Profile fields

A workload profile should be able to express:
- required tools
- preferred backends
- minimum free RAM
- GPU preference / requirement
- local-only requirement
- storage sensitivity
- network sensitivity
- long-context flag
- interactive flag
- heavy flag

### Integration

Placement should stop owning free-text parsing details directly.

The final `PlacementDecision` should eventually carry:
- `workload_class`
- `workload_match_notes[]`

### Non-goals
- no ML classifier
- no user-defined profiles in v1
- no per-node logic in the parser

---

## 2. Proper Failure / Tombstone Learning System

### Goal
Turn tombstones into a disciplined failure-memory subsystem that influences decisions without becoming observed truth.

### Core rule
Failure memory is operator memory, not fact-plane truth.

### Model additions

Add to `internal/models`:
- `FailureClass string`
- `FailureScope struct`
- `FailureRecord struct`
- `FailureSummary struct`

Suggested `FailureClass` values:
- `exec-crash`
- `tool-missing`
- `resource-exhaustion`
- `thermal-failure`
- `battery-failure`
- `network-failure`
- `timeout`
- `backend-misfit`
- `operator-abort`
- `unknown`

Suggested `FailureScope` fields:
- `Node`
- `WorkloadClass`
- `Tool`
- `Backend`
- `Surface`

Suggested `FailureRecord` fields:
- `ID`
- `Class`
- `Scope`
- `OccurredAt`
- `ExpiresAt`
- `Count`
- `Confidence`
- `Reason`
- `Evidence[]`
- `OperatorOverride`
- `OperatorNote`

### New package

`internal/failures/`

Suggested files:
- `record.go`
- `scope.go`
- `expiry.go`
- `summary.go`
- `override.go`

### Behavior

1. a guarded execution surface emits a structured failure event
2. failure memory updates or creates a scoped record
3. placement requests a scoped summary
4. summary can hard-block, soft-penalize, or attach warnings
5. `PlacementDecision` includes failure-derived notes as warnings/rejections

### Scope precedence
Prefer the narrowest matching scope:
- node + workload_class + backend
- node + workload_class
- node + tool
- node only

### Decay / expiry
Suggested policy:
- first failure: ~24h influence
- repeated similar failures: exponential backoff to 7d cap
- successful execution on same scope reduces penalty
- operator override clears immediately

### Placement semantics
Operator-facing strings should be explicit, e.g.:
- `penalized: repeated timeout for local-llm-inference on node X`
- `blocked: exec-crash repeated 3 times for backend mlx on node Y`

Not observed-health claims unless backed by live probes.

### Non-goals
- no hidden ML adaptation
- no silent mutation of workload profiles from failure memory

---

## 3. Agent Integration Without Breaking Substrate

### Goal
Let agent workflows consume AXIS safely without creating alternate truth or execution semantics.

### Rule
The agent is an interaction client, not a decision authority.

### Agent should consume only substrate-native services
- snapshot service
- placement service
- execution proposal / confirmation service
- advisory operator memory reads

### Tool contract
Preferred read tools:
- `get_snapshot`
- `get_node_details`
- `place_task`
- `get_placement_explanation`
- `get_failures_for_scope`

Optional write/execute tools:
- `propose_execution`
- `execute_with_confirmation`
- `clear_failure_override` (operator-confirmed only)

### Hard constraints
- the agent may not choose target nodes independently when placement is available
- the agent may not bypass confirmation / policy
- the agent may not redefine provenance or warnings
- the agent may not invent alternate cache semantics

### Execution flow

`agent -> snapshot -> placement -> explanation -> execution proposal -> guarded execution engine`

### Package shape
`internal/agent/` should stay thin.
If needed, add thin wrappers such as:
- `tools_snapshot.go`
- `tools_place.go`
- `tools_execute.go`

But semantic logic must remain outside the agent package.

### Non-goals
- no agent-managed ranking logic
- no hidden auto-execution path
- no agent-only heuristics

---

## 4. Operator TUI

### Goal
Introduce a substrate-first operator surface for observing truth, degraded state, and placement explainability.

### Rule
The TUI must not own truth, placement, or execution semantics.

### Core data sources
The TUI should consume:
- `ClusterSnapshot`
- canonical `PlacementDecision`
- cache freshness metadata
- reservation / failure-memory summaries

### Layout
Suggested first screen:

#### Top bar
- source: live / daemon-cache
- snapshot age
- node counts by status
- warnings count

#### Left pane: node table
Columns such as:
- node
- status
- role
- RAM / CPU
- pressure
- GPU/backend summary
- storage class
- locality
- reserved MB

#### Right upper pane: selected node details
- hostname / addresses
- interfaces / speed class
- tools / backends
- GPU details
- battery / thermal / pressure metadata
- warnings / partial-fact notes
- reservation and failure-memory summary

#### Right lower pane: placement explainability
- task string
- workload class
- selected node
- fit score
- score breakdown
- rejected nodes + reasons
- provenance + warnings

### Secondary views
Hotkeys/tabs can start with:
- snapshot
- placement
- warnings/degraded-state
- reservations/failures

### New package
`internal/tui/`

Suggested files:
- `app.go`
- `layout.go`
- `snapshot_view.go`
- `placement_view.go`
- `warnings_view.go`
- `state.go`
- `bindings.go`

### TUI local state only
The TUI may own:
- selected row
- active tab
- filter text
- current task input

The TUI may not own:
- ranking logic
- cache policy
- node identity rules
- execution policy semantics

### v1 acceptance criteria
- operator can distinguish live vs cached immediately
- operator can inspect degraded-state visibility clearly
- operator can understand why a node was selected
- operator can inspect why nodes were rejected

### Non-goals for v1
- no embedded chat
- no autonomous agent control
- no hidden execution automation

---

## 5. Multi-Node Orchestration Layer

### Goal
Lift AXIS from single-task placement to deterministic multi-node workload coordination.

### Rule
Start with a plan artifact, not autonomous distributed execution.

### Core flow

`operator intent -> work graph -> per-unit placement -> WorkPlan -> optional guarded execution`

### Model additions
Add to `internal/models`:
- `WorkUnit`
- `WorkPlan`
- `WorkAssignment`
- `ExecutionPolicy`

Suggested `WorkUnit` fields:
- `ID`
- `Name`
- `WorkloadClass`
- `TaskDescription`
- `DependsOn[]`
- `Parallelizable`
- `Inputs[]`
- `Outputs[]`
- `PreferredNode` (hint only)

Suggested `WorkAssignment` fields:
- `WorkUnitID`
- `Placement PlacementDecision`
- `AssignedNode`
- `Reasoning[]`

Suggested `WorkPlan` fields:
- `PlanID`
- `RequestedAt`
- `Units[]`
- `Assignments[]`
- `Warnings[]`
- `Source`

Suggested `ExecutionPolicy` fields:
- `Mode` (`plan-only|confirm-each|auto-safe`)
- `MaxParallel`
- `AllowRemote`
- `RequireConfirmation`

### New package
`internal/orchestrate/`

Suggested files:
- `plan.go`
- `graph.go`
- `assign.go`
- `schedule.go`
- `policy.go`

### v1 scope
Deterministic orchestration planning only.

Example intents:
- `analyze repo, then run tests, then summarize results`
- `index docs while building binary`
- `run inference on best GPU node, store output locally`

The system should:
1. parse the request into work units using deterministic templates first
2. assign each unit using the canonical placement contract
3. return a `WorkPlan`
4. let the operator inspect before any execution occurs

### Parsing strategy
Start small:
- sequential: `A then B then C`
- parallel: `A while B`
- simple chain via comma-separated clauses

### Why this matters
This is the point where AXIS becomes a real workload constellation engine:
not just “where should this run?” but “how should this set of dependent tasks flow across the cluster?”

### Non-goals for v1
- no autonomous self-healing retries
- no artifact transport engine yet
- no speculative distributed execution
- no hidden background orchestration

---

## Recommended implementation order

1. placement contract unification
2. workload profiles
3. proper failure memory
4. stable node identity
5. TUI v1
6. orchestration plan artifact
7. guarded orchestration execution

This preserves substrate-first growth:
`truth plane -> decision plane -> orchestration plane -> interaction surfaces`
