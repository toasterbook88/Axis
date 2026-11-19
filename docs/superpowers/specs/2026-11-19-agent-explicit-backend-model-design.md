# `axis agent --model`: use a configured backend that serves the named model

Date: 2026-11-19. Status: implemented on `fix/agent-explicit-backend-model`; spec review pending.
The code was written on 2026-11-04 and 2026-11-05 on a branch that was never pushed, and was ported
to current `main` on 2026-11-19. This spec was written after that code, which is out of order under
AGENTS.md §8; the PR asks the maintainer to accept or reject that order.

## Problem

`axis agent --model M` accepts M only when the `/models` catalog lists it: a role entry from
`~/.axis/ai.yaml` (`ai-role:<name>`), a local Ollama tag, a resident or node endpoint, or a cloud
model. A model that a configured `ai.yaml` backend serves, but that no role names, is refused before
any request:

```
error: model "M" is not in the model catalog or on local Ollama; /models lists the choices
```

`axis ai route --model M` resolves the same model to that backend (`no role; scanning enabled
backends for model "M"`, healthy, `model_seen: true`), so two AXIS surfaces disagree about the same
configuration. On a cluster whose hub lists eleven models, `axis ai route --model` resolved all
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
2. Loads `ai.yaml`. With no file, no backends or a load error it returns the catalog unchanged.
3. Walks the enabled backends in file order, skipping kinds other than `ollama`,
   `openai-compatible`, `llamacpp` and `mlx`. For each it picks the endpoint the catalog would use
   (`chooseAICatalogEndpoint`: `base_url` on the backend's own node, `advertise_url` elsewhere),
   strips `/v1` for Ollama, and asks the router to resolve the model against that one backend with
   `RequireModelListed`.
4. Accepts a backend only on positive proof: the router probed it, it is healthy, the model is
   present, and the decision's backend, model, endpoint and kind all equal what was asked; and the
   backend's own probe listed exactly M, or `M:latest` for an untagged Ollama name.
5. Appends one choice for the first accepted backend and stops:
   `ID: ai-model:<backend>:<M>`, the backend's protocol (`ollama` or OpenAI), `ProviderName:
   ai-backend:<backend>` so the existing key lookup (`apiKeyForAIBackend`) binds the backend's
   credential, `ProviderKind: local`, and `SecurityClass` local only when
   `llmrouter.EndpointIsClusterLocal(endpoint)`, otherwise remote. These match the fields of role
   catalog entries (`cmd/axis/inference_hint.go`), so redaction and credential handling are the
   same as for a role pointing at the same backend.

The whole walk shares one `inferenceHintTimeout` (3 s) and stops on cancellation. Any failure, an
expired budget or no accepting backend leaves the catalog as it was, and startup refuses with the
existing error. `docs/current-state.md` records the behavior.

Tests (`cmd/axis/agent_explicit_model_test.go`, `agent_explicit_model_regression_test.go`) cover:
a backend-only model reaching startup; empty, tag-only, disabled and unsupported backends skipped;
each kind of unproven or mismatched router decision refused; the viewer-selected endpoint proved;
the Ollama `:latest` rule; an existing catalog entry kept; cancellation and an empty model probing
nothing; the credential binding kept; implicit startup unchanged; cloud precedence unchanged;
`--role` not escaping its preferences (CLI and console); the console keeping the explicit origin;
and an Ollama `/v1` base URL still reaching `/api/chat`.

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
