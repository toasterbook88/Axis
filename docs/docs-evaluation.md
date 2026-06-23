# Evaluation of the AXIS Documentation Corpus

**Status**: Evaluation snapshot (one-time analysis)  
**Date**: 2026-05-28  
**Evaluator**: Grok (xAI)  
**Scope**: All 39 Markdown files under `/home/cranium/axis/docs/` (including `future/`, `decisions/`, and `runbooks/` subdirectories)  
**Version of AXIS under review**: 0.10.7 (commit d8900ec, binary built 2026-05-27)

---

## Important: How to Read This Document

Per `docs/README.md`, the established truth hierarchy in this repository is:

1. Live code and command behavior
2. `docs/current-state.md` (CI-refreshed facts)
3. `docs/agent-worklog.md`
4. Older design docs, white papers, roadmaps, and evaluations (this document falls here)

**This file is an evaluation snapshot, not canonical truth.** It will age. When it disagrees with live code, `current-state.md`, `doctrine.md`, `architecture.md`, or `lifecycle.md`, those sources take precedence. Current canonical state lives in `docs/current-state.md` (CI-validated).

This document was created at the explicit request of the operator after a complete evaluation pass. It does not modify any other files.

---

## Executive Summary

The `/home/cranium/axis/docs/` corpus (39 files, ~396 KB, ~6,670 lines) is **above average for a project of this complexity** and shows genuine commitment to the project's own "Live Reality Beats Narrative" principle.

**Core strengths**:
- `doctrine.md`, `architecture.md`, `lifecycle.md`, and `current-state.md` form an exceptionally strong, mutually reinforcing guardrail system.
- The 11-file `authority-*.md` series provides unusually deep, code-anchored documentation of the reservation/execution authority model.
- `future-roadmap.md` (especially Path 8) and `sovereign-grid-architecture.md` correctly identify the risk of becoming a heavyweight orchestrator and position Axis as a truth + MCP substrate for other agents.
- Maturity language (stable / experimental / scaffolded) is rigorously defined in `lifecycle.md` and largely accurate when cross-checked against source and live binary.

**Key risks**:
- Visionary documents (`distributed-cognitive-architecture.md`, parts of `substrate-roadmap.md`, `hybrid-ai-router-plan.md`) use expansive language ("Orchestration Bus (Thalamus)", "Distributed Cognitive Architecture", "orchestration plane") that can drift from doctrine if read without the truth hierarchy.
- "Orchestration" terminology is used inconsistently across the corpus.
- The new Triangle proposal (`future/triangle-orchestration.md`) is one of the *best* aligned documents in the entire set, but it is not yet widely cross-referenced.
- Some older specs and roadmaps describe states that have been superseded by shipped functionality (especially empirical observations, resident models, pressure/thermal awareness, and guarded execution details now in `current-state.md`).

**Triangle-specific finding**: The post-verification version of `future/triangle-orchestration.md` (as of 2026-05-28) is doctrinally sound. It correctly stays in the Advisory layer, qualifies experimental/scaffolded components, and directly implements ideas from `sovereign-grid-architecture.md` without violating the "do not become a general orchestrator" boundary stated in `future-roadmap.md` and `doctrine.md`.

---

## Methodology

- Full filesystem enumeration of `/home/cranium/axis/docs/` (39 files, subdirectories, sizes, mtimes).
- Multiple passes of `grep` across the entire tree for high-signal terms (Triangle, Constellations, Execution Lease, orchestrator, heavyweight, "fact plane", Truth Rule, Cortex, MCP + read-only, scaffolded, experimental, advisory, reservations/ledger, empirical).
- Deep reads (full or large strategic chunks) of: `docs/README.md`, `doctrine.md`, `architecture.md`, `lifecycle.md`, `current-state.md`, `future-roadmap.md`, `sovereign-grid-architecture.md`, `distributed-cognitive-architecture.md`, `consistency-model.md`, `reservation-doctor.md`, `authority-reservation.md`, and representative others.
- Cross-checks against:
  - Live binary output (`axis --help`, `status --cached`, `reservations`, `doctor`, `mcp`, `agent`, `cortex`, `version`).
  - Source files (`internal/reservation/ledger.go`, `execution/guarded.go`, `mcp/server.go`, `cortex/client.go`, `agent/tools.go`, `placement/empirical.go`, `cmd/axis/main.go`, etc.).
  - CI/hack tooling (`hack/verify-repo-truth.sh`, `lifecycle.md` enforcement ideas).
  - Remote GitHub state (toasterbook88/axis).
