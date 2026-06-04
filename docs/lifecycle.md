# AXIS Lifecycle Taxonomy

This document defines the maturity taxonomy for AXIS packages, CLI commands, and features.
Every unit of code MUST carry one of the six lifecycle states listed below.

---

## 1. State Definitions

| State | Definition |
|-------|------------|
| **stable** | Production-ready. Covered by CI, covered by tests, expected to work, safe for operator workflows. Breaking changes require a deprecation window. |
| **experimental** | Active development. Functional but unstable; semantics may change without notice. Must emit a warning on first use per invocation. Not allowed on the critical path of stable operator workflows. |
| **scaffolded** | Code exists but is gated by a build tag or runtime feature flag. May return 501, may be unwired from CLI, may be invisible to operators unless explicitly enabled. |
| **dormant** | No active development. Preserved for reference or migration shims only. Imports outside explicitly allowed shim files fail CI. |
| **deprecated** | Scheduled for removal in a future release. Must log a deprecation warning on use and document the replacement path. |
| **internal-only** | Pure infrastructure with no operator-facing contract. Safe for stable code to depend on, but never exposed directly to users or external callers. |

---

## 2. Inheritance Rules (Dependency Constraints)

```text
stable            → may depend on: stable, internal-only
experimental      → may depend on: stable, internal-only, experimental
scaffolded        → may depend on: stable, internal-only, experimental, scaffolded
dormant           → may depend on: stable, internal-only, dormant, deprecated
deprecated        → may depend on: stable, internal-only, deprecated
internal-only     → may depend on: stable, internal-only
```

### Key Constraints

1. **Stable packages MUST NOT transitively depend on experimental, scaffolded, dormant, or deprecated packages.**
   The transitive closure of a stable package must contain only `stable` and `internal-only` code.

2. **Experimental surfaces MUST emit a warning or telemetry event on first use per invocation.**
   This applies to CLI commands, HTTP endpoints, and library entry points.

3. **Scaffolded code MUST require a build tag or a runtime feature gate.**
   It must not appear in default release binaries unless explicitly enabled.

4. **Dormant packages MUST fail CI if imported outside an allow-listed shim file.**
   The allow list is maintained in the CI configuration.

5. **Deprecated symbols MUST carry a replacement hint and a removal target version.**

---

## 3. Current Package Classification

Classification is based on live code inspection (`internal/`), test coverage, operator exposure, and the architecture notes in `docs/current-state.md`.

| Package | State | Rationale |
|---------|-------|-----------|
| `internal/agent` | experimental | Tool-calling agent loop; advisory only; semantics evolving |
| `internal/api` | experimental | Optional HTTP API + execution surface; high-risk; still evolving |
| `internal/auth` | internal-only | Small forwarded-origin auth helpers; no operator contract |
| `internal/buildinfo` | internal-only | Version/commit/date injection; pure infrastructure |
| `internal/chat` | experimental | Structured Ollama client; explicitly advisory per Truth Rule |
| `internal/config` | stable | Strict YAML parsing; core to every operator workflow |
| `internal/cortex` | experimental | Distributed vector memory / event bus; optional coordination layer |
| `internal/daemon` | stable | Explicit cache seam; powers stable cached reads |
| `internal/discovery` | stable | UDP beacon + configured-node fan-out; core to snapshots |
| `internal/execution` | experimental | Guarded execution path; early-stage; high risk |
| `internal/facts` | stable | Local + SSH fact collection; core to the fact plane |
| `internal/failures` | stable | Failure-scope hashing and expiry; used by stable placement |
| `internal/git` | stable | Local git repository workspace querying; core to repo-aware context |
| `internal/knowledge` | stable | Execution context builder; heavily covered; stable contract |
| `internal/llmrouter` | experimental | Hybrid local/cloud routing; new surface |
| `internal/mcp` | experimental | Read-only MCP server; optional diagnostic layer |
| `internal/mesh` | scaffolded | Gossip peer discovery; HMAC auth; NOT wired into CLI operator path |
| `internal/models` | internal-only | Core shared types; no public API surface |
| `internal/persist` | internal-only | Corrupt-file quarantine helpers; pure infrastructure |
| `internal/placement` | stable | Filter → rank → select; deterministic and well-tested |
| `internal/repairs` | scaffolded | Type definitions only; no live wiring |
| `internal/reservation` | scaffolded | Double-entry ledger wired as library; `/v2/reservations` returns 501; no standalone CLI |
| `internal/runtimectx` | stable | Unified live runtime loader; shared by stable read paths |
| `internal/safety` | experimental | Execution blocker is live; structured analysis scaffolding exists but learned approvals are deliberately disabled |
| `internal/scripts` | stable | Built-in script registry; deterministic matching |
| `internal/secrets` | experimental | Cortex auth token handling; only used by experimental surfaces |
| `internal/skills` | experimental | Learned skills/failures; recovers from corrupt files but semantic validation is light |
| `internal/snapshot` | stable | Cluster snapshot assembly; best-tested package in the repo |
| `internal/snapshotview` | internal-only | Deep-clone + reservation overlay; pure helper |
| `internal/state` | stable | Placement memory + exact-scope observations; explicit acquire/release |
| `internal/transport` | stable | SSH execution layer; host-key verification is required |
| `internal/turboexec` | internal-only | TurboQuant flag injection; pure helper |
| `internal/ui` | internal-only | Terminal colors, tables, spinners, help templates |
| `internal/versioncmp` | internal-only | Module version comparison; pure helper |
| `internal/workload` | stable | Workload class inference; deterministic; powers `axis profile match` |

