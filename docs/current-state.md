# AXIS Current State

This document is the fastest way to understand what AXIS actually is today.

When this file disagrees with older design docs, trust the live code first, then this file, then the older phase/spec material.

Truth rule: no generated output may present itself as cluster truth unless it is backed by a real snapshot or live probe.

## Generated Facts

Refresh this section with `./hack/refresh-current-state.sh`.

<!-- BEGIN GENERATED CURRENT STATE FACTS -->
- Refreshed: 2026-06-29 EDT
- Repo version: `0.12.3`
- Latest published GitHub release: `v0.12.2` (2026-06-19T18:44:30Z)
- Release truth: repo version is ahead of the latest published release
<!-- END GENERATED CURRENT STATE FACTS -->

## Executive Summary

AXIS is no longer just a read-only Phase 1 fact collector.

The live repo currently contains:

- Cluster fact collection for local and remote nodes
- Cluster snapshot assembly and advisory placement
- A local chat surface backed by Ollama
- A local HTTP API with task execution
- A daemon-backed cached snapshot seam behind `axis serve`
- Explicit cached status reads via `axis status --cached`
- Explicit cached placement reads via `axis task place --cached`
- Explicit cached context reads via `axis task context --cached`
- Explicit cache refresh via `axis daemon refresh`
- Explicit cache invalidation via `axis daemon invalidate`
- Content-aware `nodes.yaml`, semantic `state.json`, and `skills.json` watch refreshes in the daemon cache, plus explicit execution-state and long-lived UDP beacon refresh triggers, with refresh-trigger metadata surfaced through daemon health/status
- Typed discovery freshness metadata on live and cached snapshots (`source`, expected/observed window, additive beacon count, completion state, warning)
- A CLI task execution path (`axis task run`)
- A read-only diagnostics + advisory lease MCP server for cluster state, with a per-session cache (`SessionCache`) with a 30s TTL to prevent redundant live discoveries
- In-process snapshot-change hooks on the daemon supporting subscriber callbacks with lock-free dispatch and panic recovery
- Persistent local state in `~/.axis/state.json` and `~/.axis/skills.json`
- Recoverable persistence for corrupt local state/skills files via quarantine + warning
- UDP beacon-based node discovery inside the live snapshot pipeline
- Per-run execution context files instead of shared temp-path injection
- Stateful placement ranking with reservation subtraction, GPU capability scoring (VRAM/vendor/backend match), and full multi-tool enforcement
- Live and cached read paths that both overlay reservations before placement/context generation
- Observed stable node identity when the host exposes it (`machine-id` on Linux, platform UUID on macOS), with locality preferring explicit identity before hostname/address fallback
- Optional `nodes.yaml` `stable_id` seeds and identity-aware UDP beacon dedupe so configured nodes survive hostname/IP drift without overriding observed truth
- Real load-average data in facts, snapshots, and execution context
- TurboQuant-aware backend grading (`mlx`, `llama.cpp`) with detected vs probe-verified states and long-context placement hints
- TurboQuant-aware execution hints: `task run` and `/run` now export `AXIS_TURBOQUANT_*` env vars, with additive `llama.cpp` flag injection only after probe-visible `--ctx-size` support
- Probe-verified local Apple Foundation Models capability on eligible Apple Silicon hosts running macOS 26 or later, surfaced in node facts/snapshots and as an `apple-foundation-models` tool, with actual model execution available only through the explicit guarded execution path
- Additive unified-memory and runtime-pressure metadata in facts (`memory_topology`, `memory_class`, `pressure_source`, `pressure_stall_10`) when the host exposes it
- Resident-model truth in node facts via `resident_models`, populated from `ollama ps`, `llama-server` `/v1/models`, and `mlx_lm.server` `/v1/models`
- Pressure-aware heavy-task filtering that avoids nodes under critical Linux PSI / Darwin VM pressure signals
- Storage class detection (nvme/ssd/hdd) with HDD penalty for heavy inference tasks
- Battery and thermal probing: nodes below 20% battery or under serious/critical thermal throttle are disqualified for heavy inference
- Network topology enrichment: interface name, CIDR subnet, and heuristic speed class (wireguard, tailscale, netbird, thunderbolt, wifi, gigabit, etc.)
- Exact-scope execution observations in persisted local state, separate from failure memory, with mandatory wall time and best-effort RAM/VRAM peaks when directly observed; when a non-empty model name is known, observation scopes are keyed per model so different models on the same node accumulate independent empirical histories, and when no model name is known they intentionally fall back to the legacy non-model key for backward compatibility
- Hard empirical filter in placement: nodes whose freshly-observed `PeakRAMMB` exceeds current allocatable RAM are excluded from `FilterCandidates` before ranking begins
- `axis status` renders a RESIDENT MODELS table showing which models are live on each node, grouped by runtime (ollama, llama.cpp, mlx, apple-foundation-models), with stable canonical ordering and `+N more` truncation for wide lists
- `axis doctor` probes local AI backends (llama-server and MLX) and reports installed/running/port/model-count state as advisory checks; probe errors surface stderr for actionability without blocking core SSH/config checks
- Tombstone immune system: task+node crash history with exponential back-off (24h–7d), automatic placement exclusion, and manual override via ClearTombstone
- Real Git-aware task routing via tool inference, built-in scripts, and repo-analysis workflows
- Protected `main` with PR review, required CI, conversation resolution, and linear history
- Lightweight security automation via Dependabot, `govulncheck`, `SECURITY.md`, and enabled GitHub private vulnerability reporting / automated security fixes
- Shell-quoting-safe remote cleanup traps: variable assignment pattern (`_axis_ctx=QUOTED; trap 'rm -f "$_axis_ctx"' EXIT`) eliminates nested quoting interaction; adversarial test suite covers spaces, quotes, dollar signs, backticks, semicolons
- `ExitCodeError` type for Cobra `RunE` handlers: exit codes propagate through Cobra without calling `os.Exit` directly; `SilenceErrors`/`SilenceUsage` on root command prevents double-printing; `Fatal()` deprecated
- Internal library packages with scaffolding not wired into the CLI operator path: `internal/mesh/` (gossip peer discovery, HMAC-SHA256 auth), `internal/reservation/` (double-entry ledger, wired into task placement as library, no standalone CLI command), `internal/safety/structured.go` (parsed command analysis, learned approvals disabled), `internal/api/v2.go` (active read routes + explicit 501 stubs for unimplemented endpoints)
- `IMPROVEMENTS.md` and `STRUCTURE.md` document the scaffolding scope
- Unified MCP client (`axis mcp client`) with per-server connection caching, retry, batch execution, interactive REPL, placement-aware auto-routing, and progress notifications