- Evaluation performed with strict fidelity to Axis identity (single-binary, fact plane primary, advisory surfaces subordinate, no hidden heavyweight orchestration).

---

## Corpus Statistics (as of 2026-05-28)

- **Total files**: 39 (all `.md`)
- **Total size**: ~396 KB
- **Total lines**: ~6,670
- **Most recently touched**: `future/triangle-orchestration.md` (2026-05-28 01:13, 20,756 bytes)
- **Largest files** (by size):
  - `current-state.md` (26,226 bytes)
  - `future/triangle-orchestration.md` (20,756 bytes)
  - `hybrid-ai-router-plan.md` (20,230 bytes)
  - `future-roadmap.md` (15,897 bytes)
- **Subdirectories**: `future/` (3 files), `decisions/` (2), `runbooks/` (2)

Full metadata appendix appears at the end of this document.

---

## Strengths

### 1. Doctrine & Guardrails Are Excellent
`doctrine.md` is one of the strongest single documents in the repository. It clearly defines:
- Primary job (facts → snapshot → deterministic advisory placement)
- "What AXIS Is Not" (general-purpose orchestrator, background scheduler, hidden agent runtime)
- Core principles ("Live Reality Beats Narrative", "Advisory Before Automatic", Minimalism)
- Explicit decision rules and in-bounds/out-of-bounds work

This is reinforced by `architecture.md` (strong "layers are subordinate" language + top disclaimer to trust code + `current-state.md`), `lifecycle.md`, and `AGENTS.md`.

### 2. Lifecycle Taxonomy Is a Major Asset
`lifecycle.md` defines a clear, enforceable maturity model (stable/experimental/scaffolded/etc.) with inheritance rules. The classifications are largely accurate when cross-checked against source and live binary:
- Core fact/placement/snapshot/state = stable
- `cortex`, `mcp`, `agent`, `execution`, `chat`, `llm`, `safety` = experimental
- `reservation`, `repairs`, `mesh` = scaffolded

This taxonomy is under-utilized outside `lifecycle.md` itself but provides the project with an excellent tool for preventing scope creep.

### 3. Current State Document Is High Signal
`current-state.md` is the best "what actually ships today" document. It includes a generated facts section (refreshed via script), detailed command surface, package map with maturity notes, and a long list of real shipped capabilities (per-model empirical observations, resident models, pressure/thermal/battery awareness, tombstone immune system, Git-aware routing, etc.). It correctly flags risk in the execution and chat surfaces.

### 4. Authority Series Is Unusually Deep
The 11 `authority-*.md` files (especially `authority-reservation.md`, `authority-execution.md`, `authority-cache.md`) are code-anchored audits of mutation points, reclamation logic, and violations. `authority-reservation.md` in particular provides an excellent map of ledger ownership and callers (directly relevant to future Execution Leases).

### 5. Strategic Self-Awareness in Key Roadmaps
`future-roadmap.md` (Path 8) explicitly rejects becoming a full orchestrator/scheduler unless the project is deliberately reframed, and states that the best future is as "the cluster truth and tool surface that other agents use, rather than the heavyweight runtime."

`sovereign-grid-architecture.md` is the clearest origin of high-leverage future ideas (Constellations, Universal MCP Context Provider with execution leases, immune system) while repeatedly stressing that Axis must remain a fast, stateless Go binary.

