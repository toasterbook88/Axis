# AXIS Phase 1 — Build Specification

## Overview

Phase 1 delivers the AXIS CLI binary and the core observability layer:
local and remote fact collection, tool discovery, and `ClusterSnapshot` assembly.
It is the foundation that later phases will build on.

## Goals

- Provide a single binary (`axis`) that works without a daemon or server
- Collect structured hardware and software facts from the local machine and remote nodes
- Emit a compact, machine-readable `ClusterSnapshot` (JSON or YAML)
- Tolerate collection failures gracefully: degrade node status rather than crash

## Non-Goals for Phase 1

- Daemon process or persistent state
- Automatic task placement or scheduling
- Mesh networking, Tailscale, or peer discovery
- Phase 2+ coordinator features

---

## CLI Commands

### `axis version`

Prints the version string (e.g. `axis 0.1.0`).

### `axis facts`

Collects facts from the **local** machine and prints them as JSON (default) or YAML.

```
axis facts [--format json|yaml]
```

Collected per run:
- OS (darwin / linux) and kernel version
- Architecture
- CPU core count and model string
- Total and free RAM (MB), with pressure classification
- Total and free disk space (GB)
- GPU list (best-effort; silent if unavailable)
- Network addresses (IPv4/IPv6, non-loopback, non-link-local)
- Installed tool inventory (see Tool Discovery below)
- Collection status: `complete` or `partial`
- Collection timestamp (UTC)

### `axis status`

Reads `~/.axis/nodes.yaml`, fans out SSH-based fact collection to all configured
nodes, and emits a `ClusterSnapshot`.

```
axis status [--format json|yaml]
```

---

## Fact Collection

### Local Collector (`internal/facts`)

`LocalCollector` collects facts from the machine it runs on. It uses platform
syscalls and standard tools (`sysctl`, `uname`, `/proc/meminfo`, `df`, `vm_stat`)
rather than external libraries to keep the dependency surface minimal.

Failure policy: any sub-collection failure sets `status: partial` rather than
returning an error. The collector never returns a hard error.

### Remote Collector (`internal/facts`)

`RemoteCollector` SSHes into a configured node, runs the same probes remotely,
and parses the output. Uses `golang.org/x/crypto/ssh` with key-based auth.

Failure policy:
- SSH connection failure → `status: unreachable`
- Connected but some commands fail → `status: partial`

### Tool Discovery

`DiscoverTools` probes for the following tools by name using `exec.LookPath`.
For each found tool it also attempts a version query:

| Tool | Class | Version Command |
|---|---|---|
| `go` | build | `go version` |
| `python3` | runtime | `python3 --version` |
| `git` | vcs | `git --version` |
| `docker` | container | `docker --version` |
| `ollama` | ai-cli | `ollama --version` |
| `node` | runtime | `node --version` |
| `swift` | build | `swift --version` |
| `cargo` | build | `cargo --version` |
| `gcc` | build | `gcc --version` |

Tools not found on `PATH` are silently omitted.

---

## Data Model (`internal/models`)

### `NodeFacts`

Principal per-node result. Contains assigned state (name, role from config)
and observed state (hardware, OS, tools, addresses).

Node status values:
- `complete` — all facts collected
- `partial` — node reachable but some facts failed
- `unreachable` — SSH/connect failure
- `error` — internal collector failure

### `ClusterSnapshot`

Top-level output of `axis status`. Contains:
- `timestamp` — UTC collection time
- `status` — `healthy` (all nodes complete) or `degraded` (any node non-complete)
- `nodes` — array of `NodeFacts`
- `summary` — cluster-level aggregates: total/reachable node count, total/free RAM
- `warnings` — per-node issues: unreachable, partial, error, ram_pressure

RAM pressure warning threshold: free RAM < 10% of total.

### `Resources`

Per-node hardware metrics:
- `cpu_cores`, `cpu_model`
- `ram_total_mb`, `ram_free_mb`
- `disk_total_gb`, `disk_free_gb`
- `gpus` (optional)
- `pressure`: `none` / `low` / `medium` / `high`

Pressure thresholds (free/total):
- `high`: < 5%
- `medium`: < 10%
- `low`: < 20%
- `none`: ≥ 20%

---

## Configuration (`internal/config`)

Config file: `~/.axis/nodes.yaml`

Required fields per node: `name`, `hostname`, `ssh_user`.
Optional: `role`, `ssh_port` (default 22), `timeout_sec` (default 10).

SSH transport uses key-based authentication only (no password auth).
`ssh_user` and transport fields are config-only and do not appear in `NodeFacts`.

---

## Node Discovery (`internal/discovery`)

`discovery.Discover` fans out over configured nodes concurrently. For each node
it creates a `RemoteCollector` and collects facts within the per-node timeout.
Results are returned as a `[]models.NodeFacts` slice.

---

## Snapshot Assembly (`internal/snapshot`)

`snapshot.Build` takes the `[]NodeFacts` slice, computes cluster-level aggregates,
applies degraded status if any node is non-complete, and generates per-node warnings.
