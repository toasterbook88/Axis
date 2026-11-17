# Doctor probes configured ai.yaml HTTP backends

Date: 2026-11-16. Status: implemented on `fix/practical-hardening` (#650).

## Problem

`axis doctor` reported process-level AI backends (llama-server, MLX, Ollama) but
not reachability of backends listed in `~/.axis/ai.yaml`. Operators could have a
misconfigured or down HTTP hub and still see green doctor output.

A shared five-second context passed into sequential `ProbeAllBackendsForNodes`
also made later healthy backends look failed when earlier ones exhausted the
deadline.

## Non-goals

- Changing routing, catalog, or placement.
- Writing `ai.yaml` or inventing new config keys.
- Concurrent probing (stable sequential order stays).
- MCP HTTP URL reachability in doctor (separate slice on the same PR branch).

## Design

- `doctorAIConfigChecks` loads `ai.yaml` (empty = no rows) and probes via
  `llmrouter.ProbeAllBackendsForNodes` under a five-second overall budget that
  gates when the next backend *starts*. An in-flight probe may still run up to
  `defaultProbeTimeout` (3s) after that parent expires because each probe uses
  `context.WithoutCancel`.
- Each backend gets an independent `defaultProbeTimeout` via
  `context.WithoutCancel` so earlier hangs cannot cancel later probes.
- When the overall context is already done before a backend starts, that row is
  `Skipped` (warn, no Fix). Observed failures keep the existing repair Fix.
- Extracted to `cmd/axis/doctor_ai_config.go` so `doctor.go` stays under the
  line cap.

## Alternatives

- One shared context for all probes: rejected; produces false warnings.
- Concurrent probes: deferred; order stability matters for operator reading.

## Research

- Prior art: `cmd/axis/ai.go` and `cmd/axis/inference_hint.go` already call
  `llmrouter.ProbeAllBackendsForNodes` / `ProbeBackendForNodes`; this change
  reuses those helpers from doctor rather than inventing a second probe path.
- Measurement: `TestProbeAllBackends_IndependentBudgets` proves later backends
  are `Skipped` (not false observed failures) when the parent budget is spent;
  `TestProbeAllBackends_HealthyAfterHangsWhenBudgetRemains` proves a healthy
  backend after hangs is still OK when budget remains.

## Review

Kind: spec
Reviewer: fresh-context subagent
Model: composer
Base: 757880319c7ef304728b3cb3bcc2900c704cf6fa
Head: 66ef65c3502e3087cb19064b6d8a80b25687e2d6
Date: 2026-11-16
Verdict: approve-with-changes

1. Research Measurement mis-cites the test. Fixed in this file: IndependentBudgets asserts Skipped; HealthyAfterHangsWhenBudgetRemains asserts OK.

2. Research prior art lacks §8 links. Fixed in this file: cites `cmd/axis/ai.go` and `cmd/axis/inference_hint.go`.

3. Five-second overall budget is ambiguous. Fixed in this file: budget gates starts; in-flight probes may overrun via WithoutCancel.

Questions:

- MCP HTTP URL reachability on the same branch is out of this spec's scope. Verified: Non-goals now name that separate slice.
