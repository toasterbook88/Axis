# AXIS repository evaluation — 2026-09-05

**Report type:** mixed. Architecture, security, and wiring claims are from **static inspection** of this worktree. Runtime claims below cite **captured commands** from the same worktree. This is not a production-approval stamp.

**Scope:** the local repository clone at `c5102a411cdc4831fea16ac7f6a803d71b3a0a96` (`refactor/agent-startup-model`, two commits ahead of `origin/main` at `129045b`). Untracked: `docs/quality/` (deadcode triage notes). Open PR: https://github.com/toasterbook88/Axis/pull/396 (CI Test & Build, govulncheck, CodeQL success at capture time).

**Qualitative assessment:** AXIS is a well-instrumented local-first cluster substrate. The fact → snapshot → placement core is coherent, heavily tested, and guarded by mechanical truth scripts. Residual risk sits in execution and advisory surfaces, documentation that still describes removed or unwired pieces, and a growing library of symbols that are defined but not on the operator path.

---

## 1. Executive summary

AXIS (repo version `0.17.0` in `internal/buildinfo/version.go`) is a Go 1.26.6 CLI that collects hardware facts over SSH, assembles snapshots, ranks placement deterministically, and optionally executes with a safety gate and reservation ledger. Advisory surfaces (agent, MCP, HTTP) are supposed to stay subordinate to that fact plane.

This worktree compiled (`CGO_ENABLED=0 go build -trimpath -buildvcs=false ./cmd/axis`), `gofmt`/`go vet`/`staticcheck` were clean via `make lint`, `go test -race ./...` passed, and statement coverage on `./...` was **73.9%** (gate in `hack/coverage-check.sh` is 45% total). `make test` failed once in this Cursor sandbox because inherited `GOCACHE`/`GOMODCACHE` broke `hack/hermetic-go-test-tests.sh`; the same script passed after unsetting those variables. Package tests themselves passed in both cases.

The product identity is still strongest below Layer 4. Layers 4–5 are real and wired, but they are also where authority, confirmation, and doc/code drift concentrate.

---

## 2. Architecture

Five-layer stack in `AGENTS.md` matches package layout (~46 `internal/` packages). Higher layers consume lower-tier authority; that rule is visible in code (placement does not call the agent; guarded execution calls `safety.NewEvaluator` / `DefaultRuleSet()`).

**Cohesion:** Fact collection (`internal/facts`, `discovery`, `transport`), snapshot assembly (`snapshot`, `daemon`, `publication`, `snapshotview`), and placement (`placement`, `workload`) are the load-bearing product. Execution (`execution`, `safety`, `reservation`) is a second product that is now first-class. Agent/MCP/HTTP/cortex/llmrouter are a third product sharing types with the first two.

**Fan-in:** `internal/models` and `internal/persist` are hubs. `cmd/axis` is a large Cobra surface (~29 `AddCommand` registrations in `cmd/axis/main.go`, including removed `chat`/`llm` stubs).

**Defined but not on the live snapshot path (static):**

- `BuildPairwiseLinkMatrix` / `ClusterSnapshot.Topology` exist (`internal/facts/pairwise.go`, `internal/models/types.go`). `internal/snapshot` does not populate topology. CHANGELOG for #266 already says the matrix is unwired unless a caller builds it.
- `internal/modelplan.Planner` vs live `PlanSingleNode` — planner struct is extra surface; CLI model plan uses the single-node path.
- Mesh HMAC is present; replay protection is explicitly **not** enforced (`internal/mesh/mesh.go` comment on `gossipMessage`).
- `/v2/history` returns 501 `"execution history wiring pending"` (`internal/api/v2.go`).
- `cmd/axis/noun_registry.go` is a registry with no production readers (called out in untracked `docs/quality/deadcode-triage.md`).
- `internal/llmrouter` cloud stack is substantial relative to the local-first CLI path; deadcode notes treat it as roadmap/allowfile.

**Test/prod size:** ~58k lines of non-test Go and ~58k lines of `*_test.go` (`find` + `wc -l`). That ratio is unusual and is a genuine quality signal, not just coverage theater.

---

## 3. Security review (static, with scoring used as reasoning only)

Inspected: `internal/auth/auth.go`, `internal/auth/forwarded_origin.go`, `internal/transport/ssh.go` (host-key path), `internal/api/server.go`, `internal/api/v2.go`, `internal/safety/structured.go`, `internal/execution/guarded.go` (imports and gate wiring), `internal/netutil/url.go`, `internal/config` KnownFields, `internal/mesh/mesh.go`.