The core observation pipeline is reasonably clean. The execution and chat surfaces are where most of the risk now lives.

## Command Surface

Top-level commands currently registered in the binary:

| Command | Purpose | Notes |
| --- | --- | --- |
| `axis version` | Print version | Shows the compiled AXIS version plus commit, build date, go version, and platform |
| `axis facts` | Collect local facts | Human text by default; `--format json\|yaml` for machines |
| `axis status` | Collect cluster snapshot | Colored table by default; `--format json\|yaml` for machines; `--cached` uses the local daemon cache; `--cached-only` fails without daemon |
| `axis task place` | Advisory placement | Human output/JSON; flat `PlacementDecision` output; `--cached` uses the local daemon cache |
| `axis placement explain` | Detailed placement breakdown | Shows eligible and excluded nodes with per-node reasoning; JSON wraps in `explanation` envelope |
| `axis profile match` | Workload class inference | Shows which workload class and requirements an intent maps to; no cluster snapshot needed; `--format text\|json\|yaml` |
| `axis task context` | Emit compact context block | Helper for external agents; `--cached` uses the local daemon cache |
| `axis task run` | Execute on selected node | TTY-aware confirmation prompt; safety-blocked shows `SAFETY BLOCKED`; `--script` or `--exec` required |
| `axis daemon start` | Start daemon HTTP API | Alias for `axis serve`; `--addr` and `--refresh` flags |
| `axis daemon invalidate` | Clear local daemon cache | Explicit operator-controlled cache invalidation |
| `axis daemon refresh` | Refresh local daemon cache now | Explicit operator-controlled cache refresh |
| `axis daemon status` | Inspect daemon freshness | Reports cache readiness, age, version metadata |
| `axis daemon restart` | Restart daemon | Restart the local cache seam |
| `axis chat` | Cluster-aware chat via Ollama | Uses `/api/chat` with structured messages and rolling context; advisory only |
| `axis agent` | Agentic tool-calling assistant | Read-only cluster tools + safety-gated shell; `--auto-approve` for safe commands |
| `axis llm` | Route prompt to local/cloud LLM | `--dry-run`, `--endpoint`, `--format`, `--model`, `--timeout` |
| `axis cortex` | Distributed vector memory | Subcommands: `events`, `recall`, `status` |
| `axis mcp serve` | Start MCP server (read-only diagnostics + advisory leases) | `stdio` transport only |
| `axis mcp client` | Unified MCP client | Subcommands: `list`, `tools`, `call`, `resources`, `read`, `prompts`, `get-prompt`, `search`, `batch`, `interactive`. Per-connection caching (60s TTL), retry with exponential backoff, placement-aware `--auto-route`, progress notifications, and REPL with 10 commands |
| `axis serve` | Start local HTTP API | Includes execution surface |
| `axis update` | Self-update binary | Safely replaces only the current executing binary with the latest release, verifying SHA-256 via `checksums.txt`. Package-manager aware (refuses to break immutable Nix/Homebrew paths). Use `--all` to upgrade all `$PATH` matches. `--check` reports only |
| `axis context show\|clear` | Inspect or clear placement memory | Uses persisted state |
| `axis scripts list` | List built-in scripts | Registry includes destructive scripts |
| `axis skills` | Show learned skills | Uses persisted skill store |
| `axis doctor` | Validate config, SSH, daemon health, and local AI backends | Checks config, TCP probes per node, daemon status; probes llama-server and MLX as advisory checks; `--strict` promotes daemon failure to core |
| `axis completion` | Generate shell completions | bash/zsh/fish/powershell |

