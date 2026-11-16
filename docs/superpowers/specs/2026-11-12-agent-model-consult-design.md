# Agent model consult tool

Date: 2026-11-12. Base: f8c258d6 on `docs/agent-model-consult`. Status: `consult_model` is registered. A spec review on 2026-11-13 approved the Ollama auto-start text with changes, and both findings are resolved in this file.

The contract the tool has to keep is `docs/decisions/agent-model-consult.md`.

## Problem

An Axis agent session has one chat backend. `bindAgentModelChoice` in `cmd/axis/agent_console_bind.go` is the only place a model change reaches the agent. `consult` in `internal/agent/consult.go` can ask a second backend for text, and the stub tests show the driver backend, model, and call count stay put. `toolVisible("consult_model", ...)` is false in every scope, so a running agent cannot call it.

A driver that can use a second model needs one advertised tool. The tool asks one configured seat for text and returns that text as the driver's next tool result.

## Non-goals

- A fan-out inside one call, or a cluster-wide queue. One call asks one seat. A turn may already dispatch several tool calls at once, so two consult calls may run together. `SingleSlot` stays process-local and keys on the resolved choice id.
- Changing `RoutingBackend`, cheap-model startup, or `spawn_subagent`. Those stay as they are: cheap-model routing is cloud-only and still passes the tool list; `spawn_subagent` stays in `neverTool` and the child keeps the parent's backend.
- A new config file, config key, CLI flag, MCP tool, or HTTP route. A seat is a model the agent can already select.
- `consult` taking `*Agent`, or the tool calling `SetBackend` or `SetModel`.
- Loading or stopping a model server, setting `max_tokens`, or reading `reasoning_content`. A consult Ollama client does not start `ollama serve`. The driver client still may. Empty visible text stays an error.
- Live hostnames, addresses, or model ids in this repo. Tests keep using names such as `driver-model` and `seat-a`.

## Design

Register `consult_model` on the agent tool registry. Add it to `observeTool` in `internal/agent/scope.go`. It is then visible in observe, edit, and exec. It stays off `clusterTool`, so cluster scope does not advertise it and does not dispatch it. `VisibleToolPrompt` for observe keeps the write and shell limits. It also says that `consult_model`, when that name is in the offered list, sends the prompt to one other configured model.

Arguments are a JSON object with two required strings, `seat` and `prompt`. `seat` is a model id or model name from the same choice list `/model` already uses (`collectModelChoices` in `cmd/axis/agent_model_choices.go`). The tool does not accept a URL, a key, or a host. The agent command injects a resolver into the tool registry. The resolver calls `findModelTargetByRef` (`cmd/axis/agent_startup_model.go`) and does not return that function's error unchanged. An empty ref, a disabled choice, a name that is not in the list, or an ambiguous name becomes a consult error that quotes the caller's seat string, does not contain `pull`, and does not call `BuildBackend`. An ambiguous ref may list the matching ids. The error does not tell the driver to pull a model, and it does not change the driver. A `cmd/axis` test drives the real resolver. A stub cannot lock this.

The resolver builds the seat with `cloudOptsForTarget` and `BuildBackend` (`internal/agent/model_target.go`) only after the ref resolves. When that backend is an Ollama `*chat.Client`, the resolver turns auto-start off. `NewClient` still leaves auto-start on, so the driver client is unchanged. `EnsureRunning` does not call `startLocalOllama` when auto-start is off. The resolver does not call `applyModelChoice` or `bindAgentModelChoice`. The executor then calls `consult` with `SingleSlot: true`, one user message, and a nil tool list. The slot key is the resolved choice id, `ModelChoice.ID`, not the raw argument. `consult` stays free of `*Agent`. The driver's backend pointer and model name are the same after the call as before it. Two calls whose refs resolve to one id do not sit inside `ChatStream` together in this process. Two refs that resolve to different ids may. Two OS processes are not coordinated.

The tool result is the seat's trimmed visible text, or the error `consult` already returns (`is not answering`, the wrapped seat error, `returned no visible text`, or `context.Canceled`). A seat error whose text contains `pull` or `ollama serve` is returned as `is not answering` and does not contain that text. The driver surfaces that string as the tool result.

`consult_model` is not in `isReadOnlyTool`. `autonomyConfirm` and `AutoApproveConfirm` send it to the fallback even when the safety score is 0, the same way an unlisted external MCP tool skips the score-0 auto-approve. Dispatch asks with score 0 (`internal/agent/dispatch.go`). Edit auto-approves a non-file tool below 70, full auto-approves below the ask threshold, and `AutoApproveConfirm` auto-approves below its threshold, so leaving `consult_model` on that path would skip the prompt. The fallthrough covers default, edit, full, and `--auto-approve`. The first call prompts. An "always" answer uses `approveForSession`. Later calls in that session do not ask again. `axis agent -p` denies `consult_model`. Its notice says the tool still asks under `--autonomy full` and `--auto-approve`, and it tells the operator to run interactively. It does not offer those flags as a way through.

