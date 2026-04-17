# AXIS Improvement Package — v0.10.0 Proposal

## Overview

This package contains **5 new capabilities** for AXIS, designed to integrate
cleanly with the existing v0.9.0 architecture. Each component respects the
truth boundary: no generated output presents itself as cluster truth unless
backed by a real snapshot or live probe.

## File Manifest

```
axis-improvements/
├── IMPROVEMENTS.md                          ← This file
├── cmd/axis/
│   └── dashboard.go                         ← Dashboard/summary render helpers (not registered commands)
├── internal/
│   ├── mesh/
│   │   ├── mesh.go                          ← Gossip-based peer discovery (NEW PACKAGE)
│   │   └── mesh_test.go                     ← 15 tests covering lifecycle + HMAC + callbacks
│   ├── reservation/
│   │   ├── ledger.go                        ← Double-entry reservation accounting (NEW PACKAGE)
│   │   └── ledger_test.go                   ← 16 tests covering reserve/release/reclaim/metrics
│   ├── api/
│   │   └── v2.go                            ← v2 route scaffolding with active reads + explicit stubs
│   └── safety/
│       ├── structured.go                    ← Structured safety scaffolding (not wired into the operator path)
│       └── structured_test.go               ← 9 tests covering safe/denied/prompt/parsing
```

---

## 1. Mesh Discovery (`internal/mesh/`)

**Problem:** Static `nodes.yaml` seed file is the only way to add nodes.
Zero-configuration node discovery is a roadmap item.

**Solution:** Gossip-based peer discovery scaffolding with a 5-state lifecycle:

```
Discovered → Verified → Trusted
     ↓           ↓
  Suspect → Dead (evicted)
```

**Key design decisions:**
- **Never weakens the fact plane.** Mesh state is advisory metadata, not cluster truth.
  Only SSH-probed NodeFacts are authoritative.
- **Seed nodes are always trusted.** Configured nodes from `nodes.yaml` start as Trusted
  and are exempt from automatic eviction.
- **Gossip peers require future promotion.** A later operator surface can decide
  how discovered peers are promoted before they participate in placement.
- **HMAC-SHA256 authentication only.** Optional shared secret authenticates
  message contents, but replay protection is not enforced in this branch.
- **Bounded growth.** MaxPeers cap (default 64) prevents unbounded memory usage.
- **Fan-out gossip.** Each round broadcasts to `FanOut` (default 3) random peers,
  preventing broadcast storms.

**Branch state:**
- Package and tests land as internal groundwork only.
- Daemon, discovery, config, and CLI wiring remain deferred.

**Test coverage:** 15 tests — lifecycle, HMAC, MaxPeers cap, seed exemption, callbacks.

---

## 2. Reservation Ledger (`internal/reservation/`)

**Problem:** RAM sharing model is heuristic. The current state.ClusterState tracks
reservations but lacks formal accounting, overcommit policy, and cluster-wide
reservation balancing.

**Solution:** Double-entry reservation ledger with:
- **Per-node, per-execution tracking** with owner identity
- **Overcommit ratio enforcement** (configurable, default 1.0 = no overcommit)
- **Fail-closed capacity checks** — reservations are rejected when node capacity is unknown
- **System reserve** held back from allocation (default 1024MB, matching existing logic)
- **Heartbeat-based liveness** with configurable stale window (default 2min, matching existing)
- **Per-node entry caps** preventing goroutine storms from concurrent reservations
- **Metrics** for observability: total reserved, released, reclaimed, failures

**Branch state:**
- Core package and tests land here.
- Daemon, placement, execution, API, and CLI wiring remain deferred.

**Test coverage:** 16 tests — reserve, release, overcommit, heartbeat, reclaim, metrics.

---

## 3. Enhanced HTTP API (`internal/api/v2.go`)

**Problem:** The current API surface is functional but minimal. Operators need
richer query endpoints for cluster intelligence, and external integrations
need structured data.

**Branch state:** this branch wires a versioned route namespace with a mix of
active read surfaces and explicit stubs.

