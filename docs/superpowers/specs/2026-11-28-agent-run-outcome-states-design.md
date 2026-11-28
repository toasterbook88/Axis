# `axis agent`: one set of state words for how every run ended

Date: 2026-11-28. Status: proposed. Builds on the approved `-p` exit-code change (`RunStop`,
`LastRunOutcome()`, exit codes 8 and 9, `axis agent -p --format json`), which is PR #687 and not on
`main` yet. Linear DEA-31. Line references marked "main" are at `ab2adcc` (v0.29.0). Lines marked
"patch" are at `1084330`, the head of PR #687 (`feat/agent-p-exit-outcomes`) when this spec was
rebased onto it; later commits on #687 may shift them.

## Problem

The exit-code change made `-p` report how a run ended. Everywhere else a finished run still reports
in its own words, or not at all:

1. **No shared vocabulary.** `RunStop` says `answered`, `turn_ceiling` or `failed`
   (`internal/agent/run_outcome.go:11-16`, patch). The session log says `answer`, `max_turns`,
   `error`, `canceled` or `timeout` (`internal/agent/sessionlog.go:127-139`, main;
   `docs/runbooks/agent-session-logs.md:44`). Background tasks say `running`, `completed` or
   `failed` from `check_task` and `running`, `done` or `failed` from the list
   (`internal/agent/async_tasks.go:65-87`, main). A2A tasks say `completed`, `failed`,
   `input-required` and more (`internal/a2a/task.go:14-21`, main). A supervisor loop that reads a
   child, an A2A task and a `-p` run reads four vocabularies. A2A and MCP tasks already share one.
2. **A cancel or deadline can land in the wrong stop.** `runLocked`'s defer sets `RunStopFailed`
   for any error (`internal/agent/agent.go:403-410`, patch), so a cancelled context reads as a
   failure. A cancel or a `--timeout` deadline that arrives while the last round's tools run is
   worse: the loop runs out of turns, `Run` returns nil, the ceiling notice prints and the outcome
   is `turn_ceiling`. Probed on the patched tree with a backend that cancels the run's context
   during round 2 of 2: `err=<nil> outcome={Stop:turn_ceiling Turns:2 MaxTurns:2} ctxErr=context
   canceled`. A cancel during an earlier round gives `err=chat stream (turn 1): context canceled
   outcome={Stop:failed ...}`. The session log decides from `Run`'s error
   (`sessionlog.go:129-139`, main), so it records the last-round case as `max_turns` too. PR #687
   corrects `-p` alone: `runOneShotPiped` turns a nil error with `turn_ceiling` and a live context
   error into a failure (`cmd/axis/agent_oneshot.go:284-288`, patch). `LastRunOutcome()`, the
   session log, the ceiling notice, the positional one-shot and sub-agents still see `turn_ceiling`.
3. **A child at its ceiling reports a generic error.** `dispatchSubagent` checks only `Run`'s error
   and then the child's final text (`internal/agent/subagent.go:154-163`, main). At the ceiling
   `Run` returns nil, `finalAssistantText` skips tool-call messages (`subagent.go:252-261`, main) and
   finds nothing, so the parent gets "sub-agent returned no final answer; the parent must not invent
   one" (`subagent.go:169-171`, main). Probed: a child with `max_turns: 2` that keeps calling tools
   returns exactly that. The parent can't tell "out of turns" from "answered with nothing". A
   cancelled child returns "sub-agent failed: chat stream (turn 1): context canceled". The async
   path has the same gap (`subagent.go:125-139`, main) and records both as `failed`.
4. **The A2A exec path has no cancelled state.** An approved A2A task runs one guarded command
   (`internal/api/server.go:700`, main), not the agent loop: neither `internal/a2a` nor
   `internal/api` imports `internal/agent`. `persistA2ARun` writes `completed` or `failed` and
   nothing else (`server.go:740-765`, main). The run uses `r.Context()`, so a client that goes away
   cancels it and the task is stored as `failed` with the context error. `TaskStateInputRequired` is
   declared (`task.go:20`, main) and never set.
