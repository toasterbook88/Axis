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

## 3. Non-Goals

- Generating completions over MCP
- Embedding operator topology
- Changing placement FitScore (follow-up)

## 4. Public boundary

Examples and tests use `127.0.0.1`, `node-a`, and documentation placeholders only.
