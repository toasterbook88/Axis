## Unreleased

### 🚀 Features
* **Doctor:** Skip remote SSH and shell probes for the local node (avoids false pubkey fail on self Tailscale IP).
* **Config:** `Normalize()` for Hostname/StableID shims; pure `Validate()`; `Load` runs both; `Save` does not invent hostname on disk.
* **Facts:** One-shot remote fact bundle (single bash session) with legacy multi-probe fallback — core + thermal/storage/tools coverage, far less sensitive to slow login shells (e.g. fish+conda).
* **Facts:** Portable bash launcher for remote probes (`command -v bash` + FHS + NixOS paths); no hard-coded `/bin/bash`.
* **Config:** `collect_timeout_sec` / `dial_timeout_sec` (defaults: collect floor 45s, dial inherits `timeout_sec`); optional `endpoints[]` for LAN+Tailscale dial targets with fallback; `SSHDialSpec()`.
* **Config:** `MembershipFingerprint()` for stable cluster membership identity (name/role/user).
* **Models:** `PartialReasons` + `FormatPartialReasons` for probe-level partial diagnostics.
* **Doctor:** Membership fingerprint in config check; mDNS `.local` seed warning; dial/collect timeout display; remote shell cost probe (slow login shell advisory).
* **Transport:** Dial fallbacks + `ConnectedHost()`; handshake bounded by dial timeout (not full collect context).

### 🐛 Bug Fixes
* **Discovery:** Locality matches PrimaryHostname and all dial hostnames (endpoints-aware).
* **Discovery:** Use collect timeout (not short dial timeout) as the full remote fact budget so multi-probe/slow-shell nodes complete instead of silent partials.
* **Transport:** Endpoint fallback uses logical SSH alias names (preserves HostKeyAlias/IdentityFile); dial timeout caps handshake so stalled peers do not burn collect budget.
* **Facts:** Bundle path fills ThermalState/ThermalZones (placement safety), tool versions, and mapper-aware storage when needed; SSHTarget tracks connected endpoint after fallback.
* **Execution/MCP/Agent/Chat:** Propagate `endpoints[]` dial fallbacks through guarded exec, MCP, agent remote tools, and chat tunnel routing.

## v0.14.3 (2026-07-15)

