# AI configuration surfaces

AXIS has three related but distinct AI configuration surfaces. Use this table
to pick the right one.

| Surface | File | Purpose | Used by |
| -------- | ---- | ------- | ------- |
| **Inference roles / backends** | `~/.axis/ai.yaml` (example: `ai.example.yaml`) | Name local/OpenAI-compatible backends and logical **roles** (`default`, `fast`, `long`, …). Health probes and prefer-order routing. | `axis ai *`, MCP `inference_route_explain`, placement/agent **hints** |
| **Cloud / hybrid providers** | `~/.axis/nodes.yaml` → `ai_providers` | Credentialed cloud providers (OpenRouter, Groq, Anthropic) and optional local entries for `axis llm` registry. | `axis llm`, agent **cloud** backends |
| **Chat default model** | `~/.axis/nodes.yaml` → `chat.default_model` | Single default Ollama model tag when `--model` is omitted. | `axis chat`, `axis agent` startup (before roles) |

## Precedence for `axis agent` model selection

When `--model` is empty (provider=auto):

1. Explicit interactive `/model` choice  
2. **`--role <name>`** from `ai.yaml` (mutually exclusive with `--model`)  
3. `chat.default_model` from `nodes.yaml`  
4. Warm-resident preferred model from the cluster snapshot  
5. **`ai.yaml` role `default`** model (if configured)  
6. First usable local catalog entry / cloud fallback  

Role catalog entries appear as `ai-role:<name>` in `/models` when `ai.yaml` is present.
They are **probed** at catalog build time: unreachable hubs are marked disabled and
will not be selected as the auto default. Security class is **local** for loopback
or private LAN base URLs, otherwise **remote** (stricter evidence redaction).

## Precedence for placement / task place

Node selection remains the placement engine (Filter → Rank → Select).

When the task description maps to an inference role (keywords like `ollama`,
`llm inference`, `role=default`, …) **and** that role exists in `ai.yaml`,
AXIS appends **advisory** reasoning lines. Resolution is **offline**
(`SkipProbe`): no live HTTP during placement, so `inference_healthy` is not
emitted — you will see `inference_probe=skipped` instead.

```text
inference_role=default
inference_backend=local-hub
inference_model=coder:latest
inference_endpoint_class=loopback
inference_probe=skipped
…
```

These do **not** override the selected node. They tell operators which backend
to call after placement. Use `axis ai route` when you want a live probe.

## Operator pattern (private grids)

1. Keep GPU processes and tunnels outside AXIS (systemd / SSH).  
2. Optionally run a localhost OpenAI-compatible **hub** (e.g. LiteLLM).  
3. Point `ai.yaml` backends at `http://127.0.0.1:…` only.  
4. Never commit real hostnames, private IPs, or keys to the public repo.

## Related commands

```bash
axis ai backends
axis ai roles
axis ai route default
axis placement explain "run ollama inference on a 7b model"
axis agent --role default
axis agent --model coder:latest   # not with --role
```

See also:

- `docs/decisions/inference-roles-and-backends.md`
- `docs/decisions/inference-route-strict-and-mcp.md`
