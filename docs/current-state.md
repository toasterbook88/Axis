# AXIS Current State

Last reviewed: 2026-03-28 23:59 EDT
Branch: `main`
Reviewed base HEAD: `bfbbf3e74222879411c9d5decb8b16c43e2f9962` (`main`, plus current uncommitted TurboQuant-aware placement worktree)

This document is the fastest way to understand what AXIS actually is today.

When this file disagrees with older design docs, trust the live code first, then this file, then the older phase/spec material.

## Executive Summary

AXIS is no longer just a read-only Phase 1 fact collector.

The live repo currently contains:

- Cluster fact collection for local and remote nodes
- Cluster snapshot assembly and advisory placement
- A local chat surface backed by Ollama
- A local HTTP API with task execution
- A daemon-backed cached snapshot seam behind `axis serve`
- Explicit cached status reads via `axis status --cached`
- Explicit cached placement reads via `axis task place --cached`
- Explicit cached context reads via `axis task context --cached`
- Explicit cache refresh via `axis daemon refresh`
- Explicit cache invalidation via `axis daemon invalidate`
- A CLI task execution path (`axis task run`)
- A read-only MCP server for cluster diagnostics
- Persistent local state in `~/.axis/state.json` and `~/.axis/skills.json`
- Recoverable persistence for corrupt local state/skills files via quarantine + warning
- UDP beacon-based node discovery
- Per-run execution context files instead of shared temp-path injection
- Stateful placement ranking with reservation subtraction, GPU preference, and full multi-tool enforcement
- Live and cached read paths that both overlay reservations before placement/context generation
- Real load-average data in facts, snapshots, and execution context
- TurboQuant-aware backend grading (`mlx`, `llama.cpp`) with detected vs probe-verified states and long-context placement hints
- Real Git-aware task routing via tool inference, built-in scripts, and repo-analysis workflows

The core observation pipeline is reasonably clean. The execution and safety surfaces are where most of the risk now lives.

## Command Surface

Top-level commands currently registered in the binary:

| Command | Purpose | Notes |
| --- | --- | --- |
| `axis version` | Print version | Version is `0.1.0` |
| `axis facts` | Collect local facts | JSON/YAML output |
| `axis status` | Collect cluster snapshot | Live SSH by default; `--cached` uses the local daemon cache |
| `axis daemon invalidate` | Clear local daemon cache | Explicit operator-controlled cache invalidation |
| `axis task place` | Advisory placement | Human output/JSON; `--cached` uses the local daemon cache |
| `axis task context` | Emit compact context block | Helper for external agents; `--cached` uses the local daemon cache |
| `axis daemon refresh` | Refresh local daemon cache now | Explicit operator-controlled cache refresh |
| `axis task run` | Execute on selected node | Explicit execution path exists |
| `axis chat` | Local chat via Ollama | Also used as the default root action |
| `axis mcp serve` | Start read-only MCP server | `stdio` transport only |
| `axis serve` | Start local HTTP API | Includes execution surface |
| `axis discover` | UDP-assisted discovery flow | Behavior is broader than its help text suggests |
| `axis context show|clear` | Inspect or clear placement memory | Uses persisted state |
| `axis scripts list` | List built-in scripts | Registry includes destructive scripts |
| `axis skills` | Show learned skills | Uses persisted skill store |

## Package Map

