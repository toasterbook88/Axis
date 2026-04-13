# CHANGELOG

## v0.8.0 — Empirical placement arc + multiruntime resident models + doctor AI checks

**Empirical placement arc (PRs #66–#71)**

The v0.8.0 release lands the full empirical placement arc. Prior releases tracked
execution observations but only used them as soft ranking bonuses. v0.8.0 makes
empirical history load-bearing:

- **Per-model observation scoping** (#69): `ObservationScope` now carries a
  `ModelName` field so different models on the same node accumulate independent
  peak-RAM histories. Observation key derivation uses SHA-256 over all five scope
  fields (node, workload class, backend, tool, model name), preventing cross-model
  contamination.

- **MLX resident model detection** (#70): `axis facts` and cluster snapshots now
  include models served by `mlx_lm.server` alongside the existing Ollama collector.
  MLX models are discovered via the `/v1/models` HTTP endpoint and tagged with
  `runtime: mlx`, `source: mlx-lm-api`.

- **Hard `PeakRAMMB` pre-filter** (#71): `FilterCandidates` now excludes any node
  whose freshly-observed `PeakRAMMB` exceeds the node's current allocatable RAM
  before the ranking phase begins. The filter short-circuits on stale or missing
  observations (safe default: allow). `inferenceModelName` is hoisted outside the
  per-node loop (one regex compile per placement call, not per node).

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
