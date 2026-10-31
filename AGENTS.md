# AGENTS.md

Canonical instructions for AI agents working in this repository (Codex, Copilot,
OpenCode, Antigravity, Hermes, OMP, grok CLI, Gemini and Claude Code via their
`GEMINI.md` / `CLAUDE.md` pointers, and `axis agent`,
which injects this file into its system prompt — keep it short).
`.github/copilot-instructions.md`, `CLAUDE.md` and `GEMINI.md` only point here.

## 1. Truth Rule (read first)

No generated output may present itself as cluster truth unless it is backed by
a real snapshot or live probe.

- Operator truth surfaces: `axis facts`, `axis status`, `axis task place`, `axis task context`.
- `axis agent`, MCP, HTTP and execution surfaces are advisory. They must never weaken the fact plane.
- Lifecycle events (`internal/events`) are observational only; subscribers get no control authority.
- When unsure, report "not observed" — never a guessed value.

## 2. Hard rules

- **Public repo.** No real hostnames, tailnet or LAN IPs, node inventories, live
  ports, model/unit names or secrets — in code, tests, fixtures, docs, commits or PRs.
  Use RFC 2606 domains and documentation IP ranges. `./hack/verify-public-boundary.sh` enforces it.
- **Merging needs the maintainer.** Review is read-only. Never merge a PR unless the maintainer explicitly asks to merge that PR.
- **Releases** go through the tag workflow (GoReleaser). Never `gh release create`. Never claim a release
  version from memory: repo state is `docs/current-state.md`, publication state is GitHub Releases.
- **Roadmap ≠ shipped.** `docs/future-roadmap.md` and phase/spec docs are design material.
- **Git.** Never hard-reset or force-push over uncommitted work; stash first.

## 3. Build, test, gates

Source of truth: `Makefile`, `.github/workflows/ci.yml`.

```bash
make build                 # CGO_ENABLED=0 go build -trimpath
make test                  # go test ./... (HOME-isolated)
make test-race
make lint                  # gofmt -l + go vet + staticcheck
make ci-preflight          # the full local PR gate set — run before opening a PR
make release-preflight     # release-candidate gates
make test-install          # install-path tests
make test-fleet            # cross-node fleet tests (guarded; real nodes)
make install-system        # /usr/local/bin (cluster nodes); then: axis daemon restart
make install-user          # ~/.local/bin (workstation development)
```

- Go: `go.mod` is authoritative (currently `go 1.26.10`); a newer local Go still runs the gates. If
  staticcheck fails with an export-data error, bump `STATICCHECK_VERSION` (Makefile) to a release that
  reads that Go's export data; `GOTOOLCHAIN=go1.26.10` reproduces CI's toolchain meanwhile.
- CI also runs: coverage gates (`hack/coverage-check.sh`), deadcode gate, import tiers, public boundary,
  repo truth, doc facts, actionlint, workflow action pins, install tests,
  `hack/lifecycle-check.go` and `hack/claude-workflow-tests.py`.
- `./hack/verify-doc-facts.sh` checks this file against the code (exit codes, command count,
  MCP tool count, 30 KB budget) — update both together.
- `go run hack/verify-import-tiers.go` checks the §4 tier layout against every `internal/` import and
  the package inventory; exceptions go in `hack/import-tiers-allowlist.txt`, each with a rationale.
- PR review loop: `./hack/pr-review-cycle.sh <PR>` (renders review state; fails closed unless CLEAN;
  waits for requested reviews and exits 3 while one is still pending).
- Bare `go test` outside `make` can touch real `~/.axis` state; prefer `make test` or `hack/hermetic-go-test.sh`.
- `axis version` must print `commit:`; it distinguishes tip-of-main from a release with the same semver.
- NixOS: `/usr/local/bin` is not on PATH by default (use `environment.sessionVariables.PATH`);
  systemd units need an absolute `ExecStart`. `axis doctor` reports duplicate installs.

## 4. Architecture

Five trust tiers. Higher tiers consume lower-tier authority; advisory surfaces never override facts.

