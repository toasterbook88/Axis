# `axis agent --model`: use a configured backend that serves the named model

Date: 2026-11-19. Status: implemented on `fix/agent-explicit-backend-model`; spec review
approve-with-changes, its findings resolved below.
The code was written on 2026-11-04 and 2026-11-05 on a branch that was never pushed, and was ported
to current `main` on 2026-11-19. This spec was written after that code, which is out of order under
AGENTS.md §8; the PR asks the maintainer to accept or reject that order.

## Problem

In the default `--provider auto`, `axis agent --model M` accepts M when the `/models` catalog lists
it (a role entry from `~/.axis/ai.yaml` as `ai-role:<name>`, a catalog Ollama tag, a resident or node
endpoint, or a cloud model) or when this machine's Ollama has it on disk. A model that a configured
`ai.yaml` backend serves, but that no role names, is refused before any request:

```
error: model "M" is not in the model catalog or on local Ollama; /models lists the choices
```

`axis ai route --model M` resolves the same model to that backend (`no role; scanning enabled
backends for model "M"`, healthy, `model_seen: true`), so two AXIS surfaces disagree about the same
configuration. With `--provider local` the name is not refused: it becomes a local Ollama target,
which fails at the first request when this machine does not have it. On a cluster whose hub lists
eleven models, `axis ai route --model` resolved all
eleven to the hub, and `axis agent --model` in v0.26.0 refused the six that no role names. The only
workaround is a role per model in `ai.yaml`.

## Non-goals

- No change when `--model` is absent: the default model, `default_model`, `--role` and the
  implicit catalog order keep their precedence (`docs/runbooks/ai-config.md`).
- `--role R` stays pinned to the role's own `prefer` list. The model a role names is not treated as
  an explicit `--model`, so a role cannot escape its preferences through this path.
- No change to cloud selection (`--provider cloud`, `--cloud-model`), to interactive `/model`
  switching, or to the `/models` catalog contents.
- No configuration writes, no model loading or lifecycle actions, no new flag, config key, state
  file or output field.
- The agent stays stricter than `axis ai route` about tags. The router treats `coder` and
  `coder:7b` as the same listing (`llmrouter.ModelListed`); this lookup accepts only the exact ID,
  plus `M:latest` for an untagged name on Ollama. A backend that lists only a tagged variant is not
  used, and startup refuses with the existing message.
- No proof that a listed model is servable. A hub can list an alias whose upstream has no
  deployment; the existing readiness check still runs and reports that.

## Design

`setupAgentStartupBackend` (`cmd/axis/agent_startup_model.go`) gains one input,
`ExplicitModel`, true only when the operator typed `--model` (set before `--role` fills the model,
in both `agentCmd` and the console prelude `consoleExplicitPrelude`). When all of these hold:

- `ExplicitModel` is true and the model is non-empty;
- no explicit target was already resolved (`explicitTarget == nil`);
- `--cloud-model` is empty and the provider mode is `auto`, `local` or empty;

startup calls `appendExplicitAIModelChoice` (`cmd/axis/agent_explicit_model.go`) before the existing
`resolveReadyStartupModelTarget`. That function:

1. Returns the catalog unchanged when an enabled local catalog entry already has the model
   (case-insensitive), so existing entries keep their identity.
2. Loads `ai.yaml`. With no file, no backends, a load error or a cancelled context it returns the
   catalog unchanged.
3. Returns the catalog unchanged when this machine's Ollama has the model on disk
   (`ollamaHasModelFn`, the check auto mode already makes), so a local copy keeps winning even when
   the snapshot catalog is stale, as before this change.
