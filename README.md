# AXIS

**A lightweight, local-first coordination substrate for cluster-aware AI execution.**

AXIS collects hardware facts and tool inventories from a configured set of nodes,
assembles them into a compact `ClusterSnapshot`, and emits the result as structured
JSON or YAML. The snapshot gives models and operators a single, accurate document
describing available compute — without requiring a daemon, server, or persistent state.

## Why AXIS?

When running AI workloads across multiple machines, tools and models repeatedly
re-discover the same facts: available RAM, installed runtimes, reachable nodes.
AXIS collects this information once and emits a single structured document,
reducing redundant probing and providing a stable context for task placement decisions.

AXIS is local-first — no daemon, no server, no persistent state.
It runs as a single binary on demand.

## Current Status

**Phase 1 — CLI bootstrap & node discovery.**

Phase 1 delivers the CLI binary, local fact collection, SSH-based remote collection,
and `ClusterSnapshot` assembly. It is the observability foundation that later phases
will build on.

## What Works Today

- `axis version` — print version string
- `axis facts` — collect hardware facts and tool inventory from the local machine; output as JSON or YAML
- `axis status` — SSH into each configured node, collect facts, assemble a `ClusterSnapshot`

**Collected per node:**
- OS (darwin / linux), kernel version, architecture
- CPU core count and model
- Total and free RAM (MB), with pressure classification (`none` / `low` / `medium` / `high`)
- Total and free disk space (GB)
- GPU list (best-effort)
- Network addresses (IPv4/IPv6, non-loopback)
- Installed tool inventory: `go`, `python3`, `git`, `docker`, `ollama`, `node`, `swift`, `cargo`, `gcc`

**`ClusterSnapshot` output includes:**
- Per-node `NodeFacts` with status (`complete`, `partial`, `unreachable`, `error`)
- Cluster-level summary: total/reachable node count, total/free RAM
- Per-node warnings: unreachable, partial, error, RAM pressure

## Not Yet Implemented

The following are project direction — not current functionality:
- Daemon / background coordinator (`axisd`)
- RAM-aware automatic task placement
- Mesh networking or peer discovery beyond a static seed file
- Phase 2+ features (see [white paper](docs/white_paper_v1.md))

## Requirements

- Go 1.22+
- SSH key-based authentication configured for remote nodes (for `axis status`)

## Installation

```bash
git clone https://github.com/toasterbook88/axis.git
cd axis
go build -o axis ./cmd/axis/
```

Move the binary to your `$PATH` if needed:

```bash
mv axis /usr/local/bin/axis
```

## Quick Start

### Local facts

```bash
# JSON output (default)
axis facts

# YAML output
axis facts --format yaml
```

### Cluster snapshot

Create `~/.axis/nodes.yaml` (see [nodes.example.yaml](nodes.example.yaml)):

```yaml
nodes:
  - name: node-a
    hostname: node-a.local
    ssh_user: alice
    role: primary

  - name: node-b
    hostname: node-b.local
    ssh_user: alice
    role: worker
    # ssh_port: 22      # default
    # timeout_sec: 10   # default
```

Then:

```bash
# JSON output (default)
axis status

# YAML output
axis status --format yaml
```

## Configuration Reference

`~/.axis/nodes.yaml` fields:

| Field | Required | Default | Description |
|---|---|---|---|
| `name` | yes | — | Logical node name |
| `hostname` | yes | — | Resolvable hostname or IP |
| `ssh_user` | yes | — | SSH username |
| `role` | no | — | `primary` or `worker` |
| `ssh_port` | no | `22` | SSH port |
| `timeout_sec` | no | `10` | Per-node collection timeout (seconds) |

## Repository Layout

```
axis/
├── cmd/axis/          # CLI entry point (main, facts, status commands)
├── internal/
│   ├── config/        # Config file parsing and validation
│   ├── discovery/     # Node discovery and concurrent fan-out
│   ├── facts/         # Local and remote fact collectors, tool discovery
│   ├── models/        # Core data types (NodeFacts, ClusterSnapshot, etc.)
│   ├── snapshot/      # ClusterSnapshot assembly and aggregation
│   └── transport/     # SSH transport layer
├── docs/              # Architecture documentation
├── nodes.example.yaml # Example cluster seed config
├── go.mod
└── README.md
```

## Architecture

AXIS is organized as a thin CLI layer over a set of pure internal packages:

1. **config** — loads and validates `~/.axis/nodes.yaml`
2. **transport** — establishes SSH connections and runs remote commands
3. **facts** — local (`LocalCollector`) and remote (`RemoteCollector`) fact collection; tool discovery
4. **discovery** — fans out collection across all configured nodes concurrently
5. **snapshot** — assembles per-node `NodeFacts` into a `ClusterSnapshot` with aggregates and warnings
6. **models** — shared data types; no external dependencies

See [Phase 1 Spec](docs/phase1_spec.md) and [White Paper](docs/white_paper_v1.md) for detailed design notes.

## License

MIT — see [LICENSE](LICENSE).
