# Decision: Capability Probe Contract

**Date:** 2026-07-25
**Scope:** `internal/facts` or new `internal/capability`, `internal/models`, `cmd/axis/probe.go`, `~/.axis/capabilities.json`
**Decision:** MEASURE real inference through backends already present on a node. Operator-invoked only. No synthetic benchmarks, no shipped hardware tables, no model downloads.
**Related:** `docs/decisions/placement-selection-contract.md` (the `fastest` objective consumes this), `docs/decisions/topology-truth-contract.md` (why network throughput is out of scope), `docs/sovereign-grid-architecture.md` §3 (Granular Capability Hashing)

---

## 1. Context

The `fastest` placement objective needs data the fact plane does not hold: how quickly a node actually completes inference work. Nothing in `Resources` or `GPUInfo` expresses memory bandwidth, compute throughput, or achievable tokens per second, and the existing scoring caps compress a roughly 13× hardware spread into single-digit point differences.

Two obvious sources were rejected.

**A shipped hardware-specification table** (GPU model → bandwidth/TFLOPS/TOPS) would be reference data. Under the observed/inferred/absent discipline it would carry an inferred marker permanently, go stale against new hardware, and miss anything unlisted. It also cannot represent the node's actual configuration — driver version, quantization support, thermal envelope, or contention.

**A synthetic microbenchmark** (STREAM-style bandwidth, FLOPS loops) would require shipping or compiling a payload on every node, expanding AXIS's footprint well past "agentless by default", and would still only proxy the thing operators care about.

The direct measurement is available for free. Backends already detected by the fact plane report exact timings for real inference work.

## 2. Decision

AXIS measures node capability by running a short, bounded inference request against a model **already present** on that node, through a backend the fact plane already detected, and recording the timings the backend itself reports.

1. Probes are **operator-invoked only** — never automatic, never scheduled, never triggered by placement.
2. Probes **never download models** and never install software.
3. Probe results are **observations**, timestamped and versioned, stored separately from cluster state.
4. Probe results are **inputs to objectives only**. They are never cluster truth and never bypass the feasibility filter.

## 3. What is measured

Backends expose structured timings. For an Ollama-class backend, a single generate call returns `load_duration`, `prompt_eval_count`, `prompt_eval_duration`, `eval_count`, `eval_duration`, and `total_duration`, yielding three distinct quantities:

| Quantity | Units | Derivation | Predicts |
| --- | --- | --- | --- |
| **Model load time** | ms | `load_duration` on a cold run | Cost of scheduling to a node without the model resident |
| **Prefill throughput** | tok/s | `prompt_eval_count ÷ prompt_eval_duration` | Prompt/context processing cost |
| **Decode throughput** | tok/s | `eval_count ÷ eval_duration` | Generation speed; the memory-bandwidth-bound term |

Backends that do not report timings are recorded as unmeasurable for that quantity. AXIS does not compute a substitute.

**Not measured, deliberately:** memory bandwidth in GB/s, FLOPS, and TOPS. Decode throughput is the bandwidth-bound outcome operators actually care about, measured directly. Reporting a synthetic GB/s figure would add a number that is harder to obtain, easier to get wrong, and less relevant than the one already available.

## 4. Measurement discipline

Probe timings are unstable in ways that make naive measurement actively misleading. Measured live on one host, same model, same backend:

| Run | load | prefill | decode | decode tokens |
| --- | --- | --- | --- | --- |
| cold | 6213 ms | **1.7 tok/s** | 248.5 tok/s | **3** |
| warm 1 | 301 ms | 1450 tok/s | 190.4 tok/s | 128 |
| warm 2 | 287 ms | **6565 tok/s** | 197.1 tok/s | 128 |

Three rules follow directly, and each has a failure it prevents:

**R1 — Cold and warm phases are separate measurements.** Cold prefill was wrong by roughly 3,800×. Load time is only meaningful on a cold run; throughput is only meaningful once warm. A probe must run an explicit warm-up, record load time from the cold pass, and record throughput from subsequent passes. One run cannot produce both.

**R2 — Token-count floors, with actual counts recorded.** The cold run reported the *highest* decode figure of all three (248 tok/s) because the model emitted 3 tokens before stopping. A probe must enforce a minimum decode and prefill token count, must use a prompt that reliably reaches it, and must record the counts actually achieved so consumers can reject undersized samples.

**R3 — Multiple warm samples, median and spread.** Consecutive warm prefill measurements differed by 4.5× at 35 prompt tokens. Decode over 128 tokens varied 3.5%. A single sample is not a measurement. Probes record N warm samples; consumers use the median and must have access to the spread.

A quantity whose spread exceeds a stated threshold is recorded as **unstable** and is not used for ranking.

## 5. Safety and consent

- **Operator-invoked.** A probe runs only when the operator asks. Placement never triggers one. Nothing schedules one. Running benchmarks as background activity would contradict `docs/doctrine.md` ("agentless by default", "not a background scheduler", "no hidden agent runtime").
- **Refuses unsafe hosts.** Skips nodes on battery power, or in `serious`/`critical` thermal state, and reports each skip with its reason rather than omitting the node.
- **Never downloads.** Probes use only models already present on the target node. If no suitable model exists, the node is reported as unprobed. Pulling a model would be a hidden download, which `docs/future-roadmap.md` lists under "Avoid".
- **Bounded.** Hard ceilings on token count, wall-clock duration per node, and total sweep duration. Exceeding a ceiling aborts that probe and records a timeout, not a slow result.
- **Reservation-aware.** A probe consumes real resources and must respect existing reservations rather than competing with running work.

