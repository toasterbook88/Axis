# Decision: Placement Selection Contract

**Date:** 2026-07-25
**Scope:** `internal/placement`, `internal/models`, `internal/workload`, `cmd/axis/task.go`, `cmd/axis/placement.go`, MCP and HTTP placement surfaces
**Decision:** SEPARATE feasibility (AXIS-owned, hard) from ranking objective (operator-owned, explicit). Retire the composite FitScore as a ranking mechanism.
**Evidence:** `docs/evaluations/2026-07-25-truth-integrity-audit.md` (B2, B3, B4)
**Related:** `docs/decisions/topology-truth-contract.md` (vantage discipline), `docs/substrate-roadmap.md` §1 (workload profiles)

---

## 1. Context

Placement today runs two mechanisms that answer different questions and disagree.

`RankCandidates` sorts by **allocatable RAM first**. Keys 2–5 (empirical observation, resident-model locality, preferred backend, GPU score) are reachable only on exact RAM ties, which effectively never occur across heterogeneous hardware. Task-specific signal therefore rarely reaches the decision — whether or not classification succeeded.

`ComputeTaskFitScore` produces a separate 0–100 composite that is displayed as the headline number but does not order anything. On the audited grid the two disagree in opposite directions: the ranker selects the largest-RAM node, the score names a different node as better.

The composite is also structurally unable to discriminate. Verified against live output, which the formula reproduces exactly:

```
node-hub:    RAM 30 + pressure 25 + GPU 15 + CPU 10 + local 10 + direct-lan 20 = 110 → clamped 100
node-big:    RAM 30 + pressure 25 + GPU 15 + CPU 10 +           relayed −20 =  60
```

Every point of that 40-point gap is locality. Network class (±20) plus local bonus (+10) is a 50-point swing — larger than RAM's 30 and GPU's 25 — so **locality dominates the score**, while capability differences vanish into saturation caps:

| Dimension | Cap | Saturates at | Consequence on the audited grid |
| --- | --- | --- | --- |
| Allocatable RAM | 30 | 7.68 GB | Four nodes spanning a 12.6× RAM range score identically |
| VRAM | 5 | 5 GB | A laptop-class 4 GB GPU scores within 1 point of an 11 GB card |
| CPU cores | 10 | 10 cores | A 32-core node ties a 10-core node |
| GPU count | — | best single | A node's second GPU is invisible |

Underlying all of this: the score conflates three different questions into one dimensionless integer — **can this run here** (boolean), **what will it cost** (a prediction in real units), and **should we place here anyway** (policy). Mixing them makes the weights unfalsifiable; there is no ground truth for "pressure is worth 25 while RAM is worth 30."

Finally, `TaskRequirements` carries no OS or architecture constraint, so a platform-specific command can be placed on an incompatible node (B4).

## 2. Decision

1. **Feasibility is AXIS's responsibility and is a hard filter.** A node either can run the task or is excluded with a stated reason.
2. **Ranking objective is the operator's choice and must be explicit.** AXIS does not define a single universal "best node." Deciding whether fastest, most local, most fleet-preserving, or most balanced is correct for a given grid is a policy judgement belonging to the operator.
3. **The composite FitScore is retired as a ranking mechanism.** Its per-dimension breakdown survives as diagnostic detail in `placement explain`; it is never again the headline number or the sort key.
4. **Every decision states its objective, its ranking metric, and the provenance of the data used.**

## 3. Definitions

- **Feasibility** — whether a node can execute the task at all. Hard, boolean, AXIS-owned.
- **Objective** — the operator-selected criterion by which feasible nodes are ordered.
- **Ranking metric** — the single quantity, in stated units, that the chosen objective sorts by. Must be the number displayed.
- **Provenance** — whether the metric came from `empirical` (observed execution), `probed` (capability micro-probe), or `estimated` (structural facts only). Rendered with the decision.

## 4. Feasibility filter

