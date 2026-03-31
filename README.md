# AXIS

[![CI](https://github.com/toasterbook88/axis/actions/workflows/ci.yml/badge.svg)](https://github.com/toasterbook88/axis/actions/workflows/ci.yml)
[![Go version](https://img.shields.io/badge/go-1.26+-00ADD8?logo=go)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-green)](LICENSE)

**A snapshot-first Go CLI that discovers hardware facts across your cluster via SSH, builds a `ClusterSnapshot`, and makes deterministic reservation-aware placement decisions. Optional chat, HTTP, and MCP surfaces are subordinate to observed state, not sources of truth.**

## Quick Start

```bash
# Install
go install github.com/toasterbook88/axis/cmd/axis@v0.2.1

# Inspect the local machine
axis facts

# Inspect the full cluster (requires ~/.axis/nodes.yaml)
axis status

# Ask where to run a task
axis task place "run ollama inference on a 7b model"
```

## Truth Boundary

**No generated output may present itself as cluster truth unless it is backed by a real snapshot or live probe.**

- The stable operator path is `axis facts`, `axis status`, `axis task place`, `axis task context`, and the daemon cache commands.
- `axis chat` is experimental and non-authoritative.

## Command Surface

### Stable operator path

- `axis version` — print the current AXIS build version
- `axis facts` — local hardware/tool snapshot
- `axis status` / `axis status --cached` — live or daemon-cached cluster snapshot
- `axis task place` / `axis task place --cached` — advisory placement with reasoning
- `axis task context` / `axis task context --cached` — compact context block backed by live or cached snapshot data
- `axis daemon status` / `refresh` / `invalidate` / `restart` — inspect and manage the local snapshot cache seam
- `axis context show` / `clear` — inspect or reset persisted placement memory

### Optional or secondary surfaces

- `axis task run` — explicit execution surface layered on top of placement, with optional observed-state-gated Nix helper wrapping when the selected node proves it supports `nix`
- `axis serve` — optional local HTTP API surface
- `axis mcp serve` — optional read-only MCP server over stdio
- `axis scripts list` — list built-in helper scripts
- `axis skills` — show learned local skills/failures
- `axis chat` — experimental local chat assistant; not authoritative cluster truth
- `axis completion` — Cobra-generated shell completion

### Available Commands (Strict Config Enforced)

- `axis version`
- `axis facts` — local facts only
- `axis status` — full cluster snapshot (uses strict `nodes.yaml`)
- `axis task place <description>` — deterministic placement

Removed: `axis discover` (fully replaced by `axis status` + snapshot assembly).

## Execution Safety (hardened)

- CLI execution requires `axis task run --script ...` or `axis task run --exec ...`; HTTP `/run` and `axis_execute` require explicit `mode=script|exec` plus `confirm: "YES"`
- Hardened safety blocker with allow-list, cluster-aware RAM/GPU checks, and learned-bad fast path
- Per-node reservation caps enforced before any command runs (`RAMTotalMB - 1024` headroom)
- Live and cached read paths both overlay local reservations before placement, context generation, or execution
- `axis task run` / `/run` now export `AXIS_TURBOQUANT_*` env hints to executed commands when a TurboQuant-capable node is selected
- Corrupt local `~/.axis/state.json` / `~/.axis/skills.json` files are quarantined to `.corrupt-*` backups and surfaced as warnings instead of crashing read paths
- Reservations auto-clean on every daemon refresh (45 min TTL + legacy leak detection)
- All cached paths (`task place --cached`, `task context --cached`, `mcp serve --cached`) use the single `internal/daemon/client`
- Visible in `/snapshot/meta` as `reserved_mb`

## Degraded-State Behavior

| Local condition | CLI behavior | API/MCP behavior | File outcome |
| --- | --- | --- | --- |
| Corrupt `~/.axis/state.json` | `axis context show` warns on stderr and still prints valid JSON | live snapshot-bearing reads keep working; state warning appears in snapshot warnings or placement reasoning | original file is renamed to `state.json.corrupt-<UTCSTAMP>` and a clean in-memory state is used |
| Corrupt `~/.axis/skills.json` | `axis skills` warns on stderr and still prints valid JSON | live API/MCP reads keep working; skills warning appears in snapshot warnings or placement reasoning | original file is renamed to `skills.json.corrupt-<UTCSTAMP>` and a clean in-memory skills store is used |

These files are local operator memory, not authoritative cluster truth. AXIS now prefers `warn + quarantine + continue` over failing shut on read paths.

## Features

| Feature | Details |
| --- | --- |
| **Local fact collection** | OS, kernel, arch, CPU cores/model, RAM (total/free + load averages + pressure), disk, GPU list, network addresses, and additive memory-topology / pressure-source metadata where available |
| **Tool inventory** | `go`, `python3`, `git`, `docker`, `ollama`, `mlx_lm`, `llama-cli`, `llama-server`, `node`, `swift`, `cargo`, `gcc`, plus probe-verified local `apple-foundation-models` on eligible Apple Silicon hosts running macOS 26 or later |
| **SSH cluster sweep** | Concurrent fan-out over all configured nodes; per-node timeout |
| **ClusterSnapshot** | Structured JSON/YAML with per-node status (`complete` / `partial` / `unreachable` / `error`) and cluster-level aggregates |
| **Advisory task placement** | `axis task place` ranks nodes deterministically by pressure, GPU, effective headroom, unified-memory suitability for `mlx`/long-context asks, allocatable RAM, reservation ratio, and locality; heavy AI tasks also avoid nodes under critical runtime pressure signals |
| **Optional local control surfaces** | `axis serve`, `axis daemon invalidate`, `axis mcp serve`, `axis task run`, and `axis chat` are available when explicitly invoked |
| **Single-binary operation** | No required daemon, database, or background process; local server/MCP surfaces are opt-in |
| **Structured output** | `axis facts` and `axis status` support JSON/YAML; `axis task place` supports human output and JSON |

## Installation

**Using `go install` (recommended):**

```bash
go install github.com/toasterbook88/axis/cmd/axis@v0.2.1
```

`@latest` now resolves to the newest tagged release. For reproducible installs, pin an explicit tag such as `@v0.2.1`.

**Tagged release pipeline:**

- `v*` tags are published through GitHub Actions and GoReleaser
- Release artifacts are configured for `darwin`/`linux` on `amd64`/`arm64`
- The release workflow refuses to publish if the pushed tag and `internal/buildinfo/version.go` disagree
- The current release is [`v0.2.1`](https://github.com/toasterbook88/axis/releases/tag/v0.2.1)

**Security hygiene:**

- Weekly Dependabot updates cover Go modules and GitHub Actions
- `govulncheck` runs on pull requests, pushes to `main`, and a weekly schedule
- Private vulnerability reporting and automated security fixes are enabled on GitHub
- Security reporting guidance lives in [SECURITY.md](SECURITY.md)

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

Placement uses keyword matching against the task description (no ML). It infers the required tool (`ollama`, `git`, `go`, `docker`) and minimum free RAM from specific keywords (`model`, `7b`, `inference`, `heavy`, etc.), then scores each reachable node — tool presence is a hard requirement, and eligible nodes are ranked by pressure, GPU preference, effective headroom, unified-memory suitability for `mlx`/Apple Silicon-shaped asks, allocatable RAM, reservation ratio, and stable name ordering. Long-context hints such as `128k`, `book-length`, or `million-token` also trigger a TurboQuant-aware preference when a node exposes `mlx` or `llama.cpp`-style backends, with stronger RAM reduction and fit bonuses reserved for recognizable backend help/probe responses. For heavy inference tasks, AXIS now filters out nodes showing critical runtime pressure from Linux PSI or Darwin VM pressure signals instead of treating them as normal fallback candidates.

When `axis task run` selects a TurboQuant-capable node, AXIS exports `AXIS_TURBOQUANT`, `AXIS_TURBOQUANT_STATUS`, `AXIS_TURBOQUANT_BACKENDS`, `AXIS_TURBOQUANT_CAPABILITIES`, and long-context hints into the execution environment. For probe-verified `llama.cpp` commands with `--ctx-size` support, AXIS can also inject safe additive flags such as `--ctx-size` and `--flash-attn` when they are absent.

Experimental local Apple Foundation Models execution is available on eligible Apple Silicon machines running macOS 26 or later through the source-visible Swift helper in [hack/apple-foundation-models.swift](hack/apple-foundation-models.swift):

```bash
axis task run --exec 'xcrun swift hack/apple-foundation-models.swift --prompt "Summarize this text"'
```

AXIS treats `apple-foundation-models` as a local-only verified capability. Remote nodes are excluded for that backend instead of being treated as fallback candidates.

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

### `axis daemon status` — inspect local daemon freshness

```bash
axis daemon status
axis daemon restart
```

`axis daemon status` reports cache readiness, age, and version metadata. `axis daemon restart` restarts the local cache seam from the current binary when you need to refresh stale daemon state explicitly.

## Configuration Reference

`~/.axis/nodes.yaml` fields:

Unknown YAML keys are rejected at load time so config typos fail fast instead of being silently ignored.

| Field | Required | Default | Description |
| --- | --- | --- | --- |
| `name` | yes | — | Logical node name |
| `hostname` | yes | — | Resolvable hostname or IP |
| `ssh_user` | yes | — | SSH username |
| `role` | no | — | `primary` or `worker` |
| `ssh_port` | no | `22` | SSH port |
| `timeout_sec` | no | `10` | Per-node collection timeout (seconds) |

Optional discovery block used by experimental UDP-assisted discovery:

| Field | Required | Default | Description |
| --- | --- | --- | --- |
| `discovery.enabled` | no | `false` | Enable UDP beacon discovery alongside configured nodes |
| `discovery.udp_port` | no | `42424` | UDP beacon port |
| `discovery.beacon_interval_sec` | no | `3` | Beacon broadcast interval |
| `discovery.secret` | no | empty | Shared discovery secret for filtering beacons |

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
- Placement is deterministic: RAM pressure → GPU → effective headroom → allocatable RAM → reservation ratio → name
- ComputeFitScore factors in GPU (+25pts) and local-node bonus (+10pts) — M1↔M3 RAM sharing would be relevant here
- Chat hardcoded to localhost:11434 Ollama — no remote inference routing yet
- `axis serve` hosts an optional daemon-backed cache; `axis status --cached`, `axis task place --cached`, `axis task context --cached`, `axis daemon refresh`, and `axis daemon invalidate` use it explicitly
- `axis serve` and `axis mcp serve` are optional local surfaces, not required infrastructure
- `axis chat` is an experimental helper and must not outrank snapshot-backed truth
- Placement memory lives locally in `~/.axis/state.json`

**Current phase:** The observability and placement core is stable; execution and chat helpers remain subordinate surfaces that must not present model output as authoritative cluster truth.

See [Phase 1 Spec](docs/phase1_spec.md) and [White Paper](docs/white_paper_v1.md) for detailed design notes.

## Roadmap

The following are planned directions, not current functionality:

- Mesh networking / peer discovery beyond a static seed file
- Phase 4+ features — see [white paper](docs/white_paper_v1.md)

### What's new in Phase 3 (v0.3.x)

- **nodes.yaml hot-reload** — daemon detects config changes and re-discovers nodes without restart
- **Daemon refresh metrics** — `/health` reports `refresh_count`, `last_refresh_duration_ms`, `stale_nodes`
- **Graceful shutdown** — `axis serve` drains in-flight work before exit
- **`axis task context --format json`** — machine-readable context block with fit score, skills, and recent decisions
- **HMAC-SHA256 beacon auth** — UDP discovery signs beacons instead of transmitting the shared secret

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Keep PRs small and focused; open an issue before adding Phase 3+ features.

## License

MIT — see [LICENSE](LICENSE).