| Route | Method | Branch Status |
|-------|--------|---------------|
| `/v2/cluster` | GET | Active |
| `/v2/nodes` | GET | Active |
| `/v2/nodes/:name` | GET | Active |
| `/v2/reservations` | GET/POST/DELETE | Stub (`501`) |
| `/v2/mesh` | GET | Stub (`501`) |
| `/v2/placement/dry-run` | GET/POST | Stub (`501`) |
| `/v2/metrics` | GET | Active |
| `/v2/batch/place` | POST | Stub (`501`) |
| `/v2/doctor` | GET | Active |

**Design decisions:**
- **Versioned under /v2/** — non-breaking addition to existing routes
- **Same auth model** — Bearer token with constant-time comparison
- **/v2/metrics is unauthenticated** — standard for Prometheus scraping
- **Prometheus text format** — native compatibility with monitoring stacks
- **Stub routes fail honestly** — unimplemented surfaces return non-2xx instead of synthetic success

**Integration points:**
- Server: `registerV2Routes(mux, cache, token)` is wired into the existing route setup
- Additional daemon, ledger, and mesh-backed behavior remains deferred

---

## 4. Structured Safety Engine (`internal/safety/structured.go`)

**Problem:** Current safety blocking is substring-based, risking over-blocking
(e.g., "rm -rf" in quoted echo) and under-blocking (creative evasion).

**Solution:** Structured rule-evaluation scaffolding with:
- **Command parsing** — splits into program + args, strips env prefixes and paths
- **7 risk categories** — safe, read-only, modify, destructive, network-mutating,
  privilege-escalate, system-critical
- **3 verdicts** — allow, deny, prompt (ask operator)
- **Priority-based evaluation** — higher priority rules match first
- **Glob patterns** — flexible matching without regex complexity
- **Surface-aware rules** — different policies for guarded-exec vs agent-run-shell
- **Learned overrides deferred** — program-name-only approvals are disabled in this branch

**Default rules include:**
- Always-safe: `uname`, `ls`, `ps`, `git status`, `go version`, etc.
- Always-deny: `rm -rf`, `sudo`, `chmod 777`, disk format commands
- Prompt: `git push`, `ollama run`, `systemctl restart`, `curl -X POST`

**Branch state:** the evaluator package and tests land here, but the existing
operator safety path remains authoritative until integration is done.

**Test coverage:** 9 tests — safe/denied/prompt commands, git force-push,
disabled override behavior, env prefix parsing, pipe handling, category coverage.

---

## 5. CLI View Helpers (`cmd/axis/dashboard.go`)

**Problem:** Operators must run multiple commands to understand cluster state.
Output is functional but not visually scannable.

**Branch state:** this file contributes render helpers only. It does not
register new Cobra commands in this branch.

**Visual improvements:**
- Unicode box-drawing for section headers
- Color-coded RAM usage bars (green → yellow → red)
- Status icons: ● healthy, ◐ degraded, ○ unreachable
- Pressure highlighting: green/yellow/red by level
- Stale reservation warnings in red

**Future integration:** register any ready Cobra subcommands only after they are
fully wired to real data sources.

---

## Integration Order (Recommended)

```
Phase 1 (foundation):
  1. reservation/ledger.go — wire into daemon and execution
  2. safety/structured.go — wrap existing IsBlocked()

Phase 2 (discovery):
  3. mesh/mesh.go — wire into daemon with refresh callbacks
  4. api/v2.go — register new routes on existing server

Phase 3 (UX):
  5. cmd/axis/dashboard.go — register only the views that are fully wired
```

Each phase can ship independently. All packages are zero-dependency on each other
(they integrate through the existing daemon/server wiring, not direct imports).

## Compatibility

- **Go 1.26.1+** — no new external dependencies required
- **Existing behavior preserved** — all new code is additive
- **Truth boundary respected** — mesh and reservation state is explicitly
  non-authoritative for cluster truth
- **Test patterns match existing** — table-driven, no test helper frameworks
- **Naming conventions match** — package doc comments, error wrapping, slog logging