## Package Map

| Package | Role | Current Maturity |
| --- | --- | --- |
| `internal/ui` | Terminal output: colors, tables, spinners, errors, help templates | Well-tested; auto-disables on non-TTY or `NO_COLOR` |
| `internal/buildinfo` | Version, commit, date, go version for ldflags injection | Small, stable |
| `cmd/axis` | CLI entrypoint and command wiring | Broad surface area, mixed behavior, low command-level coverage |
| `internal/config` | Load and validate `~/.axis/nodes.yaml` | Small, stable, and now rejects unknown YAML fields so config typos fail fast |
| `internal/facts` | Local/remote hardware + tool collection | Local RAM/disk parsing is less brittle now; remote collection is still round-trip heavy, and the local path can now probe-verify Apple Foundation Models on eligible Macs in addition to TurboQuant/backend metadata |
| `internal/discovery` | Fan-out discovery and UDP beacons | Node ordering is stabilized, live discovery now emits typed freshness metadata for the bounded beacon window, and the daemon cache has a long-lived beacon watcher for event-driven refreshes |
| `internal/snapshot` | Build `ClusterSnapshot` | Best-tested package in the repo |
| `internal/daemon` | Background snapshot refresh and cache metadata | Small, explicit seam; now powers cached reads, invalidate, reservation-aware snapshot views, trigger-aware refresh metadata, and cached discovery-freshness exposure for config/state/skills, execution events, and long-lived discovery beacons |
| `internal/models` | Shared types and locality helpers | Types are fine; locality now prefers observed stable identity, then hostname/address matches, over logical names |
| `internal/placement` | Requirement inference, filter, rank, select | High unit coverage; allocatable-RAM-first ranking, fresh exact-scope empirical preferences (keyed per model name), hard `PeakRAMMB` pre-filter, resident-model locality, reservations, GPU preference, multi-tool requirements, TurboQuant-aware long-context hints, unified-memory bonuses, critical-pressure heavy-task filtering, and explicit local-only Apple Foundation Models qualification are now live |
| `internal/state` | Persist placement memory | Explicit acquire/release is live and tested; state now also stores exact-scope execution observations separately from failure memory |
| `internal/knowledge` | Build execution context blob | Load-aware, nil-safe, and now heavily covered |
| `internal/scripts` | Built-in task scripts | Useful; `jq` prerequisites are now modeled explicitly, but broader shell assumptions are still under-modeled |
| Git-oriented execution surfaces | Repo analysis, status, and review helpers | Promising lane; useful already, but should become more explicit and first-class |
| `internal/skills` | Learned skills/failures | Persists state, now recovers from corrupt JSON, but semantic validation is still light |
| `internal/safety` | Execution blocker + structured command analysis | Heuristic substring blocker is well unit-tested; structured command analysis scaffolding exists but learned approvals are deliberately disabled and NOT wired into the operator path |
| `internal/transport` | SSH execution layer | Respects OpenSSH-resolved identities and known_hosts paths; integration coverage still needs to grow, but baseline unit coverage is now solid |
| `internal/api` | Local HTTP API and execution surface | High-risk surface, now above the v1 coverage gate with injectable execution seams |
| `internal/mcp` | Read-only MCP surfaces | Diagnostic layer now shares the live runtime path and meets the v1 coverage gate |
| `internal/mcpclient` | Unified MCP client library | Connection pooling, per-server caching (60s TTL), retry with exponential backoff, progress notifications, placement-aware routing, batch execution, and metrics collection; powers `axis mcp client` |
| `internal/persist` | Corrupt-file recovery helpers | Small helper package used for quarantine + warning recovery |
| `internal/runtimectx` | Unified live runtime loader | Centralizes config + discovery + overlay + warning assembly for live reads |
| `internal/chat` | Structured /api/chat client | Rolling context window, system prompt builder, model catalog |
| `internal/agent` | Tool-calling agent loop | Read-only tools (status, facts, place) + safety-gated shell with adversarial tests |
| `internal/mesh` | Gossip peer discovery | Scaffolding only; HMAC-SHA256 auth, 5-state lifecycle; NOT wired into CLI operator path |
| `internal/reservation` | Double-entry reservation ledger | Used as library by task placement; `/v2/reservations` exists but returns 501 pending ledger integration; no standalone CLI command |
| `internal/workload` | Workload profile matching | Powers `axis profile match`; deterministic class inference for 8 workload classes |
| `internal/llmrouter` | Hybrid AI model routing | Powers `axis llm`; local/cloud provider registry with semantic reflex classification |
| `internal/cortex` | MCP client for cluster brain | Powers `axis cortex`; FastMCP 3.x Streamable HTTP protocol |

