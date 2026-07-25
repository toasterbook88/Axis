# Decision: Wire inference roles into placement and agent

**Date:** 2026-07-24  
**Scope:** `cmd/axis` placement/task/agent, `internal/llmrouter` role mapping, docs  
**Decision:** Advisory enrichment only; no change to FitScore math

## 1. Context

After #251 and #252, roles live in `ai.yaml` and MCP can explain routes, but
`axis placement` / `axis agent` ignored that file. Operators still had three
unrelated knobs (`ai.yaml`, `ai_providers`, `chat.default_model`).

## 2. Decision

1. Document the three surfaces in `docs/runbooks/ai-config.md`.
2. Map task text → role via `RoleFromTaskDescription` (keywords + `role=`),
   slicing only the lowercased string (UTF-8 safe).
3. Append `FormatRouteReasoning` lines onto placement decisions (advisory),
   offline (`SkipProbe`), only when the role exists in `ai.yaml`. Endpoints
   are emitted as `loopback|private|remote`, not raw URLs.
4. Include `ai-role:*` entries in the agent model catalog with health probes,
   dedupe, API key resolution, and SecurityClass from the base URL; consult
   role `default` when resolving startup model after `chat.default_model`.
5. Do **not** alter placement FitScore or force node selection from backend
   `node:` hints in this PR (hint-only via reasoning).

## 3. Non-Goals

- AXIS-owned tunnels or LiteLLM process management  
- Completions over MCP  
- Merging `ai_providers` and `ai.yaml` schemas  

## 4. Public boundary

No private hostnames/IPs; examples remain loopback / `node-a`.
