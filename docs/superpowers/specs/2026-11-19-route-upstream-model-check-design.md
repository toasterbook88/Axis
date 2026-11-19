# `axis ai route`: an upstream that does not list the forwarded model does not serve it

Date: 2026-11-19. Status: proposed; spec review approve-with-changes, its findings resolved below.
No code yet.

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

- No chat request, and no added request. Each distinct upstream `api_base` still gets one model-list
  `GET` (two after a 404), as §2.5 requires.
- No change where no check runs today: a hub on another node, a hub without a readable
  `/model/info`, and a deployment without `api_base`.
- No model verdict from a list that is empty or cannot be parsed, or from a 401/403 answer; those
  stay "answered" as today.
- No model verdict for a server that answers a name it does not list. Two are known: llama-server,
  which in single-model mode ignores the request's model name and lists its alias or the weights'
  file path, and Text Generation Inference (TGI), which lists its model ID and serves the name `tgi`
  (any other name is taken as a LoRA adapter). A name mismatch there proves nothing.
- No model verdict when the forwarded name cannot be derived: a deployment whose provider is not
  one of the six below, by prefix or by `custom_llm_provider`.
- No change to how the hub's own `/v1/models` list is matched (`ModelListed`).

## Design

1. `modelAPIBases` (`internal/llmrouter/upstream.go`) picks the `/model/info` entries whose
   `model_name` equals the requested model exactly, because LiteLLM routes on the exact name; only
   when there is none does it fall back to today's `ModelListed` match. For each entry it returns
   the `api_base` and the forwarded model, for the providers that name a server by `api_base`:
   `openai`, `ollama`, `ollama_chat`, `hosted_vllm`, `lm_studio` and `llamafile`. When
   `litellm_params.model` starts with `<provider>/` for one of them, the forwarded model is the rest
   (only the first segment is removed, so `openai/org/model` forwards `org/model`). When it has no
   such prefix but `litellm_params.custom_llm_provider` is one of them, the whole model is
   forwarded, as LiteLLM prepends the provider itself. Otherwise the entry forwards nothing.
   Entries are distinct by (`api_base`, forwarded model).
2. Each distinct `api_base` is fetched once, exactly as today; every forwarded model on it is checked
   against that one body, and each (`api_base`, forwarded model) gets its own `UpstreamCheck`.
