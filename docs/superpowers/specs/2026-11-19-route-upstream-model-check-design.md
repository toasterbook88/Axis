# `axis ai route`: an upstream that does not list the forwarded model does not serve it

Date: 2026-11-19. Status: proposed; spec review pending. No code yet.

## Problem

For a LiteLLM-style hub on this machine, `axis ai route` reads the hub's `GET /model/info` for the
requested model's `litellm_params.api_base` values and sends each one a model-list `GET`
(`docs/decisions/inference-route-strict-and-mcp.md` §2.5, `internal/llmrouter/upstream.go`). Any
2xx, 401 or 403 answer counts as serving. The list itself is never read, so an alias whose upstream
server is up but no longer has the model passes: the route reports the hub healthy with
`model_seen: true` and confidence 0.95, the upstream check says `answered`, and every chat to the
alias fails (LiteLLM answers 404 "model not found", then 429 while the deployment cools down).

On 2026-11-19 a live hub listed eleven aliases. Three forwarded to Ollama servers that were up but
no longer had those models. `axis ai route --model` reported all three healthy, and chats to each
failed with 404 and then 429. The same decision feeds MCP `inference_route_explain` and the backend
lookup of `axis agent --model` (#666), which therefore picked a hub that could not serve the model.

## Non-goals

- No chat request. The check stays one model-list `GET` per upstream, as §2.5 requires.
- No change where no check runs today: a hub on another node, a hub without a readable
  `/model/info`, and a deployment without `api_base`.
- No model verdict from a list that is empty or cannot be parsed, or from a 401/403 answer; those
  stay "answered" as today.
- No model verdict for a llama-server upstream. In single-model mode llama-server ignores the
  request's model name and lists its own alias or the weights' file path, so a name mismatch there
  proves nothing.
- No model verdict when the forwarded name cannot be derived: a `litellm_params.model` without a
  known provider prefix.
- No change to how the hub's own `/v1/models` list is matched (`ModelListed`).

## Design

1. `modelAPIBases` (`internal/llmrouter/upstream.go`) returns each deployment's `api_base` with its
   forwarded model: `litellm_params.model` without its LiteLLM provider prefix, for the prefixes
   that name a server by `api_base`: `openai/`, `ollama/`, `ollama_chat/`, `hosted_vllm/`,
   `lm_studio/` and `llamafile/`. Only the first segment is removed, so a repository-style name
   (`openai/org/model`) forwards `org/model`. Any other value forwards nothing. Deployments are
   distinct by (`api_base`, forwarded model) instead of `api_base` alone.
2. The upstream's model-list body, which is already read, is parsed as `{"data":[{"id",
   "owned_by"}]}`. The model check applies only when the forwarded model is known, the list has at
   least one `id`, and no entry has `"owned_by": "llamacpp"`. It passes when some `id` equals the
   forwarded model ignoring case, or one of the two is the other plus `:latest` (Ollama's implicit
   tag).
3. A failed model check makes that upstream not OK, with `Message` `answered, but does not list
   "<model>"`. `UpstreamCheck` gains two fields: `model` (the forwarded name, omitted when unknown)
   and `unlisted` (true for this case, omitted otherwise). They appear in
   `RoleRouteDecision.upstreams`, so in `axis ai route --format json|yaml` and
   `inference_route_explain`; both are additive.
4. Routing is unchanged in shape: a hub whose checks have no OK upstream is skipped, as for a dead
   upstream. When any of those checks is `unlisted`, the reasoning reads `skip "<backend>": model
   "<m>" listed, but no upstream serves it (<api_base>: answered, but does not list "<forwarded>")`
   and the strict-mode error ends ` (listed, but no upstream serves it)`. When every upstream failed
   to answer, both keep today's wording ("no upstream answers").
5. `docs/current-state.md` and the decision record (a new §2.6, dated) describe the check.

Tests (`internal/llmrouter/upstream_test.go`), each with a local `httptest` hub and upstream:

- an upstream listing other models makes the hub not healthy, falls through to the next preferred
  backend, and fails strict mode with "no upstream serves it";
- the check reports `model` and `unlisted` in the decision;
- a llama-server entry (`owned_by: llamacpp`) whose list does not name the forwarded model passes;
- case and `:latest` variants pass (`openai/M` against `m:latest`);
- a repository-style forwarded name passes against the same `id`;
- an unknown provider prefix and an empty list both pass, as today;
- one upstream without the model and one with it on the same alias passes;
- a dead upstream keeps the "no upstream answers" wording.

## Alternatives

- **Send a one-token chat.** Proves serving, but costs tokens and time, can load weights on a lazy
  Ollama, and can start paid compute on a burst backend. §2.5 rules out prompts in this check.
- **Match with `ModelListed`.** It treats `coder` and `coder:7b` as the same listing, while Ollama
  resolves `coder` to `coder:latest` and refuses it when only `coder:7b` exists. Too loose for an
  upstream list; it stays the matcher for the hub's own list.
- **Check llama-server entries too.** Measured below: it would reject a working deployment.
- **Use LiteLLM's own health checks.** `/health` makes real model calls, and background health
  checks are off unless configured, so there is often nothing to read.

## Research

- LiteLLM routes an OpenAI-compatible server through the `openai/` prefix with its `api_base`
  ("add `openai/` prefix to model so litellm knows to route to OpenAI",
  [LiteLLM docs](https://docs.litellm.ai/docs/providers/openai_compatible)). The forwarded name is
  what follows the prefix. `ollama/`, `ollama_chat/`, `hosted_vllm/`, `lm_studio/` and `llamafile/`
  are LiteLLM provider names for local servers addressed the same way.
- llama-server lists models with `"owned_by": "llamacpp"`, and its chat `model` field is "Required
  in router mode ... Ignored in single model mode"
  ([server README](https://github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md)). In
  router mode the name does matter, but skipping the check there keeps today's behavior.
- Measured on the live hub on 2026-11-19, after its three dead aliases were disabled: of the seven
  configured deployments, two sat behind an upstream that answers 401 (no verdict), two forwarded
  to an Ollama server that lists the forwarded name exactly, one to an MLX server that lists the
  repository-style name exactly, one to a server that lists the alias exactly, and one to a
  llama-server whose list names a GGUF file path. Under this design all seven pass, and the three
  disabled aliases fail.
- Optimization: none needed. No request is added; the change parses a body the check already reads,
  bounded by the existing `maxProbeResponseBytes`.

## Review