```text
Layer 5  Advisory    internal/chat internal/agent internal/console internal/mcp internal/api
                     internal/cortex internal/mcpclient internal/a2a internal/knowledge
Layer 4  Execution   internal/execution internal/safety internal/scripts internal/modellife
                     internal/modelinventory
Layer 3  Placement   internal/placement internal/workload internal/modelplan internal/llmrouter
Layer 2  Snapshot    internal/snapshot internal/daemon internal/snapshotview internal/runtimectx
                     internal/publication internal/reservation internal/skills
Layer 1  Fact Plane  internal/facts internal/discovery internal/mesh internal/transport internal/multipath
Support              internal/config internal/models internal/buildinfo internal/ui internal/persist
                     internal/repairs internal/turboexec internal/auth internal/failures internal/fleettest
                     internal/git internal/lockutil internal/netutil internal/secrets internal/versioncmp
                     internal/state internal/events
```

`cmd/axis/` — Cobra CLI, one file per subcommand (29 commands).
`internal/transport` is SSH; host-key verification must stay on.
`internal/mcp`: 21 tools (18 read-only diagnostics + 3 advisory lease primitives); list in `docs/runbooks/mcp-network-tools.md`.

**Live wiring (do not describe as scaffolding):** `internal/mesh` runs from `axis serve` via the daemon
(`WatchMesh`) and is exposed by `axis mesh` and HTTP handlers. `internal/reservation` backs the daemon
ledger, guarded execution, snapshot overlays, `axis reservations` and `/v2/reservations`.
`internal/safety` structured evaluator is in default builds (any `safety_scaffolded` build-tag claim in
`doc.go` is stale). Learned approvals stay disabled.

**Core types** (`internal/models/types.go`): `NodeFacts` (assigned config + observed state),
`ClusterSnapshot` (nodes + aggregates + warnings), `PlacementDecision` (selected node, typed capacity
ranking, diagnostic FitScore), `NodeStatus` = `complete | partial | unreachable | error`.

**Placement** first filters (`FilterCandidates`, `internal/placement/explain.go`): ineligible nodes drop out
before ranking — e.g. `RequiredLabels` (all pairs must match) and the reservation budget (`CanReserveRAM`).
Default objective is `capacity` (headline `allocatable_ram`, MB, derived); explicit `--objective spread`
ranks by resulting cluster reservation-share stddev (`reservation_skew`, lower wins). FitScore (0–100) is
diagnostic only, never the sort key (`docs/decisions/placement-selection-contract.md`). Capacity order:
allocatable RAM → empirical observation → resident model locality → preferred backend → GPU score →
effective headroom → TurboQuant → unified memory → pressure → model warmth → reservation ratio/share → node name.

## 5. CLI (29 top-level commands registered in `cmd/axis/main.go`)

| Command | Purpose |
| --- | --- |
| `axis update` | Self-update from GitHub Releases, SHA-256 verified |
| `axis version` | Version, commit, date, go, platform |
| `axis facts` | Local node facts (alias of `axis node facts`); `--hardware` / `-hw` adds detailed hardware specifications and live load |
| `axis status` | Cluster snapshot; cache-first, prints source and age; `--live`, `--cached-only` |
| `axis task` | `place`, `context`, `run` |
| `axis placement explain` | Per-node placement breakdown |
| `axis profile match` | Workload class inference |
| `axis mcp serve` | MCP server over stdio |
| `axis serve` | HTTP API + daemon cache |
| `axis daemon` | Lifecycle, cache, `service install/status/uninstall` |
| `axis chat` | Removed; prints `use axis agent` |
| `axis agent` | Advisory tool-calling assistant (console on TTY, `--plain`, `--live`) |
| `axis llm` | Removed; prints `use axis ai route` |
| `axis ai` | Inference backends, roles, dry-run route resolve |
| `axis model` | Catalog, inspect, plan, `start`/`stop` (the model lifecycle surface) |
| `axis cluster` | `status`, `summary` |
| `axis node` | `facts` |
| `axis cortex` | Cortex memory / event bus client |
| `axis context` | `show`, `clear` placement memory |
| `axis scripts list` | Built-in helper scripts |
| `axis skills` | Learned skills / failures |
| `axis completion` | Shell completions |
| `axis doctor` | Config, SSH, daemon health, duplicate installs |
| `axis summary` | Cluster summary view |
| `axis reservations` | List, inspect, release, doctor |
| `axis init` | Configuration wizard |
| `axis mesh` | `status`, `peers` |
| `axis observations` | Execution observations |
| `axis tui` | Full-screen workspace: embedded agent console, cluster sidebar, dashboard (`--dashboard` alone) |