| Package | Role | Current Maturity |
| --- | --- | --- |
| `cmd/axis` | CLI entrypoint and command wiring | Broad surface area, mixed behavior, low command-level coverage |
| `internal/config` | Load and validate `~/.axis/nodes.yaml` | Small and stable, but not strict against unknown YAML fields |
| `internal/facts` | Local/remote hardware + tool collection | Local RAM/disk parsing is less brittle now; remote collection is still round-trip heavy, but it now also annotates nodes with graded TurboQuant-capable backends, probe-derived verification, and backend capabilities |
| `internal/discovery` | Fan-out discovery and UDP beacons | Node ordering is now stabilized and baseline tests exist; UDP timing behavior still needs broader hardening |
| `internal/snapshot` | Build `ClusterSnapshot` | Best-tested package in the repo |
| `internal/daemon` | Background snapshot refresh and cache metadata | Small, explicit seam; now powers cached reads, invalidate, and reservation-aware snapshot views |
| `internal/models` | Shared types and locality helpers | Types are fine; locality matching now prefers real hostnames/addresses over logical names |
| `internal/placement` | Requirement inference, filter, rank, select | High unit coverage; reservations, GPU preference, multi-tool requirements, and TurboQuant-aware long-context hints are now live, with verified backends preferred over detected-only candidates |
| `internal/state` | Persist placement memory | Explicit acquire/release is now live and tested; broader balancing semantics still need refinement |
| `internal/knowledge` | Build execution context blob | Load-aware, nil-safe, and now heavily covered |
| `internal/scripts` | Built-in task scripts | Useful; `jq` prerequisites are now modeled explicitly, but broader shell assumptions are still under-modeled |
| Git-oriented execution surfaces | Repo analysis, status, and review helpers | Promising lane; useful already, but should become more explicit and first-class |
| `internal/skills` | Learned skills/failures | Persists state, now recovers from corrupt JSON, but semantic validation is still light |
| `internal/safety` | Execution blocker | Heuristic, but now well unit-tested |
| `internal/transport` | SSH execution layer | Respects OpenSSH-resolved identities and known_hosts paths; integration coverage still needs to grow, but baseline unit coverage is now solid |
| `internal/api` | Local HTTP API and execution surface | High-risk surface, now above the v1 coverage gate with injectable execution seams |
| `internal/mcp` | Read-only MCP surfaces | Diagnostic layer now shares the live runtime path and meets the v1 coverage gate |
| `internal/persist` | Corrupt-file recovery helpers | Small helper package used for quarantine + warning recovery |
| `internal/runtimectx` | Unified live runtime loader | Centralizes config + discovery + overlay + warning assembly for live reads |
| `internal/chat` | Ollama-backed chat | Moderately tested, utility-oriented |

## Verification Snapshot

Audit commands run against this repo state:

- `go test ./... -count=1` -> passes
- `go test -race ./... -count=1` -> passes
- `go build ./...` -> passes
- `go build -o /tmp/axis ./cmd/axis` -> passes
- `/tmp/axis status --cached --cache-addr 127.0.0.1:42433` -> returns wrapped snapshot with `source: "daemon-cache"`
- `/tmp/axis task place --cached --cache-addr 127.0.0.1:42437 "test inference"` -> returns placement output sourced from `daemon-cache`
- `/tmp/axis task context --cached --cache-addr 127.0.0.1:42438 "test inference"` -> returns prompt block with `Source: daemon-cache`
- `/tmp/axis daemon refresh --cache-addr 127.0.0.1:42437` -> forces a fresh cached snapshot immediately
- `/tmp/axis daemon invalidate --cache-addr 127.0.0.1:42434` -> returns `AXIS daemon cache invalidated`
- `go test ./... -cover` -> passes (total coverage: `47.2%`)

Coverage gaps called out by `go test ./... -cover`:

- v1 package gates now pass: `internal/knowledge` `90.9%`, `internal/api` `63.8%`, `internal/mcp` `51.2%`
- direct coverage is now also strong in `internal/persist` `100.0%` and `internal/runtimectx` `92.6%`
- remaining lower-coverage surfaces: `cmd/axis`, `internal/facts`

## Degraded-State Matrix

These are the operator-facing degraded-state contracts currently locked in by tests:

| Condition | CLI contract | API contract | MCP contract | Recovery action |
| --- | --- | --- | --- | --- |
| Corrupt `~/.axis/state.json` | `axis context show` warns on stderr and still emits valid JSON | live reads continue; state warning is included in snapshot warnings | `placement_decision` prepends a state warning to reasoning | file is quarantined to `.corrupt-*`; empty in-memory state becomes authoritative until next write |
| Corrupt `~/.axis/skills.json` | `axis skills` warns on stderr and still emits valid JSON | live reads continue; skills warning is included in snapshot warnings | `placement_decision` prepends a skills warning to reasoning | file is quarantined to `.corrupt-*`; empty in-memory skills store becomes authoritative until next write |

