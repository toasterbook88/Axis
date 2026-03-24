# AXIS

[![CI](https://github.com/toasterbook88/axis/actions/workflows/ci.yml/badge.svg)](https://github.com/toasterbook88/axis/actions/workflows/ci.yml)
[![Go version](https://img.shields.io/badge/go-1.26+-00ADD8?logo=go)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-green)](LICENSE)

**A single-binary Go CLI that discovers hardware facts across your cluster via SSH, builds a `ClusterSnapshot`, provides deterministic advisory task placement, and exposes optional local control surfaces — no daemon-first architecture required.**

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

## What Works Today

- `axis facts` — local hardware/tool snapshot
- `axis status` — full cluster snapshot over SSH
- `axis status --cached` — explicit daemon-backed cached snapshot read
- `axis task place` — advisory placement with fit score and failure reasoning
- `axis task place --cached` — explicit daemon-backed cached placement read
- `axis task context --cached` — explicit daemon-backed cached context block
- `axis serve` — optional local HTTP API surface
- `axis daemon refresh` — force a fresh daemon snapshot now
- `axis daemon invalidate` — explicit local daemon cache invalidation
- `axis mcp serve` — optional read-only MCP server over stdio
- `axis task run` — explicit execution surface layered on top of placement

## Execution Safety (hardened)

- `/run` and `axis_execute` require explicit `mode=script` or `mode=exec` + `confirm: "YES"`
- Hardened safety blocker with allow-list, cluster-aware RAM/GPU checks, and learned-bad fast path
- Per-node reservation caps enforced before any command runs (`RAMTotalMB - 1024` headroom)
- Reservations auto-clean on every daemon refresh (45 min TTL + legacy leak detection)
- All cached paths (`task place --cached`, `task context --cached`, `mcp serve --cached`) use the single `internal/daemon/client`
- Visible in `/snapshot/meta` as `reserved_mb`

## Features

| Feature | Details |
| --- | --- |
| **Local fact collection** | OS, kernel, arch, CPU cores/model, RAM (total/free + pressure), disk, GPU list, network addresses |
| **Tool inventory** | `go`, `python3`, `git`, `docker`, `ollama`, `node`, `swift`, `cargo`, `gcc` |
| **SSH cluster sweep** | Concurrent fan-out over all configured nodes; per-node timeout |
| **ClusterSnapshot** | Structured JSON/YAML with per-node status (`complete` / `partial` / `unreachable` / `error`) and cluster-level aggregates |
| **Advisory task placement** | `axis task place` ranks nodes deterministically by pressure, GPU, effective headroom, free RAM, and locality; `--cached` uses the explicit daemon snapshot cache |
| **Optional local control surfaces** | `axis serve`, `axis daemon invalidate`, `axis mcp serve`, `axis task run`, and `axis chat` are available when explicitly invoked |
| **Single-binary operation** | No required daemon, database, or background process; local server/MCP surfaces are opt-in |
| **Structured output** | `axis facts` and `axis status` support JSON/YAML; `axis task place` supports human output and JSON |

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

**Requirements:** Go 1.26.1+, SSH key-based auth for remote nodes.

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
axis status --cached     # read explicit daemon cache instead of live SSH sweep
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
axis task place --cached "run ollama inference on a 7b model"
```

Placement uses keyword matching against the task description (no ML). It infers the required tool (`ollama`, `git`, `go`, `docker`) and minimum free RAM from specific keywords (`model`, `7b`, `inference`, `heavy`, etc.), then scores each reachable node — tool presence is a hard requirement; free RAM breaks ties.

With `--cached`, placement uses the explicit daemon snapshot cache instead of a fresh SSH sweep. JSON output includes a `source` wrapper so you can tell whether the decision came from `daemon-cache` or live fallback.

### `axis task context` — compact operator/agent prompt block

```bash
axis task context "test inference"
axis task context --cached "test inference"
```

`--cached` uses the explicit daemon snapshot cache and includes a `Source:` line in the rendered context block so you can tell where the prompt data came from.

### `axis serve` — optional local HTTP API

```bash
axis serve
```

Starts the local AXIS HTTP API and execution surface on `127.0.0.1:42425` by default, plus a background snapshot refresh loop that powers the explicit cached-read path.

### `axis daemon invalidate` — clear local daemon cache

```bash
axis daemon invalidate
axis daemon invalidate --cache-addr 127.0.0.1:42425
```

Clears the daemon-backed snapshot cache explicitly. This does not change the default `axis status` live path; it only affects cached reads and other daemon-backed surfaces.

### `axis daemon refresh` — force a fresh daemon snapshot now

```bash
axis daemon refresh
axis daemon refresh --cache-addr 127.0.0.1:42425
```

Forces the daemon to rebuild its cached snapshot immediately. This is the fastest way to ensure `axis status --cached` and `axis task place --cached` use fresh cluster state without waiting for the next background tick.

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
│ cmd/axis/           │ Cobra CLI entry — chat, facts, status, task, serve, context, scripts, skills   │
├─────────────────────┼─────────────────────────────────────────────────────────────────────────────────┤
│ internal/config/    │ Loads ~/.axis/nodes.yaml (node list, SSH user/port/timeout)                     │
├─────────────────────┼─────────────────────────────────────────────────────────────────────────────────┤
│ internal/facts/     │ SSH into each node, collects RAM/CPU/GPU/tools                                  │
├─────────────────────┼─────────────────────────────────────────────────────────────────────────────────┤
│ internal/placement/ │ Filter + rank nodes by free RAM, pressure, GPU, locality; ComputeFitScore 0–100 │
├─────────────────────┼─────────────────────────────────────────────────────────────────────────────────┤
│ internal/chat/      │ Streams via local Ollama (localhost:11434), graceful fallback message           │
├─────────────────────┼─────────────────────────────────────────────────────────────────────────────────┤
│ internal/snapshot/  │ Assembles `ClusterSnapshot` from `[]NodeFacts`                                  │
├─────────────────────┼─────────────────────────────────────────────────────────────────────────────────┤
│ internal/daemon/    │ Background snapshot refresh, in-memory cache, and explicit invalidation         │
├─────────────────────┼─────────────────────────────────────────────────────────────────────────────────┤
│ internal/state/     │ Persists local placement memory and execution state                              │
├─────────────────────┼─────────────────────────────────────────────────────────────────────────────────┤
│ internal/api/       │ Local HTTP API and execution surface                                             │
├─────────────────────┼─────────────────────────────────────────────────────────────────────────────────┤
│ internal/mcp/       │ Read-only MCP server over stdio                                                  │
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
- Placement is deterministic: RAM pressure → GPU → effective headroom → free RAM → name
- ComputeFitScore factors in GPU (+25pts) and local-node bonus (+10pts) — M1↔M3 RAM sharing would be relevant here
- Chat hardcoded to localhost:11434 Ollama — no remote inference routing yet
- `axis serve` hosts an optional daemon-backed cache; `axis status --cached`, `axis task place --cached`, `axis task context --cached`, `axis daemon refresh`, and `axis daemon invalidate` use it explicitly
- `axis serve` and `axis mcp serve` are optional local surfaces, not required infrastructure
- Placement memory lives locally in `~/.axis/state.json`

**Current phase:** The Phase 1 observability core is complete, and Phase 2 advisory placement is live on `main`. Optional local server, daemon-backed cache, MCP, chat, and explicit execution surfaces also exist, but the project is still not daemon-first.

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
