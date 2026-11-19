# Decision: Strict model_seen + MCP inference_route_explain

**Date:** 2026-07-24  
**Scope:** `internal/llmrouter` route options, `cmd/axis/ai route`, `internal/mcp`  
**Decision:** Fail closed when a non-empty model list omits the requested model; expose dry-run via MCP

## 1. Context

#251 shipped advisory role resolution. Live evaluation showed that healthy hubs with non-empty `/v1/models` could still accept missing model ids at confidence 0.55 (lazy-load path). Operators and agents need a clear "not available" signal.

## 2. Decision

1. **`RequireModelListed`** on `ResolveRoleOptions`: skip backends whose probe returned a non-empty list without the model; if none remain, return `ErrModelUnlisted`.
2. **CLI default strict when probing:** `axis ai route` exits **7** (`ExitErrModelUnlisted`) on failure. Opt out with `--allow-unlisted`. `--skip-probe` cannot verify listing.
3. **MCP tool `inference_route_explain`:** read-only; args `role`, `model`, `skip_probe`, `allow_unlisted`; returns `RoleRouteDecision` JSON; sets `IsError` when the model is unlisted or config is missing.
4. Empty/unparsed model lists remain acceptable (hub may lazy-load); only **explicit non-empty absence** is strict.
5. **Listed is not served (2026-10-28).** A hub can keep listing a model whose upstream server is gone. When a probed `openai-compatible` hub lists the model and runs on this machine, AXIS reads the hub's LiteLLM-style `GET /model/info` (with the hub's key) for that model's `litellm_params.api_base` values and sends each one `GET <api_base>/models` (2 s each, no prompt). If none answers, the backend is skipped like an unhealthy one, the reasoning names the upstream and its error, and strict mode fails with `ErrModelUnlisted` "(listed, but no upstream answers)". An answer of 401/403 counts as serving. The checks are in `RoleRouteDecision.upstreams`. No check runs, and the reasoning says "upstream not checked", when the hub is on another node (its `api_base` addresses are relative to the hub's machine) or has no readable `/model/info`; the decision is then unchanged.

6. **Answering is not serving (2026-11-19).** An upstream can be up without the model the hub forwards to it. The check now reads each upstream's list (still one `GET` per distinct `api_base`) and compares it with the forwarded name: `litellm_params.model` without its provider prefix, for `openai`, `ollama`, `ollama_chat`, `hosted_vllm`, `lm_studio` and `llamafile`, or given by `custom_llm_provider`. Entries with the exact requested `model_name` are used when any exist. A non-empty list without that name makes the upstream `unlisted`: strict mode skips the hub ("(listed, but no upstream serves it)"); `allow_unlisted` keeps it selectable at confidence 0.55. Lists that cannot say give no verdict: an empty or unparseable body, a 401/403, llama-server (`owned_by: llamacpp`) and TGI (`owned_by` equal to `id`, or the name `tgi`). `axis agent --model` always resolves strictly; for a server that serves a name it does not list, a role naming the model avoids the check. Spec: `docs/superpowers/specs/2026-11-19-route-upstream-model-check-design.md`.

## 3. Non-Goals

- Generating completions over MCP
- Embedding operator topology
- Changing placement FitScore (follow-up)

## 4. Public boundary

Examples and tests use `127.0.0.1`, `node-a`, and documentation placeholders only.
