# AUTH-4: Empirical Observation Authority

## 1. Where Execution Observations Are Collected

| Layer | File | Role |
|-------|------|------|
| **Collection** | `internal/execution/guarded.go` | `recordExecutionOutcome()` gathers post-execution metrics (elapsed time, peak RAM, success/failure) from both local and remote runs. |
| **Storage** | `internal/state/observations.go` | `ClusterState.RecordObservation()` persists observations into `ClusterState.Observations` map. |
| **Schema** | `internal/models/types.go` | `ExecutionObservation` and `ObservationScope` define the data model. |

Observations are written **only after a guarded execution finishes** (success or failure). There is no background probe that creates observations independently of execution.

## 2. Who Mutates Observations

The sole writer path is:

```
guarded execution (local or remote)
  → recordExecutionOutcome()
    → st.RecordObservation()
      → mergeObservation() (if key already exists)
      → save to ClusterState.Observations map
      → st.Save() (caller in recordExecutionOutcome)
```

**Key functions:**
- `recordExecutionOutcome` (`internal/execution/guarded.go:1039`) — called from `runLocal` and `runRemote` after the shell command returns.
- `RecordObservation` (`internal/state/observations.go:106`) — upserts into the in-memory map; does **not** auto-save; caller must `st.Save()`.
- `mergeObservation` (`internal/state/observations.go:83`) — merges new sample with existing entry using weighted averaging.

No other package writes observations. Read-only consumers:
- `internal/placement/ranker.go` — `RankCandidates` precomputes empirical observations for the sort comparator.
- `internal/placement/selector.go` — `SelectBestNode` includes empirical history in decision reasoning.
- `internal/placement/explain.go` — `empiricalPeakRAMExclusionReason` reads observations for filter eligibility.

## 3. Authoritative vs Advisory

**Observations are advisory, not authoritative.**

- They **do not** override snapshot facts (allocatable RAM, tool presence, pressure, GPU state).
- They **do not** block execution directly; they only influence placement ranking and can trigger a soft filter exclusion when a fresh observation's `PeakRAMMB` exceeds current allocatable RAM.
- The freshness gate (`ObservationStaleAfter = 7 days`) means absent or stale observations are silently ignored.

The only place observations affect hard eligibility is `empiricalPeakRAMExclusionReason` in `internal/placement/explain.go`, and even that is explicitly documented as conservative: it only fires when a **fresh** observation exists and `PeakRAMMB > 0`.

## 4. Does Placement Directly Use Observations vs Snapshot Facts?

**Placement uses both, with snapshot facts taking precedence.**

| Stage | Observation usage | Snapshot fact usage |
|-------|-------------------|---------------------|
| **Filter** (`FilterCandidates`) | `empiricalPeakRAMExclusionReason` may exclude a node if observed peak RAM > current allocatable | Status, tools, pressure, thermal, battery, MinFreeRAMMB |
| **Rank** (`RankCandidates`) | `compareObservationPreference` ranks nodes by last success, lower peak RAM, lower wall time, higher sample count | Allocatable RAM, GPU score, backend rank, headroom, TurboQuant, unified memory, reservation ratio |
| **Explain** (`buildSuccessDecision`) | `empiricalReason` adds diagnostic text | All snapshot-derived reasoning |

Observations are **never** used to compute `allocatableRAM`, `gpuScore`, or any core resource metric. They are a tie-breaker and a guardrail, not a primary signal.

## 5. Silent Reconciliation of Observation Data

The observation pipeline silently normalizes and merges data in several ways:

### 5.1 Normalization (`normalizeObservation`)
- `ObservedAt` defaults to `time.Now().UTC()` if zero.
- `SampleCount` defaults to `1` if ≤ 0.
- `WallTimeMS` defaults to `1` if ≤ 0 (prevents division by zero in weighted average).
- `PeakRAMMB` and `PeakVRAMMB` clamped to `0` if negative.
- `ModelName` synced from `Scope.ModelName` if empty.
- All string fields trimmed and lower-cased for key generation.

### 5.2 Merge semantics (`mergeObservation`)
- `SampleCount` is additive (`existing + next`).
- `WallTimeMS` is a **weighted average** across total samples.
- `PeakRAMMB` and `PeakVRAMMB` keep the **maximum** seen across runs (not average).
- `ObservedAt` and `LastSuccess` are overwritten with the latest values.
- `ModelName` is overwritten if the new observation carries one.

### 5.3 Key scope
`ObservationKey` hashes `node + workload + backend + tool + model_name` (SHA-256, 12 hex chars). This means:
- Observations are **scoped per node, per workload, per tool, per model**.
- Two different model names on the same node get separate observation entries.
- Empty `ModelName` preserves backward-compatible keys for unscoped observations.

### 5.4 Staleness
`ObservationIsFresh` uses a fixed 7-day window. Stale observations are invisible to placement; there is no decay or partial weighting.

## Summary Table

| Property | Value |
|----------|-------|
| Source of truth for observations | `state.json` (`ClusterState.Observations`) |
| Writer | `internal/execution/guarded.go` (post-execution only) |
| Reader | `internal/placement` (rank, filter, explain) |
| Authority | Advisory (ranking tie-breaker, conservative hard filter) |
| Reconciliation | Weighted-average wall time, max peaks, additive samples |
| Staleness threshold | 7 days |
