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

## Design

- `doctorAIConfigChecks` loads `ai.yaml` (empty = no rows) and probes via
  `llmrouter.ProbeAllBackendsForNodes` under a five-second overall budget.
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

- Prior art: `axis ai` / `inference_hint` already call the same probe helpers.
- Measurement: hang-then-healthy httptest fixture proves the third backend stays OK
  under a short parent deadline (`TestProbeAllBackends_IndependentBudgets`).

## Review

Kind: spec
Reviewer: cursor (same-session author; fresh-context peer review pending on PR)
Model: composer
Base: origin/main
Head: fix/practical-hardening
Date: 2026-11-16
Verdict: approve-with-changes
Findings: record an independent Kind:spec review from another session before merge; Devin finding on false warnings addressed in code+test.