---

## 4. CLI Command Classification

Classification is based on `cmd/axis/` source files and the command surface documented in `docs/current-state.md`.

| Command | State | Rationale |
|---------|-------|-----------|
| `axis version` | stable | Immutable build info surface |
| `axis facts` | stable | Core fact plane; primary operator truth surface |
| `axis status` | stable | Core snapshot plane; primary operator truth surface |
| `axis task place` | stable | Deterministic placement; primary operator truth surface |
| `axis task context` | stable | Compact context for external agents |
| `axis task run` | stable | Guarded execution; stable CLI contract even though execution package is experimental |
| `axis placement explain` | stable | Detailed placement breakdown |
| `axis profile match` | stable | Workload class inference; no cluster snapshot needed |
| `axis doctor` | stable | Config / SSH / daemon / backend validation |
| `axis update` | stable | Self-update with SHA-256 verification |
| `axis context show` | stable | Placement memory inspection |
| `axis context clear` | stable | Placement memory reset |
| `axis scripts list` | stable | Built-in script registry |
| `axis skills` | stable | Read-only skill/failure inspection |
| `axis completion` | stable | Shell completions |
| `axis daemon start` | stable | Alias for `axis serve`; explicit cache seam |
| `axis daemon status` | stable | Cache freshness inspection |
| `axis daemon invalidate` | stable | Explicit cache invalidation |
| `axis daemon refresh` | stable | Explicit cache refresh |
| `axis daemon restart` | stable | Daemon lifecycle restart |
| `axis serve` | experimental | Optional local HTTP API; execution surface |
| `axis mcp serve` | experimental | Optional read-only MCP diagnostic server |
| `axis chat` | experimental | Advisory Ollama chat; explicitly experimental per Truth Rule |
| `axis agent` | experimental | Agentic tool-calling assistant; safety-gated but evolving |
| `axis llm` | experimental | Hybrid LLM routing; local + cloud fallback |
| `axis cortex` | experimental | Distributed vector memory / event bus; requires optional Foundry node |
| `axis summary` | stable | Cluster summary view |
| `axis reservations` | stable | Reservation inspection (read-only) |

---

## 5. Transition Policy

| From → To | Requirements |
|-------------|--------------|
| **scaffolded → experimental** | Code is wired into a CLI command or API route; build tag is removed or feature gate defaults to on; emits experimental warning; has basic test coverage. |
| **experimental → stable** | ≥ 80% unit test coverage (or meets package-specific gate); no open P1/P2 bugs; no breaking changes for two minor releases; dependency closure is clean (only stable + internal-only); operator documentation is current. |
| **stable → deprecated** | RFC or issue documenting removal rationale; replacement path is documented and available; deprecation warning is added; target removal version is declared. |
| **deprecated → dormant** | Code is removed from the default build or hidden behind a shim; only migration helpers remain. |
| **dormant → removed** | Allow-listed shim files are deleted; code is removed from the repository. |
| **any → internal-only** | Never happens. `internal-only` is assigned at creation based on lack of operator contract. A package can be split into a stable surface + internal-only helpers. |

### Emergency Exceptions

A package may be downgraded (e.g. `stable → experimental`) without an RFC if a critical security or correctness issue is discovered that cannot be patched within a single release cycle. The downgrade MUST be accompanied by:

1. A `SECURITY.md` or issue reference.
2. An immediate warning emitted on use.
3. A recovery timeline in the next release notes.

---

## 6. CI Enforcement Ideas

These are design proposals, not implemented checks.

### 6.1 Transitive Dependency Lint
- A Go program or script that reads `go list -deps` output for every package classified as `stable`.
- If any transitive dependency maps to `experimental`, `scaffolded`, `dormant`, or `deprecated`, the build fails.
- The mapping file (`docs/lifecycle.md` or a machine-readable `lifecycle.json`) is the source of truth.

### 6.2 Experimental Warning Lint
- A static-analysis pass (e.g., `go vet` plugin or AST grep) that verifies every function exported from an `experimental` package logs a warning on first call.
- For CLI commands, verify the `RunE` or `PersistentPreRun` prints to stderr or sets a warning flag.

### 6.3 Scaffolded Gate Lint
- Verify that every symbol in a `scaffolded` package is guarded by a build tag (`//go:build scaffolded`) or a runtime feature-gate check.
- Default release builds (`make build`) must not link scaffolded code.

### 6.4 Dormant Import Allow List
- A CI step that greps all non-test `.go` files for imports of packages classified as `dormant`.
- Only files explicitly listed in `.github/dormant-allow-list.txt` are permitted to import them.
- Any new import outside the allow list fails the build.

### 6.5 Deprecation Window Check
- A check that verifies every `deprecated` symbol has a `// Deprecated: <replacement>` comment and a target removal version.
- Target removal version must be ≤ current version + 2 minor releases.

### 6.6 Lifecycle Drift Detection
- A weekly CI job (or PR check when `docs/lifecycle.md` changes) that compares the documented classification against:
  - Actual test coverage per package.
  - Actual import graph (stable must not import experimental).
  - Presence of `//go:build` tags on scaffolded packages.
- Mismatches are surfaced as PR comments or build warnings.

---

## References

- `docs/current-state.md` — live package maturity and command surface
- `AGENTS.md` — Truth Rule and architecture overview
- `internal/buildinfo/version.go` — release version source of truth
