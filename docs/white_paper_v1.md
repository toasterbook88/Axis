# AXIS — White Paper v1

## The Problem

Running AI workloads across multiple machines creates a recurring overhead: every
tool, model, and script that needs to dispatch work must first re-discover the
available compute. Which nodes are up? How much RAM is free? Is `ollama` installed?
Which machine has a GPU?

This context is collected redundantly, often in ad-hoc ways, and discarded after
each session. When a frontier model is asked to help orchestrate work across a
cluster, it typically has no stable, structured view of what it is working with.

## What AXIS Is

AXIS is a lightweight, local-first coordination substrate for cluster-aware AI
execution.

It collects hardware facts and tool inventories from a configured set of nodes,
assembles them into a compact `ClusterSnapshot`, and emits the result as structured
JSON or YAML. The snapshot gives models and operators a single, accurate document
describing available compute — without requiring a daemon-first control plane.
Live `main` also includes advisory placement, an optional local HTTP API, a
read-only MCP server, and explicit execution/helper surfaces layered on top of
that fact plane.

AXIS is intentionally minimal:

- Single binary, no installation beyond `go build`
- No required daemon or background process
- No proprietary wire protocol — SSH for transport, YAML for config, JSON for output
- Dependency surface is small by design: `cobra`, `golang.org/x/crypto`, `gopkg.in/yaml.v3`

## Architecture

```text
axis facts          (local)
axis status         (fan-out over SSH)
       │
       ▼
  config ──► discovery ──► facts collector ──► snapshot builder
  (YAML)      (fan-out)    (local + SSH)        (aggregation)
                                │
                          NodeFacts per node
                                │
                          ClusterSnapshot
                          (JSON / YAML out)
```

### Core Packages

| Package | Responsibility |
| --- | --- |
| `internal/config` | Load and validate `~/.axis/nodes.yaml` |
| `internal/transport` | SSH session management |
| `internal/facts` | Local and remote collectors, tool discovery |
| `internal/discovery` | Concurrent fan-out across configured nodes |
| `internal/snapshot` | Assemble `ClusterSnapshot` from `[]NodeFacts` |
| `internal/placement` | Deterministic task placement with LLM fit scoring |
| `internal/models` | Shared data types; no external dependencies |

### Data Flow

1. `config.Load` reads `~/.axis/nodes.yaml` and validates required fields.
2. `discovery.Discover` fans out over all nodes concurrently.
3. For each node, a `RemoteCollector` opens an SSH session and runs platform
   probes, producing a `NodeFacts` struct.
4. `snapshot.Build` aggregates the results: computes totals, classifies cluster
   health, and generates per-node warnings.
5. The CLI marshals the result to JSON or YAML and writes it to stdout.

## Current Status

The Phase 1 observability layer is live and forms the core of the project. It
answers the question: *what compute do I have right now?*

What it produces:

- Per-node: OS, arch, CPU, RAM (with pressure), disk, GPUs, network addresses,
  installed tool inventory
- Cluster-wide: reachable node count, total/free RAM, degraded status, warnings

This is the data foundation for later phases.

## Phase 2: Advisory Task Placement (Active)

**Phase 2** uses the `ClusterSnapshot` as input to a deterministic placement layer.
Given a task description and a snapshot, AXIS selects the most appropriate node.

Implemented features:

- `axis task place "<description>"` — advisory-only placement command
- LLM Fit score (0-100): RAM, pressure, GPU, CPU, local-node bonus
- Keyword inference: extracts tool and RAM requirements from description
- Failure diagnostics: per-node exclusion reasons (RAM gap, missing tool, status)
- Runner-up comparison with fit score delta
- Scope-aware: tasks too large for the cluster fail gracefully
- Optional local HTTP API via `axis serve`
- Optional read-only MCP surface via `axis mcp serve`

Longer-term directions under consideration:

- A lightweight daemon (`axisd`) for periodic background collection
- Structured context export suitable for injection into LLM system prompts
- Multi-hop or mesh node discovery (beyond a static YAML seed file)
- More fully hardened execution and automation surfaces

## Design Principles

**Local-first.** The binary runs on demand. There is no server to keep alive and
no state to manage. Any machine with the binary and SSH access can collect a
snapshot.

**Accuracy over completeness.** A node that returns partial facts gets
`status: partial` rather than being omitted or promoted to `complete`. The
snapshot reflects reality, including uncertainty.

**Minimal dependencies.** Each dependency is a liability. AXIS avoids pulling in
monitoring frameworks, ORMs, or large CLI toolkits. Standard library and three
focused packages is the current budget.

**Cluster-topology neutral.** The config schema and data model avoid hardcoding
assumptions about interface names, provider-specific metadata, or private
naming conventions. A two-node home lab and a ten-node cloud cluster should both
be first-class use cases.

## License

MIT