### 6. Honest Treatment of Experimental/Scaffolded Surfaces
MCP is consistently and correctly described as read-only across multiple files. Cortex and reservation/ledger are properly qualified where maturity language appears. `future/consistency-model.md` and `future/reservation-doctor.md` are appropriately caveated as design documents/sketches.

### 7. Triangle Proposal (as of 2026-05-28) Is Well-Aligned
The version of `future/triangle-orchestration.md` at the time of this evaluation is one of the most doctrinally careful documents in the corpus. It:
- Stays strictly in the Advisory layer
- Uses the existing MCP server as primary interface
- Correctly qualifies Cortex (EXPERIMENTAL) and reservation ledger (scaffolded)
- Directly maps to sovereign-grid Phase 3 ideas
- Includes a substantial "Verification & Evidence Basis" section
- Explicitly references the risk of scope creep into heavyweight territory

---

## Risks, Inconsistencies, and Narrative Drift

### 1. Uneven Use of "Orchestration" Language
- Careful and qualified in: `doctrine.md`, `future-roadmap.md` (Path 8 rejection), `sovereign-grid-architecture.md`, and the current Triangle document.
- More structural/visionary in: `distributed-cognitive-architecture.md` ("Orchestration Bus (Thalamus)"), `substrate-roadmap.md` ("orchestration plane", "guarded orchestration execution"), and parts of `hybrid-ai-router-plan.md`.

This is the primary vector for future scope creep risk.

### 2. Visionary Documents Can Drift from Doctrine
`distributed-cognitive-architecture.md` (2026-05-13) frames Axis as evolving into a "Distributed Cognitive Architecture" / "brain-like intelligence system" with prefrontal cortex, thalamus, etc. This is inspirational but sits in tension with the single-binary, fact-plane-primary, minimalism rules in doctrine. Per `docs/README.md`, it is lower priority than live code and `current-state.md`.

### 3. Limited Cross-References for New Concepts
"Triangle", "Constellations", and "Execution Leases" appear almost exclusively in `future/triangle-orchestration.md` and `sovereign-grid-architecture.md`. Most other files (including current-state, lifecycle, the authority series, and most roadmaps) have no mention. This is normal for a new proposal but represents an integration gap.

### 4. Maturity Language Is Under-Used Outside lifecycle.md
The terms "scaffolded" and "experimental" appear in only 5 files in the entire docs tree (heavily concentrated in `lifecycle.md`). Many other documents describe surfaces without the precise qualifiers that `lifecycle.md` and `current-state.md` provide.

### 5. Older Specs and Roadmaps Have Aged
`phase1_spec.md`, `substrate-roadmap.md`, `white_paper_v1.md`, and parts of `hybrid-ai-router-plan.md` contain planning language and assumptions that have been overtaken by shipped reality (especially the rich empirical, resident-model, pressure, thermal, and guarded execution features now documented in `current-state.md`).

### 6. Hardware Asymmetry and Cluster Reality
Live `axis status` shows a highly heterogeneous environment (asymmetric RAM, mixed Apple Silicon + discrete NVIDIA, unreachable nodes, varying storage/thermal/battery states). This reality is present in `current-state.md` facts and `internal/facts/`, but is under-represented in higher-level architecture and roadmap documents outside of `GEMINI.md` (which is deliberately gitignored).

---

## Category-by-Category Evaluation

**Core Doctrine & Guardrails** (`doctrine.md`, `architecture.md`, `lifecycle.md`, `current-state.md`, `invariants.md`, `docs/README.md`): **Excellent**. These form the project's immune system. Highest fidelity to live reality.

**Authority Series** (11 files): **Very strong**. Deep, code-referenced, and directly useful for anyone working on execution, reservations, or future lease primitives. `authority-reservation.md` is particularly high value.

