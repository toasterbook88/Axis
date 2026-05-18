# Decision: Dashboard (`summary`) Command Fate

**Date:** 2026-05-17  
**Scope:** `cmd/axis/dashboard.go`, `cmd/axis/summary_test.go`, `cmd/axis/main.go`  
**Decision:** KEEP

## 1. Current State

The `axis summary` command renders a visual terminal dashboard of cluster health and resources. It displays:

- A framed header (`AXIS CLUSTER SUMMARY`)
- Version and cache-age/staleness indicator
- Aggregate node health counts (healthy, degraded, unreachable)
- A visual RAM usage bar with reserved-RAM annotation
- GPU availability count
- Mesh peer count (when applicable)
- Cluster warnings

The command uses the same snapshot-collection helper (`collectStatusSnapshot`) as `axis status`, supports `--cached`/`--cache-addr` flags, and pulls from the daemon cache or live discovery.

It is implemented in `cmd/axis/dashboard.go` (lines 37–76) and registered as a top-level CLI command in `cmd/axis/main.go`.

## 2. Arguments for Keeping

1. **Distinct operator view.** `axis status` prints a detailed per-node table (name, status, RAM free, pressure, tools, resident models). `axis summary` provides an *at-a-glance* aggregate view with a visual RAM bar and health summary. Operators use the two commands for different mental models: `status` for inspection, `summary` for a quick health pulse.
2. **Now tested.** E-5 added golden-file tests in `cmd/axis/summary_test.go` covering empty state, populated nodes, corrupt-state warnings, daemon-cache paths, live-snapshot paths, bar edge cases, and stale-cache indicators. The untested rationale for removal no longer applies.
3. **Low maintenance surface.** The rendering code is contained in a single file (~240 lines) and uses the shared `collectStatusSnapshot` helper. It does not duplicate discovery, snapshot assembly, or placement logic.
4. **No hidden authority.** The command is read-only and subordinate to the snapshot fact plane, satisfying the Truth Rule.

## 3. Arguments for Removing

1. **Overlap with `status`.** Both commands consume the same snapshot and present cluster health. An operator could derive the aggregate counts from `axis status --format json | jq`.
2. **UI maintenance burden.** The dashboard adds color-bar rendering, framed ASCII headers, and a second text-formatting path that must be kept consistent with UI conventions (e.g., new pressure states, new resource fields).
3. **Future drift risk.** If new resource dimensions (VRAM bar, disk bar, network bandwidth) are added, the dashboard may require proportional expansion.

## 4. Decision

**KEEP the `summary` command.**

## 5. Rationale

The operator-explainability benefit outweighs the maintenance cost. A visual dashboard communicates cluster health faster than a tabular dump, especially for operators who run `axis summary` as their first command of the day. The command is read-only, truth-backed, and now has test coverage. The overlap with `status` is *data* overlap, not *purpose* overlap—similar to `df` vs. `df -h` in Unix tooling.

Removing it would force operators to pipe `status --format json` through external tools to get the same cognitive speed, which weakens the CLI's explicitness goal.

## 6. Required Improvements

1. **Keep tests current.** Any change to `ClusterSummaryView` fields or `Render()` output must update the corresponding `.golden` files in `cmd/axis/testdata/`.
2. **Consider `--format json` parity.** If operators request programmatic access to the aggregate view, add a `--format json` flag so `summary` can emit `V2ClusterResponse`-shaped JSON instead of forcing them to query `/v2/cluster` directly.
3. **Split `reservationsCmd` out of `dashboard.go`.** The `reservations` CLI command is co-located in `dashboard.go` for historical reasons. It should move to its own file (e.g., `cmd/axis/reservations.go`) to clarify that it is an independent command surface.
4. **Surface ledger summary in dashboard.** When the snapshot overlay includes ledger-derived reserved RAM, the dashboard already shows it. Ensure this stays wired as the ledger becomes the canonical reservation authority.
