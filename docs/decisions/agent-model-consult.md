# Decision: Agent model consult

**Date:** 2026-11-12
**Scope:** `internal/agent` (`consult.go`, `consult_test.go`) and the consult Ollama client's auto-start flag in `internal/chat`
**Status:** `consult_model` is registered. The driver stays in place. The tool spec is `docs/superpowers/specs/2026-11-12-agent-model-consult-design.md`. A second spec review on 2026-11-13 approved the revised text.

## 1. Context

An Axis agent session has one chat backend. `bindAgentModelChoice` (`cmd/axis/agent_console_bind.go`) replaces that backend. `/model` and startup both go through it.

`RoutingBackend` (`internal/agent/routing.go`) sends each turn to either the primary or the cheap backend, and the chosen backend still receives the tool list. Startup attaches it only when a cheap model is set and the active protocol is cloud (`cmd/axis/agent_startup_model.go`). A local driver never enters that path.

`spawn_subagent` is hidden by `neverTool` (`internal/agent/scope.go`). The child inherits the parent's backend (`internal/agent/subagent.go`). It splits work across nodes. It does not split work across models.

A substrate that uses more than one model needs a call that asks a second backend for text and leaves the driver in place.

## 2. Decision

1. `consult` takes a context, a seat `ChatBackend`, and a `consultRequest`. It does not take `*Agent`, so it cannot call `SetBackend` or `SetModel`.
2. The seat receives one user message and a nil tool list. The prompt is the caller's excerpt, not the driver transcript.
3. These results are errors the caller can show:
   - the seat is nil (`seat "<id>" is not answering`)
   - the seat returns an error (wrapped with the seat id), unless that error contains `pull` or `ollama serve`, which is reported as `is not answering` and does not contain that text
   - the seat returns blank visible text (`returned no visible text`)
   - the context is already cancelled when the seat would be called
4. `SingleSlot` takes one process-local token for that `SeatID` and holds it across the seat call. A different `SeatID` is not blocked. The token map lock is not held during the wait or the call. Two OS processes are not coordinated.
5. `consult_model` is registered. It is visible in observe, edit, and exec when the command installed a resolver; a registry with no resolver does not advertise it, and a child agent registry has none. It is hidden in cluster. The executor calls `consult` with `SingleSlot` true and the resolved choice id as `SeatID`. `consult` still does not take `*Agent`. The first call asks the operator, including under edit, full, and auto-approve. An always answer covers later calls that see it; two calls dispatched together can both ask before either answer is stored. A call waiting for the slot returns `context.Canceled` when its context ends.

The caller chooses the seat and whether it is single-slot. This package does not name operators, hosts, or model ids.

## 3. Non-goals

- A flag, config key, command, MCP tool, or HTTP route
- A change to cheap-model routing or to `spawn_subagent`
- A cluster-wide queue
- Loading or stopping a model server
- A fan-out that asks several seats inside one call

## 4. Public boundary

The repository stays free of real hostnames, addresses, and live inventory. Tests use `driver-model`, `seat-a`, `seat-b`, and `slot-a`.

## 5. Tests

`go test ./internal/agent -count=1 -run TestConsult` covers:

| Test | Contract |
| --- | --- |
| `TestConsultLeavesTheDriverInPlace` | Seat text comes back. Driver model, driver backend, and driver call count stay put. The seat sees one user message and no tools. |
| `TestConsultDownSeatReturnsError` | A nil seat and a seat error name that seat. The driver is unchanged. |
| `TestConsultEmptyVisibleTextIsAnError` | Blank visible text is an error, not a successful empty answer. |
| `TestConsultSingleSlotSerializesOneSeat` | Two single-slot consults for one id do not sit inside `ChatStream` together. |
| `TestConsultDifferentSingleSlotSeatsMayOverlap` | Two single-slot ids may sit inside `ChatStream` together. |
| `TestConsultCancelledCallDoesNotReachTheSeat` | A cancelled context returns `context.Canceled` and does not call a seat that ignores cancellation. |
| `TestConsultModelIsVisibleOutsideCluster` | `consult_model` is visible in observe, edit, and exec, and hidden in cluster. The observe prompt names the tool only when it is offered. |

The same prefix runs the tool tests: the tool result is the seat text, a seat error and blank text name the seat, two refs that share a resolved id do not overlap inside `ChatStream`, different ids may, cluster dispatch does not run the seat, score 0 asks under edit, full, and auto-approve, and a session always skips the later confirm.

`go test ./cmd/axis -count=1 -run TestConsultRealResolver` drives `findModelTargetByRef`. A missing, empty, disabled, or ambiguous seat quotes the caller, does not contain `pull`, and does not build a backend. `TestConsultResolverDisablesOllamaAutoStart` builds a real client at `http://127.0.0.1:9` without dialing it and checks that auto-start is off. `TestEnsureRunningDisableAutoStartDoesNotLaunch` leaves the start stub at 0. The driver client still auto-starts.

`TestConsultLive` compiles only with the `liveconsult` tag. It skips unless `AXIS_CONSULT_LIVE=1`. The endpoint, model ids, and bearer token come from the environment, and the endpoint must be a loopback URL with no userinfo. One subtest dials `127.0.0.1:1` and expects an error that names `seat-down`. The default command above does not compile this file.