**Roadmaps & Future Planning** (`future-roadmap.md`, `sovereign-grid-architecture.md`, `roadmap-status.md`, `phase-tracking.md`, `substrate-roadmap.md`, `hybrid-ai-router-plan.md`, `distributed-cognitive-architecture.md`, `phase1_spec.md`): **Mixed but useful**. Strong strategic self-awareness in the top two. Visionary drift risk in the middle tier. Older specs are historical artifacts.

**Future/ Subdirectory** (`triangle-orchestration.md`, `consistency-model.md`, `reservation-doctor.md`): **Best-in-class for their age**. All three are properly caveated. The Triangle document (post 2026-05-28 verification) is a model of how future proposals should be written in this project.

**Operational & Current Surfaces** (`reservations.md`, `profiling.md`, `ram-balancing-research.md`, `agent-worklog.md`, `lifecycle.md` overlap): **Good**. `reservations.md` and `lifecycle.md` are particularly well-grounded.

**Decisions, Runbooks & Misc** (`decisions/`, `runbooks/`, `session-handoff.md`, `development-process.md`, `white_paper_v1.md`): **Variable**. Some are very short. `runbooks/mcp-network-tools.md` correctly emphasizes read-only nature. Decisions documents are narrowly scoped and accurate.

---

## Specific Findings Regarding Triangle

The Triangle proposal (as documented in `future/triangle-orchestration.md` after the 2026-05-28 verification pass) is **doctrinally sound and well-positioned** for this codebase.

It correctly:
- Treats Axis as the orchestration *substrate*, not the runtime.
- Routes all significant coordination through the existing (read-only) MCP server.
- Builds on the scaffolded reservation ledger rather than inventing new authority paths.
- Acknowledges Cortex as EXPERIMENTAL.
- Includes concrete evidence from live binary, source inspection, and remote GitHub.
- Flags its own primary risk (scope creep into heavyweight territory).

**Recommended next integration points** (for future work, not immediate action):
- Add a classification entry in `lifecycle.md` once any `triangle` command or package is introduced.
- Reference the concept (once prototyped) in `current-state.md`.
- Add a short pointer in `future-roadmap.md` under Advanced Orchestration / MCP-native surfaces.
- Consider a lightweight cross-reference in `sovereign-grid-architecture.md` and `authority-reservation.md` if/when Execution Leases move from sketch to implementation.

---

## Recommendations (Evaluation Only)

1. **Protect the core guardrails**. Any future work on multi-agent coordination (Triangle or otherwise) should be required to pass the decision rules in `doctrine.md` and the maturity gates in `lifecycle.md`.

2. **Improve cross-referencing for new high-utility ideas**. Once a concept proves itself (e.g., Triangle, execution leases, reservation doctor), add minimal pointers in `current-state.md`, `lifecycle.md`, and `future-roadmap.md`.

3. **Consider a lightweight "maturity note" convention** for documents that describe experimental or scaffolded surfaces.

4. **Periodically re-evaluate the docs corpus**. This evaluation (2026-05-28) should itself become stale. A lightweight annual or milestone-based docs audit (perhaps owned via `agent-worklog.md`) would be valuable.

5. **Keep visionary documents clearly labeled**. `distributed-cognitive-architecture.md` and similar pieces are useful as inspiration but should carry stronger disclaimers about their relationship to the doctrine and current-state truth hierarchy.

---

## Appendix: Complete File Inventory with Metadata (2026-05-28)