5. **The positional one-shot exits 0 at the ceiling.** `axis agent "question"` without `-p` checks
   only `Run`'s error (`cmd/axis/agent.go:298-302`, main; `:333-340`, patch), so a run that ran out
   of turns exits 0, the code `-p` now reserves for an answer. The patch kept this on purpose and
   says so in `docs/current-state.md:117` (patch).

## Non-goals

- Changing `-p`'s documented `outcome` values, exit codes or precedence
  (`docs/machine-output.md`, patch). They stay `answered`/0, `turn_ceiling`/8, `needs_input`/9 and
  `failed`/4.
- Changing `Run`'s return value. It stays nil at the ceiling and on a last-round cancel or deadline,
  so the REPL, console and slash commands keep their behavior. Callers that need the cause read
  `LastRunOutcome()` and their own context.
- Opening `spawn_subagent`, `check_task` or the remote tools. They stay in the never set and the
  hidden set (`internal/agent/scope.go:96-119`, main). That's the peer-loop spec (DEA-32), which
  reuses the type defined here. Until then the subagent part of this change is reachable only from
  tests and operator surfaces.
- Cancelling open children when a session ends (`subagent.go:115` uses `context.Background()`,
  main, and nothing calls `backgroundTask.cancel` but its own goroutine). Also DEA-32. Until then an
  async child's `cancelled` outcome is unreachable; the code path is still tested directly.
- Resuming a run that stopped at its ceiling. No machine-read surface can today, so none reports
  `input_required` (section 1). DEA-32 adds that word if it makes children resumable.
- Moving `internal/a2a` to the A2A v1 wire shape. `task.go` uses the pre-1.0 string form
  (`input-required`, `Part.Type` at `task.go:30`, main); A2A v1 serializes `TaskState` as enum names
  (`TASK_STATE_CANCELED`). That's a separate change.
- Running the agent loop behind A2A. No A2A skill does today; this spec only fixes the mapping that
  such a skill would use.
- Changing the session log's documented `stop` words. They stay; section 2 only feeds them the same
  decision.
- `auth_required`, `rejected`, `submitted` and `working` in the agent's run state. A finished run is
  never in them; surfaces that track a task in flight keep their own running state.

## Design

### 1. One run-state type in `internal/agent`

Add to `run_outcome.go`:

```go
// RunState is the A2A and MCP terminal task-state word for a finished run.
type RunState string

const (
	RunStateCompleted RunState = "completed"
	RunStateFailed    RunState = "failed"
	RunStateCancelled RunState = "cancelled"
)

// RunStopCancelled means the run's own context was cancelled before an answer.
const RunStopCancelled RunStop = "cancelled"

// State maps the stop to the shared word. Reason returns the stop when the
// state alone loses it.
func (o RunOutcome) State() RunState
func (o RunOutcome) Reason() string
```

| `RunStop` | `State()` | `Reason()` |
|---|---|---|
| `answered` | `completed` | `""` |
| `turn_ceiling` | `failed` | `turn_ceiling` |
| `failed` | `failed` | `""` |
| `cancelled` | `cancelled` | `""` |
| `""` (no run yet) | `""` | `""` |

The spellings are MCP's (`cancelled`). A2A spells it `canceled` in its pre-1.0 strings, so a
surface that writes A2A maps the word at its edge (section 5). Every state here is terminal. Both
specs make `input_required` an interrupted state that promises the task will continue once input
arrives, and MCP blocks `tasks/result` until a terminal state (`tasks.mdx:465`). No surface that a
machine reads can continue a run that hit its ceiling: a `-p` process has exited and a synchronous
child is discarded. So `turn_ceiling` reports `failed` with reason `turn_ceiling`. The reason keeps
the difference the exit-code change was made to keep, and `-p` still exits 8 for it. `-p`'s
`needs_input` reports `failed` with reason `needs_input` for the same cause; it's decided in
`cmd/axis` from the recorded denials, not by the agent package.