Exit codes (`cmd/axis/exit.go`): 0 `ExitOK` · 1 `ExitErrGeneric` · 2 `ExitErrConfigLoad` ·
3 `ExitErrNoNodesFit` · 4 `ExitErrCommandFail` · 5 `ExitErrContextWrite` · 6 `ExitErrIO` · 7 `ExitErrModelUnlisted`.

## 6. Configuration and state

- `~/.axis/nodes.yaml`: `name`, `hostname`, `ssh_user` required; optional `role`, `ssh_port`, `timeout_sec`,
  `stable_id`, `discovery.*`. Unknown keys are rejected.
- `~/.axis/ai.yaml`: optional inference backends and roles (`docs/runbooks/ai-config.md`).
- State under `~/.axis/` (owner-only `0700`/`0600`): `state.json`, `skills.json`, `snapshot.json`,
  `ledger.json`, `events*.jsonl`, `logs/task-*.log`, `*-history.json`. Corrupt files are quarantined
  to `.corrupt-*` and surfaced as warnings, never crashes.

## 7. Working style

- Prefer changes that improve fact quality, snapshot quality, placement quality or operator clarity.
- Surgical diffs: touch only what the task needs; match existing style; remove only orphans you created.
- Tests first for bugs: reproduce with a failing test, then fix. Tests use stub seams with a restore
  pattern (`restore := stubX(v); defer restore()`); `cmd/axis` tests stub SSH; contract tests use golden files.
- No hidden authority paths, no guessed cluster truth, no new heavy dependencies without a strong reason
  (15 direct dependencies; `go.mod` is authoritative).
- When wiring changes, update `docs/current-state.md` and pass `verify-repo-truth.sh` / `verify-doc-facts.sh`.
- Say "suspect" until a test or probe confirms a cause.

## 8. New features need a reviewed spec

A feature is any new command, subcommand, flag, MCP tool, HTTP route, config key, state file or
output-schema change, or any change to documented behavior. Bug fixes that restore documented
behavior, refactors, tests, dependency bumps and docs are exempt. If unsure, treat it as a feature.
Only the maintainer can waive this, in writing on the PR.

1. Before feature code, commit `docs/superpowers/specs/YYYY-MM-DD-<topic>-design.md`: problem,
   non-goals, design, alternatives, and `## Research` (prior art with links; the optimization chosen
   and its measurement, or why none applies). Size it to the change: one flag can be 20 lines.
   Throwaway spikes for measurement are fine; do not merge them.
2. Review the spec with a fresh-context agent: a new session or subagent given only
   `docs/superpowers/review-prompt.md` and its five inputs (repo, base, head, spec path, ask). A
   different model family is optional; that file cites the evidence.
3. Record each review in the spec's `## Review` section as a block of `Kind:` (`spec` or `diff`),
   `Reviewer:`, `Model:` (vendor family, not a cluster model), `Base:`, `Head:`, `Date:` and
   `Verdict:` (`approve`, `approve-with-changes` or `revise`) lines, then its findings and questions.
   Code starts after a spec review that approves, or approves with changes and every finding
   resolved. A substantial spec change needs a new spec review.
4. When the code is done, review the full diff against the spec the same way (`Kind: diff`). A larger
   feature (at least 150 changed lines in non-test `.go` files, or changes in 3 or more of `cmd/axis`
   and the `internal/<pkg>` directories) gets two independent diff reviews. Merge only when every
   diff review approves, or approves with changes and every finding is resolved.
5. Link the spec in the PR body. If the code departs from the spec, update the spec in that PR.

Every PR, feature or exempt: resolve each review finding, review-bot findings included, as `fixed in
<commit>` or `declined: <reason>, <the run that supports it>`; close each reviewer question as a
finding or as `open: <why not probed>`; list declines and open questions in the PR body. Do not merge
while a requested review is pending: `hack/pr-review-cycle.sh` waits for requested reviews and exits 3
while one is still pending. An empty `reviewRequests` list alone is not proof.