```
2026-05-28 01:13       20756 bytes  /home/cranium/axis/docs/future/triangle-orchestration.md
2026-05-25 14:49       26226 bytes  /home/cranium/axis/docs/current-state.md
2026-05-18 10:00        8927 bytes  /home/cranium/axis/docs/roadmap-status.md
2026-05-18 02:19        2693 bytes  /home/cranium/axis/docs/future/consistency-model.md
2026-05-18 02:19        2409 bytes  /home/cranium/axis/docs/future/reservation-doctor.md
2026-05-18 00:21        4981 bytes  /home/cranium/axis/docs/decisions/v2-reservations-endpoint.md
2026-05-18 00:21        4067 bytes  /home/cranium/axis/docs/decisions/dashboard-command.md
2026-05-18 00:19        7536 bytes  /home/cranium/axis/docs/reservations.md
2026-05-18 00:15        5044 bytes  /home/cranium/axis/docs/profiling.md
2026-05-18 00:15       10625 bytes  /home/cranium/axis/docs/authority-transition.md
2026-05-17 23:35       11065 bytes  /home/cranium/axis/docs/authority-violations.md
2026-05-17 23:32       10726 bytes  /home/cranium/axis/docs/authority-observability.md
2026-05-17 23:31       12469 bytes  /home/cranium/axis/docs/authority-cache.md
2026-05-17 23:28        6661 bytes  /home/cranium/axis/docs/authority-config.md
2026-05-17 23:28        5795 bytes  /home/cranium/axis/docs/authority-secrets.md
2026-05-17 23:26       11979 bytes  /home/cranium/axis/docs/lifecycle.md
2026-05-17 23:24        8284 bytes  /home/cranium/axis/docs/authority-execution.md
2026-05-17 23:24        5504 bytes  /home/cranium/axis/docs/authority-observations.md
2026-05-17 23:22        6070 bytes  /home/cranium/axis/docs/distributed-cognitive-architecture.md
2026-05-17 23:22       20230 bytes  /home/cranium/axis/docs/hybrid-ai-router-plan.md
2026-05-17 23:22        1344 bytes  /home/cranium/axis/docs/README.md
2026-05-17 23:21        7000 bytes  /home/cranium/axis/docs/architecture.md
2026-05-17 23:21        1173 bytes  /home/cranium/axis/docs/phase-tracking.md
2026-05-17 23:14        6690 bytes  /home/cranium/axis/docs/authority-freshness.md
2026-05-17 23:14        6458 bytes  /home/cranium/axis/docs/authority-identity.md
2026-05-17 23:12        9956 bytes  /home/cranium/axis/docs/authority-reservation.md
2026-05-13 16:57        8275 bytes  /home/cranium/axis/docs/sovereign-grid-architecture.md
2026-05-13 16:57        8134 bytes  /home/cranium/axis/docs/ram-balancing-research.md
2026-05-13 16:57         771 bytes  /home/cranium/axis/docs/runbooks/mcp-network-tools.md
2026-05-13 16:57         764 bytes  /home/cranium/axis/docs/development-process.md
2026-05-13 16:57         731 bytes  /home/cranium/axis/docs/invariants.md
2026-05-13 16:57        7289 bytes  /home/cranium/axis/docs/doctrine.md
2026-05-13 16:57         705 bytes  /home/cranium/axis/docs/runbooks/local-assist.md
2026-05-13 16:57        6945 bytes  /home/cranium/axis/docs/phase1_spec.md
2026-05-13 16:57        6723 bytes  /home/cranium/axis/docs/white_paper_v1.md
2026-05-13 16:57        4724 bytes  /home/cranium/axis/docs/session-handoff.md
2026-05-13 16:57       15897 bytes  /home/cranium/axis/docs/future-roadmap.md
2026-05-13 16:57       10628 bytes  /home/cranium/axis/docs/agent-worklog.md
2026-05-13 16:57       10612 bytes  /home/cranium/axis/docs/substrate-roadmap.md
```

**Total**: 39 files.

---

**End of Evaluation**

This document captures the state of the AXIS documentation corpus as observed on 2026-05-28. It is offered in service of the project's own values: explicitness, evidence over narrative, and continuous improvement of fact quality.

For the current authoritative view, start with:
- `docs/current-state.md`
- `docs/doctrine.md`
- `docs/lifecycle.md`
- `docs/architecture.md`
- Live `axis` commands and source code.