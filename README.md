# AXIS

[![CI](https://github.com/toasterbook88/axis/actions/workflows/ci.yml/badge.svg)](https://github.com/toasterbook88/axis/actions/workflows/ci.yml)
[![Go version](https://img.shields.io/badge/go-1.26+-00ADD8?logo=go)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-green)](LICENSE)

**A single-binary Go CLI that discovers hardware facts across your cluster via SSH, builds a `ClusterSnapshot`, and gives deterministic advisory task placement — no daemon, no server, no persistent state.**

## Quick Start

```bash
# Install
go install github.com/toasterbook88/axis/cmd/axis@latest

# Inspect the local machine
axis facts

# Inspect the full cluster (requires ~/.axis/nodes.yaml)
axis status

# Ask where to run a task
axis task place "run ollama inference on a 7b model"
```

## Features

| Feature | Details |
| --- | --- |
| **Local fact collection** | OS, kernel, arch, CPU cores/model, RAM (total/free + pressure), disk, GPU list, network addresses |
| **Tool inventory** | `go`, `python3`, `git`, `docker`, `ollama`, `node`, `swift`, `cargo`, `gcc` |
| **SSH cluster sweep** | Concurrent fan-out over all configured nodes; per-node timeout |
| **ClusterSnapshot** | Structured JSON/YAML with per-node status (`complete` / `partial` / `unreachable` / `error`) and cluster-level aggregates |
| **Advisory task placement** | `axis task place` scores nodes by free RAM and tool availability; deterministic, no ML |
| **Zero runtime deps** | Single static binary; no daemon, no database, no background process |
| **JSON & YAML output** | All commands support `--format yaml` |

## Installation

**Using `go install` (recommended):**

```bash
go install github.com/toasterbook88/axis/cmd/axis@latest
```

**Build from source:**

```bash
git clone https://github.com/toasterbook88/axis.git
cd axis
go build -o axis ./cmd/axis/
# Optional: move to $PATH
mv axis /usr/local/bin/axis
```

**Requirements:** Go 1.22+, SSH key-based auth for remote nodes.

## Usage

### `axis facts` — local machine snapshot

```bash
axis facts               # JSON (default)
axis facts --format yaml # YAML
```

### `axis status` — cluster snapshot

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
    # ssh_port: 22
    # timeout_sec: 10
```

Then:

```bash
axis status              # JSON cluster snapshot
axis status --format yaml
```

### `axis task place` — advisory placement

```bash
axis task place "analyze a git repo"
# → Selected node: node-b (remote, fit 82/100)
#   Tool: git
#   Reason:
#     - has required tool: git
#     - free RAM: 14336 MB

axis task place "run ollama inference on a 7b model" --format json
```

Placement uses keyword matching against the task description (no ML). It infers the required tool (`ollama`, `git`, `go`, `docker`) and minimum free RAM from specific keywords (`model`, `7b`, `inference`, `heavy`, etc.), then scores each reachable node — tool presence is a hard requirement; free RAM breaks ties.

## Configuration Reference

`~/.axis/nodes.yaml` fields:

| Field | Required | Default | Description |
| --- | --- | --- | --- |
| `name` | yes | — | Logical node name |
| `hostname` | yes | — | Resolvable hostname or IP |
| `ssh_user` | yes | — | SSH username |
| `role` | no | — | `primary` or `worker` |
| `ssh_port` | no | `22` | SSH port |
| `timeout_sec` | no | `10` | Per-node collection timeout (seconds) |

## Architecture

```text
┌─────────────────────┬─────────────────────────────────────────────────────────────────────────────────┐
│       Package       │                                      Role                                       │
├─────────────────────┼─────────────────────────────────────────────────────────────────────────────────┤
│ cmd/axis/           │ Cobra CLI entry — chat, facts, status, task, exit codes                         │
├─────────────────────┼─────────────────────────────────────────────────────────────────────────────────┤
│ internal/config/    │ Loads ~/.axis/nodes.yaml (node list, SSH user/port/timeout)                     │
├─────────────────────┼─────────────────────────────────────────────────────────────────────────────────┤
│ internal/facts/     │ SSH into each node, collects RAM/CPU/GPU/tools                                  │
├─────────────────────┼─────────────────────────────────────────────────────────────────────────────────┤
│ internal/placement/ │ Filter + rank nodes by free RAM, pressure, GPU, locality; ComputeFitScore 0–100 │
├─────────────────────┼─────────────────────────────────────────────────────────────────────────────────┤
│ internal/chat/      │ Streams via local Ollama (localhost:11434), graceful fallback message           │
├─────────────────────┼─────────────────────────────────────────────────────────────────────────────────┤
│ internal/snapshot/  │ Persists node facts to disk                                                     │
├─────────────────────┼─────────────────────────────────────────────────────────────────────────────────┤
│ internal/transport/ │ Raw SSH execution layer                                                         │
├─────────────────────┼─────────────────────────────────────────────────────────────────────────────────┤
│ internal/discovery/ │ Node discovery                                                                  │
├─────────────────────┼─────────────────────────────────────────────────────────────────────────────────┤
│ internal/models/    │ Shared types: NodeFacts, TaskRequirements, Locality                             │
└─────────────────────┴─────────────────────────────────────────────────────────────────────────────────┘
```

### Key design notes

- Config lives at `~/.axis/nodes.yaml` — no cluster IPs hardcoded in code
- Placement is deterministic: RAM pressure → free RAM → name (stable tiebreak)
- ComputeFitScore factors in GPU (+25pts) and local-node bonus (+10pts) — M1↔M3 RAM sharing would be relevant here
- Chat hardcoded to localhost:11434 Ollama — no remote inference routing yet
- No Cortex/MCP integration — fully standalone from the rest of the AXIS stack

**Phase boundary:** This is Phase 1 — facts collection and advisory placement only. No task execution, no remote dispatch.

See [Phase 1 Spec](docs/phase1_spec.md) and [White Paper](docs/white_paper_v1.md) for detailed design notes.

## Roadmap

The following are planned directions, not current functionality:

- Background coordinator (`axisd`)
- Mesh networking / peer discovery beyond a static seed file
- Phase 2+ features — see [white paper](docs/white_paper_v1.md)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Keep PRs small and focused; open an issue before adding Phase 2+ features.

## License

MIT — see [LICENSE](LICENSE).
