<p align="center">
  <img src="docs/assets/axis-banner.jpg" alt="AXIS" width="720" />
</p>

<p align="center">
  <strong>Local-first, reservation-aware cluster substrate</strong><br/>
  Deterministic placement · Guarded execution · Observed-state truth
</p>

<p align="center">
  <a href="https://github.com/toasterbook88/axis/releases/tag/v0.19.4"><img src="https://img.shields.io/github/v/release/toasterbook88/axis?include_prereleases&label=release" alt="Latest release" /></a>
  <a href="https://github.com/toasterbook88/axis/actions/workflows/ci.yml"><img src="https://github.com/toasterbook88/axis/actions/workflows/ci.yml/badge.svg" alt="CI" /></a>
  <a href="go.mod"><img src="https://img.shields.io/badge/go-1.26+-00ADD8?logo=go" alt="Go version" /></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-green" alt="License: MIT" /></a>
</p>

<p align="center">
  <a href="#quick-start">Quick start</a> ·
  <a href="#what-it-is">What</a> ·
  <a href="#architecture">Architecture</a> ·
  <a href="#commands">Commands</a> ·
  <a href="#documentation">Docs</a>
</p>

---

## Quick start

Each block below is safe to paste. Commands match `axis --help` on v0.19.4 (`axis serve`, `axis cluster status`, `axis init`, `axis daemon`).

### 1. Install

**Pinned release** — `install.sh` writes `/usr/local/bin/axis` and uses `sudo` when that directory is not writable:

```bash
curl -fsSL https://raw.githubusercontent.com/toasterbook88/axis/main/install.sh \
  | AXIS_VERSION=v0.19.4 AXIS_REQUIRE_PINNED=1 bash
axis version
```

Same installer, user-local (no sudo). Put `~/.local/bin` on `PATH` if it is not already:

```bash
curl -fsSL https://raw.githubusercontent.com/toasterbook88/axis/main/install.sh \
  | AXIS_VERSION=v0.19.4 AXIS_REQUIRE_PINNED=1 AXIS_INSTALL_DIR="${HOME}/.local/bin" bash
"${HOME}/.local/bin/axis" version
```

**From source** (Go 1.26+, see `go.mod`):

```bash
git clone https://github.com/toasterbook88/axis.git
cd axis
make build
make install-user
"${HOME}/.local/bin/axis" version
```

Or install the tagged module into `$(go env GOPATH)/bin`:

```bash
go install github.com/toasterbook88/axis/cmd/axis@v0.19.4
axis version
```

### 2. Init and status

`axis init` is interactive. It writes `~/.axis/nodes.yaml` (override the path with `--config`).

```bash
axis init
axis node facts
axis doctor
axis cluster status
```

`axis cluster status` reads the daemon publication when it is inside the 5-minute stale window and prints the source and age. Force a sweep with:

```bash
axis cluster status --live
```

### 3. Placement (advisory)

```bash
axis task place "run ollama inference on a 7b model"
axis placement explain "run ollama inference on a 7b model"
```

`place` and `explain` do not run the workload. Execution is a separate command: `axis task run`.

### 4. Serve, health, agent card

`axis serve` stays in the foreground. It is the same listener as `axis daemon start`. The default address is the Unix socket `~/.axis/axis.sock` (`--addr` opts into TCP).

```bash
axis serve
```

In another terminal. `/health` and the agent card are public; the host in the URL is only a curl placeholder for `--unix-socket`:

```bash
curl -fsS --unix-socket "${HOME}/.axis/axis.sock" \
  http://localhost/health
curl -fsS --unix-socket "${HOME}/.axis/axis.sock" \
  http://localhost/.well-known/agent-card.json
```

TCP, if you pass `--addr`:

```bash
axis serve --addr 127.0.0.1:8082
```

```bash
curl -fsS http://127.0.0.1:8082/.well-known/agent-card.json
```

Check the daemon without curl, and install a user service (launchd or systemd) when you do not want a foreground process:

```bash
axis daemon status
axis daemon service install
axis daemon service status
```

The public card advertises **`streaming: false`** (sync send/get only; no subscribe/SSE). That is not an A2A TCK claim. Other API routes (`/snapshot`, `/run`, `/a2a/v1/...`) require `Authorization: Bearer` with the token in `~/.axis/token`.

Fleet rollouts and mesh membership are **operator-driven**. AXIS does not auto-provision or auto-join a fleet.

---

## What it is

AXIS discovers hardware on machines you already own (SSH, optional UDP beacons), builds deterministic cluster snapshots, and makes **reservation-aware placement decisions**. Optional surfaces add guarded task execution, resident model lifecycle, a local HTTP API, MCP, and an A2A agent-card / task plane.

> **Truth boundary:** No generated output may present itself as cluster truth unless it is backed by a real snapshot or live probe.

| You need | AXIS does |
| --- | --- |
| Honest placement | Filter → Rank → Select on a real `ClusterSnapshot` |
| Shared RAM accounting | Soft reservations overlaid on live and cached reads |
| Safe execution | Safety gates + reserve → run → release |
| Local operation | API on `~/.axis/axis.sock`; state under `~/.axis/` |

## Features

**Core**

- SSH fact collection and cluster snapshot assembly (live or daemon-cached)
- Deterministic placement. Ranking objective is `allocatable_ram`; FitScore is diagnostic suitability only
- Reservation ledger and reservation-aware status / place / context reads
- Guarded execution (`axis task run`, HTTP `/run`) with structured NDJSON streaming
- Optional daemon cache with explicit refresh / invalidate, plus launchd / systemd user service install