## Verification Snapshot

Refresh this section with `./hack/refresh-current-state.sh`.

<!-- BEGIN GENERATED CURRENT STATE VERIFICATION -->
- `go test ./... -count=1` -> passes
- `go test -race ./... -count=1` -> passes
- `go build ./...` -> passes
- `./hack/coverage-check.sh` -> passes
  - Coverage gates:
    - `coverage gate passed: internal/knowledge 92.9% >= 90.0%`
    - `coverage gate passed: internal/api 80.9% >= 50.0%`
    - `coverage gate passed: internal/mcp 87.7% >= 35.0%`
    - `coverage gate passed: internal/ui 90.4% >= 80.0%`
    - `coverage gate passed: total 69.3% >= 45.0%`
<!-- END GENERATED CURRENT STATE VERIFICATION -->

## Degraded-State Matrix

These are the operator-facing degraded-state contracts currently locked in by tests:

| Condition | CLI contract | API contract | MCP contract | Recovery action |
| --- | --- | --- | --- | --- |
| Corrupt `~/.axis/state.json` | `axis context show` warns on stderr and still emits valid JSON | live reads continue; state warning is included in snapshot warnings | `placement_decision` prepends a state warning to reasoning | file is quarantined to `.corrupt-*`; empty in-memory state becomes authoritative until next write |
| Corrupt `~/.axis/skills.json` | `axis skills` warns on stderr and still emits valid JSON | live reads continue; skills warning is included in snapshot warnings | `placement_decision` prepends a skills warning to reasoning | file is quarantined to `.corrupt-*`; empty in-memory skills store becomes authoritative until next write |

