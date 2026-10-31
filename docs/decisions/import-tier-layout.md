# Decision: Import Tier Layout Is Enforced, and Matches the Code

**Date:** 2026-10-30
**Scope:** `AGENTS.md` §4, `docs/architecture.md`, `hack/verify-import-tiers.go`, `hack/import-tiers-allowlist.txt`
**Decision:** ENFORCE the `AGENTS.md` §4 tier layout on every `internal/` import, and MOVE three packages so the layout describes the code: `internal/knowledge` to Layer 5, `internal/reservation` and `internal/skills` to Layer 2.
**Evidence:** `docs/superpowers/specs/2026-10-30-import-direction-check-design.md` (Problem, "Layout or code?", Review)
**Related:** `docs/decisions/placement-selection-contract.md` (reservation-aware ranking), `docs/architecture.md`

---

## 1. Context

`AGENTS.md` §4 says higher tiers consume lower-tier authority, but nothing checked it. On `main`
at 1493da2, 10 of 153 internal import edges went upward. Seven of them came from the layout, not
the code: `reservation` imports only Support packages, `skills` imports only `persist`, and
`knowledge` is imported only from Layer 5 and `cmd/axis`.

## 2. Options

| Option | Seed exceptions | Effect |
| --- | --- | --- |
| A. Keep the layout, allowlist all 10 | 10 | The diagram stays wrong; the gate freezes the drift |
| B. `knowledge` to Layer 5; `reservation`, `skills` to Layer 2 | 3 | The diagram matches the code; the ledger stays above the fact plane |
| C. As B, but `reservation`, `skills` to Support | 2 | Treats the reservation ledger as plumbing, which understates its authority |

## 3. Decision

**B**, chosen by the maintainer on 2026-10-30. `hack/verify-import-tiers.go` reads the layout from
`AGENTS.md` itself and fails CI on any upward import not listed, with a rationale, in
`hack/import-tiers-allowlist.txt`. Three exceptions remain, each waiting on its own spec:
`events -> cortex`, `events -> reservation`, `daemon -> execution`.

## 4. Consequences

- Moving a package between tiers is a change to this decision: edit `AGENTS.md` §4 and record why
  here or in a new decision record.
- The allowlist should shrink. A stale entry fails the gate, so an exception cannot outlive its reason.
- `docs/architecture.md` keeps its own copy of the layout and still differs from `AGENTS.md` for
  `runtimectx`, `publication` and `llmrouter`. Replacing that copy with a pointer is a follow-up.
