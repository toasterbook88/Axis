# AXIS Apple Foundation Models Roadmap

**Status:** planning / design only. Not shipped behavior.  
**Date:** 2026-09-30  
**Authority:** live code and `docs/current-state.md` beat this file.  
**Related PRs (open on 2026-09-30 against `main` @ `7f0fbb8`):**

- [#482](https://github.com/toasterbook88/Axis/pull/482) `feat(ui): use the Unicode ANSI Shadow wordmark` (`feat/unicode-ansi-shadow-wordmark`)
- [#483](https://github.com/toasterbook88/Axis/pull/483) `fix(placement): put plan helpers back on the cache-first path` (`fix/placement-plan-deadcode`)
- [#484](https://github.com/toasterbook88/Axis/pull/484) `feat(placement): support remote Apple Foundation Models discovery and cluster placement` (`feat/remote-apple-foundation-models-placement`)

## Scope

Evolve AXIS from probe-verified Apple Foundation Models *facts* to cluster-wide AFM placement, guarded remote execution, structured generation, `axis ai route` backend wiring, and Apple-ecosystem advertisement.

This document is a roadmap. It does not authorize merges, change the fact plane, or claim live node availability.

## Shipped vs planned

Verified on `main` via `docs/current-state.md` @ `7f0fbb8`:

- Probe-verified **local** Apple Foundation Models capability on eligible Apple Silicon hosts running macOS 26 or later.
- Surfaced in node facts/snapshots and as an `apple-foundation-models` tool.
- Actual model execution only through the explicit guarded execution path.

Not shipped (this plan):

- Remote AFM as a first-class placement/execution backend (PR #484, unmerged).
- Helper invocation harness over SSH with reservation + thermal shedding.
- `@Generable` Swift schemas and AFM tool-calling.
- `~/.axis/ai.yaml` AFM backend + cascading fallback.
- A2A agent-card advertisement, App Intents / Siri bridge, AxisMCP-iOS companion sync.

## Invariants this plan must not break

- Truth rule: AFM availability is a fact-plane probe (`SystemLanguageModel.default.availability == .available`), not SDK import presence.
- Authority: fact → snapshot → placement → execution → advisory. AFM routing and agent cards stay advisory unless a live probe and guarded exec path back them.
- Placement pipeline stays `FilterCandidates` → `RankCandidates` → `SelectBestNode`. `FitScore` remains diagnostic.
- Guarded execution: safety → reserve → run → release. No auto-approve.
- One binary. Helper cache path is operator runtime (`persist.AxisPath("cache")`), not a second control plane.
- Public docs: RFC 2606 / public-boundary. No private hosts, IPs, or cluster topology.

## Five tracks

```
Track 1 Delivery     PR #482 + #483 merge → PR #484 probe fixes → main stability
Track 2 Execution    SSH helper harness → streaming/guardrails → empirical warmth/latency
Track 3 Structured   @Generable schemas → read-only MCP tools as Swift Tools
Track 4 Routing      ai.yaml AFM backend → cascade: local AFM → remote AFM → resident Qwen → Ollama
Track 5 Ecosystem    A2A agent-card → App Intents/Siri → AxisMCP-iOS companion
```

Tracks 2–5 depend on Track 1. Track 3 may proceed in parallel after the probe contract is honest.

## Track 1 — Delivery and probe rigor

Hold gate: no merge without operator authorization.

### #482 / #483

Open, targeting `main`. Treat as independent of AFM placement. Merge only after the operator says so and CI on those heads is green. This session did not re-run CI logs.

### #484 Copilot findings (review on head `f003487`, 2026-09-30)

1. **High.** Fallback probe must not treat FoundationModels importability as runtime-verified availability. Required check: `SystemLanguageModel.default.availability == .available`.
2. **Medium.** Helper lookup must include the canonical cache path from `persist.AxisPath("cache")` (`~/.axis/cache/apple-foundation-models-helper` on a default home), not only ad-hoc locations.
3. **Low.** `placement_test.go` comment claiming `localBonus` changes node order is wrong if the test only asserts selection/explanation. Fix the comment.

Do not land remote AFM placement while (1) is open. That would admit unusable nodes into `FilterCandidates`.

## Track 2 — Guarded remote AFM execution

Proposed surface: `axis task run` invoking `apple-foundation-models-helper --prompt "<prompt>"` over existing SSH transport.

Required wiring (re-read call sites before coding):

- `internal/transport` for the remote invoke
- `internal/safety` then `internal/reservation` then run then release
- Timeouts; thermal/battery shedding from already-collected power/thermal facts (`pmset` on Darwin is a probe input, not a new authority)
- Empirical warmth/latency observations in the existing empirical observation path — do not invent a parallel store

Confirmation stays explicit. Helper binary is not execution authority by itself.

## Track 3 — Structured generation

Swift `@Generable` for typed JSON consumed by `axis task context` and `axis agent`.

AXIS diagnostic MCP/HTTP tools remain subordinate and mostly read-only. Expose them into Swift `Tool` types only as advisory inputs. Leases still write the local reservation ledger only.

## Track 4 — `axis ai route` cascade

Add `apple-foundation-models` as an explicit backend in `~/.axis/ai.yaml` (separate from `nodes.yaml`).

Proposed preference order for AFM-capable prompts, **after** feasibility filters:

1. Local Apple Silicon AFM (same node as requester)
2. Remote Apple Silicon AFM (probe-verified)
3. Resident Qwen-class local weights
4. Ollama cluster

Ranks in PR #484 are placement ranks, not this cascade. Do not conflate `FitScore`, placement rank, and router tier. Document provenance on every route decision.

## Track 5 — Apple ecosystem and mesh

Advisory only:

- Advertise AFM capability on `/.well-known/agent-card.json` from observed facts, with publication source and age.
- Native macOS App Intents / Shortcuts as a client of AXIS HTTP/MCP, not a second control plane.
- Coordinate with AxisMCP-iOS only through that repo's own PR process. Do not treat an iOS companion PR number as AXIS shipped state.

## Operator-reported telemetry (unverified here)

A prior session claimed probe-verified AFM availability on three operator Macs and a compiled helper under `~/.axis/cache/`. This Grok session did not SSH those hosts and does not promote that claim to cluster truth.

Re-verify on the operator workstation:

```bash
# per Darwin node, after helper exists
apple-foundation-models-helper --self-test
# and the Swift availability enum, not import-only compile
```

## Verification when code changes

- Behavior: `make test` and `make lint`
- Placement / race: `make test-race`
- Doc / CLI / MCP / exit codes: `./hack/verify-doc-facts.sh`
- Public strings: `./hack/verify-public-boundary.sh`
- Release / current-state claims: `./hack/verify-repo-truth.sh`

## Next operator decision

1. Authorize the three #484 probe/comment fixes, or
2. Authorize merge of #482 and #483 first.

Do not start Tracks 2–5 on `main` until #484's availability probe is honest.
