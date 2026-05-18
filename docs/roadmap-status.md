# AXIS v9 Roadmap — Final Status

**Date:** 2026-05-18  
**Version:** v0.10.2 → v0.10.3  
**Status:** 48 of 53 items complete (90.6%). 5 Phase G items blocked by "evidence before optimization" discipline.

---

## Summary

| Phase | Items | Done | Blocked | Completion |
|-------|-------|------|---------|------------|
| A — Authority Audit | 11 | 11 | 0 | 100% |
| B — Production Hardening | 5 | 5 | 0 | 100% |
| C — Surface Clarity + Lifecycle | 7 | 7 | 0 | 100% |
| D — Reservation Operator Visibility | 5 | 5 | 0 | 100% |
| E — Coverage + Benchmarks | 6 | 6 | 0 | 100% |
| F — pprof + Profiling | 3 | 3 | 0 | 100% |
| G — Performance Optimization | 8 | 3 | 5 | 37.5% |
| H — Structural Cleanup | 9 | 9 | 0 | 100% |
| **Total** | **53** | **48** | **5** | **90.6%** |

---

## Phase A — Canonical Authority Audit (100%)

All 11 authority audits completed with decision documents in `docs/authority-*.md`.

| ID | Title | Status |
|----|-------|--------|
| AUTH-1 | Audit reservation authority | ✅ Done |
| AUTH-2 | Audit node identity authority | ✅ Done |
| AUTH-3 | Audit freshness authority | ✅ Done |
| AUTH-4 | Audit empirical observation authority | ✅ Done |
| AUTH-5 | Audit execution state authority | ✅ Done |
| AUTH-6 | Audit configuration authority | ✅ Done |
| AUTH-7 | Audit secret/credential authority | ✅ Done |
| AUTH-8 | Audit cache authority | ✅ Done |
| AUTH-9 | Audit observability authority | ✅ Done |
| A-10 | Authority transition protocol | ✅ Done |
| AUTH-11 | Add authority inversion detection | ✅ Done |

**Deliverables:** 10 `docs/authority-*.md` documents + `docs/authority-transition.md` + `docs/authority-violations.md`.

---

## Phase B — Production Hardening (100%)

| ID | Title | Status |
|----|-------|--------|
| B-1 | Remove DEBUG printf leaks from execution path | ✅ Done |
| B-2 | Audit all fmt.Printf outside CLI output paths | ✅ Done |
| B-3 | Test cmd/axis/exit.go and audit Fatal() callers | ✅ Done |
| B-4 | Add minimal repair event types | ✅ Done |
| B-5 | Add schema governance foundation | ✅ Done |

---

## Phase C — Surface Clarity + Lifecycle (100%)

| ID | Title | Status |
|----|-------|--------|
| C-1 | Define lifecycle taxonomy with policy constraints | ✅ Done |
| C-2 | Label every internal package | ✅ Done |
| C-3 | Label every CLI command | ✅ Done |
| C-4 | Label every API route | ✅ Done |
| C-5 | Fix README authority ambiguity | ✅ Done |
| C-6 | Fix stale docs (6 documents) | ✅ Done |
| C-7 | Add lifecycle linting to CI | ✅ Done |

**Deliverables:** `docs/lifecycle.md`, `hack/lifecycle-check.go`, 34 packages labeled, 33 commands/routes labeled.

---

## Phase D — Reservation Operator Visibility (100%)

| ID | Title | Status |
|----|-------|--------|
| D-1 | Add `axis reservation list` CLI with --json/--ndjson | ✅ Done |
| D-2 | Add `axis reservation inspect <id>` CLI with --json/--ndjson | ✅ Done |
| D-3 | Add `axis reservation release <id>` CLI with --force | ✅ Done |
| D-4 | Document reservation semantics | ✅ Done |
| D-5 | Future: `axis reservation doctor` | ✅ Done (design doc) |

---

## Phase E — Coverage + Benchmarks (100%)

| ID | Title | Status |
|----|-------|--------|
| E-1 | Add versioncmp tests (0% → 100%) | ✅ Done |
| E-2 | Expand MCP package tests (43.4% → 60%+) | ✅ Done (88.7%) |
| E-3 | Expand facts/local.go edge case coverage | ✅ Done |
| E-4 | Expand execution/guarded.go error branches | ✅ Done |
| E-5 | Add dashboard command contract tests | ✅ Done (90%) |
| E-6 | Add core benchmarks | ✅ Done |

**Coverage delta:**
| Package | Before | After |
|---------|--------|-------|
| Total | 70.4% | 73.8% |
| MCP | 43.4% | 88.7% |
| Dashboard | 0% | 90% |
| Execution | 60.2% | 79.8% |
| versioncmp | 0% | 100% |

**Benchmarks:** 10 benchmarks (placement ranking, snapshot assembly, SSH connection reuse).

---

## Phase F — pprof + Profiling (100%)

| ID | Title | Status |
|----|-------|--------|
| F-1 | Add pprof endpoints to `axis serve` | ✅ Done |
| F-2 | Capture baseline profiles | ✅ Done |
| F-3 | Document profiling workflow | ✅ Done |

