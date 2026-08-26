# AGENTS.md

Instructions for AI agents (Claude Code, GitHub Copilot, MCP consumers)
working in this repository.

This file is the **canonical** repository knowledge and architecture source
(repository facts, architecture, scope rules, and verification requirements).
[`.github/copilot-instructions.md`](.github/copilot-instructions.md) is a
supported **thin entry point** for Copilot surfaces: it restates the Truth Rule
and points here. Do not treat that file as a second full copy of these rules.

## Truth Rule

No generated output may present itself as cluster truth unless it is backed by
a real snapshot or live probe.

- `axis facts`, `axis status`, `axis task place`, and `axis task context` are
  the primary operator truth surfaces.
- `axis agent` is an experimental helper subordinate to observed state.
- `axis chat` was removed; use `axis agent`.

- Optional HTTP, MCP, and execution surfaces must not weaken the fact plane.

### Event Surface (observational only)

Lifecycle events (see `internal/events/events.go`) are provided for observation
and advisory integration by external agents. They are strictly observational
and advisory. Agents may subscribe to events via MCP but must not assume control
or execution authority.

## Release State

The repo version constant lives in `internal/buildinfo/version.go`.  The latest
**published** GitHub release may differ from the repo version — check the
[Releases page](https://github.com/toasterbook88/axis/releases). Committed
current-state facts are derived only from repository contents, so publishing a
release cannot make `main` stale. CI still checks explicit release links and
"current release" claims in `README.md` and `docs/current-state.md` via
`./hack/verify-repo-truth.sh`.

Do not fabricate or assume a release version. Use `docs/current-state.md` for
repository state and GitHub Releases for publication state; each has one
authority.

For planned work, read `docs/future-roadmap.md` and older phase/spec docs as
design material, not live product truth. Do not describe roadmap phases or
future-path documents as shipped behavior unless they are backed by the code,
`docs/current-state.md`, and the latest published GitHub release.

## Build & Test

Source of truth: [`Makefile`](Makefile).

```bash
make build          # CGO_ENABLED=0 go build -trimpath with LDFLAGS
make install-system # Build + install to /usr/local/bin (preferred on cluster nodes)
make install-user   # Build + install to ~/.local/bin (workstation development)
make install        # Build + copy to $GOPATH/bin (legacy)
make test           # go test ./... -count=1 -timeout 180s
make test-race      # go test ./... -count=1 -timeout 180s -race
make lint           # gofmt -l (fails if dirty) + go vet
make coverage       # ./hack/coverage-check.sh
make clean          # rm -f axis
```

After installing on a daemon host: `axis daemon restart && axis daemon status`.

AXIS installs to `/usr/local/bin` by default so every node resolves the same
absolute path. A second binary earlier in `$PATH` shadows the canonical one:
`axis update` then refreshes the copy the operator invokes while a systemd unit
with an absolute `ExecStart` keeps running the stale one, and each host's
`axis version` still looks correct in isolation. `axis doctor` reports duplicate
installs.

NixOS omits `/usr/local/bin` from `PATH` by default. Add it with
`environment.sessionVariables.PATH = [ "/usr/local/bin" ];` in `configuration.nix`
— that is set through PAM, so it reaches non-interactive SSH, which the remote
fact collectors rely on. Do not substitute `environment.extraInit`: it runs
during shell initialization only and misses non-interactive SSH. Neither option
configures systemd; NixOS units carry a hermetic `PATH` built from their own
inputs, so any unit invoking axis needs an absolute `ExecStart`.
`axis version` must print `commit:` — that line distinguishes tip-of-main from a GitHub release with the same semver.
Never `gh release create` before the tag workflow; GoReleaser owns GitHub Releases.

Requires Go 1.26.1+ (`go.mod` is authoritative for the minimum; use the latest
1.26 patch release). Remote node tests require SSH
key-based auth.

### CI Pipeline

Source of truth: [`.github/workflows/ci.yml`](.github/workflows/ci.yml).

Runs on `ubuntu-latest` for every push and PR across all branches.
Verification steps:

- `make test`
- `make test-race`
- `go build -buildvcs=false ./...` (portable across linked worktrees; release builds inject explicit metadata)
- `./hack/coverage-check.sh` — enforces per-package and total coverage gates
- `./hack/verify-repo-truth.sh` — enforces release-tag and doc-fact accuracy
- `./hack/verify-doc-facts.sh` — enforces code/doc agreement: exit codes, command count, MCP tool count, and CHANGELOG completeness (no network)

Coverage gates are authoritative in `hack/coverage-check.sh`.

### Release Pipeline

Source of truth: [`.github/workflows/release.yml`](.github/workflows/release.yml).

Triggered by `v*` tag push. Validates that the tag matches
`internal/buildinfo/version.go`, runs the full test+coverage+build suite, then
publishes via GoReleaser (`darwin`/`linux` × `amd64`/`arm64`).

## Architecture

AXIS is organized into five architectural trust/role tiers. Higher tiers consume lower-tier authority; advisory surfaces (chat, agent, console, MCP, HTTP) may compose with peer advisory packages but never override the fact plane.

```text
Layer 5  Advisory     internal/chat  internal/agent  internal/console
                        internal/mcp  internal/api
Layer 4  Execution    internal/execution  internal/safety  internal/reservation
                        internal/scripts  internal/skills
Layer 3  Placement    internal/placement  internal/workload
Layer 2  Snapshot     internal/snapshot  internal/daemon  internal/snapshotview
                        internal/runtimectx
Layer 1  Fact Plane   internal/facts  internal/discovery  internal/mesh
                        internal/transport (SSH)
```

### Stable operator path

```text
cmd/axis/             Cobra CLI — one file per subcommand (29 commands)

internal/config/      Load ~/.axis/nodes.yaml; strict YAML parsing
internal/facts/       Local + SSH remote fact collection, tool probes, GPU,
                      pressure, thermal, battery, network, TurboQuant, AFM
internal/discovery/   Fan-out configured nodes + opt-in UDP beacons
internal/snapshot/    Assemble ClusterSnapshot from []NodeFacts
internal/placement/   Deterministic filter → rank → select (FitScore 0–100)
internal/runtimectx/  Unified live runtime loader for read surfaces
internal/transport/   SSH execution layer (host-key verification must stay on)
```

### Secondary / optional surfaces

```text
internal/daemon/      Background snapshot refresh, in-memory cache
internal/api/         Optional local HTTP API (axis serve)
internal/mcp/         MCP server (axis mcp serve): 20 tools (17 read-only
                      diagnostics + 3 advisory lease primitives); see
                      docs/runbooks/mcp-network-tools.md for the full list
internal/chat/        Structured Ollama /api/chat client (subordinate to facts)
internal/agent/       Tool-calling agent loop with safety-gated shell
internal/execution/   Guarded execution: safety → reserve → run → release
internal/safety/      Execution blocker (0–100 score; ≥80 = hard block)
internal/state/       Reservation tracking, per-exec liveness/provenance, and
                      failure immune system
internal/skills/      Learned skills/failures, corrupt-file recovery
internal/scripts/     Built-in helper scripts with keyword matching
internal/knowledge/   Cluster knowledge context for execution
```

### Supporting packages

```text
internal/models/      Core types: NodeFacts, ClusterSnapshot, PlacementDecision
internal/buildinfo/   Version, commit, date, go version (ldflags injection)
internal/ui/          Terminal colors, tables, spinners, help templates
internal/persist/     Corrupt-file quarantine + warning recovery
internal/snapshotview/ Deep clone + reservation overlay on snapshots
internal/turboexec/   TurboQuant flag injection for execution
```

### Core types (`internal/models/types.go`)

- **NodeFacts** — assigned config (name, role, ssh_user) + observed state
  (hostname, OS, arch, resources, tools, addresses, GPUs, Ollama, TurboQuant,
  Apple Foundation Models)
- **ClusterSnapshot** — `[]NodeFacts` + cluster aggregates, health status,
  warnings
- **PlacementDecision** — selected node, FitScore 0–100, IsLocal, reasoning
  strings
- **NodeStatus**: `complete | partial | unreachable | error`

### Placement ranking (stable sort order)

RAM pressure → GPU score → preferred backend → effective headroom → TurboQuant
rank → unified memory rank → allocatable RAM → reservation ratio → node name.

Scoring components: allocatable RAM (max 30), pressure (max 25), GPU (max 25),
CPU cores (max 10), local bonus (10), TurboQuant (5–25 if preferred), unified
memory (8–18 on Apple Silicon; upper end requires TurboQuant verification).
HDD penalty: −15 for heavy inference.

## CLI Subcommands

29 top-level commands registered via `AddCommand` in `cmd/axis/main.go`:

| Command | Purpose |
| --------- | --------- |
| `axis update [--check] [--all] [--path]` | Self-update via GitHub Releases (running install by default; `--all` for validated shadows); SHA-256 verified |
| `axis version` | Print build version, commit, date, go, platform |
| `axis facts [--format json\|yaml]` | Local node facts (alias of `axis node facts`) |
| `axis status [--cached] [--format]` | Cluster snapshot (alias of `axis cluster status`) |
| `axis task` | Task subcommands: `place`, `context`, `run` |
| `axis placement explain` | Detailed per-node placement breakdown |
| `axis profile match` | Workload class inference |
| `axis mcp serve` | Read-only MCP server over stdio |
| `axis serve [--addr] [--refresh]` | HTTP API + daemon cache |
| `axis daemon` | Lifecycle/cache commands plus native `service install\|status\|uninstall`; `status` emits `axis.output/v1` JSON |
| `axis chat` | Removed; prints `use axis agent` |
| `axis agent [--auto-approve] [--autonomy MODE] [--console]` | Agentic tool-calling assistant; REPL slash commands `/plan /todo /diff /undo /compact /autonomy /export /fleet`; `--console` opens the experimental transcript console (interactive TTY only; tool approvals are denied) |
| `axis llm` | Removed; prints `use axis ai route` |
| `axis ai` | Inference backends, roles, dry-run route resolve |
| `axis model` | Start/stop llama-server on a named node (`start --node --weights --port`, `stop --node --port`) |
| `axis cluster` | Fleet snapshot: `status`, `summary` |
| `axis node` | This machine: `facts` |
| `axis cortex` | Distributed vector memory / event bus |
| `axis context show\|clear` | Inspect or clear placement memory |
| `axis scripts list` | List built-in helper scripts |
| `axis skills` | Show learned skills/failures |
| `axis completion` | Shell completions (bash/zsh/fish/powershell) |
| `axis doctor` | Validate config, SSH connectivity, daemon health |
| `axis summary` | Cluster summary view |
| `axis reservations` | Reservation inspection |
| `axis init` | Interactive cluster configuration wizard |
| `axis mesh` | Gossip mesh peer diagnostics (subcommands: `status`, `peers`) |
| `axis observations` | Show execution observations tracked by the cluster |
| `axis tui` | Interactive full-screen Bubble Tea cluster dashboard |

### Exit codes (`cmd/axis/exit.go`)

| Code | Constant | Meaning |
| ------ | ---------- | --------- |
| 0 | `ExitOK` | Success |
| 1 | `ExitErrGeneric` | Generic error |
| 2 | `ExitErrConfigLoad` | Configuration load failure |
| 3 | `ExitErrNoNodesFit` | No nodes satisfy task requirements |
| 4 | `ExitErrCommandFail` | Command execution failure |
| 5 | `ExitErrContextWrite` | Context write failure |
| 6 | `ExitErrIO` | I/O failure |
| 7 | `ExitErrModelUnlisted` | `axis ai route`: model not listed on healthy backends |

## Configuration

`~/.axis/nodes.yaml` — required per node: `name`, `hostname`, `ssh_user`.
Optional: `role` (primary/worker), `ssh_port` (default 22), `timeout_sec`
(default 10), `stable_id` (optional observed machine identity used for locality
matching and discovery dedupe). Unknown YAML keys are rejected at load time.

`axis init` automatically records the local stable identity when the platform
exposes one. After validation, use `axis daemon service install` for a
supervised per-user daemon. `axis daemon restart` remains an unmanaged
development/recovery path, not a persistence mechanism.

`~/.axis/ai.yaml` — optional inference **backends** and **roles** (see
`ai.example.yaml` and `docs/runbooks/ai-config.md`). Separate from
`ai_providers` / `agent.default_model` in `nodes.yaml` (`chat.default_model` still accepted).

Optional UDP discovery block: `discovery.enabled`, `discovery.udp_port`
(default 42424), `discovery.beacon_interval_sec` (default 3),
`discovery.secret` (HMAC-SHA256 beacon auth).

Persisted local state:

- `~/.axis/state.json` — reservation tracking, failure records, recent
  decisions, per-exec heartbeats, and local caller/origin provenance
- `~/.axis/skills.json` — learned skills and failures
- `~/.axis/snapshot.json` — daemon-cached snapshot
- `~/.axis/ledger.json` — reservation authority
- `~/.axis/events*.jsonl` and `event-sequence*` — event history and ordering
- `~/.axis/logs/task-*.log` — combined task stdout/stderr
- `~/.axis/{chat,agent}-history.json` — advisory conversation history

AXIS-created runtime directories are owner-only (`0700`) and runtime files are
owner-only (`0600`). Opening an append/lock store or replacing an atomic store
tightens files created by older releases. Readline does not persist a second
prompt-history file; the private structured conversation store is authoritative.

Corrupt state/skills files are quarantined to `.corrupt-*` backups and surfaced
as warnings instead of crashing read paths.

## Testing Patterns

Tests use stub/mock helpers with a restore pattern:

```go
restore := stubSomeFn(fakeValue)
defer restore()
```

Mock nodes (`nodeComplete()`, `nodeTurboQuant()`, etc.) are defined in
placement tests. Integration tests in `cmd/axis/` stub SSH; unit tests in
`internal/` stub the remote executor interface. Contract tests validate golden
file outputs for degraded-state recovery.

## Dependencies

15 direct dependencies (`go.mod`; versions authoritative in go.mod):

| Module | Purpose |
| -------- | --------- |
| `al.essio.dev/pkg/shellescape` | Shell argument escaping |
| `github.com/charmbracelet/bubbletea` | Elm-architecture TUI runtime (`axis tui`) |
| `github.com/charmbracelet/bubbles` | Bubble Tea component library (`axis tui`) |
| `github.com/charmbracelet/lipgloss` | TUI layout/styling (`axis tui`) |
| `github.com/chzyer/readline` | Interactive line editing (agent/chat flows) |
| `github.com/fatih/color` | Terminal color output |
| `github.com/google/uuid` | Event/reservation/provenance identifiers |
| `github.com/mark3labs/mcp-go` | MCP protocol implementation |
| `github.com/mattn/go-isatty` | Terminal detection for UI/color decisions |
| `github.com/spf13/cobra` | CLI framework |
| `golang.org/x/crypto` | SSH (agent, knownhosts, keys) |
| `golang.org/x/mod` | Module version comparison |
| `golang.org/x/sys` | Extended syscalls (signals, platform specifics) |
| `golang.org/x/term` | Terminal primitives (raw mode, size queries) |
| `gopkg.in/yaml.v3` | YAML parsing |

## Scope Discipline

Prefer changes that improve fact quality, snapshot quality, placement quality,
or reduce operator confusion. Remove dead or duplicate complexity. Strengthen
explicitness, determinism, and test coverage.

Avoid changes that create hidden authority paths, guess at cluster truth instead
of surfacing uncertainty, add duplicate control surfaces without strong operator
reason, or add heavy dependencies without strong justification.

## Hack Scripts

| Script | Purpose |
| -------- | --------- |
| `hack/coverage-check.sh` | Per-package and total coverage gates |
| `hack/hermetic-go-test.sh` | Run Go tests without touching operator AXIS state |
| `hack/hermetic-go-test-tests.sh` | Regression tests for the hermetic Go test runner |
| `hack/verify-repo-truth.sh` | Enforce doc facts and release tag accuracy |
| `hack/verify-doc-facts.sh` | Enforce code/doc agreement (exit codes, command count, MCP tools, CHANGELOG) |
| `hack/refresh-current-state.sh` | Rebuild repository-derived facts and verification in `docs/current-state.md` |
| `hack/repo-truth-tests.sh` | Regression tests for repository/release truth boundaries |
| `hack/validate-release-version.sh` | Enforce source version and release-tag equality |
| `hack/apple-foundation-models.swift` | Probe Apple Foundation Models support |
| `hack/verify-plan-progress.sh` | Verify execution-plan progress matrix test references and completed tests |

## Agent-Specific Guidelines

### Gemini

- Do not duplicate build commands, core types, or coverage gates here. Rely on `AGENTS.md`.
- Adhere strictly to the Truth Rule and scope discipline documented in `AGENTS.md`.

### Claude

- **Do not invent inactive integrations.** Describe packages by their live wiring, not by outdated scaffolding headers:
  - `internal/mesh` is started from `axis serve` via the daemon (`WatchMesh` when mesh is enabled/default) and is exposed for diagnostics through `axis mesh` and HTTP mesh handlers.
  - `internal/reservation` backs the daemon reservation ledger, guarded execution reserve/release, snapshot overlays, `axis reservations` (list/inspect/release/doctor), and API `/v2/reservations`.
  - Do not claim mesh or reservation are “library-only” or “not wired into the operator CLI path” while those surfaces exist.
- **`internal/safety` structured evaluator** (`structured.go`): included in default builds and used by the stable blocker (`safety.Check` → `NewEvaluator`/`Evaluate`) and by guarded execution. Learned approvals remain disabled. Package `doc.go` text that claims a `safety_scaffolded` build-tag gate is stale relative to the code; do not restate that claim.
- **Truth over features**: Do not weaken the fact plane to support an advisory surface (`axis chat`, `axis agent`).
- Do not describe roadmap material or scaffolding as shipped runtime behavior. When wiring changes, update `docs/current-state.md` and pass `verify-repo-truth.sh` / `verify-doc-facts.sh`.

### Copilot

Copilot surfaces should load [`.github/copilot-instructions.md`](.github/copilot-instructions.md)
(thin pointer + Truth Rule) and then follow this file for full guidance.
Guardrails that matter for every edit:

- **Truth Rule**: Do not make release or state claims without code/current-state proof. No generated output may present itself as cluster truth unless backed by a real snapshot or live probe.
- **Surgical Changes**: Prefer small, explicit changes. Do not touch adjacent code that isn't broken.
- **Verification**: Run Makefile gates (`make test`, `make lint`) to verify any changes before proposing them.