4. Walks the enabled backends in file order, skipping kinds other than `ollama`,
   `openai-compatible`, `llamacpp` and `mlx`. For each it picks the endpoint the catalog would use
   (`chooseAICatalogEndpoint`: `base_url` on the backend's own node, `advertise_url` elsewhere),
   strips `/v1` for Ollama, and asks the router to resolve the model against that one backend with
   `RequireModelListed`.
5. Accepts a backend only on positive proof: the router probed it, it is healthy, the model is
   present, and the decision's backend, model, endpoint and kind all equal what was asked; and the
   backend's own probe listed exactly M, or `M:latest` for an untagged Ollama name.
6. Appends one choice for the first accepted backend and stops:
   `ID: ai-model:<backend>:<M>`, the backend's protocol (`ollama` or OpenAI), `ProviderName:
   ai-backend:<backend>` so the existing key lookup (`apiKeyForAIBackend`) binds the backend's
   credential, `ProviderKind: local`, and `SecurityClass` local only when
   `llmrouter.EndpointIsClusterLocal(endpoint)`, otherwise remote. `Node` is the backend's node,
   cleared by `localizeChoiceNodes` when that is this machine, and no catalog fact such as `Listed`
   is set. These match role catalog entries (`cmd/axis/inference_hint.go`) after
   `collectModelChoices`, so redaction, credential handling, the banner and session-log provenance
   are the same as for a role pointing at the same backend.

The added choice is used for this startup only. `agentStartupBackendResult.Choices`, which feeds
plain-REPL completion, stays the catalog without it, because `/model` rebuilds the catalog and would
refuse it. In local mode the first matching catalog entry wins over the local Ollama target, so with
`--provider local` a name that is not on disk now reaches the backend that lists it instead of
failing at the first request.

The whole walk shares one `inferenceHintTimeout` (3 s) and stops on cancellation. Any failure, an
expired budget or no accepting backend leaves the catalog as it was, and startup refuses with the
existing error (in local mode, the local Ollama target). `docs/current-state.md` records the
behavior. `agentCmd` calls the setup through `agentStartupBackendFn`, a test seam, so command tests
can stop after startup without generating.

Tests (`cmd/axis/agent_explicit_model_test.go`, `agent_explicit_model_regression_test.go`) cover:
a backend-only model reaching startup, and staying out of the switchable choices; a copy on local
Ollama winning with no backend probed (auto and local); empty, tag-only, disabled and unsupported
backends skipped; each kind of unproven or mismatched router decision refused; the viewer-selected
endpoint proved, with the security class it implies (remote for an advertised URL, local for
loopback); an entry for this machine having no node name and no `Listed`; the Ollama `:latest`
rule; an existing catalog entry kept; cancellation and an empty model probing nothing; the
credential binding kept; implicit startup unchanged; cloud precedence unchanged, and
`--provider cloud` without `--cloud-model` probing no backend; `--role` not escaping its
preferences and not reaching setup as an explicit model (CLI, which stops after setup, and
console); the console keeping the explicit origin; and an Ollama `/v1` base URL still reaching
`/api/chat`. The six tests added or changed for the spec review were each checked to fail when the
behavior they cover is removed.

## Alternatives

- **List every backend model in `/models`.** Every startup would probe every backend and grow the
  catalog, and an unlisted model could become an implicit default. Rejected: the catalog stays
  curated by roles, and only an explicit request widens what one startup considers.
- **Reuse the router decision as is.** `axis ai route` can answer from configuration without a
  fresh probe and fall back to other backends. Rejected for startup: an explicit model is accepted
  only on a fresh positive listing from the backend that will serve it.
- **Require a role per model.** That is the status quo. It keeps the two surfaces disagreeing and
  makes the operator restate what the backend already reports.

## Research

- The OpenAI-compatible `GET /v1/models` list
  ([API reference](https://platform.openai.com/docs/api-reference/models/list)) and Ollama's
  `GET /api/tags` ([API docs](https://github.com/ollama/ollama/blob/main/docs/api.md#list-local-models))
  are what AXIS already probes for backends and roles. The Ollama docs state that a model name's
  tag "is optional and, if not provided, will default to latest", which is why an untagged request
  matches `M:latest` on Ollama only.
- Optimization: none beyond bounding the probe. The walk stops at the first accepting backend and
  shares the existing 3 s hint budget rather than adding a timeout. An on-box hub answered
  `/v1/models` in about 3 to 4.5 ms in `axis ai backends` runs, so the budget is not the limit in
  the common case; a slow remote backend can exhaust it, and then startup fails closed as before.
- A listing proves less than serving: two of a hub's listed aliases answered chat with 404 and then
  429 because the hub had no working deployment for them. This change does not try to detect that;
  `checkModelReadiness` already warns that the model is not answering, and the chat error is shown.

## Review

Kind: spec
Reviewer: fresh-context session (Claude Code `claude -p`, new session on another machine, given only `docs/superpowers/review-prompt.md` and its five inputs)
Model: Anthropic Claude (same family as the author, so a fresh top-tier session rather than a cross-family review)
Base: b55931b81192ec3d4d39541dfb498257ca12864a
Head: 6bc1459737c694e70b8c183fa3fb85461dca88fe
Date: 2026-11-19
Verdict: approve-with-changes

1. Major: an explicit `--model` skipped a copy already on this machine's Ollama when the snapshot catalog was stale, because the appended backend entry matched before auto mode's on-disk check; the Problem section also misstated the base behavior (base accepts an on-disk model, and `--provider local` never refuses). Fixed in 41c39880: `appendExplicitAIModelChoice` returns the catalog unchanged when `ollamaHasModelFn` has the model (Design step 3); the Problem section and `docs/current-state.md` now describe both modes; TestExplicitModelOnLocalOllamaKeepsTheLocalCopy covers auto and local and fails with the check removed.

2. Minor: no test pinned the entry's security class or the `--provider cloud` guard. Fixed in 41c39880: TestExplicitGatewayProvesTheViewerEndpoint asserts remote for an advertised URL and local for loopback, and TestExplicitModelWithCloudProviderProbesNoBackend asserts that `--provider cloud` without `--cloud-model` probes no backend; each fails when its behavior is forced the other way.

3. Minor: the disagreement with `axis ai route --model` remains for tagged listings, and no non-goal said so. Fixed in 41c39880: a non-goal states that the agent accepts only the exact ID (plus `M:latest` for an untagged Ollama name) while the router treats tags as the same listing. The suggested change to name a tag-only listing in the refusal message is declined: that message is existing behavior shared by every catalog miss and is outside this spec; `go test ./cmd/axis -run TestExplicitGatewaySkipsEmptyAndTaggedOnlyBackends` passes, pinning that a tag-only backend is skipped as the non-goal says.

4. Minor: the entry's fields did not match role entries as the spec claimed: it kept the backend's node name for this machine and set `Listed`. Fixed in 41c39880: the entry runs through `localizeChoiceNodes` and sets no `Listed`; Design step 6 names both; TestExplicitBackendChoiceOnThisNodeHasNoNodeName covers it and fails with the localization removed.

5. Minor: plain-REPL completion offered `/model M`, which `/model` then refused. Fixed in 41c39880: `agentStartupBackendResult.Choices` stays the catalog without the startup-only entry; TestExplicitGatewayModelWithoutRoleReachesStartup asserts it and fails when `Choices` includes the entry.

6. Minor: the CLI variant of the role regression test generated against the real local Ollama and could start `ollama serve`. Fixed in 41c39880: `agentCmd` calls the setup through `agentStartupBackendFn`; the test runs the real setup, then stops before generation, and asserts that `--role` reaches setup with `ExplicitModel` false; it fails when `agentCmd` passes true.

Questions:

- Is the Ollama quote still at the linked page? Verified: the linked `docs/api.md` fetched on 2026-11-19 reads "The tag is optional and, if not provided, will default to latest."
- Do the live numbers hold (eleven models, six refused, 3 to 4.5 ms, 404 then 429)? Verified: on 2026-11-19, against the cluster this was built on, `axis ai route --model` resolved all eleven hub models to the hub; v0.26.0 `axis agent --model` refused six with the catalog error and accepted the five a role names; two `axis ai backends` runs reported the hub's `/v1/models` latency as 3.0 ms and 4.5 ms; two hub aliases answered chat with 404 and then 429 in runs of this branch.
- Is writing this spec after the code acceptable under AGENTS.md §8? Open: only the maintainer can rule on it; the PR asks.
- Do `gofmt` and `make lint` pass? Verified: `hack/ci-preflight.sh`, which runs both, passed at 9dd3bbab, and `gofmt -l cmd/` is empty at 41c39880.
