# Import-direction check for the AGENTS.md tier layout

Date: 2026-10-30. Base: `origin/main` 1493da2. Status: reviewed (approve-with-changes, all findings
applied). Maintainer chose B on 2026-10-30. Implemented in the same PR as this spec.

## Problem

`AGENTS.md` §4 defines five trust tiers plus a Support group and says higher tiers consume
lower-tier authority. Nothing enforces it. `hack/verify-doc-facts.sh` §4b only greps `AGENTS.md`
for each `internal/<pkg>` name; it does not read the tier block or check import direction.

Measured on base with `go list -json ./internal/...` (default tags): 47 packages, 153 internal
import edges, 10 of them upward (a package importing a package in a higher tier):

| From (tier) | To (tier) | What is used |
| --- | --- | --- |
| `events` (Support) | `cortex` (5) | package-level `var cortexClient *cortex.Client`, set by `SetCortexClient`, `internal/events/events.go:19` |
| `events` (Support) | `reservation` (4) | `reservation.SetEventEmitter`, `internal/events/reservation_sink.go` |
| `knowledge` (Support) | `snapshotview` (2) | `Clone`, `ApplyReservationView` |
| `daemon` (2) | `execution` (4) | `GuardedExecutionResult` (26 uses), request and state types |
| `daemon` (2) | `skills` (4) | `skills.Load` |
| `daemon` (2) | `reservation` (4) | `Ledger`, `NewLedger`, `Entry`, `DefaultLimits` |
| `publication` (2) | `reservation` (4) | `Entry` |
| `runtimectx` (2) | `skills` (4) | `Store`, `Load` |
| `runtimectx` (2) | `reservation` (4) | `Ledger`, `Entry`, `NewLedger`, `DefaultLimits` |
| `snapshotview` (2) | `reservation` (4) | `Ledger`, `Entry` |

Without a gate, every new upward import lands silently and the tier diagram drifts from the code.

## Layout or code?

Some of these edges say the layout is wrong, not the code. Measured on base:

- `reservation` imports only `models`, `persist`, `repairs` (all Support).
- `skills` imports only `persist` (Support).
- `knowledge` is imported only by `internal/agent`, `internal/api` and `cmd/axis` (Layer 5 and above).
- `execution` imports `placement` (3) and `runtimectx` (2), so `daemon` hosting it is a composition
  root, not a snapshot-tier consumer.

| Choice | Seed allowlist | Remaining exceptions |
| --- | --- | --- |
| A. Keep the layout, allowlist all 10 | 10 | all |
| B. Move `knowledge` to Layer 5; `reservation` and `skills` to Layer 2 | 3 | `events -> cortex`, `events -> reservation`, `daemon -> execution` |
| C. As B, but `reservation` and `skills` to Support | 2 | `events -> cortex`, `daemon -> execution` |

Moving `reservation` changes what a trust tier means (it backs guarded execution and the daemon
ledger), so this is the maintainer's call. **Recommendation: B.** It makes the diagram match the code
without claiming the ledger is support plumbing. The layout edit ships in the same PR as the check.

## Non-goals

- Code fixes for the remaining exceptions (a sink interface so `events` stops importing `cortex`;
  whether `daemon` stays exempt as a composition root). Each gets its own spec.