## 6. Storage and provenance

Probe results are written to `~/.axis/capabilities.json`, separate from `~/.axis/state.json`.

The separation is deliberate. `state.json` is the state authority per `docs/authority-violations.md`; capability observations are a distinct concern with a distinct lifecycle, and the audit (C3) documents what happens when writes into the state authority are not tightly controlled.

Each record carries:

| Field | Purpose |
| --- | --- |
| node identity (stable ID where available) | Survives hostname drift |
| backend, model, quantization | Results are per-backend and per-model, never per-node alone |
| quantity, value, unit | Explicit units; no dimensionless numbers |
| sample count, median, spread | Supports R3 |
| tokens achieved | Supports R2 |
| probe version | Results from different probe logic are not comparable |
| observed-at | Supports staleness |
| host conditions at probe time | Thermal state, power source, load — a result measured under contention is not the same result |

**Staleness is explicit.** Records age; stale records render as stale and are excluded from ranking rather than silently trusted. Hardware changes rarely, but drivers, thermal conditions, and background load do not.

**Results are per model and backend, not per node.** A node is not "fast" — a node running a specific model through a specific backend at a specific quantization has a measured throughput. Generalizing across models would be inference presented as observation.

## 7. Command surface

```
axis probe run    [--node <name>] [--all] [--backend <name>] [--model <name>]
axis probe show   [--node <name>] [--stale]
axis probe clear  [--node <name>]
```

`run` is explicit and reports per node: measured, skipped (with reason), or unprobed (no suitable model). `show` renders results with units, sample counts, spread, and staleness. `clear` removes records, for use after hardware or driver changes.

Consistent with existing multi-verb commands (`axis mesh status|peers`, `axis context show|clear`, `axis scripts list`).

## 8. Consumption by placement

Capability observations feed the `fastest` objective in `docs/decisions/placement-selection-contract.md` and nothing else.

- They **never** bypass or influence the feasibility filter.
- They **never** appear as cluster truth in `axis facts` or `axis status`.
- When absent, stale, or unstable, `fastest` degrades to `headroom` and states the degradation, per that contract's rule P6.
- A node that has never been probed is not penalized. It is ranked by the fallback and reported as unprobed.

That last rule matters: penalizing unprobed nodes would create pressure to probe, which is a soft form of the automatic sweeping this contract forbids.

## 9. Non-goals

- **No node-to-node network throughput probing.** That would measure an edge, which `docs/decisions/topology-truth-contract.md` rejects pending its own decision record. Probes measure a node, from itself.
- No synthetic bandwidth, FLOPS, or TOPS benchmarks.
- No shipped hardware-specification lookup table.
- No model downloads, installs, or configuration changes on target nodes.
- No automatic, scheduled, or placement-triggered probing.
- No training, fine-tuning, or long-running benchmark workloads.
- No cross-node result comparison as a ranking of nodes in the abstract — results are per backend and model.

## 10. Acceptance criteria

| ID | Test | Pass condition |
| --- | --- | --- |
| **C1** | Probe a node with no resident or available model | Reported unprobed; no download attempted |
| **C2** | Probe a node on battery power | Skipped with a stated reason; node still listed |
| **C3** | Probe a thermally throttled node | Skipped with a stated reason |
| **C4** | Cold-run timings | Load time recorded from cold pass; throughput not taken from it (R1) |
| **C5** | Generation stops below the token floor | Sample marked undersized and excluded (R2) |
| **C6** | Warm samples with spread above threshold | Quantity marked unstable and not used for ranking (R3) |
| **C7** | Backend reporting no timings | Recorded unmeasurable; no substitute computed |
| **C8** | Stale records with `fastest` selected | Objective degrades and says so |
| **C9** | Unprobed node in a `fastest` ranking | Not penalized; reported as unprobed |
| **C10** | Probe exceeding its wall-clock ceiling | Recorded as timeout, not as a slow result |
| **C11** | Test suite execution | No writes to real `~/.axis/capabilities.json`; `HOME` isolated in every test |

C11 is a direct regression of audit finding C3, where a test lacking `HOME` isolation wrote into the operator's canonical state and accumulated 70 fixture samples. Any new persistent store must not repeat it.

## 11. Consequences

**Added.** One operator-invoked command, one local store, and a set of observations with explicit units and provenance. No new dependencies: measurement uses backends the fact plane already detects, via HTTP endpoints it already probes.

**Coverage is partial by design.** Nodes without a local model, without a detected backend, on battery, or thermally stressed are unprobed — and stay unprobed until the operator chooses otherwise. `fastest` is therefore a best-effort objective, which is why `headroom` remains the default.

**Cost.** Real but bounded: seconds per node, consuming actual compute. This is why it is explicit. An operator who never runs `axis probe` sees no change in AXIS's behavior.

**Honest limitation.** These measurements predict inference throughput for the model and backend measured. They do not predict build times, test-suite duration, or I/O-bound work. Extending `fastest` to those workload classes needs its own measurements, not extrapolation from these.

## 12. Public repository boundary

Probe definitions, fixtures, and documentation use symbolic node names and RFC 5737 documentation addresses. Measured values in this document come from a single host and are illustrative of measurement behavior, not of any particular hardware. No grid hostnames, private or relay addresses, operator home paths, or model catalogs appear here or in fixtures.

`~/.axis/capabilities.json` is operator-local and is never committed.