`RunOutcome` keeps its fields. DEA-32 references `RunOutcome`, `RunStop`, `RunState`, `State()`,
`Reason()` and `SubAgentError` (section 4). If it makes children resumable it adds
`RunStateInputRequired` for those children only.

### 2. Cancelled is its own stop, and the context decides

`runLocked` decides the stop once per run, from one `ctx.Err()` read, and keeps the result in a
run-local `decided` variable that everything after it reads:

```go
func decideStop(ctxErr error, stop RunStop, err error) RunStop
```

In this order:

1. `stop == RunStopAnswered`: keep it. The answer is in the conversation.
2. `errors.Is(ctxErr, context.Canceled)`: `RunStopCancelled`, whether or not `err` is set.
3. `ctxErr != nil` (a deadline): `RunStopFailed`, whether or not `err` is set.
4. `err != nil`: `RunStopFailed`.
5. Otherwise `RunStopTurnCeiling`, as now.

It's called in one of two places. When the loop runs out of turns (`internal/agent/agent.go:533-538`,
patch), `decided = decideStop(ctx.Err(), "", nil)` runs before the ceiling notice. Otherwise the
single deferred function calls it with its own `ctx.Err()` read. Today's two defers, the outcome
defer at `agent.go:405-410` and the session-log defer at `:416-418` (patch), merge into that
one function, which calls `setRunOutcome` and `sessionLog.finishTurn` with the same `decided`.

Rules 2 and 3 cover the last-round cases in Problem 2. The test reads the run's own `ctx`, not the
returned error, because a backend can wrap a cancel of its own: the cloud stream's idle guard turns
its internal cancel into `context.DeadlineExceeded` (`internal/agent/cloud_backend.go:825-829`,
main), and a cancel of the caller's context must not be confused with that. A deadline
(`--timeout`, `agentRequestContext` at `cmd/axis/agent.go:518-523`, main) is `failed`: neither spec
has a timeout state, MCP's `ttl` expiry isn't `cancelled`, and nobody asked the run to stop.

Two side effects read `decided`, never the context again:

- `emitMaxTurnsReached` runs only when `decided == RunStopTurnCeiling`, so a cancelled or timed-out
  run doesn't print the ceiling notice, and a cancel that lands after the decision can't make the
  notice and the outcome disagree.
- `sessionLog.finishTurn` takes `decided` and maps it to the log's existing words:
  `answered` to `answer`, `turn_ceiling` to `max_turns`, `cancelled` to `canceled`, `failed` to
  `timeout` when `ctx.Err()` or `err` is a deadline and `error` otherwise. Today it decides from
  `runErr` and the ceiling flag (`sessionlog.go:127-139`, main). The words and the runbook don't
  change; the log and `LastRunOutcome()` now agree on every run.

### 3. `-p` keeps its values; the JSON gains `state` and `reason`

- `classifyPipedRun` (`cmd/axis/agent_oneshot.go:214-225`, patch) adds `stop ==
  agent.RunStopCancelled` to its first case, so a cancelled `-p` run stays `failed` and exit 4.
  Without it a last-round cancel, which returns nil, would fall through to `answered` and exit 0.
- `runOneShotPiped` already sets `runErr = ctx2.Err()` for a nil error with `turn_ceiling` and a
  live context error (`agent_oneshot.go:284-288`, patch). With section 2 that stop is `failed` or
  `cancelled` instead, so the condition becomes a nil error with either of those stops. The
  assignment is the same, and `ctx2.Err()` is non-nil in both cases (rules 2 and 3). Everything
  after it runs the existing error path unchanged: the `error: Agent failed:`
  line, no token summary, no history save, `data.error` set and `final_text` cleared. The two
  promises in `docs/machine-output.md` (patch) about `failed` hold for a last-round cancel or
  deadline.
- `pipedRunResult` (`agent_oneshot.go:228-238`, patch) gains `state` and `reason` (`omitempty`):
  `answered` gives `completed`; `turn_ceiling` gives `failed`/`turn_ceiling`; `needs_input` gives
  `failed`/`needs_input`; `failed` gives `failed` with no reason, or `cancelled` when the stop was
  cancelled. The envelope's `status`, `ok` and `data.outcome` don't change. `docs/machine-output.md` gets the two
  fields and the mapping. This is an additive output-schema change.

