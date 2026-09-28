<p align="center">
  <img src="docs/assets/axis-logo-web.jpg" alt="AXIS" width="420" />
</p>

<p align="center">
  <strong>Local-first, reservation-aware cluster substrate</strong><br/>
  Deterministic placement · Guarded AI execution · Observed-state truth
</p>

<p align="center">
  <a href="https://github.com/toasterbook88/axis/releases/tag/v0.19.4"><img src="https://img.shields.io/github/v/release/toasterbook88/axis?include_prereleases&label=release" alt="Latest release" /></a>
  <a href="https://github.com/toasterbook88/axis/actions/workflows/ci.yml"><img src="https://github.com/toasterbook88/axis/actions/workflows/ci.yml/badge.svg" alt="CI" /></a>
  <a href="go.mod"><img src="https://img.shields.io/badge/go-1.26+-00ADD8?logo=go" alt="Go version" /></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-green" alt="License: MIT" /></a>
</p>

---

## What

AXIS discovers hardware across machines you already own (SSH + optional UDP beacons), builds deterministic cluster snapshots, and makes **reservation-aware placement decisions**. Optional surfaces add guarded task execution, resident model lifecycle, a local HTTP API (Unix socket by default), MCP, and an A2A agent-card / task plane.

> **Truth boundary:** No generated output may present itself as cluster truth unless it is backed by a real snapshot or live probe.

## Why

Most “AI cluster” tooling either trusts a cloud control plane or lets models invent topology. AXIS keeps **observed facts** at the bottom of the stack and treats agents, MCP, and chat as advisory layers that cannot override the fact plane.

| You need | AXIS does |
| --- | --- |
| Honest placement | Filter → Rank → Select on a real `ClusterSnapshot` |
| Shared RAM accounting | Soft reservations overlaid on live and cached reads |
| Safe execution | Safety gates + reserve → run → release |
| Local operation | Default API on `~/.axis/axis.sock`; state under `~/.axis/` |

## Features

**Core**

- SSH fact collection and cluster snapshot assembly (live or daemon-cached)
- Deterministic placement with honest ranking objective (`allocatable_ram`); FitScore is diagnostic suitability only
- Reservation ledger and reservation-aware status / place / context reads
- Guarded execution (`axis task run`, HTTP `/run`) with structured NDJSON streaming
- Optional daemon cache with explicit refresh / invalidate and native launchd / systemd user service install

**Models & agents (v0.19.x)**

- Resident model lifecycle: `axis model plan | start | await | query | stop | list`
- Interactive TUI dashboard (`axis` / `axis tui`) with advisory placement preview
- Tool-calling `axis agent` (console default on TTY; cache-first cluster context)
- Read-oriented MCP server (`axis mcp serve`) plus unified MCP client

**A2A (landed in 0.19.4)**