The key point is that local persistence is treated as recoverable operator memory, not hard cluster truth.

## Reality Check

Areas where the live repo has moved past the older docs/specs:

- The repo has a local HTTP server and execution surface
- The repo persists state and learned skills to disk
- The repo includes task execution, not just advisory placement
- The repo includes UDP discovery and MCP diagnostics
- The repo now includes an explicit daemon-backed snapshot cache, not just ad hoc live discovery
- Cached reads, cache refresh, and cache invalidation are explicit operator actions, not hidden behavior
- Placement now subtracts reserved RAM during ranking, prefers GPU nodes after pressure, and enforces full script toolchains
- Live status/placement/context reads now use the same reservation overlay model as cached reads
- Corrupt local state/skills files are now quarantined and surfaced as warnings instead of collapsing read surfaces
- Execution context now carries real load averages rather than placeholder zeros
- Long-context task hints can now prefer graded TurboQuant-capable backends without changing the default placement contract for ordinary tasks
- Execution paths can now carry TurboQuant hints into local and remote commands, and can add an observed-state-gated ephemeral Nix helper wrapper when the selected node proves it has `nix` plus a trusted package mapping
- Facts and placement now carry additive unified-memory and runtime-pressure metadata instead of relying only on coarse free-RAM pressure guesses
- Live and cached snapshot-bearing surfaces now expose the same typed discovery-freshness contract instead of relying only on free-form warnings
- Placement now consumes fresh exact-scope empirical history and truth-backed resident-model locality as additive preferences instead of relying only on static heuristics
- Empirical observation scopes are now keyed per model name so different models on the same node do not contaminate each other's peak RAM history
- Placement hard-filters nodes before ranking when a fresh empirical `PeakRAMMB` observation exceeds the node's current allocatable RAM, making the empirical arc load-bearing rather than advisory
- Resident-model facts now include llama-server and MLX in addition to Ollama, making the resident-model view multiruntime
- `axis status` now surfaces a live RESIDENT MODELS table so operators can see at a glance which models are running where and under which runtime
- `axis doctor` now probes local AI backends and reports their state as advisory checks alongside the existing SSH/config/daemon checks
- `axis placement explain` provides a detailed placement breakdown showing eligible and excluded nodes, distinct from the simpler `task place` output
- `axis profile match` shows workload class matching and requirement inference without needing a cluster snapshot
- `axis task run` now has a TTY-aware confirmation prompt and uses `SAFETY BLOCKED` instead of the earlier `BULLSHIT BLOCKED` banner
- `axis daemon start` is an alias for `axis serve`, making daemon lifecycle more discoverable
- Git-aware workflows are already a meaningful part of AXIS behavior, not just incidental tool detection

That does not mean the execution model is fully hardened yet. It means the codebase should now be understood as a hybrid of observability, advisory placement, and early execution tooling.

## RAM Sharing Intent

The intended purpose of AXIS RAM balancing is cluster-level memory sharing.

AXIS should treat node RAM as a resource the cluster tries to balance across
machines, not just as a local anti-overcommit guard.

In practical terms:

- `RAMFreeMB` is the live observed memory on a node
- AXIS now derives a shared reservable pool as `min(RAMFreeMB, RAMTotalMB - 1024MB system reserve)`
- persisted reserved memory is a soft claim against that node's contribution to
  the shared cluster memory pool
- late placement ties now break toward the node holding a smaller share of the
  current cluster reservation pool
- placement should use those soft claims to spread memory pressure across the
  cluster instead of repeatedly selecting the same node

## Current Weak Spots