### 4. Sub-agents return the child's outcome

In `dispatchSubagent` (`internal/agent/subagent.go:154-163`, main), after `child.Run`, read
`child.LastRunOutcome()`. Every non-answer comes back as one exported type:

```go
// SubAgentError reports a child run that ended without an answer.
type SubAgentError struct {
	Outcome RunOutcome
	State   RunState // what the parent should report; failed for an empty answer
	Reason  string
	Err     error // the child's Run error, if any
}

func (e *SubAgentError) Unwrap() error { return e.Err }
```

| child ended | `State` | `Reason` | `Error()` text |
|---|---|---|---|
| answered with text | (no error) | | the text is returned |
| answered with no text | `failed` | `empty_answer` | unchanged: "sub-agent returned no final answer; the parent must not invent one" |
| `failed` | `failed` | `""` | unchanged: "sub-agent failed: %w" |
| `turn_ceiling` | `failed` | `turn_ceiling` | "failed: turn_ceiling: sub-agent stopped at its N-turn ceiling without a final answer; the parent must not invent one" |
| `cancelled` | `cancelled` | `""` | "cancelled: sub-agent was cancelled after N turns" |

`State` is set by the dispatcher, not read from `Outcome.State()`, because an empty answer has
outcome `answered` but no result. The two legacy texts stay byte for byte; only the two new cases
lead with the state. `Unwrap` keeps `errors.Is(err, context.Canceled)` true for a parent that
checks the tool error, as the `%w` text does today.

The async path (`subagent.go:125-139`, main) builds the same error. `backgroundTask` gains an
`outcome *SubAgentError` field, which `finish` takes as an argument and sets under the task's
`mu`, so `formatTaskStatus` reads it under the same lock as the state. `bgTaskState` and its labels don't change
(`async_tasks.go:30-87`, main); both labels are kept as compat surfaces and `finish` still maps
any error to `failed`. `formatTaskStatus` (`async_tasks.go:288-310`, main) adds one line after the
first, `Outcome: <state> (<reason>, <turns>/<max_turns> turns)`, only for sub-agent tasks that
ended without an answer. The first line, `Task <id>: <label>`, stays as it is.

### 5. A2A records cancelled

- Add `TaskStateCanceled TaskState = "canceled"` to `internal/a2a/task.go`, in the pre-1.0 spelling
  the file already uses (A2A v0.3.0 `TaskState` enum).
- `AllowTerminalReport` (`internal/a2a/terminal_actor.go:22-32`, main) adds it to the states only
  the assignee may report, so a client can't mark its own task cancelled.
- In `approveA2ATask`, after `runLiveGuarded` returns (`internal/api/server.go:700-702`, main), the
  result decides first. `res.OK && res.Error == ""` stays `completed` whatever happened to the
  request context: the command ran and its effects are real. A non-OK result whose `r.Context()`
  is cancelled is stored as `canceled`, with a status message saying the request ended before the
  run finished. Every other result keeps today's `failed` rule in `persistA2ARun`.
- The only client that can see `canceled` is `axis task status`, which reads a stored task later.
  It exits 4 on `canceled` with "task <id> is canceled" in both branches: json
  (`cmd/axis/task_delegate.go:301`, main) and text (`:350`, main). Today an unknown state there
  exits 0. Delegate and approve can't receive it: delegate's send never runs a guarded command
  (exec-shaped skills queue as pending, `internal/a2a/handler.go:119-129`, main), and approve's own
  request is the one whose context ended, so its response never arrives.
- When an agent-backed skill exists, it maps `SubAgentError.State` or `RunOutcome.State()` to A2A
  as: `completed` to `completed`, `failed` to `failed` with the reason in the status message,
  `cancelled` to `canceled`. Not built now: nothing would call it, and `hack/verify-deadcode.sh`
  rejects unreachable functions.