`TestConsultModelIsVisibleOutsideCluster` locks that `consult_model` is visible in observe, edit, and exec, and stays hidden in cluster.

Tests, beside the existing `TestConsult*` contracts:

| Test | Contract |
| --- | --- |
| Tool call leaves the driver in place | Stub seat text comes back as the tool result. Driver model, backend pointer, and driver call count stay put. The seat sees one user message and no tools. |
| Unknown seat | A `cmd/axis` test uses the real resolver. The error quotes the seat, does not contain `pull`, and does not call `BuildBackend`. No `ChatStream` runs. The driver is unchanged. |
| Seat error and blank text | The tool result is the `consult` error and names the seat. A seat error whose text contains `pull` or `ollama serve` is `is not answering` and does not contain that text. |
| Ollama consult does not start the daemon | `DisableAutoStart` on the default local endpoint, with a transport that fails and is never dialed, leaves the start stub at 0. A new client still starts once. `consultResolver` with `BuildBackend` returns a client whose auto-start is off. The endpoint in that resolver test is `http://127.0.0.1:9` and is not dialed. |
| Same seat is single-slot | Two refs that resolve to one choice id do not sit inside `ChatStream` together. Two refs that resolve to different ids may. |
| Scope | Visible in observe, edit, and exec. Hidden in cluster. The observe prompt says `consult_model` sends the prompt to one other configured model. Cluster dispatch does not run the seat. |
| Confirmation | Score 0 prompts under edit, full, and `AutoApproveConfirm`. A session "always" skips later confirms. |
| Piped denial | `axis agent -p` denies `consult_model`. `TestPipedConfirmConsultStillAsks` checks that the notice says the tool still asks under `--autonomy full` and `--auto-approve`, says to run interactively, and does not contain `pass --autonomy full`. |

## Alternatives

Pass the seat URL in the tool arguments. The model could then be steered at an arbitrary endpoint, and the URL would land in the transcript. Rejected. The seat has to be a name the operator already configured.

Reuse `RoutingBackend`. It swaps the whole turn to one backend and still hands that backend the driver's tools. Startup attaches it only for a cloud cheap model. Rejected. The driver has to stay in place, and the seat must not receive tools.

Unhide `spawn_subagent`. The child shares the parent's backend (`internal/agent/subagent.go`). Rejected. That splits work across runs, not across models.

Fan out to several seats, or add the seat as a council member. Rejected for this change. One call, one seat.

## Research

`bindAgentModelChoice` (`cmd/axis/agent_console_bind.go`) replaces the session backend. A tool that called it would drop the driver. `consult` does not take `*Agent`, which is why the wiring stays outside that function.

`CheapModel` attaches `NewRoutingBackend` only when the cheap model is set and the active protocol is cloud (`cmd/axis/agent_startup_model.go`). The chosen backend still receives the tool list (`internal/agent/routing.go`). A local driver never enters that path.

`spawn_subagent` is in `neverTool` (`internal/agent/scope.go`). `dispatchSubagent` builds the child on the parent's backend.

The helper's measurement is the stub suite `TestConsult*` and the build-tagged `TestConsultLive`. No further optimization applies: this change advertises the helper. The consult Ollama client does not auto-start. The driver client does. A seat error that contains `pull` or `ollama serve` becomes `is not answering`.

## Review

Kind: spec
Reviewer: fresh-context subagent (feature-dev:code-reviewer)
Model: Grok (xAI). Same vendor family as the session that filed this record. Claude Opus was not available in this harness.
Base: f8c258d696c78f363ac7bc23ea82a16ecd1e0289
Head: f8c258d696c78f363ac7bc23ea82a16ecd1e0289
Date: 2026-11-13
Verdict: approve-with-changes

1. Major: score 0 did not ask under edit, full, or auto-approve. Fixed in the confirmation paragraph before the second spec review.
2. Major: missing and disabled seats returned the ollama pull text. Fixed in the resolver paragraph before the second spec review.
3. Minor: the same-seat test did not lock the resolved choice id. Fixed in the slot sentence and the same-seat test row before the second spec review.

Questions:
- The observe prompt said the scope was read-only inspection. Verified: the design sentence keeps the write and shell limits and names consult_model.
- One call asks one seat, while a turn may run two consults. Verified: the non-goal now says one call asks one seat, two calls may run together, and the same resolved id stays single-slot.

Note: this verdict does not cover the revised paragraphs. The next spec review does. Head is the branch tip. The spec was untracked. There is no review commit.

Kind: spec
Reviewer: fresh-context subagent (feature-dev:code-reviewer)
Model: Grok (xAI). Same vendor family as the session that filed this record. Claude Opus was not available in this harness.
Base: f8c258d696c78f363ac7bc23ea82a16ecd1e0289
Head: f8c258d696c78f363ac7bc23ea82a16ecd1e0289
Date: 2026-11-13
Verdict: approve