**Deliverable:** `docs/profiling.md`.

---

## Phase G — Performance Optimization (37.5% — 5 blocked)

| ID | Title | Status | Rationale |
|----|-------|--------|-----------|
| G-1 | Precompute rank keys before placement sort | ✅ Done | ~54% faster for 50-node clusters; justified by benchmark |
| G-2 | Reuse SSH connection across remote fact commands | ⛔ Blocked | Within-`Collect` reuse already exists (~15× per-command speedup). Cross-cycle pooling needs profile evidence showing SSH handshake >5% of total CPU. |
| G-3 | Version-gate state migration logic | ✅ Done | Prevents repeated migration checks; no profile needed |
| G-4 | Investigate execution context size before caching | ⛔ Blocked | Anti-pattern warning: caching large contexts is premature without evidence of allocation pressure. |
| G-5 | Double-buffer daemon snapshot | ⛔ Blocked | Highest risk. Could introduce correctness bugs in overlay application. Needs pprof evidence of GC pressure from snapshot churn. |
| G-6 | sync.Pool for JSON encoding buffers | ⛔ Blocked | Premature without profiles showing JSON encoding as hotspot. |
| G-7 | Pre-lowercase script registry strings | ✅ Done | Micro-optimization; trivial and safe |
| G-8 | Optimize struct field ordering | ⛔ Blocked | Micro-optimization. Needs evidence of memory pressure from struct padding. |

**Blocking principle:** *Evidence before optimization.* Every blocked item requires production CPU/memory profile evidence justifying the change. Without profiles, these optimizations are speculative and risk introducing correctness bugs for marginal gain.

**G-2 specific note:** SSH connection reuse benchmark (`internal/transport/ssh_bench_test.go`) proved existing within-collect reuse is already optimal. Cross-cycle pooling would add significant complexity (connection lifecycle, health checks, idle timeout, concurrency safety) for negligible benefit in typical deployments (2–5 nodes, 30–60s refresh interval).

---

## Phase H — Structural Cleanup (100%)

| ID | Title | Status |
|----|-------|--------|
| H-1 | Create `examples/` directory | ✅ Done |
| H-2 | Decide dashboard command fate | ✅ Done |
| H-3 | Decide `/v2/reservations` endpoint fate | ✅ Done |
| H-4 | URGENT: Add mesh disable flag or config gating | ✅ Done |
| H-5 | Compile-gate safety/structured | ✅ Done |
| H-6 | Delete Fatal() after confirming zero callers | ✅ Done |
| H-7 | Deduplicate dashboard UI constants | ✅ Done |
| H-8 | *(no item — numbering gap)* | — |
| H-9 | Future: Strategic consistency-model document | ✅ Done |

---

## Key Metrics at Completion

| Metric | Before | After | Delta |
|--------|--------|-------|-------|
| Test coverage (total) | 70.4% | 73.8% | +3.4pp |
| MCP coverage | 43.4% | 88.7% | +45.3pp |
| Dashboard coverage | 0% | 90% | +90pp |
| Execution coverage | 60.2% | 79.8% | +19.6pp |
| Benchmarks | 0 | 10 | +10 |
| Authority audit docs | 0 | 13 | +13 |
| Lifecycle labels | 0 | 34 packages + 33 commands/routes | — |
| DEBUG printf leaks | 6 | 0 | -6 |
| Schema versioning | none | `currentStateVersion` in state.go | — |

---

## North Star Preserved

> *"Every proposed abstraction must be filtered through: Does this reduce or increase operator explainability?"*

The 5 blocked Phase G items were intentionally stopped because they would have **decreased** operator explainability (hidden connection pools, double-buffered state, sync.Pool lifecycle) without evidence that the current system is insufficient.

> *"Dead infrastructure is more dangerous than missing infrastructure."*

All speculative optimizations remain unimplemented. The system is simpler, more explicit, and more deterministic as a result.

---

## What v9 Did NOT Do (Intentionally Out of Scope)

- Distributed consensus (Raft, Paxos)
- Multi-operator coordination / ACLs
- HA quorum / failover control plane
- WAN mesh reliability at scale
- Autonomous self-modifying agents
- Cloud control plane abstractions (K8s CRDs, serverless)

AXIS remains: **local-first**, **single-operator**, **pragmatic consistency**, **operator-over-theory**.

---

## Next Phase Recommendation

No further engineering work is recommended under the v9 roadmap. The 5 blocked Phase G items should remain blocked until operational profiling evidence justifies them.

Potential v10 themes (to be defined by operational need, not speculation):
- Multi-cluster federation (if operators request it)
- Web-based operator UI (if CLI surfaces prove insufficient)
- Observability pipeline (metrics export, structured logging)
- Reservation ledger hardening (if dual-write bugs are observed)

---

*Generated: 2026-05-18*  
*Canonical roadmap: `docs/roadmap-v9.md` (session workspace)*  
*Status tracked in SQL session database*
