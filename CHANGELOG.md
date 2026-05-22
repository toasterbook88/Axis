## v0.10.7 (2026-05-22)

### 🚀 Features
* New `axis daemon mesh` subcommand for operator mesh introspection.
  - Queries the daemon's `/mesh` endpoint and displays active gossip peers in a table.
  - Shows peer name, hostname, state, source, and relative last-seen time.
  - Handles empty peer lists and "mesh not available" gracefully.

### 🔧 Internal
* Added `Mesh() *mesh.Mesh` to the `daemon.SnapshotCache` interface and implemented it on `*Daemon`.
* Added `/mesh` handler to the daemon router (`internal/daemon/handlers.go`).
* Added `MarshalJSON`/`UnmarshalJSON` to `mesh.PeerState` so the API serializes states as human-readable strings (`"discovered"`, `"verified"`, `"trusted"`, `"suspect"`, `"dead"`).
* Refactored daemon HTTP request creation into shared `newDaemonRequest` helper with consistent auth handling.

## v0.10.6 (2026-05-22)

### 🚀 Features
* Surface hidden hardware facts in `axis facts` and `axis status`:
  - `axis status` table expanded with **STORAGE** and **GPU** columns.
  - `axis facts` now shows storage class, GPU details (vendor, VRAM, capabilities), thermal state, power source, battery level, load averages, memory topology, and network addresses with interface/speed class.
  - Refactored GPU name formatting into shared `formatGPUBaseName` helper to ensure consistent vendor redundancy handling and "unknown" filtering across commands.

## v0.10.5 (2026-05-22)

### 🚀 Features
* `axis chat` and `axis agent` UX improvements:
  - Signal context wiring (Ctrl+C interrupts all modes: single-shot, resume, REPL).
  - `--verbose` flag prints model auto-detection, turn progress, and tool parameters.
  - `--dry-run` flag on `axis agent` skips tool execution while preserving reasoning loop.
  - Cobra output compliance (`--no-color`, redirection) across all chat/agent handlers.
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
  - Power source detection (AC/battery) for better placement decisions
  - Thermal state monitoring to avoid throttled nodes
  - Thermal zones enumeration for detailed thermal monitoring

### 📚 Documentation
* fac90d9: chore: refresh current-state.md for v0.10.0 release

### 🐛 Bug Fixes
* None in this release

### 🔧 Maintenance
* Version bump to 0.10.1

# CHANGELOG

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

- `POST /v2/batch/place` returns `501 Not Implemented` instead of synthetic `200 OK`
- Reservation accounting fails closed when node capacity is unknown
- Structured safety learned approvals deliberately disabled (program-name-only too broad)
- Mesh gossip remains internal scaffolding; HMAC present, replay protection not enforced
- Dashboard/rendering helpers present but not registered as CLI commands
- Release pipeline and GoReleaser improvements

**AX-005/006/007/024/025 integration (PR #93)**

- Link-local addresses tagged with `scope: "link-local"` instead of silently dropped
- SSH `IdentitiesOnly yes` from config now respected (skips agent, default keys)
- `ssh -G` passes `-F` for correct config file resolution on macOS
- Cached-reads doctrine documented: explicit, operator-facing, no hidden fallbacks
- Daemon staleness threshold configurable (default 5 min)

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

- **Per-model observation scoping** (#69): `ObservationScope` now carries a
  `ModelName` field so different models on the same node accumulate independent
  peak-RAM histories. Observation key derivation uses SHA-256 over the base scope
  fields (node, workload class, backend, tool), conditionally extending the hash
  input with model name when known to prevent cross-model contamination while
  preserving existing keys for unscoped observations.

- **MLX resident model detection** (#70): `axis facts` and cluster snapshots now
  include models served by `mlx_lm.server` alongside the existing Ollama collector.
  MLX models are discovered via the `/v1/models` HTTP endpoint and tagged with
  `runtime: mlx`, `source: mlx-lm-api`.

- **Hard `PeakRAMMB` pre-filter** (#71): `FilterCandidates` now excludes any node
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

- `llama-server` and `mlx` are probed via the same discovery scripts used by
  `axis facts`, keeping the doctor and fact-collection views consistent.
- Each probe reports installed / running / port / model-count state.
- Probe errors (e.g. `bash: command not found`) surface `stderr` for actionability
  instead of emitting an opaque exit-code message.
- Each backend gets an independent 5-second timeout derived from the command
  context, preventing a slow first probe from starving the second.
- `--strict` flag now also promotes daemon-cache failure to a core failure (existing
  behaviour documented and tested in this release).

**Earlier arc PRs in this series**

- **#66** — Exact-scope execution observations separate from failure memory
- **#67** — `freshObservation` scoping helper and ranking integration
- **#68** — TurboQuant-aware backend grading for long-context placement hints

---

## v0.7.0

See GitHub release notes: https://github.com/toasterbook88/axis/releases/tag/v0.7.0