Note: no findings and no questions. This review read the revised Problem, Non-goals, Design, Alternatives, Research, and test table, and re-checked score 0, the resolver pull text, the shared choice id, concurrent tool dispatch, and the observe prompt. The spec stayed untracked at this tip.

Kind: diff
Reviewer: fresh-context subagent (feature-dev:code-reviewer)
Model: Grok (xAI). Same vendor family as the authoring session. Claude Opus was not available in this harness.
Base: f8c258d696c78f363ac7bc23ea82a16ecd1e0289
Head: f8c258d696c78f363ac7bc23ea82a16ecd1e0289
Date: 2026-11-13
Verdict: approve-with-changes

1. Blocker: the review records failed go run hack/verify-specs.go. Fixed by rewriting both spec blocks and these diff blocks into the verify-specs form. Head is the branch tip because the change is still uncommitted.
2. Minor: .claude/settings.json, .github/copilot-instructions.md, and AGENTS.md are dirty beside this feature. Declined: those hunks are a separate tree this session did not author and must not revert. git diff --stat on those three paths is 8 insertions. A consult commit must leave them out.
3. Minor: full mode and auto-approve copy said a score of 0 was auto-approved. Fixed in agentSafetyLabel, the autonomy and auto-approve flag text, and the piped consult notice.

Questions:
- A resolved Ollama seat uses chat.Client.ChatStream, which can start the local daemon and return ollama pull. Became: the consult client turns auto-start off, and a seat error containing pull or ollama serve is not answering. TestEnsureRunningDisableAutoStartDoesNotLaunch, TestConsultResolverDisablesOllamaAutoStart, and TestConsultModelToolReportsSeatErrors. Resolver failures still strip pull. The approving spec review did not read this sentence.
- axis agent -p denies consult_model even under full and auto-approve. Verified: pipedConfirm denies every call that reaches it, and TestPipedConfirmConsultStillAsks checks the notice no longer offers those flags as a way through.

Note: the reviewed tree is the uncommitted working tree at this tip, not a descendant commit. The Ollama auto-start sentences were written after both diff reviews. Those reviews do not approve them. A later spec review has to read the revised non-goal, design, and research sentences before merge.

Kind: diff
Reviewer: fresh-context subagent (general-purpose)
Model: Grok (xAI). Same vendor family as the authoring session. Claude Opus was not available in this harness.
Base: f8c258d696c78f363ac7bc23ea82a16ecd1e0289
Head: f8c258d696c78f363ac7bc23ea82a16ecd1e0289
Date: 2026-11-13
Verdict: approve-with-changes

1. Major: an Ollama consult can start ollama serve and return ollama pull. Fixed in DisableAutoStart, EnsureRunning, and consultHidesSeat. TestEnsureRunningDisableAutoStartDoesNotLaunch leaves the start stub at 0. TestConsultResolverDisablesOllamaAutoStart checks the built client. TestConsultModelToolReportsSeatErrors hides pull and ollama serve.
2. Minor: full mode and auto-approve copy said a score of 0 was auto-approved. Fixed in agentSafetyLabel and the flag text, with TestAgentSafetyLabelFullNamesSpecificRisks.
3. Minor: the same three unrelated files are in the worktree. Declined: same reason as the other diff review. They stay out of a consult commit.

Questions:
- Two consult calls in one turn can both pass the session-approval check before either stores always. Open: not probed with two goroutines. The sequential test covers a later call. The spec already says the first call asks and that two calls may overlap.

Note: the reviewed tree is the uncommitted working tree at this tip, not a descendant commit. The Ollama auto-start sentences were written after both diff reviews. Those reviews do not approve them. A later spec review has to read the revised non-goal, design, and research sentences before merge.

Kind: spec
Reviewer: fresh-context subagent (feature-dev:code-reviewer)
Model: Grok (xAI). Same vendor family as the authoring session. Claude Opus was not available; this harness cannot pick another model for a subagent.
Base: f8c258d696c78f363ac7bc23ea82a16ecd1e0289
Head: f8c258d696c78f363ac7bc23ea82a16ecd1e0289
Date: 2026-11-13
Verdict: approve-with-changes

1. Minor: the design names TestConsultIsNotAdvertisedToTheModel, and that test is not in the tree. Fixed by pointing the sentence at TestConsultModelIsVisibleOutsideCluster.
2. Minor: piped denial was outside the design and the test table. Fixed by stating that axis agent -p denies consult_model and does not offer --autonomy full or --auto-approve as a way through, and by adding TestPipedConfirmConsultStillAsks to the test table.

Note: this review read the untracked working tree, including the Ollama auto-start sentences. Head is the branch tip. There is no review commit. The two findings above are resolved in this file. No questions.