**Models and agents (v0.19.x)**

- Resident model lifecycle: `axis model plan | start | await | query | stop | list`
- Interactive TUI: bare `axis` on a TTY, or `axis tui`
- Tool-calling `axis agent` (console on a TTY; cache-first cluster context)
- Read-oriented MCP server (`axis mcp serve`) and MCP client (`axis mcp client`)

**A2A (landed in 0.19.4)**

- Per-node agent card at `/.well-known/agent-card.json` — [#464](https://github.com/toasterbook88/Axis/pull/464)
- Authenticated observe task send/get (`POST /a2a/v1/message:send`, `GET /a2a/v1/tasks/{id}`) — [#465](https://github.com/toasterbook88/Axis/pull/465)
- Approval queue for exec-shaped work (`approve` / `reject`; no auto-approve) — [#466](https://github.com/toasterbook88/Axis/pull/466)
- Delegate CLI: `axis task delegate | status | approve | reject` — [#468](https://github.com/toasterbook88/Axis/pull/468)

## Architecture

Five layers. Each is subordinate to the one below. Advisory surfaces never override observed state.

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

Default HTTP / A2A listen address: Unix socket `~/.axis/axis.sock`. TCP is opt-in via `axis serve --addr`.

## Commands

### Stable operator path

| Command | Purpose |
| --- | --- |
| `axis version` | Build version, commit, Go version |
| `axis init` | Interactive cluster configuration (`--config` overrides `~/.axis/nodes.yaml`) |
| `axis node facts` | This machine (`axis facts` is the same command) |
| `axis cluster status` | Daemon publication inside 5 minutes (prints source and age); `--live` sweeps; `--cached-only` fails closed |
| `axis task place` | Advisory placement (`--format text\|json`) |
| `axis placement explain` | Per-node ranking explanation (one intent argument) |
| `axis task run` | Guarded execution with safety gates |
| `axis task delegate \| status \| approve \| reject` | A2A task lifecycle |
| `axis model …` | `plan`, `start`, `await`, `query`, `stop`, `list`, `inspect` |
| `axis reservations` | Reservation inspection (`list`, `inspect`, `release`) |
| `axis doctor` | Config, SSH, daemon ownership, local AI backends (`--strict`) |
| `axis daemon status \| refresh \| invalidate \| restart` | Cache daemon |
| `axis daemon service install` | launchd / systemd user service |
| `axis serve` | Local HTTP API (foreground; Unix socket default) |
| `axis mesh status \| peers` | Gossip mesh diagnostics |
| `axis tui` | Full-screen cluster dashboard |
| `axis update` | Self-update from GitHub Releases |

### Experimental / advisory

| Command | Purpose |
| --- | --- |
| `axis mcp serve` | Read-only MCP over stdio |
| `axis mcp client` | MCP client (`list`, `tools`, `call`, …) |
| `axis agent` | Tool-calling agent loop (subordinate to facts) |
| `axis ai` / `axis cortex` | Inference routing / vector memory helpers |

`axis chat` and `axis llm` were removed. Use `axis agent` and `axis ai route`.

Full command notes: [`docs/current-state.md`](docs/current-state.md).

## Documentation

| Doc | Contents |
| --- | --- |
| [`docs/current-state.md`](docs/current-state.md) | Live product truth (trust this over older specs) |
| [`docs/architecture.md`](docs/architecture.md) | Layered architecture reference |
| [`docs/operator-quickstart.md`](docs/operator-quickstart.md) | Longer operator onboarding |
| [`docs/authority-*.md`](docs/) | Authority contracts (reservation, secrets, execution, …) |
| [`CHANGELOG.md`](CHANGELOG.md) | Release history |
| [`AGENTS.md`](AGENTS.md) | Guidance for AI agents working in this repo |
| [`SECURITY.md`](SECURITY.md) | Vulnerability reporting |

## Status

| Item | Value |
| --- | --- |
| Latest release | [**v0.19.4**](https://github.com/toasterbook88/axis/releases/tag/v0.19.4) (tag commit `aad3426`) |
| Supported security line | `v0.19.x` (see [`SECURITY.md`](SECURITY.md)) |
| License | MIT — **Smith Software Solutions** |
| Platforms | darwin / linux × amd64 / arm64 (reproducible GoReleaser builds) |

## Build and test

```bash
make build       # CGO_ENABLED=0 go build -trimpath -o axis ./cmd/axis/
make install-user    # ~/.local/bin/axis
make test        # hermetic go test ./... plus docs tool tests
make test-race
make lint        # gofmt, go vet, staticcheck
make coverage    # hack/coverage-check.sh
```

Requires Go **1.26+** (`go.mod` is authoritative). `make install-system` installs to `/usr/local/bin` and needs `sudo`.

## Contributing

See [`CONTRIBUTING.md`](CONTRIBUTING.md). Protected `main`: required CI checks, squash-only merges. For AI agents: [`AGENTS.md`](AGENTS.md).

## License

[MIT](LICENSE) — Copyright © 2026 **Smith Software Solutions**

---

[axismcp.app](https://axismcp.app) · [axismcp.tech](https://axismcp.tech) · [smithsolutionssc.com](https://smithsolutionssc.com) · [@AXISBRIDGEMACOS](https://twitter.com/AXISBRIDGEMACOS)