Extends the existing `evaluateCandidates` / `ExclusionReasons` mechanism in `internal/placement/explain.go`. Present criteria are retained.

**Existing (retained):** node status, critical memory pressure, battery floor, thermal throttle state, Apple Foundation Models locality, blocking failure classes / tombstones, required tools, minimum RAM, empirical peak-RAM exceeding allocatable.

**New:**

| Criterion | Rule |
| --- | --- |
| **OS constraint** | When `TaskRequirements.OS` is set, exclude nodes whose `NodeFacts.OS` differs. |
| **Architecture constraint** | When `TaskRequirements.Arch` is set, exclude nodes whose `NodeFacts.Arch` differs. |
| **Model fit** | When the requested model's size is known, exclude nodes whose usable accelerator memory cannot hold weights plus KV cache. |

`TaskRequirements` gains `OS string` and `Arch string`. Both are populated by workload classification where inferable and may be set explicitly.

**Model fit is conditional and must degrade honestly.** Model weight size is known when it comes from resident-model facts (`ResidentModel.WeightSizeMB`), from the local model inventory, or from an explicit requirement. `ResidentModel.SizeRAMMB` and `ResidentModel.SizeVRAMMB` are observations of current residency, not substitutes for model weight size. When weight size is unknown, **the filter does not apply** and the decision records `model_fit: unknown`. AXIS must not guess a model's memory footprint from its name.

Usable accelerator memory is `GPUInfo.VRAMMB` for discrete GPUs, and allocatable system RAM for unified-memory nodes (where `VRAMMB` is reported as 0). This corrects a current asymmetry in which unified-memory nodes score zero VRAM despite their real ceiling being system RAM.

## 5. Objectives

`PlacementObjective` is a new field on the placement request, not on `TaskRequirements` — it is an operator preference, not a property of the task.

| Objective | Ranking metric | Units | Notes |
| --- | --- | --- | --- |
| `headroom` | allocatable RAM remaining after placement | MB | Today's behavior, named and preserved |
| `fastest` | predicted completion time | seconds | Empirical → probed → estimated |
| `local` | route cost from vantage | ordinal | Per the topology truth contract; vantage-labeled |
| `spread` | resulting cluster reservation skew | stddev | The current skew penalty term, promoted to an objective |
| `preserve` | fleet-impact cost | ordinal | Prefers AC power, cool nodes, non-battery hosts |

Each objective is a total order over feasible candidates with a deterministic tiebreak chain ending in node name ascending, preserving determinism.

**`fastest` degrades explicitly.** With an empirical observation for the exact scope it uses observed wall time. With capability probes but no observation it uses a modeled estimate. With neither it falls back to `headroom` ordering **and says so** — it never presents an estimate it cannot support.

## 6. Default objective

Default is `headroom`, matching current behavior so that upgrading changes no placement outcome by itself.

The default is configurable and is **always displayed**, so operators can see that a policy choice is in effect rather than inheriting one invisibly. Documentation should recommend setting an explicit objective.

This default is deliberately conservative. Much of the absurdity motivating this contract — a trivial command routed to a large relayed node, a platform-specific command placed on an incompatible OS — is corrected by the feasibility filter regardless of objective, so the default need not carry that weight.

## 7. Capability micro-probes

The `fastest` objective needs data the fact plane does not currently hold: memory bandwidth, storage read throughput, and achievable tokens per second. None is derivable from `Resources` or `GPUInfo` today.

These are acquired by **explicit, operator-invoked micro-probes**, per `docs/sovereign-grid-architecture.md` §3 — not from a shipped hardware-specification table. A static table would be reference data and, under the observed/inferred/absent discipline, would carry an inferred marker permanently. Probed values are observed, in real units, and age honestly.

**Probe discipline:**