- Per-node agent card at `/.well-known/agent-card.json` (skills from observe / edit / exec scope) — [#464](https://github.com/toasterbook88/Axis/pull/464)
- Authenticated observe task send/get (`POST /a2a/v1/message:send`, `GET /a2a/v1/tasks/{id}`) — [#465](https://github.com/toasterbook88/Axis/pull/465)
- Approval queue for exec-shaped work (`approve` / `reject`; no auto-approve) — [#466](https://github.com/toasterbook88/Axis/pull/466)
- Delegate CLI: `axis task delegate | status | approve | reject` — [#468](https://github.com/toasterbook88/Axis/pull/468)

Public agent cards advertise **`Streaming: false`** (sync send/get only; no subscribe/SSE). This is not an A2A TCK claim.

## Quick start

```bash
# Install
curl -fsSL https://raw.githubusercontent.com/toasterbook88/axis/main/install.sh | bash
# Or: go install github.com/toasterbook88/axis/cmd/axis@v0.19.4

axis init                 # interactive nodes.yaml wizard
axis node facts           # local hardware / tools
axis cluster status       # live cluster snapshot
axis task place "run ollama inference on a 7b model"
axis placement explain "run ollama inference on a 7b model"
axis doctor
```

Common follow-ons:

```bash
axis daemon start         # local API + background cache (Unix socket default)
axis model plan qwen3.8-27b
axis model list
axis serve                # same daemon/API entry used by HTTP + A2A surfaces
```

Fleet rollouts and mesh networking are **operator-driven** — AXIS does not auto-provision or auto-join a fleet.

## Architecture

Five layers; each is subordinate to the one below. Advisory surfaces never override observed state.

```text
┌─────────────────────────────────────────────────────────────┐
│  Layer 5  Advisory — agent · MCP · A2A cards/tasks · chat   │
├─────────────────────────────────────────────────────────────┤
│  Layer 4  Execution & lifecycle — guarded exec · models     │
├─────────────────────────────────────────────────────────────┤
│  Layer 3  Placement — Filter → Rank → Select                │
├─────────────────────────────────────────────────────────────┤
│  Layer 2  Snapshot — ClusterSnapshot · daemon cache         │
├─────────────────────────────────────────────────────────────┤
│  Layer 1  Fact plane — SSH probes · UDP beacons · mesh      │
└─────────────────────────────────────────────────────────────┘
```

Default HTTP / A2A listen address: Unix socket `~/.axis/axis.sock` (TCP opt-in via `--addr`).

## Command surface

### Stable operator path

| Command | Purpose |
| --- | --- |
| `axis version` | Build version, commit, Go version |
| `axis init` | Interactive cluster configuration |
| `axis node facts` / `axis cluster status` | Local facts / cluster snapshot (`--cached`) |
| `axis task place` / `axis placement explain` | Advisory placement |
| `axis task run` | Guarded execution with safety gates |
| `axis task delegate \| status \| approve \| reject` | A2A task lifecycle (observe + approval) |
| `axis model …` | Resident model plan / start / await / query / stop / list |
| `axis reservations` | Reservation inspection |
| `axis doctor` | Config, SSH, daemon ownership, local AI backends |
| `axis daemon …` | Cache daemon + optional user service |
| `axis serve` | Local HTTP API + daemon (Unix socket default) |
| `axis mesh status \| peers` | Gossip mesh diagnostics |
| `axis tui` | Full-screen cluster dashboard |
| `axis update` | Self-update from GitHub Releases |

### Experimental / advisory

| Command | Purpose |
| --- | --- |
| `axis mcp serve` | Read-only MCP over stdio |
| `axis mcp client` | Unified MCP client |
| `axis agent` | Tool-calling agent loop (subordinate to facts) |
| `axis ai` / `axis cortex` | Inference routing / vector memory helpers |

Full command notes: [`docs/current-state.md`](docs/current-state.md).

## Documentation

| Doc | Contents |
| --- | --- |
| [`docs/current-state.md`](docs/current-state.md) | Live product truth (trust this over older specs) |
| [`docs/architecture.md`](docs/architecture.md) | Layered architecture reference |
| [`docs/operator-quickstart.md`](docs/operator-quickstart.md) | Operator onboarding |
| [`docs/authority-*.md`](docs/) | Authority contracts (reservation, secrets, execution, …) |
| [`CHANGELOG.md`](CHANGELOG.md) | Release history |
| [`AGENTS.md`](AGENTS.md) | Guidance for AI agents working in this repo |
| [`SECURITY.md`](SECURITY.md) | Vulnerability reporting |

## Status

| Item | Value |
| --- | --- |
| Latest release | [**v0.19.4**](https://github.com/toasterbook88/axis/releases/tag/v0.19.4) (`main` @ `aad34268`) |
| Supported security line | `v0.19.x` (see [`SECURITY.md`](SECURITY.md)) |
| License | MIT — **Smith Software Solutions** |
| Platforms | darwin / linux × amd64 / arm64 (reproducible GoReleaser builds) |

## Build & test

```bash
make build          # CGO_ENABLED=0 go build -trimpath
make test           # go test ./... -count=1
make test-race
make lint           # gofmt + go vet
make coverage       # coverage gates via hack/coverage-check.sh
```

Requires Go **1.26+** (`go.mod` is authoritative).

## Contributing

See [`CONTRIBUTING.md`](CONTRIBUTING.md). Protected `main`: required CI checks, squash-only merges. For AI agents: [`AGENTS.md`](AGENTS.md).

## License

[MIT](LICENSE) — Copyright © 2026 **Smith Software Solutions**

---

[axismcp.app](https://axismcp.app) · [axismcp.tech](https://axismcp.tech) · [smithsolutionssc.com](https://smithsolutionssc.com) · [@AXISBRIDGEMACOS](https://twitter.com/AXISBRIDGEMACOS)