- Transitive checks, external-dependency policy, public-API leak checks (higher-tier types in a
  lower tier's exported API).
- Test imports, including `-tags fleet` (for example `fleettest` test files import `facts` and
  `transport`). Tests may cross tiers for fixtures.
- `cmd/`, `examples/` and the module root, which sit above every tier.

## Design

`hack/verify-import-tiers.go`, `//go:build ignore`, run as `go run hack/verify-import-tiers.go` from
the module root, like `hack/lifecycle-check.go`. Standard library only. Because of the build tag,
`go vet`, staticcheck, deadcode and coverage do not see it; `gofmt` and the tests script cover it.

**Inputs.** Module path from `go list -m`. Repo root is the working directory. Fail closed on a
nonzero `go list` exit and on any package whose `Error` or `DepsErrors` is set. Decode `go list
-json` output as a stream of objects. Every go command runs with `GOWORK=off`: in a multi-module
workspace `go list -m` prints every module and the joined prefix would match nothing. Fail closed when
`go list` returns no packages or a package outside `<module>/internal/`. Run `go list` with
`CGO_ENABLED=0` for each release platform in `.goreleaser.yml` (linux and darwin, amd64 and arm64)
and check the union of edges; they are identical on base.

**Tier block.** Locate the first fenced `text` block after the `## 4.` heading; fail if absent.
A header line matches `^(Layer [1-5]  <name>|Support)` and sets the current tier; the name is the
text between `Layer N` and the first `internal/` (for example "Fact Plane"); Support is tier 0.
Indented continuation lines inherit the current tier. Every other token must be exactly
`internal/<name>`; a nested path or stray token is a parse error. A package listed twice fails. A
listed package with no directory under `internal/` fails as stale. A directory under `internal/`
missing from the block fails: "internal/foo is not in the AGENTS.md tier layout". This replaces
`verify-doc-facts.sh` §4b, which the same PR retires.

**Rule.** `internal/<pkg>/...` inherits `<pkg>`'s tier, and imports within the same top-level
directory are skipped. `go list` runs with `-deps`, keeping only packages under `<module>/internal/`,
because the `./internal/...` pattern skips `testdata` and `_`- or `.`-prefixed directories that
production code can still import. For every production import (`Imports`) of another top-level package: allowed
when `tier(dst) <= tier(src)`. Support (0) is importable by everyone and may import only Support.

**Allowlist.** `hack/import-tiers-allowlist.txt`. `#` comment lines and blank lines are ignored.
Each entry is `<pkg> -> <pkg>  # <rationale>` with bare top-level names. Fails on: a missing
rationale, an unknown package, a duplicate, and a stale entry (no longer an upward import). Failing
on stale entries deliberately differs from the deadcode gate, which only reports them: an exception
that outlives its reason would silently allow a later regression.

**Failure text** names the edge, both tiers and the three ways out:
`internal/events -> internal/cortex: Support may not import Layer 5 (Advisory). Invert the dependency,
move internal/events or internal/cortex to the right tier in AGENTS.md §4, or add "events -> cortex  # <why>"
to hack/import-tiers-allowlist.txt.`

**Wiring.** `hack/ci-preflight.sh` and `.github/workflows/ci.yml` both run the check and its tests
script, next to the deadcode gate and its tests. `.github/workflows/release.yml` runs the check, since
it ran the retired inventory check through `verify-doc-facts.sh`. The "CI also runs" line in
`AGENTS.md` §3 names it, and `docs/architecture.md` moves the same two packages.

**Tests.** `hack/verify-import-tiers-tests.sh`: a shell harness like `verify-deadcode-tests.sh`
(`mktemp` root, a `run_gate` helper, `fail()`, exit-code and stderr asserts), but driving the real
toolchain on throwaway fixture modules with `GOWORK=off GOTOOLCHAIN=local GOFLAGS=-buildvcs=false`,
fixture `go.mod` `go` lines no newer than the local toolchain, and no `main` packages. Cases:
upward import fails; same-tier and downward pass; nested sub-package inherits its parent's tier;
allowlisted passes; stale, duplicate, unknown-package and rationale-less entries fail; untiered and
stale block packages fail; missing or malformed tier block fails; a package that fails to load fails.
It also asserts its own wiring by grepping `ci-preflight.sh` and `ci.yml`, as the deadcode tests do.

## Alternatives

| Option | Why not chosen |
| --- | --- |
| Edit `AGENTS.md` only, no gate | Fixes today's drift but not the next one. Included as part of choice B, not instead of the gate. |
| depguard (standalone `.depguard.yml` or via golangci-lint) | Prefix allow and deny lists per file glob; the tier layout becomes a second config file that drifts from `AGENTS.md`. |
| go-arch-lint | Clean adjacency-list manifest and graph output, but a new tool and a second copy of the layout. |
| arch-go | Broader rules (package contents, naming) not needed yet; same second-copy problem. |
| Kubernetes `import-boss` | Per-directory `.import-restrictions` files; pulls in the large `k8s.io/kubernetes` module. |
| Custom `go/analysis` analyzer | Works, but more code than a `go list` pass for the same result. |
| Extend `hack/lifecycle-check.go` | It already checks imports, but by lifecycle state (a stable package may not import an experimental one), with its package list and allowlist in Go maps. Trust tier is a different axis; folding both into one tool would mix two policies and add a third copy of the package list. Kept separate; a later spec could have it read the same `AGENTS.md` inventory. |

## Research

- Go itself declares its layered dependency policy in-repo in `src/go/build/deps_test.go`: a partial
  order in a small DSL, standard library only, fail closed, with "DO NOT CHANGE THIS DATA TO FIX
  BUILDS" (<https://raw.githubusercontent.com/golang/go/master/src/go/build/deps_test.go>). Closest
  precedent; we take the in-repo declaration and the fail-closed rule.
- Kubernetes runs `import-boss` from `hack/verify-import-boss.sh`; rules files are searched upward and
  an undecided import is an error (<https://pkg.go.dev/k8s.io/kubernetes/cmd/import-boss>).
- depguard v2 matches allow and deny lists by import-path prefix per file glob, standalone or inside
  golangci-lint (<https://github.com/OpenPeeDeeP/depguard>).
- go-arch-lint and arch-go keep components and allowed dependencies in their own manifests
  (<https://github.com/fe3dback/go-arch-lint>, <https://github.com/arch-go/arch-go>).
- Import linters cannot see higher-tier types leaking through a lower tier's exported API
  (<https://pkg.go.dev/github.com/ddromanidis/arch-linter/analyzer>). Recorded as a non-goal.

Optimization chosen: read the layout from `AGENTS.md` rather than a separate config, so the layout has
one home and every listed tool's duplicate copy disappears. No new dependency (15 direct dependencies
stay 15). Cost is one `go list` pass per GOOS: one linux pass measured 0.11 s warm and 0.51 s cold on
base for 47 packages; the built gate with two passes measured 0.22 s warm, and `go run` with all four
passes, compile included, 0.64 s.

## Risks

- The `AGENTS.md` tier block becomes load-bearing for CI. The parser fails closed with a named error.
- The allowlist grows instead of shrinking. Stale entries fail and every entry carries its reason.
- Platform-specific files could add edges only one platform sees. The union over all four release
  platforms covers what ships; other platforms are not checked.

## Review

Reviewer: fresh-context subagent (prompt held only this spec's path, the base commit and the review ask)
Model: same vendor family as the author; a different family was not selectable in this session
Base: 1493da2
Date: 2026-10-30
Verdict: approve-with-changes

1. Major: the gate would freeze a layout the code contradicts. Resolved: "Layout or code?" section,
   doc-only alternative, choice table; author re-verified the import facts behind it.
2. Major: the tests script was not wired into CI. Resolved: Wiring and Tests now run it in both
   places and assert the wiring.
3. Major: checker inputs undefined. Resolved: module path, root, `go list` failure handling,
   stream decoding, hermetic fixture environment.
4. Minor: missing `//go:build ignore`. Resolved in Design.
5. Minor: tier-block parsing under-specified. Resolved: fence location, header grammar, tier names,
   strict tokens, stale block entries; §4b retired.
6. Minor: sub-package rule implicit. Resolved: inherit parent tier; nested fixture case.
7. Minor: one GOOS only; tag caveat. Resolved: linux and darwin union; test imports and `fleet` tag
   stated out of scope. Author verified internal edges are identical for both GOOS on base.
8. Minor: allowlist semantics. Resolved: format, comments, duplicates, unknown packages, deliberate
   stale failure, third way out in the failure text.
9. Minor: test-style reference inaccurate. Resolved: reworded.
10. Minor: small factual errors (package var not field; §4b behavior; `examples/`). Resolved.
11. Minor: missing `deps_test.go` precedent; depguard row scope. Resolved in Research and Alternatives.

Found during implementation, after the review: `hack/lifecycle-check.go` is an in-repo precedent
that the spec and the review both missed. Added to Alternatives.

Implementation review (second fresh-context agent, code reviewer, same vendor family, base 1493da2,
2026-10-30): approve-with-changes.

1. Major: under a multi-module `go.work` the gate checked nothing and passed. Resolved: `GOWORK=off`
   on every go command; fail closed on zero packages or a package outside the module; fixture test.
2. Minor: only GOOS varied, not GOARCH. Resolved: all four release platforms; arm64 fixture test,
   which failed before the fix.
3. Minor: the release workflow lost the inventory check. Resolved: release step added and asserted.
4. Minor: `docs/architecture.md` still placed `reservation` and `skills` in Layer 4. Resolved for the
   two moved packages; its older drift (`runtimectx`, `publication`, `llmrouter`) is left for a
   follow-up so this change stays scoped.
5. Minor: test gaps versus the spec. Resolved: missing heading, stray line, early continuation,
   `p.Error` load failure, darwin-only and arm64-only imports.

PR review (Copilot, on #585): `./internal/...` skips `testdata` and `_`- or `.`-prefixed
directories, so an upward import routed through a package there passed. Reproduced in a fixture
(0 edges, exit 0), then fixed by listing with `-deps`; two fixture cases (`testdata/helper` and
`_hidden`) failed before the fix and pass after.

Replay review (fresh-context agent with the standard prompt in `docs/superpowers/review-prompt.md` (added by #586),
same vendor family, base 1493da2, head 14f3034c, 2026-10-30): approve-with-changes. It did not find
the `-deps` blind spot above.

1. Minor, partial: the tier-block search was not bounded to §4; with no block under §4 the gate read
   a later section's block and passed. Fixed: the search stops at the next `## ` heading; a fixture
   with the block under §5 failed before the fix and passes after.
2. Minor, partial: `releaseTargets` is hardcoded rather than read from `.goreleaser.yml`. Fixed by
   assertion: the tests script fails when `.goreleaser.yml` ships a different platform set.

Decision record: `docs/decisions/import-tier-layout.md`.

Decision: the maintainer chose B on 2026-10-30 (`knowledge` to Layer 5; `reservation` and `skills`
to Layer 2; seed allowlist of 3). Every finding is resolved, so code may start.