- **Operator-invoked only.** Never automatic, never on a schedule, never triggered by placement. Running capability benchmarks as background activity would contradict `docs/doctrine.md` ("agentless by default", "not a background scheduler").
- **Refuses unsafe hosts.** Skips nodes on battery or in a serious/critical thermal state, and reports the skip.
- **Bounded.** Each probe has a hard time and resource ceiling.
- **Timestamped with provenance.** Stored as capability observations carrying probe version, duration, and observation time.
- **Ages explicitly.** Stale probe data is rendered as stale, never silently trusted.

Probe results are inputs to objectives. They are not truth about placement and never bypass the feasibility filter.

## 8. Rendering contract

Binding on CLI, HTTP, MCP, agent, and chat surfaces. This is what `docs/invariants.md` *Single Placement Contract* requires: one decision model across every surface.

| Rule | Requirement |
| --- | --- |
| **P1** | The decision states the objective in effect and whether it was default or explicit. |
| **P2** | The displayed headline number is the ranking metric, with units. |
| **P3** | Metric provenance (`empirical` / `probed` / `estimated`) is shown. |
| **P4** | Runner-up is reported using the same metric as the winner. |
| **P5** | No dimensionless composite may appear as a headline or ordering basis. |
| **P6** | When an objective degrades for lack of data, the degradation is stated. |
| **P7** | Excluded nodes are listed with their exclusion reason. |

B3 dissolves under P2 and P4: the number an operator reads is the number that decided the outcome.

## 9. Non-goals

- No power or monetary cost modeling — AXIS has no power draw data.
- No multi-node task splitting or constellation placement.
- No automatic or scheduled probing.
- No ML-based prediction. `fastest` estimates are arithmetic over observed quantities.
- No change to the reservation ledger or execution authority.
- No hardware-specification lookup table shipped as fact.

## 10. Acceptance criteria

| ID | Test | Pass condition |
| --- | --- | --- |
| **S1** | Platform-specific command against a mixed-OS cluster | Incompatible-OS nodes excluded with a stated reason (B4) |
| **S2** | Trivial command vs large inference task | Rankings differ; identical ordering across unrelated tasks is a failure (B2) |
| **S3** | Any decision output | Headline number equals the ranking metric (B3, P2) |
| **S4** | Cluster with no probes, objective `fastest` | Degrades to `headroom` and states the degradation (P6) |
| **S5** | No objective configured | Default named explicitly in output (P1) |
| **S6** | Requested model size unknown | Model-fit filter does not apply; `model_fit: unknown` recorded |
| **S7** | Unified-memory node, model larger than allocatable RAM | Excluded by model fit |
| **S8** | Same request across CLI, HTTP, and MCP | Identical selection and identical metric |
| **S9** | Node on battery or thermally throttled | Probe refuses and reports the skip |
| **S10** | Repeated identical request | Deterministic selection preserved |

S1 and S2 are regressions of live failures recorded in the audit.

## 11. Consequences

**Behavior.** Default `headroom` means no placement outcome changes on upgrade from ranking alone. The feasibility filter *will* change outcomes — that is its purpose, and every exclusion is stated.

**Removed.** `ComputeTaskFitScore` as sort key and headline. Its dimension breakdown remains available in `placement explain` as diagnostic detail. Existing tests asserting fit-score-driven ordering encode the rejected model and must be replaced rather than adjusted.

**Sequencing.** The feasibility filter and objective plumbing are independent of micro-probes and should land first; only `fastest` depends on probe data. Workload taxonomy expansion (Rust and peers, audit B1) should follow this contract, since taxonomy improvements have no measurable effect on placement until classification can reach the decision.

**Honest limitation.** Separating feasibility from objective does not make placement optimal. It makes the basis for each decision visible and contestable, and it moves the "which node is best" judgement to the party that can actually answer it.

## 12. Public repository boundary

Objectives, probe definitions, fixtures, and documentation use symbolic node names and RFC 5737 documentation addresses. No grid hostnames, private or relay addresses, operator home paths, or model catalogs, consistent with `docs/decisions/inference-roles-and-backends.md` §3. Probe results are operator-local and are never committed.
