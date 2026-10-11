# Model discovery: probe-backed load state and local endpoint candidates

**Status:** design, for review. Not implemented.
**Base:** `main` at `ca76a46` (#507, model catalog as a fact).
**Scope:** the node-local fact collector and the catalog projection over it. No new store, no new control surface.

## Problem

#507 made the model catalog a pure function of the snapshot, so every node renders the same list. The facts under it are still uneven:

1. **MLX residents are wrong.** `mlx_lm.server`'s `GET /v1/models` lists the Hugging Face cache (`huggingface_hub.scan_cache_dir`) plus the `--model` path, not the loaded model. The MLX discovery script publishes every listed id as a resident, so every cached repo on a Mac appears resident on its MLX server. #507 already marks MLX residents `listed`, which bounds the damage but still names models the server is not serving.
2. **llama.cpp `loaded` trusts argv alone.** The resident row comes from the process's `-m`. Nothing checks the server finished loading. llama.cpp documents a public, key-free load signal (`/health`) that the collector does not read. Keyed servers (`--api-key`) answer `401` on `/v1/models` and `/props`, so they cannot be checked any other way.
3. **Apple Foundation Models availability is computed by generating.** The probe runs the cached helper's `--self-test` (a full generation) first and treats a timeout as unavailable. On a fleet Mac this reported `available: false` while `SystemLanguageModel.default.availability` returned `.available`. The probe also omits the facts Apple exposes without generating: `contextSize`, `variant`, `capabilities`.
4. **Engines with no dedicated script are invisible.** A vLLM, LM Studio, LiteLLM-style proxy, or a second Ollama on a non-default port is not seen at all.

## Invariants

- A fact is something the node observed. A listener, a file, or a config entry is not a model until a probe confirms it.
- `loaded` / `ready` requires a load signal from the runtime itself. Listing an id is not a load signal.
- Every published field carries its provenance (endpoint or API plus field). A field the response does not state is omitted, never guessed.
- Probes are localhost only, known dialects only, hard timeouts, bounded counts and budgets. A missing tool degrades to an empty result with a reason, never a failed collection.
- Unconfirmed listeners are recorded as candidates. `Catalog`, `/models`, and placement never read candidates.
- `ai.yaml` overrides apply only by name match to a probed model. They never create a model.
- No automatic start, stop, or load. Lifecycle stays an explicit `axis model start` on an already-verified instance.
- Remote collection keeps working over SSH with `bash -c`, with nothing new installed on the node.

## State rules

States in the catalog: `loaded`, `listed`, `installed` (#507), plus `loading` and `down` from this spec.

| Engine | `loaded` | `loading` | `listed` | `installed` | `down` | Served context | Name |
|---|---|---|---|---|---|---|---|
| Ollama | id in `/api/ps` | — | — | in `/api/tags`, not in `/api/ps` | process seen, API silent | omitted unless the response states a served value | `/api/tags` name |
| llama.cpp (single model) | argv `-m` and `/health` 200 | `/health` 503 `Loading model` | `/v1/models` id differs from argv (named by the server, not the process) | — | process seen, `/health` silent | `/props` `default_generation_settings.n_ctx` when reachable (unkeyed) | argv `-m` basename; `/v1/models` id when readable |
| llama.cpp (router, no `--model`) | — | — | — | — | — | — | candidate only |
| MLX (`mlx_lm.server`) | never | — | argv `--model` | (HF cache entries are already `DiskWeights`) | process seen, API silent | omitted | argv `--model` |
| Apple on-device (`SystemLanguageModel`) | `availability == .available` | — | — | `.unavailable(.modelNotReady)` | — | `contextSize` | `variant.displayName` (macOS 27+), else `apple-foundation-models` |
| Unknown loopback server | never | — | `/v1/models` or `/api/tags` named the model | — | — | vLLM `max_model_len` only | response id |

Notes:

- llama.cpp `/health` is public under `--api-key` (`get_public_endpoints` = `/health`, `/v1/health`). 200 is documented as "the model is successfully loaded and the server is ready". Pairing it with the `-m` of the process that owns the port is the load signal for single-model servers, keyed or not.
- `/v1/models` `meta.n_ctx_train` is training context and is never mapped to `context_window`.
- Apple exposes no residency state for the on-device model; `availability` is its documented readiness signal ("whether the system is entirely ready"). Provenance says residency is OS-managed.
- `.unavailable(.appleIntelligenceNotEnabled)` and `.deviceNotEligible` produce no model entry. The reason is recorded on the node's Apple facts.
- A probe timeout is `unknown`, never a negative.

## Changes

### 1. Correct the existing collectors (PR 1)

**MLX (`MLXDiscoveryScript`)**
- The served model is argv `--model` (or `-m`) of the validated `mlx_lm.server` process, published as one resident with `source: mlx-argv`.
- `/v1/models` is no longer read for residents. The Hugging Face cache it lists is already in `DiskWeights` (`source: hf-hub`).
- Process seen, port silent on `/v1/models` within the timeout: `down`.

**llama.cpp (`LlamaServerDiscoveryScript`)**
- Per validated process: `GET 127.0.0.1:<port>/health` (2 s). 200 → `load_signal: health-ok`; 503 → `loading`; no answer → `down`.
- If `/v1/models` answers (unkeyed): record the served id; if it does not match the argv basename, the row is `listed` under the served id.
- If `/props` answers: record `context_window` from `default_generation_settings.n_ctx`.
- A process with no `--model` is router mode: no resident row; it becomes a candidate (section 2).

**Apple Foundation Models**
- Add `--facts --json` to `hack/apple-foundation-models.swift`. It reads `availability` (with reason), `contextSize` (when available), `variant.displayName` (macOS 27+), the four `LanguageModelCapabilities` (macOS 27+), and `supportedLanguages.count`. It never creates a session or generates.
- The discovery script runs the cached compiled helper in `--facts` mode. It falls back to `xcrun swift -e` with the same read-only body only when no cached helper exists. `--self-test` is no longer run during sweeps.
- New fields on `AppleFoundationModelsInfo`: `state` (`ready` | `cold` | `unavailable` | `unknown`), `reason`, `context_window`, `model`, `capabilities`, `provenance`. `available`/`verified` keep their meaning for existing readers.

**Data model**
- `ResidentModel` gains `state` (`loaded` | `loading` | `listed` | `down`), `load_signal` (e.g. `ollama-ps`, `health-ok`, `argv`), `context_window`, and `provenance` (map of field → source). Existing fields unchanged.
- `modelinventory.Catalog` maps resident `state` directly instead of inferring from runtime. A resident with no `state` (older collectors) keeps the #507 runtime mapping.
- Apple facts add a catalog entry: engine `apple-foundation-models`, locality `on-node`, name from `model`. Catalog state: `ready` → `loaded`, `cold` → `installed`; `unavailable` and `unknown` produce no entry (the reason stays on the node's Apple facts).

### 2. Loopback candidate discovery (PR 2)

New pushed script `LocalEndpointDiscoveryScript`, bash, same language and invocation as the others.

- **Listeners:** `ss -ltnH`, else `lsof -nP -iTCP -sTCP:LISTEN`, else `netstat -an`. None present: empty result, `reason: no-listener-tool`.
- **Filter:** keep listeners bound to loopback or a wildcard address. Always dial `127.0.0.1:<port>`; never dial an address taken from the listener row.
- **Skip owned ports:** the Go caller runs this after the Ollama, llama.cpp, and MLX scripts and prefixes the command with the ports they own.
- **Budget:** at most 32 ports, `curl --max-time 2` per request, 8 s total. Hitting a cap sets `truncated: true`.
- **Dialects, in order:** `GET /api/tags` (Ollama: `models[]`), else `GET /v1/models` (OpenAI-compatible: `data[]`). Engine from `owned_by` (`llamacpp`, `vllm`, …), else `openai-compatible`. `max_model_len` → `context_window`.
- **Parsing:** `python3` where present, as the existing scripts do. Absent: the port stays a candidate with `reason: parser-unavailable`.
- **Output** on `NodeFacts.local_endpoints`: `confirmed[]` (port, dialect, engine, models with state `listed`, provenance) and `candidates[]` (port, process name, reason). `401`/`403` or any answer that names no model is a candidate with its reason.
- `Catalog` reads `confirmed` only.

### 3. Apple extras and disk kinds (PR 3)

- **Private Cloud Compute (macOS 27+):** the `--facts` helper also reads `PrivateCloudComputeLanguageModel` `availability`, `contextSize` (async throws), `capabilities`, and `quotaUsage` (`isLimitReached`, `status`, `resetDate`). Catalog entry: engine `apple-pcc`, locality `cloud-proxy`. Never selected automatically; any use is an explicit operator policy. Whether an unsigned helper can generate through PCC without the managed entitlement is unverified and out of scope.
- **Disk kinds:** `DiskWeights` recognises Core AI export folders (`.aimodel` + tokenizer) as `format: aimodel`. Core ML bundles (`.mlpackage`, `.mlmodelc`) are not assumed to be language models; they are recorded only with `kind: unknown` unless metadata says otherwise. Disk facts never set a load state.

## Evidence

From a 12-node heterogeneous fleet (Linux and macOS), 2026-10-08/09:

- Keyed llama-servers: `/health` 200 `{"status":"ok"}`; `/props`, `/v1/models`, `/slots` 401. Unkeyed llama-server: `/props` `n_ctx` 4096; `/v1/models` `meta.n_ctx_train` present (training context).
- Three macOS 27.2 nodes: `SystemLanguageModel` available on all three; `contextSize` 4096, 8192, 8192; `variant` "AFM 3 Core" ×2 and "AFM 3 Core Advanced"; read-only probe 0.7–7.2 s via `xcrun swift -e` (compile dominated). The current probe reported one of them unavailable.
- One macOS 27.2 node, unsigned CLI: on-device capabilities toolCalling, guidedGeneration, vision (no reasoning); PCC available, `contextSize` 32768, quota below limit, capabilities toolCalling, reasoning, vision.
- A Mac's Core ML bundles were speech models (ASR, VAD), not language models.
- An unbounded home-directory scan timed out on two Macs.

Sources: llama.cpp `tools/server/README.md` and `server-http.cpp`; mlx-lm `SERVER.md` and endpoint docs; Apple Developer Documentation for `SystemLanguageModel` (`availability`, `contextSize`, `variant`, `tokenCount(for:)`), `LanguageModel`, `LanguageModelCapabilities`, `PrivateCloudComputeLanguageModel` (`quotaUsage`), "Managing the context window", "Adding server-side intelligence with Private Cloud Compute", "Running a Core AI model in a Foundation Models session", and Foundation Models updates.

## Testing

Each script change follows the existing stubbed-PATH harness in `internal/facts` (`pgrep`/`ps`/`curl`/`ss` stubs, fake process tables):

- MLX: served model from argv; `/v1/models` listing several cache repos produces one resident; silent port → `down`.
- llama.cpp: `/health` 200 → `loaded`; 503 → `loading`; silent → `down`; 401 on `/v1/models` and `/props` with `/health` 200 → `loaded`, no `context_window`; `/v1/models` id mismatch → `listed`; router mode → no resident.
- Apple: `--facts` output parsing for each `availability` case; timeout → `unknown`; helper missing → `xcrun` fallback; no generation call in sweep mode (stub asserts `--self-test` is never invoked).
- Candidates: each listener-tool fallback; none present → empty with reason; owned ports skipped; non-loopback-only listener skipped; cap and budget set `truncated`; `401` → candidate; `python3` missing → candidate.
- Catalog: state mapping from resident `state`; older rows without `state` keep #507 behaviour; candidates never appear; Apple entry shape.
- Live verification on Linux and macOS nodes before each PR, as for #507.

## Out of scope

- Fleet sharing of self-reports between daemons (needs signed reports).
- Daemon-owned routes, tunnels, or a model gateway.
- Automatic start/stop/unload, and automatic local→cloud escalation.
- Dialects beyond Ollama `/api/tags` + `/api/show` and OpenAI-compatible `/v1/models` (e.g. LM Studio's native API).
- PCC generation from an unsigned helper.