### 🚀 Features
* **CLI:** Professionalize `axis init` onboarding — first-run vs update flows, discovery paths, validated atomic config saves with backups. (#230)

### 🐛 Bug Fixes
* **Events:** Harden event log isolation and flush; restore JSONL decoder; non-fatal init host:port duplicate handling. (#229, #230 follow-ups)
* **Static analysis:** Resolve lint/static-analysis warnings including safe JSON-RPC request ID marshaling. (#226)
* **UI:** Resolve repeating title bug in terminal select menu. (#222)

### 🔧 Maintenance
* **Release:** Harden release workflow dry-run, gate parity with CI, strict version parse, asset checksum verification. (#231)
* **Docs/agents:** Restore thin Copilot entry point; fix AGENTS.md mesh/reservation wiring claims; sync dependencies with go.mod. (#227, #228)
* **Architecture:** Invert L4→L5 dependencies in safety/execution. (#225)
* **Daemon:** Lock-free snapshot reads and hardening scaffolding. (#223)
* **Deps:** Bump go-modules minor/patch group. (#224)

## v0.14.2 (2026-07-12)

### 🚀 Features
* **Transport:** SSH multipath routing with endpoint authority. (#213)
* **Agent:** Endpoint authority and resident model picker. (#212)

### 🐛 Bug Fixes
* **Multipath:** Activate multipath routing + resident-model ports; filter Docker bridge IPs and fix `probeSSH` return. (#214, #219)
* **API:** Require bearer-token auth on `/debug/pprof` endpoints. (#216)
* **State/API:** Harden state updates and API safeguards. (#220)
* **Error handling:** Propagate errors that were silently swallowed. (#217)
* Close P0 residuals (daemon restart, PR helper, docs). (#211)

### 🔧 Maintenance
* **Refactor:** Consolidate `~/.axis` path and atomic-write duplication into `persist`. (#218)
* **Tests:** Cover previously untested internal packages. (#215)
* **Docs:** Post-release current-state facts (v0.14.1). (#210)

## v0.14.1 (2026-07-10)

### Fixes
* **Placement network class:** Classify by SSH dial target (`NodeFacts.SSHTarget`), not observed machine hostname. LAN-reached nodes no longer take a false Tailscale/VPN −20 penalty when overlay interfaces are present. Tailscale IPv6 ULA and public-dial edge cases handled. (#206)
* **make lint fail-closed:** Propagate `gofmt` process failures so a missing/crashing gofmt cannot pass CI. (#208)
* **CI:** Run push CI only on `main` (stop double Test & Build on in-repo PR pushes); invoke `make lint` in CI; remove permanently disabled Claude review workflow. (#207)

### Maintenance
* **`make install-user`:** Install to `~/.local/bin` with commit/date ldflags (matches operator `axis update` path). (#207)
* **Release truth:** Drop live `published_at` timestamps from generated `docs/current-state.md` facts; disable broken auto-refresh workflow that could not open PRs under branch protection. GitHub Releases remain authority. (#207)
* **Docs:** Honest merge-policy description; tag-only GoReleaser release process; daemon restart guidance. (#207)
* **`hack/pr-review-cycle.sh`:** Fail-closed required-check helper for the solo-operator PR loop (collection only; no auto-merge). (#208)

## Unreleased

## v0.14.0 (2026-07-09)

### 🚀 Features

* **Agent Harness (P0–P3):** Turn `axis agent` into a distributed, cluster-native agent harness (32 tools, was 13).
  * Parallel tool dispatch within a turn (bounded worker pool); results append in tool-call order. LLM-backed context compaction at 70% of the token budget.
  * Robust editing: `multi_edit` (batch edits to one file, atomic on failure), `edit_file` `replace_all`, `undo_last`/`review_changes` via a session checkpointer.
  * In-session plan tracking (`todo`) and conversation branching (`branch_session`/`rollback_session`).
  * `symbol_search` (Go-AST-aware definition lookup + generic fallback), `web_fetch`/`web_search`.
  * Remote tool surface: `run_on_node`, `remote_read_file`, `remote_grep`, `remote_list` — the cluster as an extended workspace.
  * Cluster-aware context: a live cluster snapshot (node health, free RAM, resident models) injected into the system prompt every turn.
  * Distributed sub-agents: `spawn_subagent` delegates a focused sub-task to a child agent running its own tool loop on a target cluster node.
  * Autonomy modes (`--autonomy default/edit/full`) and multi-model routing (`--cheap-model`).
  * Background/async tasks: `run_background`/`check_task`/`list_background_tasks`.
  * Cortex as native cluster memory + coordination: when Cortex MCP tools are connected, the agent proactively recalls, remembers, locks shared files, and publishes events.
* **Placement:** Route-based network classification — a node reached over a private LAN address is classified `direct-lan` even if it also carries a Tailscale/VPN interface (previously penalized −20 for the overlay interface alone). Handles IPv4-mapped IPv6 and IPv6 ULA.

### 🔧 Maintenance

* Bump Go toolchain to 1.26.5 (fixes GO-2026-5856, Encrypted Client Hello privacy leak in crypto/tls).

## v0.13.0 (2026-07-07)

### 🚀 Features

* **Routing & Placement:**
  * Implement Intelligent Auto-Routing for `axis chat` with zero-latency SSH tunneling and `modelWarmthRank` support.
  * Implement v2 placement endpoints.
* **Execution & Safety:**
  * Add `--expose-port` flag for task execution.
  * Swap old blocker with the new Structured Safety Engine.
* **UX & Networking:**
  * Zero-config Tailscale auto-discovery for cluster setup.
  * Agent REPL UX improvements.

### 🔧 Maintenance

* Update Go dependencies.
* Safety/speed class allocations optimizations.

## v0.12.3 (2026-06-29)

### 🚀 Features

* **Network & Topology Enrichment:**
  * Wire local identity and ignore docker bridges.
  * Add secondary disk probing.
  * Measure explicit network `speed_class` via sysfs to improve deterministic classification across hosts.

### 🐛 Bug Fixes

* **Parser & Facts Refinement:**
  * Normalize timestamps to UTC to resolve cross-timezone issues.
  * Add execution timeout to the local `df` command and fix trailing space parsing for mount points.
  * Optimize `shellSplit` allocations and improve word tracking logic in the structured safety parser.

### 🔧 Maintenance

* Dependency bumps: `github.com/mark3labs/mcp-go` (v0.55.1), `actions/checkout` (v7.0.0), `actions/setup-go` (v6.5.0).
* Documentation: sanitize local paths in `session-handoff.md` and reconcile stale facts.

## v0.12.2 (2026-06-19)

### 🚀 Features

* **Distributed Model Planner (PR #175):**
  * Snapshot-backed multi-node pipeline and single-node advisory placement planner.
  * Support for exact layer memory mapping, context window cache scaling, and freshness validation.
* **Interactive CLI UI:**
  * Added interactive selection dropdown UI, live remote Ollama probing, `/mcp` diagnostics, and active SSH verification during initialization.

### 🐛 Bug Fixes

* **Planner Tie-Breaker Logic:**
  * Fixed `betterLink` selection logic bug where alphabetical sorting on identical link qualities incorrectly overrode downstream candidate memory checks (`betterCandidate`).
* **Test Stability:**
  * Resolved an asynchronous race condition in the MCP lifecycle event test suite (`TestLifecycleEventTools`).

## v0.12.1 (2026-06-18)

### 🔒 Security

* `axis agent` now classifies LLM backends as local or remote and restricts untrusted evidence for remote backends.
  * Added `BackendSecurityClass` to `agent.Config` and `Agent`; constructor code paths must declare locality explicitly.
  * Remote backends receive only structured skill metadata (ID, success count, timestamps, preferred node, node success counts); free-form descriptions, decision summaries, and raw commands are excluded.
  * Local backends continue to receive descriptions and decision summaries; raw command text is redacted by default and only included when `--allow-raw-command-evidence` is set.

### 🔧 Internal

* Encapsulated `ToolContext` in `internal/agent`: the current runtime view is stored in an unexported `atomic.Pointer` and reloaded atomically via `NewToolContext`, `Current`, and `ReloadCurrent`. Incomplete reloads preserve the previous view.
* Hardened `axis llm configure` and `axis llm select` key/config file transactions:
  * Symlink rejection via `os.Lstat`.
  * `O_CREATE|O_EXCL` temporary files with `fsync`, atomic rename, and parent-directory `fsync`.
  * Advisory file locking on `nodes.yaml.lock` and SHA-256 hash check to detect concurrent modification.
  * `llm configure` rolls back only a newly created key file when YAML validation fails; pre-existing keys survive.
  * Inferred cloud provider `kind` is persisted into `nodes.yaml` AST automatically.
* Fixed portability and resource-handling issues from PR 173 review:
  * Replaced direct `syscall.Flock` with portable `internal/lockutil` package.
  * Closed temporary files before removal to support Windows cleanup.
  * Always close HTTP response bodies and defer context cancellation in the `/reservations` slash handler.
  * Rewrote command redaction with a single-pass parser and optimized payload truncation.
  * Added a nil guard for runtime snapshots during guarded execution preparation.

## v0.12.0 (2026-06-17)

### 🚀 Features

* **Cloud LLMs + premium terminal UI/UX (#171):** Support cloud LLM backends with local capability checks and a premium terminal UI/UX.
* **Reservations doctor (#169):** `axis reservations doctor` diagnoses reservation inconsistencies, stale leases, and memory leaks.
* **Pressure-aware lease-based cluster RAM balancer (#166):** Scheduler shifts from instantaneous free memory to allocatable headroom, leased soft-claims, and Linux kernel PSI.
* **Cluster topology, interactive LLM select, nested parent reservations (#165).**
* **Git-aware interactive preflight checks for dirty trees (#164).**
* **Dynamic local-port forwarding over SSH for remote tasks (#163).**

### 🔧 Maintenance

* Make stash preflight test hermetic; refresh docs (#168).
* Make LLM select command tests hermetic (#167).
* Bump go-modules-minor-patch group with 2 updates (#170).

## v0.11.0 (2026-06-10)

### 🚀 Features

* **Structured AXIS lifecycle events + flock rotation (#160).**
* **Git Intelligence for workspace context (#156).**
* **`axis init` CLI command, mesh gossip peer diagnostics, and topology (#161).**
* **Generic SSH connection latency and path classification (#159).**
* **Per-session MCP cache and daemon snapshot hook deadlock fix (#153).**
* **Ollama warmth lifetime scoring as a bounded placement tiebreaker (#151).**

### 🐛 Bug Fixes

* Print CLI execution errors to stderr before exiting (#157).
* Resolve daemon data race and wire MCP snapshot cache invalidation (#155).

### 📚 Documentation

* MCP defense-in-depth paragraph and assertion test (#150).
* Update agent worklog (#158).

### 🔧 Maintenance

* Bump Go toolchain 1.26.3 → 1.26.4 (#154).
* Bump actions/checkout 6.0.2 → 6.0.3 (#152).

## v0.10.9 (2026-06-01)

### 🚀 Features

* **Phase 1 advisory leases and structured safety evaluator (#144):** Adds the Triangle advisory lease MCP tools (`triangle_request_lease`, `triangle_release_lease`, `triangle_heartbeat_lease`) and the structured safety evaluator.
* **Per-node configurable system RAM reserve (#147).**

### 🐛 Bug Fixes

* Resolve PR #145 post-merge bugs in api/v2 and mcp (#146).
* Make `TestLocalCollectorCollectsFacts` hermetic (#148).
* Correct release badge to track tag pushes (#143).

### 🔧 Maintenance

* Add safety benchmarks and enable structured evaluator tests (#145).
* Version bump to v0.10.9 (#149).

## v0.10.8 (2026-05-29)

### 🚀 Features

* **Unified MCP client (#140):** prompts, caching, retry, batch, REPL, metrics, and auto-routing.
* **Execution observations in `axis task run` + `axis observations` CLI (#139).**

### 🔧 Maintenance

* Bump `github.com/mark3labs/mcp-go` 0.54.0 → 0.54.1 (#141).
* Release v0.10.8 (#142).

## v0.10.7 (2026-05-22)

### 🚀 Features

* New `axis daemon mesh` subcommand for operator mesh introspection.
  * Queries the daemon's `/mesh` endpoint and displays active gossip peers in a table.
  * Shows peer name, hostname, state, source, and relative last-seen time.
  * Handles empty peer lists and "mesh not available" gracefully.

### 🔧 Internal

* Added `Mesh() *mesh.Mesh` to the `daemon.SnapshotCache` interface and implemented it on `*Daemon`.
* Added `/mesh` handler to the daemon router (`internal/daemon/handlers.go`).
* Added `MarshalJSON`/`UnmarshalJSON` to `mesh.PeerState` so the API serializes states as human-readable strings (`"discovered"`, `"verified"`, `"trusted"`, `"suspect"`, `"dead"`).
* Refactored daemon HTTP request creation into shared `newDaemonRequest` helper with consistent auth handling.

## v0.10.6 (2026-05-22)

### 🚀 Features

* Surface hidden hardware facts in `axis facts` and `axis status`:
  * `axis status` table expanded with **STORAGE** and **GPU** columns.
  * `axis facts` now shows storage class, GPU details (vendor, VRAM, capabilities), thermal state, power source, battery level, load averages, memory topology, and network addresses with interface/speed class.
  * Refactored GPU name formatting into shared `formatGPUBaseName` helper to ensure consistent vendor redundancy handling and "unknown" filtering across commands.

## v0.10.5 (2026-05-22)

### 🚀 Features

* `axis chat` and `axis agent` UX improvements:
  * Signal context wiring (Ctrl+C interrupts all modes: single-shot, resume, REPL).
  * `--verbose` flag prints model auto-detection, turn progress, and tool parameters.
  * `--dry-run` flag on `axis agent` skips tool execution while preserving reasoning loop.
  * Cobra output compliance (`--no-color`, redirection) across all chat/agent handlers.
* Placement explain now displays **headroom** (allocatable minus reserved) per candidate.
* New Copilot CLI skill: `.github/copilot/skills/pr-review-responder.yml` — codifies the PR lifecycle workflow (prepare → push → monitor → fix → respond → verify).

### 🔧 Maintenance

* Upgrade `golang.org/x/crypto` v0.51.0 → v0.52.0 to resolve `govulncheck` findings (GO-2026-5013 through GO-2026-5021).

### 📚 Documentation

* Refresh `docs/current-state.md` for v0.10.5 release.

## v0.10.4 (2026-05-20)

### 🔧 Maintenance

* Extend resident-model VRAM probes to llama-server and MLX backends. `LlamaServerDiscoveryScript` now stats the model file to compute `size_vram_mb`; `MLXDiscoveryScript` now queries process RSS to compute `size_vram_mb`. Previously only Ollama populated this field.

### 📊 Observability

* `axis summary` now displays allocatable RAM alongside reserved RAM, making the cluster RAM accounting model explicit to operators. Uses `snap.Summary.TotalAllocatableMB` computed by the reservation overlay.
* `axis status` table now shows **allocatable RAM** as the primary metric (replacing raw "RAM FREE"). When a node carries active reservations, the reserved amount is shown in parentheses (e.g. `6144 MB (1024 reserved)`). Falls back to raw free RAM when the reservation overlay has not been applied.

### 🔧 API & Doctor

* `POST /v2/reservations` now returns `405 Method Not Allowed` instead of `501 Not Implemented`, making the read-only API contract explicit per `docs/decisions/v2-reservations-endpoint.md`.
* `axis doctor` now probes the **Ollama** local AI backend alongside llama-server and MLX, ensuring parity with the primary inference backend used throughout the project.

## v0.10.3 (2026-05-18)

### 🐛 Bug Fixes

* Prefer tool-capable models when auto-selecting for `axis agent` — prevents 400 Bad Request when the alphabetically first installed model (e.g. `gemma3n:e2b`) does not support Ollama tool calling. Falls back to known tool-capable families (llama3.1, qwen3.5, qwen3, etc.) and skips embedding/vision variants (PR #127).

### 🔧 Maintenance

* Extract `state.Maintain()` from `state.Load()` — eliminates repair-on-read side effect, making `Load()` idempotent and preventing silent `state.json` rewrites on every CLI invocation (PR #126).
* Update summary golden files to reflect version bump.

### 📚 Documentation

* Add `docs/roadmap-status.md` — final status of all 53 v9 roadmap items (48 done, 5 Phase G items blocked by evidence discipline).

## v0.10.2 (2026-05-17)

### 🚀 Features

* Daemon health endpoints (`axis daemon status`) with reservation count and last-refresh timestamp.
* Placement ranking 54% faster for 50-node clusters via unified memory caching.

### 📚 Documentation

* Refresh `docs/current-state.md` for v0.10.2 release.

## v0.10.1 (2026-05-06)

### 🚀 Features

* Integrated reservation-ledger-wiring enhancements:
  * Power source detection (AC/battery) for better placement decisions
  * Thermal state monitoring to avoid throttled nodes
  * Thermal zones enumeration for detailed thermal monitoring

### 📚 Documentation

* fac90d9: chore: refresh current-state.md for v0.10.0 release

### 🐛 Bug Fixes

* None in this release

### 🔧 Maintenance

* Version bump to 0.10.1

## v0.10.0 — Operator-honest groundwork: shell safety, reservation ledger, mesh scaffolding

**Shell quoting vulnerability fix (PR #96)**

Remote cleanup traps in `runRemote` used `trap 'rm -f QUOTED_PATH' EXIT`, which
created a nested quoting interaction: `shellescape.Quote` wraps paths in single
quotes, and a single-quoted path containing a single quote produces an unparseable
trap body. Replaced with variable assignment pattern
`_axis_ctx=QUOTED; trap 'rm -f "$_axis_ctx"' EXIT`, which eliminates the nesting
entirely. The heredoc delimiter changed from `EOF` to `AXIS_EOF`. An adversarial
test suite covers paths with spaces, single quotes, dollar signs, backticks,
and semicolons.

**Cobra error handling overhaul (PR #96)**

`os.Exit` and `Fatal` calls in Cobra `RunE` handlers skip Cobra's cleanup. Added
`ExitCodeError` type carrying both an exit code and a user-facing message, with
`errors.As`-based unwrapping. Root command now uses `SilenceErrors`/`SilenceUsage`
to prevent double-printing. All `RunE` handlers in placement, task, agent, and
chat commands converted. `Fatal()` marked as deprecated.

**v0.10.0 groundwork (PRs #94, #95)**

* `POST /v2/batch/place` returns `501 Not Implemented` instead of synthetic `200 OK`
* Reservation accounting fails closed when node capacity is unknown
* Structured safety learned approvals deliberately disabled (program-name-only too broad)
* Mesh gossip remains internal scaffolding; HMAC present, replay protection not enforced
* Dashboard/rendering helpers present but not registered as CLI commands
* Release pipeline and GoReleaser improvements

**AX-005/006/007/024/025 integration (PR #93)**

* Link-local addresses tagged with `scope: "link-local"` instead of silently dropped
* SSH `IdentitiesOnly yes` from config now respected (skips agent, default keys)
* `ssh -G` passes `-F` for correct config file resolution on macOS
* Cached-reads doctrine documented: explicit, operator-facing, no hidden fallbacks
* Daemon staleness threshold configurable (default 5 min)

---

## v0.9.0 — Cortex MCP client, hybrid AI router, VRAM observation

**`axis cortex` MCP client (PR #88)**

New command connects to the AXIS Cortex cluster brain via MCP protocol, supporting
tool discovery, resource listing, and prompt execution. Aligns with FastMCP 3.x
Streamable HTTP protocol. Timeout increased to 45s for recall operations.

**Hybrid AI router (PRs #84–#87)**

Three-phase `axis llm` implementation: provider registry + config + model listing
(Phase 1), semantic reflex classification + `axis llm` command (Phase 2), cloud
provider module with OpenRouter/Groq/Anthropic support + secrets management (Phase 3).
Local model auto-selection when no model is recommended.

**Ollama VRAM observation (PR #76)**

Resident model VRAM usage surfaced in `axis status` output column. Unknown VRAM
shown explicitly rather than silently omitted.

---

## v0.8.0 — Empirical placement arc + multiruntime resident models + doctor AI checks

**Empirical placement arc (PRs #66–#71)**

The v0.8.0 release lands the full empirical placement arc. Prior releases tracked
execution observations but only used them as soft ranking bonuses. v0.8.0 makes
empirical history load-bearing:

* **Per-model observation scoping** (#69): `ObservationScope` now carries a
  `ModelName` field so different models on the same node accumulate independent
  peak-RAM histories. Observation key derivation uses SHA-256 over the base scope
  fields (node, workload class, backend, tool), conditionally extending the hash
  input with model name when known to prevent cross-model contamination while
  preserving existing keys for unscoped observations.

* **MLX resident model detection** (#70): `axis facts` and cluster snapshots now
  include models served by `mlx_lm.server` alongside the existing Ollama collector.
  MLX models are discovered via the `/v1/models` HTTP endpoint and tagged with
  `runtime: mlx`, `source: mlx-lm-api`.

* **Hard `PeakRAMMB` pre-filter** (#71): `FilterCandidates` now excludes any node
  whose freshly-observed `PeakRAMMB` exceeds the node's current allocatable RAM
  before the ranking phase begins. The filter short-circuits on stale or missing
  observations (safe default: allow). `inferenceModelName` is hoisted outside the
  per-node loop to avoid repeating model-name extraction/matching for each node.

**`axis status` resident model display (PR #72)**

`axis status` now renders a **RESIDENT MODELS** table when at least one node has
live resident models. Rows are ordered node-first, then by runtime in canonical
order (ollama → llama.cpp → mlx → apple-foundation-models), with unknown runtimes
sorted alphabetically for deterministic output. Model lists exceeding three entries
are truncated with a `+N more` suffix. Runtime labels are colour-coded (ollama:
green, llama.cpp: yellow, mlx: cyan, apple-fm: green).

**`axis doctor` AI backend health checks (PR #73)**

`axis doctor` now probes local AI backends as advisory checks:

* `llama-server` and `mlx` are probed via the same discovery scripts used by
  `axis facts`, keeping the doctor and fact-collection views consistent.
* Each probe reports installed / running / port / model-count state.
* Probe errors (e.g. `bash: command not found`) surface `stderr` for actionability
  instead of emitting an opaque exit-code message.
* Each backend gets an independent 5-second timeout derived from the command
  context, preventing a slow first probe from starving the second.
* `--strict` flag now also promotes daemon-cache failure to a core failure (existing
  behaviour documented and tested in this release).

**Earlier arc PRs in this series**

* **#66** — Exact-scope execution observations separate from failure memory
* **#67** — `freshObservation` scoping helper and ranking integration
* **#68** — TurboQuant-aware backend grading for long-context placement hints

---

## v0.7.0

See GitHub release notes: <https://github.com/toasterbook88/axis/releases/tag/v0.7.0>