| Area | Assessment | Evidence |
| --- | --- | --- |
| Auth | Bearer token is 32-byte `crypto/rand` hex, 0600 atomic write, flock on generate. HMAC-SHA256 forwarded-origin headers with 5-minute skew. **Empty token skips auth** (`withAuth` in `internal/api/server.go`). | Not JWT. Token always generated on `axis serve` via `LoadOrGenerateToken`. |
| Execution | Guarded path: safety evaluator + reservation + confirm word for raw exec. Learned approvals disabled. Agent autonomy still auto-approves below score thresholds. | `sharedSafetyEvaluator` in `guarded.go`; `autonomySafetyThreshold = 80`. |
| Network | SSH host-key callback is mandatory (`knownhosts.New`). Outbound URL helper blocks loopback/private/link-local unless allowlisted; DNS failure fails closed. | `internal/transport/ssh.go` ~645–676; `internal/netutil/url.go`. |
| HTTP | Default listen is Unix socket (`persist.AxisPath("axis.sock")`). Timeouts on read/header/idle; write timeout 0 for `/run` streams. pprof is behind `withRequiredAuth` **when a token is configured**. `/v2/metrics` is **unauthenticated** and emits cache + node counts. `/health` is unauthenticated (expected). | `registerPprofRoutes` comment; `mux.HandleFunc("/v2/metrics", h.handleMetrics)`. |
| State | Owner-only dirs/files, atomic replace, quarantine on corrupt stores, ledger fail-closed described in current-state and reservation code. | Persist + reservation packages; not re-proven at filesystem runtime here. |
| Config | YAML `KnownFields(true)` on nodes and AI config. | `internal/config/config.go`, `ai.go`. |
| Mesh | HMAC on gossip; nonce/timestamp emitted but replay not enforced. | Comment in `mesh.go`. |

**Pattern scan (approximate; `rg` counts, this session):**

- `exec.Command` appears across facts, execution, git, MCP tests, CLI — expected for a probe/exec tool. Highest density in `internal/facts/local.go`.
- pprof import is limited to `internal/api/server.go` (+ tests / command-surface tests).
- `yaml`/`json` Unmarshal is widespread (config, MCP, agent tools).

**Highest-priority residual risks (not claimed as exploitable without a live TCP bind + stolen or empty token):**

1. Layer 4 HTTP `/run` and agent `run_shell` / `run_on_node` — confirmation and safety scores are the last line, not the fact plane.
2. `/v2/metrics` without `withAuth` if the daemon is bound to TCP on a shared network.
3. Mesh replay (documented as pending) if gossip is enabled on an untrusted L2.
4. `withAuth` no-op when token is empty — a footgun if any caller constructs the server without `LoadOrGenerateToken`.

---

## 4. Code quality

- Error wrapping is dense (`if err != nil` across nearly every package). Timeouts exist on discovery, SSH, MCP client, daemon refresh, model await — not claimed as “no leaks.”
- Concurrency: daemon cache, reservation ledger, mesh, discovery semaphore — race detector run on this tree was clean (see appendix). That is **this suite**, not a proof of no races in untested paths.
- Large files: `internal/facts/local.go` (~1890), `internal/agent/agent.go` (~1506), `internal/execution/guarded.go` (~1464), `cmd/axis/model.go` (~1128). Agent was recently split (`agent_startup_model.go` on this branch).
- `internal/safety/doc.go` correctly says the structured evaluator is in default builds. `docs/architecture.md` still calls it “scaffolding, not wired into operator path” — **doc defect**.
- `EXECUTION-PLAN.md` still talks about `axis chat` / `ChatStream` line numbers — stale relative to `cmd/axis/chat.go` (removal stub).

---

## 5. Test coverage (live, 2026-09-05)

Command: `./hack/hermetic-go-test.sh ./... -count=1 -timeout 180s -coverprofile=/tmp/axis-eval-cover.out`

`go tool cover -func` **total: 73.9%** statements.

Package statement coverage from the same `go test -cover` lines (selected):

| Package | Statements | vs gate |
| --- | --- | --- |
| `internal/knowledge` | 100.0% | gate 90% |
| `internal/api` | 78.3% | gate 50% |
| `internal/mcp` | 86.5% | gate 35% |
| `internal/ui` | 90.1% | gate 80% |
| **total** | **73.9%** | gate 45% |
| `internal/mcpclient` | 23.8% | ungated low |
| `internal/persist` | 53.2% | ungated |
| `internal/lockutil` | 53.8% | ungated |
| `cmd/axis/tui` | 57.5% | ungated |
| `internal/repairs` | 58.3% | ungated |
| `internal/mesh` | 63.1% | ungated |
| `internal/agent` | 68.0% | ungated |
| `internal/execution` | 82.0% | ungated (high for risk) |
| `internal/facts` | 74.6% | ungated |
| `internal/placement` | 79.1% | ungated |
| `internal/buildinfo` | no test files | version const only |

Gates in `hack/coverage-check.sh` are **low relative to actuals** (especially MCP 35% vs 86.5%). That is a policy choice: CI will not notice a large coverage drop until it falls a long way.

No-test / thin: `examples/mcp-event-client`, `internal/buildinfo`. `internal/fleettest` is `//go:build fleet`.

---

## 6. Component deep dives (inspected paths)

**Fact plane.** Local/remote collectors, disk weights, resident models, pressure/thermal/battery, TurboQuant and AFM probes are present and tested. Pairwise topology is a library, not snapshot assembly.

