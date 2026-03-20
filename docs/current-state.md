# AXIS Current State

Last reviewed: 2026-03-20 00:34 EDT
Branch: `main`
Reviewed HEAD: `2bca37a`

This document is the fastest way to understand what AXIS actually is today.

When this file disagrees with older design docs, trust the live code first, then this file, then the older phase/spec material.

## Executive Summary

AXIS is no longer just a read-only Phase 1 fact collector.

The live repo currently contains:

- Cluster fact collection for local and remote nodes
- Cluster snapshot assembly and advisory placement
- A local chat surface backed by Ollama
- A local HTTP API with task execution
- A CLI task execution path (`axis task run`)
- A read-only MCP server for cluster diagnostics
- Persistent local state in `~/.axis/state.json` and `~/.axis/skills.json`
- UDP beacon-based node discovery
- Per-run execution context files instead of shared temp-path injection
- Stateful placement ranking with reservation subtraction, GPU preference, and full multi-tool enforcement

The core observation pipeline is reasonably clean. The execution and safety surfaces are where most of the risk now lives.

## Command Surface

Top-level commands currently registered in the binary:

| Command | Purpose | Notes |
| --- | --- | --- |
| `axis version` | Print version | Version is `0.1.0` |
| `axis facts` | Collect local facts | JSON/YAML output |
| `axis status` | Collect cluster snapshot | Uses configured nodes |
| `axis task place` | Advisory placement | Human output or JSON |
| `axis task context` | Emit compact context block | Helper for external agents |
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
| `internal/facts` | Local/remote hardware + tool collection | Parsing is decent; remote collection is round-trip heavy |
| `internal/discovery` | Fan-out discovery and UDP beacons | Works, but has ordering/timing issues and no tests |
| `internal/snapshot` | Build `ClusterSnapshot` | Best-tested package in the repo |
| `internal/models` | Shared types and locality helpers | Types are fine; locality rules are too permissive |
| `internal/placement` | Requirement inference, filter, rank, select | High unit coverage; reservations, GPU preference, and multi-tool requirements are now live, but balancing policy is still simple |
| `internal/state` | Persist placement memory | Explicit acquire/release is now live and tested; broader balancing semantics still need refinement |
| `internal/knowledge` | Build execution context blob | Thin wrapper, currently placeholder-heavy |
| `internal/scripts` | Built-in task scripts | Useful, but registry prerequisites are under-modeled |
| `internal/skills` | Learned skills/failures | Persists state, lightly validated, no tests |
| `internal/safety` | Execution blocker | Heuristic and brittle, no tests |
| `internal/transport` | SSH execution layer | Critical runtime package with no tests |
| `internal/api` | Local HTTP API and execution surface | High-risk surface, low coverage |
| `internal/mcp` | Read-only MCP surfaces | Useful diagnostic layer, no tests |
| `internal/chat` | Ollama-backed chat | Moderately tested, utility-oriented |

## Verification Snapshot

Audit commands run against this repo state:

- `go vet ./...` -> passes
- `go test ./...` -> passes
- `go test -race ./internal/state ./internal/placement` -> passes
- `go run ./cmd/axis task run "nuke-caches"` -> safe suggestion path shows `docker` + `go` requirements and does not execute implicitly
- `go test ./... -cover` -> still shows several runtime-critical packages at `0.0%` coverage

Coverage gaps called out by `go test ./... -cover`:

- `internal/discovery`
- `internal/knowledge`
- `internal/mcp`
- `internal/safety`
- `internal/skills`
- `internal/transport`

## Reality Check

Areas where the live repo has moved past the older docs/specs:

- The repo has a local HTTP server and execution surface
- The repo persists state and learned skills to disk
- The repo includes task execution, not just advisory placement
- The repo includes UDP discovery and MCP diagnostics
- Placement now subtracts reserved RAM during ranking, prefers GPU nodes after pressure, and enforces full script toolchains

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
- Execution confirmation is inconsistent across surfaces: the CLI is stricter than the HTTP API
- Locality detection can treat a node as local based on its logical name, not just its real hostname/address
- Discovery order is not stabilized before later logic uses the node list
- Safety blocking is substring-based and can both over-block and under-block
- Script prerequisites like `jq` and broader shell assumptions are still under-modeled even though multi-tool requirements are now enforced
- Persistence helpers do not consistently create parent directories or surface save/load corruption clearly

## Recommended Next Sequence

Documentation and organization first:

1. Keep this file current as the repo-orientation entry point.
2. Align `README.md` and `docs/phase1_spec.md` with the command surface that actually exists.
3. Decide whether AXIS should be described as:
   - a read-only cluster observability tool, or
   - an execution-capable local orchestration tool with safety constraints.
4. Once the product description is stable, harden the implementation to match that description.

Engineering follow-up after doc alignment:

1. Refine reservation accounting into a clearer cluster RAM balancing model.
2. Make all execution paths explicit and consistent.
3. Add integration tests around SSH, API execution, safety, and discovery.
4. Use [ram-balancing-research.md](ram-balancing-research.md) as the technical basis for the next placement-intelligence phase.