- Placement state accounting now subtracts reserved RAM correctly and releases on completion, but the broader RAM-sharing model is still heuristic
- The current balancing model now has an explicit reservable/allocatable system-reserve floor, a cluster-reservation-share tie-break, explicit per-exec reservation heartbeats, owner-PID reclaim for local AXIS processes, persisted local caller provenance across guarded execution surfaces, runtime-derived local execution-origin identity (node / hostname / stable ID when observable), signed forwarded upstream origin acceptance on trusted HTTP execution boundaries, and exact-scope empirical observations, but it still lacks multi-hop or cross-cluster exec identity beyond a single AXIS token trust domain
- TurboQuant grading is still heuristic today; AXIS now distinguishes detected vs probe-verified backend responses, but it still does not verify kernel correctness or backend feature parity
- TurboQuant execution injection is intentionally narrow today: env hints everywhere, direct flag injection only for probe-verified `llama-cli` / `llama-server` command lines that expose `--ctx-size`
- Execution confirmation and reservation caps are now explicit across CLI and HTTP, but the UX and error contracts still differ between surfaces
- Chat now uses structured `/api/chat` with a rolling context window and cluster-aware system prompt, but output remains advisory
- Agent tool-calling loop has safety gates and adversarial error recovery, but is still subordinate to observed state
- Locality and discovery can now use optional stable IDs, but those IDs are still operator-seeded hints until a live probe confirms observed identity
- Network `speed_class` remains heuristic metadata today, not measured throughput or latency
- UDP discovery now has both a long-lived daemon watcher for beacon-triggered cache refreshes and a typed freshness contract for the ad hoc live-discovery accumulation window, but broader runtime hardening is still needed
- Safety blocking is substring-based and can both over-block and under-block
- Built-in script prerequisites now model `jq` explicitly, but broader shell assumptions are still under-modeled
- Git-aware workflows exist, but there is no dedicated doctrine/runbook/test layer for “AXIS as a Git expert” yet
- degraded-state contracts are now stronger, but concurrency around simultaneous first-read / first-write recovery is still only indirectly exercised
- The daemon cache still relies on a timer for full probe freshness, but config/state/skills changes, explicit execution events, and long-lived UDP beacon changes now trigger immediate refreshes and surface their trigger explicitly; heartbeat-only `state.json` writes are filtered out so guarded-run liveness does not cause cache-refresh churn
- `axis task run`, HTTP `/run`, and approved `axis agent` shell execution now share the same guarded execution path and persist both internal caller provenance and runtime-derived local origin identity; both `axis task run` and `axis agent` now prefer the local daemon/API `/run` client when available, and the streamed NDJSON contract preserves ready-time placement output, explicit state changes, and live stdout/stderr across that hop, but broader runtime identity is still limited to one AXIS token trust domain rather than a richer distributed or multi-hop model
- The streamed `/run` contract now carries explicit execution state-change events (`execution-reserved`, `execution-finished`) in addition to ready/output/final-result events, cross-boundary callers can replay those into their existing callback hooks instead of losing reservation/finish semantics at the daemon/API seam, and early runtime/load failures now stay inside that same final-result stream contract instead of falling back to buffered JSON
- Local daemon `/run` callers now share one execution transport path, and that path no longer inherits the short snapshot/meta HTTP timeout, so long-running daemon-hop executions are bounded by caller context instead of a fixed 5-second client timeout
- `axis status`, `axis task place`, and `axis task context` now overlay local reservation state on live reads and can surface typed discovery freshness in machine-readable output, but cache provenance is still only explicit on cached-path output
- Most read surfaces still hit live discovery by default unless `--cached` is used explicitly
- Daemon freshness policy: 7 refresh triggers (startup, interval, manual, config-change, state-change, skills-change, beacon-change) plus execution events; staleness threshold is configurable (default 5 min, exposed via `stale_threshold_sec` in metadata); `--cached` is explicit and operator-facing, never a hidden fallback

## Recommended Next Sequence

V1 hardening is now mostly about durability, not feature growth:

1. Keep this file, `README.md`, `SECURITY.md`, and the CI/release/security workflows current as the orientation layer.
2. Keep Dependabot and `govulncheck` green so the protected-merge path stays actionable instead of noisy.
3. Push resident-model VRAM peak probes where truth-backed process metrics are available; the multi-runtime resident-model view (Ollama + llama-server + MLX) is now live.
4. Refine reservation accounting into a clearer cluster RAM balancing model.
5. Add more SSH/integration coverage around the transport layer and end-to-end execution paths.

## Decommissioned

- `cmd/axis/discover.go` — removed. Use `axis status` for discovery-backed cluster views.
- Config is strictly validated; unknown keys in `~/.axis/nodes.yaml` now cause immediate failure.
