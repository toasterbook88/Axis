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
│   └── dashboard.go                         ← Rich CLI dashboard + doctor + reservation views
├── internal/
│   ├── mesh/
│   │   ├── mesh.go                          ← Gossip-based peer discovery (NEW PACKAGE)
│   │   └── mesh_test.go                     ← 15 tests covering lifecycle + HMAC + callbacks
│   ├── reservation/
│   │   ├── ledger.go                        ← Double-entry reservation accounting (NEW PACKAGE)
│   │   └── ledger_test.go                   ← 16 tests covering reserve/release/reclaim/metrics
│   ├── api/
│   │   └── v2.go                            ← Enhanced HTTP API with 9 new endpoints
│   └── safety/
│       ├── structured.go                    ← Structured safety rule engine (REPLACES substring)
│       └── structured_test.go               ← 9 tests covering safe/denied/prompt/parsing
```

---

## 1. Mesh Discovery (`internal/mesh/`)

**Problem:** Static `nodes.yaml` seed file is the only way to add nodes.
Zero-configuration node discovery is a roadmap item.

**Solution:** Gossip-based peer discovery with a 5-state lifecycle:

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
- **Gossip peers require promotion.** Discovered peers must be explicitly promoted via
  `axis node trust <name>` before they participate in placement.
- **HMAC-SHA256 authentication.** Optional shared secret prevents gossip spoofing
  (consistent with existing beacon auth).
- **Bounded growth.** MaxPeers cap (default 64) prevents unbounded memory usage.
- **Fan-out gossip.** Each round broadcasts to `FanOut` (default 3) random peers,
  preventing broadcast storms.

**Integration points:**
- Daemon: Register mesh callbacks (OnPeerJoin → trigger discovery refresh)
- Discovery: Merge mesh.ActivePeers() with configured nodes
- Config: Add `mesh:` section to `nodes.yaml`
- CLI: `axis mesh status`, `axis node trust`, `axis node list`

**Test coverage:** 15 tests — lifecycle, HMAC, MaxPeers cap, seed exemption, callbacks.

---

## 2. Reservation Ledger (`internal/reservation/`)

**Problem:** RAM sharing model is heuristic. The current state.ClusterState tracks
reservations but lacks formal accounting, overcommit policy, and cluster-wide
reservation balancing.

**Solution:** Double-entry reservation ledger with:
- **Per-node, per-execution tracking** with owner identity
- **Overcommit ratio enforcement** (configurable, default 1.0 = no overcommit)
- **System reserve** held back from allocation (default 1024MB, matching existing logic)
- **Heartbeat-based liveness** with configurable stale window (default 2min, matching existing)
- **Per-node entry caps** preventing goroutine storms from concurrent reservations
- **Metrics** for observability: total reserved, released, reclaimed, failures

**Integration points:**
- Daemon: Hold the Ledger instance, populate node capacities from snapshots
- Placement: Query `ledger.AllocatableRAM(node)` instead of heuristic free RAM
- Execution: `ledger.Reserve()` on start, `ledger.Release()` on finish, `ledger.Heartbeat()` loop
- API: `/v2/reservations` endpoint
- CLI: `axis reservation list`, `axis reservation clean`

**Test coverage:** 16 tests — reserve, release, overcommit, heartbeat, reclaim, metrics.

---

## 3. Enhanced HTTP API (`internal/api/v2.go`)

**Problem:** The current API surface is functional but minimal. Operators need
richer query endpoints for cluster intelligence, and external integrations
need structured data.

**New endpoints (9):**

| Route | Method | Purpose |
|-------|--------|---------|
| `/v2/cluster` | GET | Full cluster overview |
| `/v2/nodes` | GET | Node list with health + resources |
| `/v2/nodes/:name` | GET | Single node deep-dive |
| `/v2/reservations` | GET/POST/DELETE | Reservation ledger CRUD |
| `/v2/mesh` | GET | Mesh peer state |
| `/v2/placement/dry-run` | GET/POST | Placement simulation (no execution) |
| `/v2/metrics` | GET | Prometheus-compatible text metrics |
| `/v2/batch/place` | POST | Batch placement for multiple tasks |
| `/v2/doctor` | GET | Cluster health diagnostics |

**Design decisions:**
- **Versioned under /v2/** — non-breaking addition to existing routes
- **Same auth model** — Bearer token with constant-time comparison
- **/v2/metrics is unauthenticated** — standard for Prometheus scraping
- **Prometheus text format** — native compatibility with monitoring stacks
- **Batch placement** — up to 50 tasks per request for workflow orchestration

**Integration points:**
- Server: Call `RegisterV2Routes(mux)` from existing `NewServer()`
- Daemon: Wire SnapshotCache, Ledger, and Mesh into Server struct

---

## 4. Structured Safety Engine (`internal/safety/structured.go`)

**Problem:** Current safety blocking is substring-based, risking over-blocking
(e.g., "rm -rf" in quoted echo) and under-blocking (creative evasion).

**Solution:** Structured rule engine with:
- **Command parsing** — splits into program + args, strips env prefixes and paths
- **7 risk categories** — safe, read-only, modify, destructive, network-mutating,
  privilege-escalate, system-critical
- **3 verdicts** — allow, deny, prompt (ask operator)
- **Priority-based evaluation** — higher priority rules match first
- **Glob patterns** — flexible matching without regex complexity
- **Surface-aware rules** — different policies for guarded-exec vs agent-run-shell
- **Learned overrides** — operator approvals persist as auto-allow/deny

**Default rules include:**
- Always-safe: `uname`, `ls`, `ps`, `git status`, `go version`, etc.
- Always-deny: `rm -rf`, `sudo`, `chmod 777`, disk format commands
- Prompt: `git push`, `ollama run`, `systemctl restart`, `curl -X POST`

**Migration path:** Wrap existing `safety.IsBlocked()` to call
`evaluator.Evaluate(cmd, surface)` and map `VerdictDeny → blocked`.
Existing behavior preserved, new capabilities added incrementally.

**Test coverage:** 9 tests — safe/denied/prompt commands, git force-push,
learned overrides, env prefix parsing, pipe handling, category coverage.

---

## 5. Enhanced CLI UX (`cmd/axis/dashboard.go`)

**Problem:** Operators must run multiple commands to understand cluster state.
Output is functional but not visually scannable.

**New commands:**
- `axis summary` — one-shot cluster overview with color-coded health bars
- `axis dashboard` — auto-refreshing TUI (every 5s)
- `axis doctor` — comprehensive health checks with suggested fixes
- `axis node list` — enhanced node table with status icons + pressure colors
- `axis reservation list` — active reservation table with stale detection
- `axis reservation clean` — reclaim stale reservations

**Visual improvements:**
- Unicode box-drawing for section headers
- Color-coded RAM usage bars (green → yellow → red)
- Status icons: ● healthy, ◐ degraded, ○ unreachable
- Pressure highlighting: green/yellow/red by level
- Stale reservation warnings in red

**Integration:** Register new Cobra subcommands in `cmd/axis/root.go`.

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
  5. cmd/axis/dashboard.go — register Cobra commands
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