### 6. The positional one-shot exits 8 at the ceiling

Recommendation: the positional one-shot (`cmd/axis/agent.go:331-340`, patch) applies section 3's
rule (`runErr = ctx2.Err()` for a nil error with a `failed` or `cancelled` stop), then:

- an error, including a cancel or deadline, takes today's path: `error: Agent failed: <cause>` on
  stderr and exit 4;
- `turn_ceiling` prints `error: agent stopped at the N-turn ceiling without a final answer` on
  stderr, the line `-p` prints (`cmd/axis/agent_oneshot.go:348`, patch), and exits
  `ExitErrAgentTurnCeiling` (8);
- an answer still exits 0.

The output contract `TestCharOneShotLegacyContractUnchanged` pins (banner, no observer, no piped
confirm; `cmd/axis/agent_cmd_characterization_test.go:405-423`, main) doesn't change. Exit 0 should
mean answered for every invocation a script can make; the positional form is one, and 8 is already
reserved for this. `needs_input` doesn't apply: the positional run asks on stdin. The bullet in
`docs/current-state.md:117` (patch) and the exit-code list in `-p`'s help (`cmd/axis/agent.go:385`,
patch) change to say the positional form exits 8 at the ceiling too.

### Tests

- `run_outcome_test.go`: `State()` and `Reason()` for every stop; a cancel before the first round,
  during a middle round and during the last round's tools all give `cancelled`; a deadline during a
  middle round and during the last round's tools both give `failed`; an answer that lands as the
  context is cancelled stays `answered`; neither last-round case calls `MaxTurnsReached` on the
  observer. A backend that returns an error wrapping `context.Canceled` while the caller's context
  is live gives `failed`, and one wrapping `context.DeadlineExceeded` (the idle guard's shape) gives
  `failed`, so a rule that reads the error instead of the context fails the test.
- `sessionlog` tests: a last-round cancel writes `stop` `canceled` and a last-round deadline writes
  `timeout`, matching `LastRunOutcome()`; the backend-wrapped cancel writes `error` and the
  backend-wrapped deadline writes `timeout`. One test reads the log's `stop` and `LastRunOutcome()`
  from the same run and checks they map to each other.
- `agent_oneshot_outcome_test.go`: drive `runOneShotPiped` through a last-round cancel and a
  last-round deadline in text and json. Assert exit 4, the `error: Agent failed:` line on stderr, no
  token summary, no ceiling notice, `data.error` set and `final_text` empty. Assert `state` and
  `reason` for all four outcomes and for a cancel, with `reason` absent for `answered`, `failed` and
  `cancelled`, and that `data.outcome` and `status` are what
  they were.
- `subagent` tests: each row of the section 4 table returns a `*SubAgentError` (or the text) with
  that `State`, `Reason` and exact `Error()` text; the async task's `check_task` output keeps its
  first line and adds the outcome line.
- `internal/api`: an approve whose request context is cancelled while the run fails stores
  `canceled`; one whose context is cancelled after an OK result stores `completed`; a normal failure
  stores `failed`. `terminal_actor_test.go`: a client report of `canceled` is refused.
- `cmd/axis`: `axis task status` exits 4 on `canceled` in json and text; the positional one-shot
  exits 8 at the ceiling, 4 on a last-round cancel and on a last-round deadline, 0 on answer.
- `go run hack/verify-specs.go`, `./hack/verify-specs-tests.sh`, `hack/verify-deadcode.sh`, `hack/verify-doc-facts.sh`, import tiers.

## Alternatives

- **Rename `-p`'s outcome values to the shared words.** Rejected: they're a documented contract
  from the approved change, and a supervisor already reading `turn_ceiling` would break. The
  additive `state` field gives the shared word without that.
- **Map `turn_ceiling` to `input_required`.** It reads closer to "more turns would let it go on".
  Rejected: both specs make `input_required` a promise that the same task continues, and nothing
  that reports it can continue. A requestor that follows MCP would wait on a run that already
  ended. The reason field carries the difference instead.
- **Cancelled as `failed` with a reason.** Keeps one fewer state. Rejected: both specs make it a
  separate terminal state, and a supervisor retries a failure but not a cancel it asked for.
- **Decide cancel from the returned error (`errors.Is(err, context.Canceled)`).** Misses the
  last-round cancel, where `err` is nil, and trusts backends to wrap faithfully. Reading `ctx.Err()`
  is one call and doesn't depend on either.
- **Make `Run` return `ctx.Err()` on a last-round cancel or deadline.** Would make the extra rule in
  section 3 unnecessary. Rejected for now: the REPL and console treat a `Run` error differently from
  nil, and changing that is outside this spec.
- **Put the state type in a support package both `internal/agent` and `internal/a2a` import.** Not
  needed yet: no A2A code reads a `RunOutcome`, and the two spellings differ anyway. Revisit with the
  A2A v1 move.
- **Keep the positional one-shot at exit 0.** Its users are mostly at a terminal and see the
  warning. Rejected because a script can't, and the fix costs nothing in output.
- **A2A: store a disconnect as `failed`.** Today's behavior. Rejected for a non-OK result because
  the command didn't fail on its own; its requester went away. An OK result stays `completed`.

## Research

- A2A `specification/a2a.proto` (main):
  https://raw.githubusercontent.com/a2aproject/A2A/main/specification/a2a.proto. `TaskState`
  (`:187-208`) lists `COMPLETED`, `FAILED`, `CANCELED`, `REJECTED` as terminal and `INPUT_REQUIRED`,
  `AUTH_REQUIRED` as interrupted. `TaskStatus` carries `state`, an optional `message` and a
  timestamp (`:211-219`). That's the shape `internal/a2a.TaskStatus` already has.
- A2A v0.3.0 JSON schema:
  https://raw.githubusercontent.com/a2aproject/A2A/v0.3.0/specification/json/a2a.json. Its
  `TaskState` enum (`:2440-2453`) is the lower-case form axis uses, and spells it `canceled`.
- MCP tasks, 2025-11-25:
  https://raw.githubusercontent.com/modelcontextprotocol/modelcontextprotocol/main/docs/specification/2025-11-25/basic/utilities/tasks.mdx.
  States `working`, `input_required`, `completed`, `failed`, `cancelled` (`:713-722`); the last
  three are terminal and `input_required` may return to `working` (`:403-407`); `tasks/result`
  blocks until a terminal state (`:465`), which is why nothing here reports `input_required`; a
  cancelled task stays cancelled (`:494-496`); `statusMessage` carries the detail (`:708`). That's
  the reason field here.
- Repository prior art: the session log already tells a cancel from an error and a timeout
  (`internal/agent/sessionlog.go:127-139`, main), with the same three-way split section 2 uses,
  but decides from the returned error and so misses the last-round cases. Section 2 moves it onto
  the context decision.
- Optimization: none applies. The new work per run is one `ctx.Err()` call and a string map, and per
  A2A task one `ctx.Err()` call.

## Review

Kind: spec
Reviewer: fresh-context subagent (Claude Code, general-purpose agent given only review-prompt.md)
Model: Anthropic Claude (same family as the author; fresh-context session, not cross-family)
Base: 9ac3b22
Head: ffdb936
Date: 2026-11-28
Verdict: revise

1. [major] A cancelled `-p` run with a nil error went into the failed branch with no error, printing "agent failed: <nil>", the ceiling notice and the token summary, and leaving `data.error` unset and `final_text` filled. Fixed in Design section 3: `runOneShotPiped` sets `runErr = ctx2.Err()` for a nil error with a failed or cancelled stop and then takes the existing error path; section 2 skips the ceiling notice; section 6 applies the same rule to the positional form; the listed test drives `runOneShotPiped` end to end.
2. [major] A `--timeout` deadline during the last round reported `turn_ceiling`, which contradicted "a deadline stays failed". Fixed in Design section 2: rule 3 maps any context deadline to `failed` whether or not `err` is set, and the tests name the last-round-tools deadline case.
3. [minor] `SubAgentError` text and outcome rules conflicted for the no-answer and failed cases, and an empty answer would expose `completed`. Fixed in Design section 4: the type carries its own `State` and `Reason` set by the dispatcher, the two legacy texts stay byte for byte, only the ceiling and cancel texts lead with the state, and a table lists each case.
4. [minor] The A2A rule marked a successful command `canceled` when the request context ended after it finished. Fixed in Design section 5: an OK result stays `completed`, only a non-OK result with a cancelled context becomes `canceled`, and a test covers OK with a cancelled context.
5. [minor] The session log and `RunStop` disagreed on a last-round cancel (`max_turns` against `cancelled`), and the prior-art claim was wrong. Fixed in Design section 2: `finishTurn` takes the decided stop and maps it to the log's existing words; the Research entry now says the log missed the last-round cases.
6. [minor] `turn_ceiling` mapped to the interrupted state `input_required` on surfaces that can't resume it, against MCP `tasks.mdx:465`. Fixed in Design section 1: every reported state is terminal, `turn_ceiling` and `needs_input` report `failed` with a reason, `RunStateInputRequired` is dropped until a resumable surface exists (DEA-32), and the old choice moved to Alternatives.

Questions:
- Can the `axis task approve` client ever receive `canceled` for its own request? Became a design change: section 5 now limits the client change to `axis task status`, because approve's response can't arrive once its request context has ended and delegate never runs a guarded command.
- Does `hack/verify-deadcode.sh` flag exported constants that only `State()` uses? Verified: the script runs `golang.org/x/tools/cmd/deadcode`, which reports unreachable functions only (`hack/verify-deadcode.sh` comments and its "no unreachable functions" message), so constants are not checked.
- Did the spec mean `-p`'s help or `--max-turns` help? Became a design change: section 6 now names the exit-code list in `-p`'s help, now at `cmd/axis/agent.go:385` (patch at `1084330`).

Kind: spec
Reviewer: fresh-context subagent (Claude Code, general-purpose agent given only review-prompt.md)
Model: Anthropic Claude (same family as the author; fresh-context session, not cross-family)
Base: 9ac3b22
Head: 9ba4e13
Date: 2026-11-28
Verdict: approve-with-changes

1. [minor] The stop was decided from three separate `ctx.Err()` reads (two defers and the ceiling notice), so a cancel landing after the loop could print the notice and log `max_turns` while `LastRunOutcome()` said `cancelled`. Fixed in Design section 2: `decideStop` takes one `ctxErr` snapshot, the result lives in a run-local `decided`, the two defers merge into one function that passes `decided` to both `setRunOutcome` and `finishTurn`, and the notice is gated on `decided`.
2. [minor] The test plan named `hack/verify-specs.sh`, which doesn't exist. Fixed in Design section Tests: it now names `go run hack/verify-specs.go` and `./hack/verify-specs-tests.sh`, as CI runs them.
3. [minor] No test pinned reading the context instead of the error. Fixed in Design section Tests: a backend-wrapped cancel and a backend-wrapped deadline with a live caller context must give `failed`, with session-log `error` and `timeout`, and one test checks the log and `LastRunOutcome()` agree on the same run.
4. [minor] The reason for a plain failure was `failed` in section 1 and absent in section 3. Fixed in Design section 1: `Reason()` returns `""` for `failed`; sections 3 and 4 and the test plan now say `reason` is absent for `answered`, `failed` and `cancelled`.

Questions:
- Should `SubAgentError` implement `Unwrap`? Became a design change: section 4 adds `Unwrap() error` returning `Err`, so `errors.Is(err, context.Canceled)` stays true as with today's `%w` text.
- Is the async `outcome` field race-free? Became a design change: section 4 has `finish` take the outcome and set it under the task's `mu`, the lock `formatTaskStatus` already reads under.
- Should `-p` tell a timeout from a backend error in `reason`? open: the session log does and `state`/`reason` don't; left to the maintainer, since no supervisor asks for it yet.
