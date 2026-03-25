# AXIS Current State

Last reviewed: 2026-03-25 11:39 EDT
Branch: `main`
Reviewed base HEAD: `f1db2b9`

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
- UDP beacon-based node discovery
- Per-run execution context files instead of shared temp-path injection
- Stateful placement ranking with reservation subtraction, GPU preference, and full multi-tool enforcement
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
| `internal/facts` | Local/remote hardware + tool collection | Local RAM/disk parsing is less brittle now; remote collection is still round-trip heavy |
| `internal/discovery` | Fan-out discovery and UDP beacons | Node ordering is now stabilized and baseline tests exist; UDP timing behavior still needs broader hardening |
| `internal/snapshot` | Build `ClusterSnapshot` | Best-tested package in the repo |
| `internal/daemon` | Background snapshot refresh and cache metadata | Small, explicit seam; now powers cached reads and invalidate |
| `internal/models` | Shared types and locality helpers | Types are fine; locality rules are too permissive |
| `internal/placement` | Requirement inference, filter, rank, select | High unit coverage; reservations, GPU preference, and multi-tool requirements are now live, but balancing policy is still simple |
| `internal/state` | Persist placement memory | Explicit acquire/release is now live and tested; broader balancing semantics still need refinement |
| `internal/knowledge` | Build execution context blob | Thin wrapper, currently placeholder-heavy |
| `internal/scripts` | Built-in task scripts | Useful, but registry prerequisites are under-modeled |
| Git-oriented execution surfaces | Repo analysis, status, and review helpers | Promising lane; useful already, but should become more explicit and first-class |
| `internal/skills` | Learned skills/failures | Persists state, moderately covered, but semantic validation is still light |
| `internal/safety` | Execution blocker | Heuristic, but now well unit-tested |
| `internal/transport` | SSH execution layer | Respects OpenSSH-resolved identities and known_hosts paths; integration coverage still needs to grow, but baseline unit coverage is now solid |
| `internal/api` | Local HTTP API and execution surface | High-risk surface, low coverage |
| `internal/mcp` | Read-only MCP surfaces | Useful diagnostic layer, low coverage |
| `internal/chat` | Ollama-backed chat | Moderately tested, utility-oriented |

## Verification Snapshot

Audit commands run against this repo state:

- `go test ./...` -> passes
- `go build ./...` -> passes
- `go build -o /tmp/axis ./cmd/axis` -> passes
- `/tmp/axis status --cached --cache-addr 127.0.0.1:42433` -> returns wrapped snapshot with `source: "daemon-cache"`
- `/tmp/axis task place --cached --cache-addr 127.0.0.1:42437 "test inference"` -> returns placement output sourced from `daemon-cache`
- `/tmp/axis task context --cached --cache-addr 127.0.0.1:42438 "test inference"` -> returns prompt block with `Source: daemon-cache`
- `/tmp/axis daemon refresh --cache-addr 127.0.0.1:42437` -> forces a fresh cached snapshot immediately
- `/tmp/axis daemon invalidate --cache-addr 127.0.0.1:42434` -> returns `AXIS daemon cache invalidated`
- `go test ./... -cover` -> passes (total coverage: `37.8%`)

Coverage gaps called out by `go test ./... -cover`:

- `internal/knowledge`
- `internal/models`
- low-coverage runtime surfaces: `cmd/axis`, `internal/api`, `internal/facts`, `internal/mcp`

## Reality Check

Areas where the live repo has moved past the older docs/specs:

- The repo has a local HTTP server and execution surface
- The repo persists state and learned skills to disk
- The repo includes task execution, not just advisory placement
- The repo includes UDP discovery and MCP diagnostics
- The repo now includes an explicit daemon-backed snapshot cache, not just ad hoc live discovery
- Cached reads, cache refresh, and cache invalidation are explicit operator actions, not hidden behavior
- Placement now subtracts reserved RAM during ranking, prefers GPU nodes after pressure, and enforces full script toolchains
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
- Execution confirmation and reservation caps are now explicit across CLI and HTTP, but the UX and error contracts still differ between surfaces
- Locality detection can treat a node as local based on its logical name, not just its real hostname/address
- UDP discovery still depends on a fixed accumulation window and needs broader runtime coverage beyond the new baseline tests
- Safety blocking is substring-based and can both over-block and under-block
- Script prerequisites like `jq` and broader shell assumptions are still under-modeled even though multi-tool requirements are now enforced
- Git-aware workflows exist, but there is no dedicated doctrine/runbook/test layer for “AXIS as a Git expert” yet
- Persistence helpers do not consistently create parent directories or surface save/load corruption clearly
- The daemon cache refresh loop is still timer-based; invalidation is now explicit, but freshness is not yet event-driven
- `axis status`, `axis task place`, and `axis task context` now overlay local reservation state on live reads, but cache provenance is still only explicit on cached-path output
- Most read surfaces still hit live discovery by default unless `--cached` is used explicitly

## Recommended Next Sequence

Documentation and organization first:

1. Keep this file current as the repo-orientation entry point.
2. Align `README.md` and `docs/phase1_spec.md` with the command surface that actually exists.
3. Decide whether AXIS should be described as:
   - a read-only cluster observability tool, or
   - an execution-capable local orchestration tool with safety constraints.
4. Once the product description is stable, harden the implementation to match that description.

Engineering follow-up after doc alignment:

1. Decide which read surfaces should gain explicit cached modes next (MCP, HTTP context/placement helpers, or richer API reads).
2. Refine reservation accounting into a clearer cluster RAM balancing model.
3. Make all execution paths explicit and consistent.
4. Add integration tests around SSH, API execution, safety, discovery, and daemon freshness.
5. Use [ram-balancing-research.md](ram-balancing-research.md) as the technical basis for the next placement-intelligence phase.