The key point is that local persistence is treated as recoverable operator memory, not hard cluster truth.

## Reality Check

Areas where the live repo has moved past the older docs/specs:

- The repo has a local HTTP server and execution surface
- The repo persists state and learned skills to disk
- The repo includes task execution, not just advisory placement
- The repo includes UDP discovery and MCP diagnostics
- The repo now includes an explicit daemon-backed snapshot cache, not just ad hoc live discovery
- Cached reads, cache refresh, and cache invalidation are explicit operator actions, not hidden behavior
- Placement now subtracts reserved RAM during ranking, prefers GPU nodes after pressure, and enforces full script toolchains
- Live status/placement/context reads now use the same reservation overlay model as cached reads
- Corrupt local state/skills files are now quarantined and surfaced as warnings instead of collapsing read surfaces
- Execution context now carries real load averages rather than placeholder zeros
- Long-context task hints can now prefer graded TurboQuant-capable backends without changing the default placement contract for ordinary tasks
- Git-aware workflows are already a meaningful part of AXIS behavior, not just incidental tool detection

That does not mean the execution model is fully hardened yet. It means the codebase should now be understood as a hybrid of observability, advisory placement, and early execution tooling.

## RAM Sharing Intent

The intended purpose of AXIS RAM balancing is cluster-level memory sharing.

AXIS should treat node RAM as a resource the cluster tries to balance across
machines, not just as a local anti-overcommit guard.

In practical terms:

- `RAMFreeMB` is the live observed memory on a node
- persisted reserved memory is a soft claim against that node's contribution to
  the shared cluster memory pool
- placement should use those soft claims to spread memory pressure across the
  cluster instead of repeatedly selecting the same node

## Current Weak Spots

- Placement state accounting now subtracts reserved RAM correctly and releases on completion, but the broader RAM-sharing model is still heuristic
- The current balancing model still lacks allocatable/system-reserve concepts, cluster skew reduction, PSI awareness, and reclaim behavior
- TurboQuant grading is still heuristic today; AXIS now distinguishes detected vs probe-verified backend responses, but it still does not verify kernel correctness or backend feature parity
- Execution confirmation and reservation caps are now explicit across CLI and HTTP, but the UX and error contracts still differ between surfaces
- Locality detection is stricter now, but still depends on hostname/interface inspection rather than explicit node identity
- UDP discovery still depends on a fixed accumulation window and needs broader runtime coverage beyond the new baseline tests
- Safety blocking is substring-based and can both over-block and under-block
- Built-in script prerequisites now model `jq` explicitly, but broader shell assumptions are still under-modeled
- Git-aware workflows exist, but there is no dedicated doctrine/runbook/test layer for “AXIS as a Git expert” yet
- degraded-state contracts are now stronger, but concurrency around simultaneous first-read / first-write recovery is still only indirectly exercised
- The daemon cache refresh loop is still timer-based; invalidation is now explicit, but freshness is not yet event-driven
- `axis status`, `axis task place`, and `axis task context` now overlay local reservation state on live reads, but cache provenance is still only explicit on cached-path output
- Most read surfaces still hit live discovery by default unless `--cached` is used explicitly

## Recommended Next Sequence

V1 hardening is now mostly about durability, not feature growth:

1. Keep this file, `README.md`, and CI coverage gates current as the orientation layer.
2. Keep degraded-state golden fixtures current as API/MCP/CLI response shapes evolve.
3. Push discovery beyond the fixed UDP accumulation window toward adaptive or event-driven freshness.
4. Refine reservation accounting into a clearer cluster RAM balancing model.
5. Add more SSH/integration coverage around the transport layer and end-to-end execution paths.
6. Treat advanced balancing, PSI/reclaim behavior, schema-versioned persistence, and event-driven cache refresh as post-v1 work, using [ram-balancing-research.md](ram-balancing-research.md) as the technical basis.
