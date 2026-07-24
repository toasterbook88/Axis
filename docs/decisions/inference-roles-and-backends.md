# Decision: Inference Roles and OpenAI-Compatible Backends

**Date:** 2026-07-24  
**Scope:** `internal/config` (AI config file), `internal/llmrouter` (role resolve + probes), `cmd/axis/ai.go`  
**Decision:** ADD a private, config-driven inference role substrate; do not embed operator topology

## 1. Context

Operators run heterogeneous local inference (Ollama, llama.cpp-class OpenAI-compatible servers, MLX) across multiple machines. Clients (CLI agents, IDEs, scripts) need a stable way to ask:

> For role `default` / `fast` / `long`, which backend and model should I use?

AXIS already has experimental hybrid routing (`internal/llmrouter`, `axis llm`) and placement warmth for Ollama. What was missing is a **generic, public** contract for:

- named **backends** (kind + base URL + optional node binding)
- named **roles** (prefer list + model id)
- **dry-run route resolution** from live health probes

## 2. Decision

1. **Ship `~/.axis/ai.yaml`** (example: `ai.example.yaml` in the repo) as the operator-local source of backends and roles. Topology and secrets stay out of git.
2. **Expose `axis ai backends|roles|route`** as an advisory surface. `route` is dry-run by default: resolve role → candidates → probe → pick, with reasoning strings.
3. **Probe only URLs from operator config** (plus documented local defaults). Never hardcode cluster hostnames or private IPs in code or public docs.
4. **Do not** make AXIS own SSH tunnels, LiteLLM process management, or multi-node tensor parallel. Those remain operator infrastructure. AXIS may *consume* a localhost OpenAI-compatible hub if the operator configured one.
5. **Keep `ai_providers` in `nodes.yaml`** for the existing cloud/local provider registry used by `axis llm`. Roles/backends are a complementary layer focused on multi-backend local fleet routing.

## 3. Public Repository Boundary

The AXIS repository is public. This feature must never introduce:

- real grid hostnames or private / Tailscale IP addresses
- operator home paths
- API keys or tunnel unit contents

Examples and tests use `node-a` / `node-b`, `127.0.0.1`, `localhost`, and RFC 5737 documentation addresses (`192.0.2.0/24`) only.

## 4. Non-Goals (this change)

- Automatic tunnel setup
- Mutating remote GPU processes
- Load-balancing algorithm productization beyond first healthy preferred backend
- Replacing placement for training/execution workloads

## 5. Consequences

- Operators get a scrubbed, portable substrate for multi-backend inference roles.
- Private grids map real machines in local `ai.yaml` only.
- Follow-up work can wire role resolution into placement warmth and MCP without redesigning the config shape.