3. The body, which is already read, is parsed as `{"data":[{"id", "owned_by"}]}`. The model check
   applies only when the forwarded model is known, the body parses with at least one `id`, no entry
   has `"owned_by": "llamacpp"` (llama-server), no entry has an `owned_by` equal to its `id` (TGI's
   signature), and the forwarded model is not `tgi`. It passes when some `id` equals the forwarded
   model ignoring case, or one of the two is the other plus `:latest` (Ollama's implicit tag). A
   body that does not parse, or lists nothing, gives no verdict ("answered").
4. A failed model check makes that upstream not OK, with `Message` `answered, but does not list
   "<model>"`. `UpstreamCheck` gains two fields: `model` (the forwarded name, omitted when unknown)
   and `unlisted` (true for this case, omitted otherwise). They appear in
   `RoleRouteDecision.upstreams`, so in `axis ai route --format json|yaml` and
   `inference_route_explain`; both are additive.
5. Routing. A hub whose checks have no OK upstream because none answered is skipped, as today. A hub
   whose checks have no OK upstream and at least one `unlisted` one depends on `RequireModelListed`:
   - strict (the `axis ai route` default when probing, MCP's default, and the #666 lookup): the
     hub is skipped with the reasoning `skip "<backend>": model "<m>" listed, but no upstream serves
     it (<api_base>: answered, but does not list "<forwarded>")`;
   - with `--allow-unlisted` / `allow_unlisted`: the hub stays selectable at confidence 0.55, the
     value the route already gives a model "not in list ... (hub may lazy-load)", with the reasoning
     `backend "<backend>" healthy; model "<m>" listed, but its upstream does not list
     "<forwarded>" — still selecting (allow_unlisted)`. This keeps the flag's documented meaning,
     accepting a model absent from a non-empty list, for a server that serves names it does not
     list.

   The strict-mode error names why hubs were skipped: ` (listed, but no upstream answers)` when every
   such hub's upstreams failed to answer (today's wording), ` (listed, but no upstream serves it)`
   when every such hub had an unlisted upstream, and ` (listed, but no upstream answers or serves
   it)` when both happened across hubs.
6. `docs/current-state.md` and the decision record (a new §2.6, dated) describe the check, and the
   `--allow-unlisted` help says it also accepts an upstream that does not list the model.

Tests (`internal/llmrouter/upstream_test.go`), each with a local `httptest` hub and upstream:

- an upstream listing other models makes the hub not healthy, falls through to the next preferred
  backend, and fails strict mode with "no upstream serves it";
- with `allow_unlisted` the same hub is selected at confidence 0.55 with the "still selecting" note;
- the check reports `model` and `unlisted` in the decision;
- a table over all six providers (as a prefix, and as `custom_llm_provider` with an unprefixed
  model) checks the forwarded name; an unknown provider gives no verdict;
- a llama-server entry (`owned_by: llamacpp`), a TGI entry (`owned_by` equal to `id`) and a
  forwarded `tgi` each pass when the list does not name the forwarded model;
- case and `:latest` variants pass (`openai/M` against `m:latest`), and a repository-style forwarded
  name passes against the same `id`;
- an empty list and a body that does not parse both give "answered", as today;
- two deployments of the alias on one `api_base`, the unlisted one first, pass, and that `api_base`
  is fetched once;
- an alias `coder` whose upstream lacks the model beside an alias `coder:7b` whose upstream lists its
  model: a request for `coder` uses only the `coder` entry and the hub is skipped as unlisted;
- a dead upstream keeps the "no upstream answers" wording, and one dead hub plus one unlisted hub
  gives "no upstream answers or serves it".

## Alternatives

- **Send a one-token chat.** Proves serving, but costs tokens and time, can load weights on a lazy
  Ollama, and can start paid compute on a burst backend. §2.5 rules out prompts in this check.
- **Match with `ModelListed`.** It treats `coder` and `coder:7b` as the same listing, while Ollama
  resolves `coder` to `coder:latest` and refuses it when only `coder:7b` exists. Too loose for an
  upstream list; it stays the matcher for the hub's own list.
- **Check llama-server and TGI entries too.** Measured below for llama-server: it would reject a
  working deployment. TGI behaves the same by its source.
- **Make the verdict hard even with `--allow-unlisted`.** That would leave an operator whose
  upstream serves an unlisted name only `--skip-probe`, which turns off every check.
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
- TGI's `GET /v1/models` returns one entry with `id` and `owned_by` both set to the model ID, and a
  chat `model` of `tgi` or none selects the loaded model, while any other value is used as an
  adapter ID (`openai_get_model_info` and the chat handler in
  [`router/src/server.rs`](https://github.com/huggingface/text-generation-inference/blob/main/router/src/server.rs)).
- LiteLLM prepends `custom_llm_provider` to a model that lacks it, and routes on the exact
  `model_name` (read in the LiteLLM 1.81 source by the spec review).
- Measured on the live hub on 2026-11-19, after its three dead aliases were disabled, by reading
  each upstream's list against the rules above by hand (the implementation is run against the same
  hub before merge): of the seven deployments in its config file (the eighth alias lives in the
  hub's database and was not measured), two sat behind an upstream that answers 401 (no verdict), two forwarded
  to an Ollama server that lists the forwarded name exactly, one to an MLX server that lists the
  repository-style name exactly, one to a server that lists the alias exactly, and one to a
  llama-server whose list names a GGUF file path. Under this design all seven pass, and the three
  disabled aliases fail.
- Optimization: none needed. No request is added, since each `api_base` is still fetched once; the
  change parses a body the check already reads, bounded by the existing `maxProbeResponseBytes`.

## Review

Kind: spec
Reviewer: fresh-context session (Claude Code `claude -p`, new session on another machine, given only `docs/superpowers/review-prompt.md` and its five inputs)
Model: Anthropic Claude (same family as the author, so a fresh top-tier session rather than a cross-family review)
Base: de6b81f2996449bbb93d3c793098ad1438aa5315
Head: 02b9729957a024711199d128222f10fdd8584b85
Date: 2026-11-19
Verdict: approve-with-changes

1. Minor: `/model/info` entries were still picked with the tag-insensitive `ModelListed`, so a healthy sibling alias (`coder:7b`) could pass a hub whose `coder` alias cannot serve, while LiteLLM routes on the exact name. Fixed in 09af5b92: Design step 1 picks exact `model_name` entries first and falls back to `ModelListed` only when none exists; the test list adds the `coder` / `coder:7b` case.

2. Minor: keying checks by (`api_base`, forwarded model) would have added model-list requests to one server, contradicting "no request is added". Fixed in 09af5b92: Design step 2 fetches each distinct `api_base` once and checks every forwarded model against that body; the non-goal and Research say so; the test list asserts one fetch for two deployments on one `api_base`.

3. Minor: the spec did not say whether `--allow-unlisted` relaxes the new verdict. Fixed in 09af5b92: Design step 5 keeps the strict skip and, with `allow_unlisted`, leaves such a hub selectable at confidence 0.55 with a "still selecting" note, as for a lazy-load hub; the flag help and §2.6 are to say so; the test list covers both modes; Alternatives records the hard-verdict option and why it was not taken.

4. Minor: a deployment that names its provider in `custom_llm_provider` with an unprefixed model got no verdict, though its forwarded name is derivable. Fixed in 09af5b92: Design step 1 forwards the whole model for one of the six providers given that way; the non-goal is narrowed; the provider table test covers both forms.

5. Minor: the listed tests would still pass with a broken prefix table, a dedup that checks only the first model per server, or an unparseable body treated as an empty list. Fixed in 09af5b92: the test list adds a table over all six providers plus an unknown one, two deployments on one `api_base` with the unlisted one first, and an unparseable body that must give "answered".

Questions:

- Do other servers reached through these providers answer names they do not list? Open: llamafile, `mlx_lm.server` and proxies such as llama-swap were not probed, since none runs where this was written; `--allow-unlisted` keeps such a route selectable if one does. For TGI the source settled it: `router/src/server.rs` lists one entry with `owned_by` equal to its model ID and serves the name `tgi`, so Design step 3 exempts both signs and the test list covers them.
- With one hub skipped as unlisted and another as dead, which strict-mode suffix applies? Verified: Design step 5 now defines all three suffixes, and the test list includes a dead hub plus an unlisted hub.
- How was "all seven pass" produced, and why seven when eight aliases remain? Verified: the lists were read by hand against the rules, which Research now says, with the implementation to be run against the same hub before merge; the eighth alias is stored in the hub's database, not its config file, and was not measured.
