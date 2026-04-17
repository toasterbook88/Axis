# AXIS

[![CI](https://github.com/toasterbook88/axis/actions/workflows/ci.yml/badge.svg)](https://github.com/toasterbook88/axis/actions/workflows/ci.yml)
[![Release](https://github.com/toasterbook88/axis/actions/workflows/release.yml/badge.svg)](https://github.com/toasterbook88/axis/actions/workflows/release.yml)
[![Go version](https://img.shields.io/badge/go-1.26+-00ADD8?logo=go)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-green)](LICENSE)
[![Latest Release](https://img.shields.io/github/v/release/toasterbook88/axis?include_prereleases)](https://github.com/toasterbook88/axis/releases)

**A local-first cluster substrate that discovers hardware across your machines via SSH,
builds deterministic snapshots, and makes reservation-aware placement decisions —
with optional gossip mesh discovery, AI agent surfaces, and guarded execution.**

> **Truth Boundary:** No generated output may present itself as cluster truth
> unless it is backed by a real snapshot or live probe.

---

## Architecture

AXIS is built as a 5-layer stack. Each layer is subordinate to the one below it —
advisory surfaces never override observed state.

```
┌─────────────────────────────────────────────────────────────────┐
│  Layer 5: ADVISORY                                              │
│  Chat · Agent · MCP Server                                      │
│  Experimental helpers — never authoritative                     │
├─────────────────────────────────────────────────────────────────┤
│  Layer 4: EXECUTION                                             │
│  Guarded Exec · Safety Gates · Heartbeat Reservations           │
│  Structured NDJSON streaming · Resource accounting              │
├─────────────────────────────────────────────────────────────────┤
│  Layer 3: PLACEMENT                                             │
│  Filter → Rank → Select · FitScore 0-100                        │
│  GPU/VRAM matching · Locality · Empirical observations          │
├─────────────────────────────────────────────────────────────────┤
│  Layer 2: SNAPSHOT                                              │
│  ClusterSnapshot assembly · Daemon cache · 7 refresh triggers   │
│  Content-aware config watches · Staleness detection             │
├─────────────────────────────────────────────────────────────────┤
│  Layer 1: FACT PLANE                                            │
│  SSH hardware probes · UDP beacons · Mesh gossip discovery      │
│  Local + remote collectors · HMAC-SHA256 authentication         │
└─────────────────────────────────────────────────────────────────┘
```

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

# Rich cluster overview
axis summary

# Health diagnostics
axis doctor
```

## Proposed for v0.10.0 (not yet shipped)

The items in this section describe proposed capabilities from this branch and
roadmap work. For shipped behavior, use `docs/current-state.md` and the latest
published GitHub release.

### 🌐 Mesh Discovery
Zero-configuration peer discovery using gossip protocol. Nodes find each other
automatically via UDP gossip with HMAC-SHA256 authentication. Discovered peers
require explicit trust promotion before participating in placement.

```
Discovered → Verified → Trusted
     ↓           ↓
  Suspect → Dead (evicted)
```

### 📊 Reservation Ledger
Double-entry resource accounting replaces the heuristic RAM sharing model.
Per-node, per-execution tracking with configurable overcommit policy,
heartbeat-based liveness, and automatic stale reclaim.

### 🛡️ Structured Safety Engine
Priority-based rule engine replaces substring safety blocking. Parsed command
analysis with 7 risk categories, context-aware evaluation, and learned
operator overrides.

### 🖥️ CLI Dashboard
Color-coded cluster overview with RAM usage bars, node health icons,
reservation tables, and comprehensive health diagnostics.

### 🔌 Enhanced HTTP API (v2)
Nine new endpoints including cluster overview, Prometheus metrics,
batch placement, dry-run simulation, and health diagnostics.

## Command Surface

### Stable Operator Path

| Command | Purpose |
|---------|---------|
| `axis version` | Print build version, commit, and Go version |
| `axis facts` | Local hardware/tool snapshot (`--format json\|yaml`) |
| `axis status` | Live cluster snapshot (`--cached`, `--cached-only`) |
| `axis task place` | Advisory placement with reasoning (`--cached`) |
| `axis placement explain` | Detailed per-node placement breakdown |
| `axis profile match` | Workload class inference (no snapshot needed) |
| `axis task context` | Compact context block (`--format json`, `--cached`) |
| `axis task run` | Guarded task execution with safety gates |
| `axis summary` | Color-coded cluster overview |
| `axis doctor` | Comprehensive health diagnostics |
| `axis daemon start` | Background snapshot refresh daemon |
| `axis daemon status` | Daemon health and cache metadata |
| `axis daemon refresh` | Trigger immediate cache refresh |
| `axis daemon invalidate` | Invalidate cached snapshot |

### Secondary Commands

| Command | Purpose |
|---------|---------|
| `axis serve` | Local HTTP API + daemon |
| `axis mesh status` | Gossip peer discovery state |
| `axis node trust <name>` | Promote discovered peer to trusted |
| `axis reservation list` | Active resource reservations |
| `axis reservation clean` | Reclaim stale reservations |
| `axis llm` | LLM routing and model management |
| `axis cortex` | Distributed vector memory / event bus |
| `axis mcp serve` | Read-only MCP server over stdio |
| `axis chat` | Ollama-backed advisory chat |
| `axis agent` | Tool-calling agent loop |
| `axis scripts list` | Built-in script catalog |
| `axis skills` | Learned execution skills |

## Placement Algorithm

The placement engine uses a deterministic **Filter → Rank → Select** pipeline:

### Filter (all must pass)
- Node status: `complete`
- Allocatable RAM ≥ requirement (after system reserve)
- GPU VRAM/vendor/backend match
- Required tools present
- Empirical PeakRAMMB filter
- No thermal throttling
- Battery ≥ 20%
- No active tombstones
- Storage class check (HDD penalty)

### Rank (priority order)
1. Highest allocatable RAM
2. Best empirical observation (fresh only)
3. Resident model locality
4. Preferred backend rank
5. GPU score (+25 pts)
6. Highest effective headroom
7. Unified-memory / TurboQuant suitability
8. Lowest RAM pressure
9. Lowest reservation ratio
10. Node name ascending (stable tiebreak)

### FitScore (0–100)
- GPU match: **+25 pts**
- Local node: **+10 pts**
- Unified memory bonus for matching workloads
- Reservation ratio factor

## HTTP API

### v1 Routes (Unix socket: `~/.axis/axis.sock`)

| Route | Auth | Purpose |
|-------|------|---------|
| `GET /health` | No | Daemon health |
| `GET /snapshot` | Yes | Full ClusterSnapshot |
| `GET /snapshot/meta` | Yes | Cache metadata |
| `POST /run` | Yes | Guarded execution (NDJSON stream) |
| `POST /refresh` | Yes | Trigger cache refresh |
| `POST /invalidate` | Yes | Invalidate cache |
| `GET /tools` | Yes | MCP tool definitions |
| `GET /knowledge` | Yes | Cluster knowledge + skills |

### v2 Routes (new in v0.10.0)

| Route | Auth | Purpose |
|-------|------|---------|
| `GET /v2/cluster` | Yes | Full cluster overview |
| `GET /v2/nodes` | Yes | Node list with health |
| `GET /v2/nodes/:name` | Yes | Single node deep-dive |
| `GET /v2/reservations` | Yes | Reservation ledger |
| `GET /v2/mesh` | Yes | Mesh peer state |
| `GET /v2/placement/dry-run` | Yes | Placement simulation |
| `GET /v2/metrics` | **No** | Prometheus-compatible metrics |
| `POST /v2/batch/place` | Yes | Batch placement (up to 50) |
| `GET /v2/doctor` | Yes | Health diagnostics |

## Configuration

```yaml
# ~/.axis/nodes.yaml
nodes:
  - name: macbook-pro
    hostname: 192.168.1.100
    ssh_port: 22
    ssh_user: admin
    role: workstation
    timeout: 10s

  - name: linux-server
    hostname: 192.168.1.200
    ssh_user: deploy
    role: server

# Mesh discovery (v0.10.0)
mesh:
  enabled: true
  listen_addr: ":42426"
  gossip_interval: 5s
  shared_secret: "your-cluster-secret"
  max_peers: 64

# Reservation policy (v0.10.0)
reservation:
  max_overcommit_ratio: 1.0   # 1.0 = no overcommit
  system_reserve_mb: 1024
  heartbeat_stale_window: 2m
  max_entries_per_node: 32
```

## Build & Test

```bash
make build          # CGO_ENABLED=0 go build -trimpath with LDFLAGS
make install        # Build + copy to $GOPATH/bin
make test           # go test ./... -count=1 -timeout 180s
make test-race      # go test ./... -count=1 -timeout 180s -race
make lint           # gofmt + go vet
make coverage       # Coverage gates via hack/coverage-check.sh
```

## Release Process

Releases are automated via GitHub Actions:

```bash
# 1. Update version in internal/buildinfo/version.go
# 2. Commit and tag
git tag v0.10.0
git push origin v0.10.0

# 3. release.yml runs automatically:
#    Test Gate → Version Validation → Security Scan → GoReleaser → Verify Install
```

Binaries are built for **darwin/linux × amd64/arm64** with:
- Reproducible builds (`-trimpath`, `CGO_ENABLED=0`)
- Embedded version, commit hash, build date
- SHA-256 checksums
- Conventional Commits changelog

## Project Layout

```
axis/
├── cmd/axis/          Cobra CLI entry point
├── internal/          Private packages (34 packages)
│   ├── facts/         SSH hardware/tool collection
│   ├── snapshot/      ClusterSnapshot assembly
│   ├── placement/     Deterministic Filter→Rank→Select
│   ├── execution/     Guarded task execution
│   ├── daemon/        Background cache + 7 refresh triggers
│   ├── api/           HTTP API (v1 + v2)
│   ├── mesh/          Gossip peer discovery
│   ├── reservation/   Resource accounting ledger
│   ├── safety/        Structured command safety
│   ├── discovery/     SSH + UDP node discovery
│   ├── mcp/           MCP server (stdio)
│   ├── agent/         Tool-calling agent loop
│   └── ...            20+ additional packages
├── docs/              Design docs + CI-validated state
├── hack/              Developer scripts
└── .github/           CI + release workflows
```

## Security

- **Air-gapped option:** On-device inference via Ollama, no cloud dependency
- **HMAC-SHA256:** Beacon and mesh gossip authentication
- **Zero-trust execution:** Allow-list based safety gates with parsed command analysis
- **Constant-time auth:** Bearer token comparison via `crypto/subtle`
- **No data exfiltration:** All state persisted locally in `~/.axis/`
- **govulncheck:** Automated vulnerability scanning in release pipeline
- **SBOM generation:** Supply chain transparency via GoReleaser

See [SECURITY.md](SECURITY.md) for our vulnerability disclosure policy.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development guidelines.

For AI agents working in this repo, see [AGENTS.md](AGENTS.md).

## License

[MIT](LICENSE) — Smith Software Solutions LLC

---

<p align="center">
  <a href="https://axismcp.app">axismcp.app</a> ·
  <a href="https://axismcp.tech">axismcp.tech</a> ·
  <a href="https://smithsolutionssc.com">smithsolutionssc.com</a> ·
  <a href="https://twitter.com/AXISBRIDGEMACOS">@AXISBRIDGEMACOS</a>
</p>