**Daemon / publication.** Cache refresh triggers, publication envelopes, exclusive socket ownership, and overlay of reservations before placement are the v0.16–v0.17 authority story. Matches `docs/current-state.md` more closely than `docs/future-roadmap.md` (last reviewed 2026-04-01; much of that “future” is already shipped).

**Placement.** Filter → rank → select remains deterministic. Empirical PeakRAMMB filter and tombstones are documented as live.

**Safety / execution.** Default rule set is in-process; `internal/execution/guarded_safety_test.go` still has `//go:build safety_scaffolded` — a **test-tag leftover** while production code is untagged. Worth deleting the tag or restoring a reason.

**Agent.** Tool-calling loop with cluster tools and gated shell. This branch extracts startup model resolution and adds characterization tests. Highest complexity in Layer 5.

**MCP.** ~20 tools (16 in `server.go` + 1 inference + 3 triangle leases), matching `docs/runbooks/mcp-network-tools.md`.

**Chat package.** `internal/chat` still exists and has ~76.8% coverage; CLI `axis chat` only prints a removal error. Library leftover, not a live operator command.

---

## 7. Roadmap vs reality / documentation drift

| Claim | Reality in this tree |
| --- | --- |
| `SECURITY.md` supported version `v0.2.x` | Repo version **0.17.0**. Material policy drift. |
| `docs/architecture.md`: chat is interactive Ollama; safety is unwired scaffolding | Chat CLI removed; safety evaluator is default-build and used by `safety.Check` / guarded exec. |
| `docs/current-state.md` exec summary: “local chat surface backed by Ollama” | Contradicts the command table two screens later (`axis chat` removed). |
| `docs/future-roadmap.md` (Apr 2026): empirical RAM, GPU structs, I/O tiering, thermal, tombstones as future | Shipped in current-state / code. Roadmap is historical. |
| `README.md` “Go 1.26+” | `go.mod` is **1.26.6**; AGENTS.md matches the patch pin. |
| Mesh replay protection | Still pending per source comment. |
| Pairwise topology enrichment | Type exists; snapshot pipeline does not fill it. |

CI still enforces `hack/verify-repo-truth.sh` / `hack/verify-doc-facts.sh` on a subset of facts; they do not catch SECURITY.md or architecture.md scaffolding language.

---

## 8. Recommendations

**High (truth and authority):**

1. Fix `SECURITY.md` supported versions to the current tagged line.
2. Align `docs/architecture.md` and the current-state executive summary with removed chat and wired safety.
3. Decide PairwiseLinkMatrix: MCP/snapshot wire-up or delete the snapshot field so operators cannot confuse an empty topology with a measured one.
4. Keep `/run` and agent execution behind Unix-socket default; treat TCP bind as a privileged operator choice. Consider authenticating `/v2/metrics` or documenting it as scrape-only and local-only.

**Medium:**

5. Make `hack/hermetic-go-test-tests.sh` ignore or snapshot parent `GOCACHE`/`GOPATH`/`GOMODCACHE` so `make test` is not environment-sensitive (reproduced in this sandbox; CI on PR #396 was green).
6. Drop or justify `//go:build safety_scaffolded` on `guarded_safety_test.go`.
7. Raise coverage gates toward observed floors (e.g. total 65%, mcp 70%) so regressions hurt.
8. Triage dead symbols in `docs/quality/deadcode-triage.md` (noun registry, superseded model helpers) — delete or allowfile, do not leave as silent surface.

**Low / defer:**

9. Mesh replay protection — only if discovery is used beyond a trusted LAN.
10. `internal/mcpclient` coverage and cloud `llmrouter` — product decision, not a CI emergency.
11. Splitting `facts/local.go` / `execution/guarded.go` further — only if review cost is measured.

**Do not implement from this report:** a new control plane, a second placement algorithm, or treating advisory MCP/agent output as cluster truth.

---

## Appendix A — captured verification

All timestamps UTC. Worktree `c5102a4`.

```
# 2026-09-05T19:57:36Z  make test
# Result: all listed packages ok; then
# ./hack/hermetic-go-test-tests.sh
# hermetic-go-test-tests: GOCACHE was not preserved
# make: *** [Makefile:69: test] Error 1
# Environment: GOCACHE and GOMODCACHE pre-set by Cursor sandbox.

# 2026-09-05T19:59:00Z  hermetic go test + coverprofile
# TEST_EXIT:0
# total: (statements) 73.9%

# 2026-09-05T19:58:57Z  make lint ; CGO_ENABLED=0 go build ...
# LINT:0  BUILD:0  (staticcheck 2025.1.1 downloaded during lint)

# 2026-09-05T19:59:39Z  hermetic go test -race ./...
# RACE_EXIT:0  (finished 2026-09-05T20:00:08Z)

# 2026-09-05T20:00:30Z  env -u GOCACHE -u GOPATH -u GOMODCACHE ./hack/hermetic-go-test-tests.sh
# hermetic Go test runner regression tests passed
```

Not run in this evaluation: `make test-race` via Makefile wrapper (equivalent `go test -race` ran), `./hack/coverage-check.sh` (full extra package loops), fleet tests, live `axis status` against the operator cluster (out of repo-eval scope; would be cluster truth, not repo truth).
