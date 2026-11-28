# Agent peer loop: reachable, scoped, placed sub-agents

Date: 2026-11-28. Linear: DEA-32. Base: `ab2adcc` (v0.29.0).

This spec amends the agent scope charter. Its two decisions were settled by the
maintainer on 2026-11-28 with the recommended option A for both.

## Problem

`spawn_subagent` runs a child tool loop with a four-field brief, a depth cap and an
honest empty-answer rule, but the model can never call it, and the child it would
build does not match what its own prompt tells it.

1. **Unreachable.** `neverTool` holds `spawn_subagent`, `fleet_exec`, `run_on_node`,
   `remote_write_file`, `remote_tail_logs`, `remote_read_file`, `remote_grep`,
   `remote_list` and `run_background` ([`internal/agent/scope.go:96-105`](../../../internal/agent/scope.go)).
   `check_task` and `list_background_tasks` sit in `observeDeferred` and stay hidden at
   every scope "until a later PR names a grant for them" (`scope.go:107-119`, `:135-141`).
   Dispatch refuses any name the registry does not show
   ([`internal/agent/dispatch.go:124-126`](../../../internal/agent/dispatch.go)).
2. **The child always runs at observe.** `buildChildAgent` calls `NewToolRegistry(tc)`
   ([`internal/agent/subagent.go:201`](../../../internal/agent/subagent.go)), which starts at
   `ScopeObserve` ([`internal/agent/tools.go:76`](../../../internal/agent/tools.go)), and never
   calls `SetScope` (`tools.go:115`). The parent's registry gets its scope in `New`
   ([`internal/agent/agent.go:178`](../../../internal/agent/agent.go)) and on `SetAutonomy`
   (`agent.go:317`). The child also skips the rest of `New`'s registry setup: MCP tools
   (`agent.go:182-184`), eager allowlist, deferral and result cap (`agent.go:179-181`), and
   the visible-tools system message (`agent.go:185`). Its `autonomy` field is never set
   (`subagent.go:219-245`), while its `confirm` is the parent's already-wrapped confirm
   (`internal/agent/confirm.go:137-148`, wrapped at `agent.go:206` and `:326`).
3. **The child's prompt names tools it cannot call.** `buildSubAgentSystemPrompt` hard-codes
   `run_on_node`, `remote_read_file`, `remote_grep`, `remote_list` and `run_shell`
   (`subagent.go:267-273`); the brief text repeats the remote names (`subagent.go:100-101`).
   None of them is visible at observe. The prompt also says "Do not spawn further
   sub-agents" and "nesting is blocked" (`subagent.go:103`, `:282`) while
   `maxSubAgentDepth = 2` lets a depth-1 child spawn a depth-2 child (`subagent.go:17`, `:91`).
4. **"Chosen by placement" is text only.** The schema says an omitted `target_node` means
   the child "chooses via placement" (`subagent.go:55`) and the brief fills in "chosen by
   placement" (`subagent.go:105`). Nothing calls the ranker. `axis_place` shows the call
   that should run: `placement.InferRequirements` then `placement.ExplainPlacement`
   (`tools.go:499-513`, [`internal/placement/explain.go:309`](../../../internal/placement/explain.go)).
5. **Open async children are dropped silently at exit.** They run under
   `context.WithCancel(context.Background())` (`subagent.go:115`). `backgroundTaskStore`
   has `nextID`, `add`, `get` and `snapshot` and no way to cancel everything
   ([`internal/agent/async_tasks.go:128-168`](../../../internal/agent/async_tasks.go)). `Agent`
   has no close hook; both surfaces build it with `agent.New` and drop it
   (`cmd/axis/agent.go:267`, `cmd/axis/agent_console_startup_job.go:343`). Both sessions
   end with the CLI process, so a child never outlives it; a child still running at exit
   just vanishes with no line saying so. The task state machine has no cancelled state
   (`async_tasks.go:38-50`). A depth-1 child's own async tasks go in the child's private
   store (`subagent.go:242-244`, `:122-123`), which nothing reads once the child returns:
   the parent's `check_task` reads only its own store (`dispatch.go:173-178`).
6. **A child at its turn ceiling reads as silence.** `Run` returns nil at the ceiling
   (`agent.go:517-518`), so a child that ran out of turns surfaces as "sub-agent returned
   no final answer" (`subagent.go:169-171`), the same as one that answered with nothing.

The charter this amends is not in the repository. #456 (`6ed053d`) says the never set
follows `artifacts/governance/axis-schema-mask-contract.md`; that path has no history on
any ref (`git log --all -- artifacts/governance/axis-schema-mask-contract.md` and
`git log --all -S axis-schema-mask-contract` both print nothing). In the tree, the charter
is `scope.go` itself, the #456 PR body, and the delegation-brief spec, which calls the tool
"prohibited by the existing public scope charter" and lists enabling it as out of scope
([`2026-11-16-delegation-brief-design.md:13`, `:32`, `:38`](2026-11-16-delegation-brief-design.md)).
This spec is the written amendment.

## Non-goals

- Moving the child's model seat. The child keeps the parent's backend and model
  (`subagent.go:217-220`, `:235`). `target_node` picks where the child's node-addressed
  tools point, not where its model runs. Seat placement is a later direction.
- Revealing `run_on_node`, `fleet_exec`, `remote_write_file`, `remote_tail_logs` or
  `run_background`. They stay in the never set.
- Designing run outcomes or shared state words. DEA-31 owns `RunOutcome`; this spec only
  reads it.
- A shared brief across A2A, MCP and `AXIS_CONTEXT_FILE`, nested reservations, or
  `agent.*` lifecycle events.
- A per-call argument that narrows a child below the parent's scope. See Alternatives.
- Changing `maxSubAgentDepth`, `defaultSubAgentMaxTurns`, the brief fields or the
  empty-answer rule.
- A cumulative sub-agent or request budget per session or per turn. D2's cap limits how
  many sub-agents run at once, not how many run in total.
- Editing `docs/doctrine.md`, `AGENTS.md` or `README.md`.

## Design

### D1. Charter: which scopes show delegation (**decision 1: option A, maintainer, 2026-11-28**)

Recommended: `spawn_subagent`, `check_task` and `list_background_tasks` are visible at
observe, edit and exec, and hidden at cluster.

- `scope.go`: drop `spawn_subagent` from `neverTool`; drop `check_task` and
  `list_background_tasks` from `observeDeferred`; add a `delegateTool(name)` set returned
  visible by `toolVisible` when `scope != ScopeCluster`.
- `list_background_tasks` joins `check_task` because compaction keeps only the last 8
  messages verbatim ([`internal/agent/compaction.go:17-19`](../../../internal/agent/compaction.go)),
  so the parent can lose a task id the receipt gave it. Both are read-only and already
  auto-approved (`confirm.go:104-111`).
- `run_background` stays in the never set, so the store only ever holds sub-agent tasks
  while this spec stands.
- Update the `check_task` and `list_background_tasks` descriptions (`async_tasks.go:422-441`)
  to say they report async sub-agents.
- The newly visible tools load deferred by default. Add a `deferredBuiltin(name)` set
  (the three tools here plus D5's three) and make `eager` return false for it when no
  eager allowlist is set (`tools.go:196-204`). They show in the deferred catalog of the
  system prompt by name and summary (`internal/agent/deferred.go:862-877`) and run through
  `tool_call`, which ends in the same `dispatchToolCall` (`dispatch.go:321-335`).
  `--eager-tools` can still name them; `--no-defer-tools` sends them eagerly. Any deferred
  tool turns on the `tool_search` and `tool_call` pair (`tools.go:206-217`, `:269-286`), so
  a session with no MCP tools now carries that pair; a session with an MCP server already
  does. Research has the measured cost.

Delegation never widens what a session can do: D2 shares the root's approval state with
every child and caps each child at the root's current scope. A read-only parent gets read-only peers, which is the
cheapest useful delegation (parallel reading).

Options for the maintainer:

- **A (recommended):** observe, edit and exec.
- **B:** edit and exec. Default sessions cannot delegate.
- **C:** exec only, next to `run_shell` and `axis_run_task`.

### D2. The child inherits the parent's scope and registry setup

- Extract `New`'s registry setup (`agent.go:177-185`) into one helper,
  `configureRegistry(r, registryConfig{Scope, EagerTools, DisableDeferral, ResultCap, MCP})`,
  and call it from `New` and `buildChildAgent`. The child's config comes from the parent's
  registry at spawn time.
- `buildChildAgent` sets `child.autonomy` to the parent's mode. A sync child holds no
  confirm of its own: it prompts through the root's current `a.confirm`, which
  `SetAutonomy` rewraps on every mode change (`agent.go:206`, `:326`), so a mode change
  reaches running children's prompts too. An async child's confirm is below.
- The child's registry shares the root's checkpointer, which today each registry makes
  for itself (`tools.go:76`). Every executor reads `r.checkpoints` at call time
  (`tools.go:1047`, `:1127`, `:1203`; `tools_p1.go:84`, `:130-139`), so setting the field in
  `configureRegistry` is enough. An edit child's write is then the session's most recent
  mutation for the root's `undo_last` and `review_changes`. The todo store stays per
  agent: `todo init` replaces the whole list (`tools_todo.go:123`, `:140-146`), so a shared
  store would let a child erase its parent's plan.
- **Approval state is shared, not copied.** Today the child gets a copy of
  `sessionApproved` and `blockAll` (`confirm.go:137-148`, `subagent.go:206`, `:239-240`), and
  an answer given to a child's prompt is written on the child alone
  (`dispatch.go:259-271`). Each prompt also locks only its own agent's confirm gate. With
  edit and exec children reachable, a "never" answered in a child would leave the parent
  running, and two agents could prompt on one terminal at once. So `Agent` gains a
  `root *Agent` (nil on the root). Every read or write of `sessionApproved` and `blockAll`,
  and every prompt through `askConfirm` (`agent.go:279-289`), goes to the root's fields,
  the root's `dispatchMu` and the root's confirm gate. "Always" in a child approves the
  tool for the session; "never" in a child blocks the session.
- **The root's current scope caps every child.** A child's registry starts at the
  parent's scope. At each dispatch, a child also refuses a tool the root's registry would
  not show at the root's current scope, with the existing "not available in this mode"
  error (`dispatch.go:124-126`). Lowering `/autonomy` on the root narrows running children
  on their next call. A child's effective scope is the lower of its spawn scope and the
  root's current scope, so raising the mode again restores a lowered child up to its spawn
  scope and never past it. The child
  reads the root's scope under the root's `dispatchMu`, the lock `SetAutonomy` writes it
  under (`agent.go:310-319`). At the start of each round a child sets its registry to that effective
  scope and refreshes its visible-tools message (`agent.go:345`), so it advertises only
  what it can call. The refusal text
  for a call caught between rounds says the operator lowered the session to that scope.
- **A child's output goes to events, not to the surface's writer.** Today the child gets
  `output: a.output` and no observer or session log (`subagent.go:219-245`), so its tool
  badges and streamed text land on the parent's writer (`observer.go:131-135`), which in
  `-p` is stdout (`cmd/axis/agent.go:264`). The child's stream writer becomes `io.Discard`,
  since its answer already comes back as the tool result or task output. Its events go to
  the root's session log, and a sync child's events also go to the parent's observer for
  the current turn (`agent.go:362-376` swaps sinks per turn), wrapped to tag each event
  with depth and task id. An async child has no display observer, because the turn that
  spawned it may be over.
- **Async children never prompt.** A sync child runs inside the parent's turn, so its
  prompts reach the operator the way the parent's do. An async child may still be running
  after the turn ends, when the plain REPL is reading stdin (`cmd/axis/agent_repl.go:134-157`)
  and a prompt would race it. So an async child's confirm is
  `autonomyConfirm(<root's current mode>, refuse)` (`autonomy.go:70-104`): the mode's own
  auto-approvals and session approvals still run, and any call that would reach the
  surface confirm is refused, the way `pipedConfirm` sits under the autonomy wrapper
  (`cmd/axis/agent_oneshot.go:85-92`). The refusal tells the child to say in its answer
  what it needs approved. Under default autonomy an async child cannot write; under edit
  or full it writes and runs low-risk commands unattended, within what the operator
  granted with that mode.
- `consult_model` stays hidden in the child (no resolver, `tools.go:181-186`); a child
  seat is the seat-placement direction.
- At `maxSubAgentDepth` the child's registry withholds `spawn_subagent`: a small
  `withheld map[string]bool` on `ToolRegistry`, checked first in `callableNonBridge`
  (`tools.go:177`). Below the cap the child sees it and may spawn one level down. This
  matches the cap the code already enforces (`subagent.go:91`) and makes the prompt agree.
- **At most 6 sub-agents run at once per session**, counted on the root across all
  depths, sync and async. A spawn at the cap is refused with "sub-agent limit reached (6
  running); wait for a running one to end, using check_task, before spawning again; do
  not retry this spawn now". No tool cancels a running sub-agent; that stays out of
  scope. 6 is `maxParallelTools` (`agent.go:19`), the most calls one round runs at
  once, so one round can still fan out fully. Claude Code caps running subagents at 20
  with the same no-retry error (Research).

### D3. The child's prompt is built from its visible tools

- `buildSubAgentSystemPrompt` drops the hard-coded tool list (`subagent.go:267-273`) and
  the two "do not spawn" lines (`:103`, `:282`). The child gets `visibleToolsMessage(childTools)`
  (`agent.go:329-343`) as its own system message, as `New` does for the parent.
- The brief text (`subagent.go:100-107`) names the target node and drops the tool names:
  "Your target node is %q" when one is set, nothing when none is.
- When `spawn_subagent` is visible to the child, its prompt adds one line: it may delegate
  one level further with a full brief.
- `VisibleToolPrompt`'s scope line tells the model to ask the operator to elevate
  (`scope.go:176-186`). A child answers to its parent, so for a child that line says to
  report what mode the task needs in its answer instead.
- `spawn_subagent`'s own description and `target_node` text (`subagent.go:40-55`) are
  rewritten to say the child inherits the parent's scope and approvals, and to give D4's
  resolution order.

### D4. An empty `target_node` is resolved, and ranked only when the task has a profile

The child's model runs in this process (Non-goals), and under D5 its node-addressed tools
are bounded reads (`head -c 32000`, `ls`, `grep`, `tools_cluster.go:94-200`). Ranking by
free capacity optimizes nothing for a read. So, in `dispatchSubagent`, before building the
child, the target comes from the first rule that applies:

1. **Set by parent.** `strings.TrimSpace(sa.TargetNode)` is not empty and names a node in
   `view.Config.Nodes`: use it. Reason `set by parent`. A name not in a loaded
   `nodes.yaml` refuses the spawn with the error `runRemote` gives, which lists the
   available nodes (`tools_cluster.go:35-39`). With no cluster config loaded (`view.Config`
   nil, as when the runtime failed to load, `agent.go:173-176`), the name is used as given
   with reason `set by parent, cluster config not loaded`; the child's remote tools then
   report the missing config themselves.
2. **No node-scoped tools.** The child's registry shows none of `remote_read_file`,
   `remote_grep` or `remote_list` (only possible under D5 option B or C): no target.
   Reason `none (no node-scoped tools at <scope>)`.
3. **Named in the brief.** Split `objective` and `prompt` into tokens on whitespace and on punctuation other than `-`, `_` and `.`, and trim a trailing
   `.` from each. Exactly one node name from `view.Config.Nodes` equals a token: use it.
   Reason `named in brief`. A brief saying "worker-1" matches `worker-1` and not `worker`.
   `sources` and `boundary` are left out because `boundary` lists what the child must not
   do (`subagent.go:54`), so a node named there is the one to avoid. Matching is
   case-sensitive, as `findNode` is (`tools_cluster.go:15-22`). A negated mention in
   `objective` or `prompt` ("leave worker-1 alone") still matches; a parent that means to
   avoid a node says so in `boundary` or sets `target_node`, and the receipt shows the
   choice either way. Config names are the names
   `runRemote` resolves, so a rule 1 or rule 3 target is always reachable by the child.
4. **Ranked.** `reqs := placement.InferRequirements(objective + "\n" + prompt)` matched a
   workload profile (`reqs.Workload.Class != models.ClassUnknown`, see
   `internal/workload/match.go:41-45`): reload the view under `dispatchMu` the way cluster
   tools do (`dispatch.go:133-148`), keep only snapshot nodes that also appear in
   `view.Config.Nodes`, and call `placement.ExplainPlacement(reqs, nodes, view.State)`, the
   call `axis_place` makes (`tools.go:499-513`). Decision OK: use `Decision.Node`, reason
   `placement suggests for <class>: <decisive criterion>, <metric name> = <value><unit>`,
   built from the decision's ranking. The child only reads on that node (D5), so the word
   is suggests: the ranker picks where the task's work would fit, and the receipt does
   not claim the child runs it there. Not OK: no target, reason `placement: no eligible
   node (N excluded)`. A failed or missing reload (`dispatch.go:142`) falls through to
   rule 5 with reason `none (cluster view unavailable)`.
5. **Otherwise** no target. Reason `none (not set by parent)`. The child picks with its
   remote tools.

The target goes into the child's prompt (D3), the receipt (D8) and `backgroundTask.node`
(`subagent.go:118`).

### D5. Read-only remote tools leave the never set (**decision 2: option A, maintainer, 2026-11-28**)

Recommended: `remote_read_file`, `remote_grep` and `remote_list` move from `neverTool` to
a `remoteReadTool(name)` set visible at observe, edit and exec, hidden at cluster.

Without them a child "on" a node can read nothing there, so D4 has nothing to point at
and `target_node` stays a word in the prompt. The tools already exist, are already
marked read-only and auto-approved (`confirm.go:104-111`), and run one bounded command over
the node's configured SSH dial (`internal/agent/tools_cluster.go:27-51`, `:94-121`).

What changes for observe: today its file reads stop at the working directory (`read_file`
and `list_directory` descriptions, `tools.go:583`, `:629`). With this pick, an observe
session reads any path on any node in `nodes.yaml` that the SSH user can read. That is the
cluster-wide version of what observe already does locally, and it is the reason this is a
separate decision from D1.

Options for the maintainer:

- **A (recommended):** visible at observe, edit and exec.
- **B:** visible at edit and exec only. Observe children stay local; D4 rule 2 applies to
  them.
- **C:** stay in the never set. D4 always stops at rule 2; this spec still fixes D2, D3,
  D6 and D7.

### D6. Async children are cancelled with the session

- `Agent` gains `sessionCtx` and `sessionCancel`, made in `New` from `context.Background()`.
  A child's `sessionCtx` derives from its parent's, so cancelling a parent cancels its
  whole subtree.
- Async children run under `context.WithCancel(a.sessionCtx)` instead of
  `context.Background()` (`subagent.go:115`).
- New terminal state `bgTaskCancelled`, legal from `bgTaskRunning` only
  (`async_tasks.go:40-50`), labelled `cancelled` by both `checkLabel` and `listLabel`. When
  the async goroutine's `child.Run` returns and the task's context is done, the task
  finishes as cancelled, whatever `err` and `LastRunOutcome` say. A cancel that lands during
  the last turn's tool calls turns into tool errors and a nil return at the ceiling
  (`agent.go:462-518`), so the error alone cannot be trusted here.
- `func (a *Agent) Close() CloseReport` sets the root's `closed` flag, cancels
  `sessionCtx` and waits up to 3 seconds (the console's own shutdown wait,
  `cmd/axis/agent_console_startup.go:38`) for task goroutines to reach a terminal state.
  `CloseReport` holds two id lists. `Cancelled` lists tasks that reached `cancelled`
  inside the wait, so their goroutine has returned. `Stopping` lists tasks whose goroutine
  had not returned when the wait ended: the cancel was requested but not confirmed,
  because a backend call or tool that ignores its context can outlive any wait. Close
  never reports a `Stopping` task as cancelled. A second call returns an empty report.
- **After Close, a late task changes nothing in the session.** Every child checks its
  `sessionCtx` before each model request and each tool call, so a child that gets control
  back stops at the next boundary. A task goroutine that returns after Close sees
  `closed`, records no state change or result, and writes no event to the session log; a
  child's stream writer is already `io.Discard` (D2). A tool call already running when
  Close fires can still finish its own side effect, such as a file write approved before
  Close; Close cannot stop it, and the `Stopping` line says the task may still be running.
- Close runs where each session really ends, and its report is printed on the surface's
  error writer, one line per non-empty list:
  `cancelled N open sub-agent task(s): bg-2, bg-3` and
  `M sub-agent task(s) still stopping after 3s, may still be running, results dropped: bg-4`.
  - `cmd/axis/agent.go`, after `agent.New` at `:267`: a deferred closure that calls
    `a.Close()` and prints its result. This covers `-p`, the positional one-shot, the plain
    REPL and `runAgentConsole` (reached through `runAgentInteractive`,
    `cmd/axis/agent_repl.go:66`), which all return inside the same `RunE`.
  - The startup console builds its agent inside a startup job that returns once the agent
    is bound (`cmd/axis/agent_console_startup_job.go:152-190`, `:329-379`), so Close must not
    go there. It goes in `runAgentConsoleStartup` after `prog.Run()` returns and the job is
    drained, next to `l.boundAgent()` (`cmd/axis/agent_console_startup.go:301-312`). The
    terminal is restored by then, so the line goes to `errW`. This covers `axis agent` and
    `axis tui` (`cmd/axis/tui.go:61`).
- Sync children already run on the parent's turn context (`subagent.go:154`) and are
  unchanged.
- **A child finishes its own async tasks before it returns.** When a child's `Run`
  returns, `dispatchSubagent` waits for every task still running in that child's store.
  The wait selects on the dispatch context too: the turn ctx for a sync child, the task
  ctx for an async one. When that context ends first (Ctrl-C, the `-p` timeout from
  `cmd/axis/agent_oneshot.go:139`, or a session cancel), the child's `sessionCtx` is
  cancelled and its open tasks end as `cancelled`. Then each task's result is appended to
  the child's answer under a line `nested <parent-id>/<id>: <outcome>`, where
  `<parent-id>` is the child's own task id, or `sync` for a sync child. Folded results
  carry no `check_task` hint, because each store numbers from `bg-1`
  (`async_tasks.go:138-143`) and the parent's `check_task` reads only its own store. This is the behavior the Claude Code docs give a subagent with background
  subagents (Research). No grandchild is left in a store nobody reads, and the root store
  holds every task the root's model can query.
- In `-p`, Close runs right after the answer, so an async child the model did not wait on
  is cancelled and reported by the exit line. The async branch of `spawn_subagent`'s
  description says so.
- At cluster scope `check_task` and `list_background_tasks` are hidden with the rest of
  D1's tools, and running children are capped to cluster. Raising the mode again shows
  the tasks; nothing is lost.

The word `cancelled` matches MCP task states; A2A spells it `canceled`. DEA-31's spec
defines `RunStateCancelled = "cancelled"`; D6 uses that word.

### D7. The child's result uses `LastRunOutcome`

Depends on the approved `-p` exit-code change (`RunOutcome`, `RunStop`, `LastRunOutcome`),
which is not on `main` yet, and DEA-31's spec
`2026-11-28-agent-run-outcome-states-design.md` builds on it. D7 and the receipt's
`outcome` field land after that change merges; see Review sequencing. After `child.Run`,
`dispatchSubagent` reads `child.LastRunOutcome()`:

| Stop | Child text | Sync result | Async task |
|---|---|---|---|
| answered | non-empty | receipt line, then the answer | succeeded, receipt line plus answer |
| answered | empty | existing no-answer error (`subagent.go:169-171`) | failed, same line |
| turn_ceiling | any | error: `sub-agent stopped at its N-turn ceiling without a final answer; the parent must not invent one` | failed, same line |
| failed | any | existing `sub-agent failed: %w` (`subagent.go:155`) | failed, error line |

Every error row starts with the receipt line (D8), then a newline, then the error text, so
the parent sees where the child ran even when it failed. The no-invented-answer rule stays.

### D8. The spawn receipt

Every spawn returns, and verbose mode prints, one receipt line before anything else.
Illustrative values:

```text
sub-agent bg-3 async | node: builder (named in brief) | scope: observe | depth: 1 | max_turns: 6
sub-agent sync | node: worker-1 (placement suggests for go-build: allocatable_capacity, allocatable_ram = 7168MB) | scope: edit | depth: 1 | max_turns: 6 | outcome: answered 4/6
```

An async receipt line is followed by the existing return text unchanged, "Sub-agent task
bg-N started in background on ... Use `check_task` ..." (`subagent.go:145-146`), which
`subagent_test.go:87` and `:131` pin. Sync
receipts carry `outcome: <stop> <turns>/<max_turns>` from D7. The line is text in a tool
result, read by the parent model; no JSON shape changes.

### Docs in the implementing PR

- `docs/current-state.md`: one bullet for the reachable peer loop.
- `docs/release-notes.md`: the scope change, the receipt, and `cancelled`.
- `docs/decisions/agent-model-consult.md:13` says `spawn_subagent` is hidden by
  `neverTool`; that sentence changes to the chosen D1 scopes.
- `docs/superpowers/specs/2026-11-16-delegation-brief-design.md` is left as written; this
  spec supersedes its `:32` statement.

## Alternatives

- **Children always at observe (today's default).** An edit or exec parent cannot
  delegate edits, and the prompt keeps promising tools the child lacks. Rejected.
- **Children always at the widest scope.** Widens what a session can do through a
  child. Rejected.
- **A `scope` argument that narrows the child.** Useful (an exec parent spawning
  read-only readers) and compatible with D2 later, but it adds schema the first version
  does not need. Deferred.
- **A separate delegation flag or config key.** Scope is already the grant; a second
  switch is a second place to look. Rejected.
- **Let the child call `axis_place` itself.** Leaves placement to the model and puts
  nothing in the receipt. Rejected.
- **Cancel async children at turn end.** Kills the background work async exists for.
  Rejected.
- **Keep the brief text, add the real tool list next to it.** Two lists drift; #456 removed
  the hand-written list from the root prompt for the same reason. Rejected.

## Research

Repository prior art:

- #456 (`6ed053d`): scope mask v1, the never set, live-defs system prompt for the root.
- #457 (`acfef93`): the exec grant revealed `run_shell` and `axis_run_task` as a change in
  `toolVisible`, the pattern D1 and D5 follow.
- [`2026-11-16-delegation-brief-design.md`](2026-11-16-delegation-brief-design.md): the brief
  and the empty-answer rule this spec keeps.
- [`2026-11-16-agent-deferred-tool-loading-design.md`](2026-11-16-agent-deferred-tool-loading-design.md):
  the eager and deferral settings D2 copies into the child.

External prior art:

- Claude Code sub-agents ([docs](https://code.claude.com/docs/en/sub-agents), read
  2026-11-28): sub-agents inherit the built-in and MCP tools of the main conversation and,
  when unset, its permission mode. A sub-agent can spawn its own, up to three layers below
  the main conversation, and at the depth limit the `Agent` tool is withheld. D2 follows
  the same shape: inherit by default, withhold the spawn tool at the cap. In an
  interactive session a subagent that launches background subagents waits for their
  results before it finishes; under `claude -p` and the Agent SDK a background nested
  subagent reports to the main conversation instead. D6 takes the interactive behavior,
  since axis has no main-conversation channel a grandchild could report to. The same
  page caps running subagents at 20 per session: a further spawn fails with "Concurrent
  subagent limit reached" and tells the model not to retry. D2's cap copies the shape.
- A2A `TaskState` ([`specification/a2a.proto`](https://raw.githubusercontent.com/a2aproject/A2A/main/specification/a2a.proto))
  and MCP tasks ([2025-11-25 tasks](https://raw.githubusercontent.com/modelcontextprotocol/modelcontextprotocol/main/docs/specification/2025-11-25/basic/utilities/tasks.mdx))
  both have a cancelled terminal state, spelled `canceled` in A2A and `cancelled` in MCP;
  D6 uses the MCP spelling, as DEA-31 does.

Optimization: request size. Measured at `ab2adcc` with a throwaway test marshalling
`Defs()` for a session with no MCP tools: observe sends 13 tools in 6565 bytes. Made
eager, the six tools this spec reveals add 3752 bytes (57%), `spawn_subagent` alone 1553.
Deferred (D1), eager `Defs()` is 15 tools in 8086 bytes (+1521, 23%): the six tools leave
and the `tool_search`/`tool_call` pair arrives. The deferred catalog adds one summary line
of at most 80 runes per tool to the system prompt (`deferred.go:24-28`). Deferred is the
smaller of the two, and with any MCP server configured the pair is already sent, so the
delta there is only the catalog lines. Shortening the descriptions instead was not
measured; it would still send all six schemas on every round.
Placement adds at most one view reload and one `ExplainPlacement` call per spawn, and only
for briefs that match a workload profile.

Optimization: concurrent model requests. Without a cap, one round of 6 parallel sync
spawns, each spawning 6 more, runs 42 sub-agents at once with no prompt at observe, since
`spawn_subagent` dispatches before the confirm step (`dispatch.go:167-169`). D2's cap
bounds concurrency only: at most 6 sub-agents run at once, so at most 6 child requests are
in flight. It does not bound the total. One depth-1 child can spend its 6 turns spawning
one 6-turn grandchild per turn, one after another: 42 child requests with 2 sub-agents
running at once. The total per root turn is bounded only by `maxSubAgentDepth`,
`maxParallelTools` and `max_turns`. A cumulative budget is a non-goal.

## Validation

- Scope pins to update under the chosen D1 and D5 options: `scope_test.go:59`, `:80`,
  `:98`, `:141`, `:178-180`, `:337`; `deferred_tools_test.go:165-183`, `:352-365`, `:502-508`
  (pick another never-set tool, such as `fleet_exec`, for the bridge refusal case);
  `agent_test.go:136-140`; `autonomy_test.go:38`; `session_approval_test.go:249`. New
  pins: the tools are visible at exactly the chosen scopes, hidden at cluster, and absent
  from eager `Defs()` by default; `run_on_node`, `fleet_exec`, `remote_write_file`,
  `remote_tail_logs` and `run_background` stay hidden at every scope and denied on the
  direct path.
- Budget and deferral tests that pin a no-MCP observe request: `agent_backend_turn_test.go:122`
  and `request_test.go:347` (compaction timing, fixtures resized to the 8086-byte baseline),
  `deferred_tools_test.go:86` (no bridge with zero deferred tools; becomes a scope that
  defers nothing) and `deferred_bounds_test.go:30` (catalog cap, re-measured).
  `sessionlog_compaction_test.go:44` stays green unchanged. The second spec review's probe
  failed the first four with D1 applied.
- A child spawned from an edit parent can call `write_file`; one from an observe parent
  cannot. After the root drops to observe, a running edit child's next `write_file` is
  refused.
- "Never" answered in a child blocks the root; "always" answered in a child approves the
  tool on the root. Parent and child prompts never overlap (one in flight).
- The child's system prompt lists exactly its registry's eager and deferred names and none
  of the old hard-coded names that are hidden.
- A depth-1 child sees `spawn_subagent`; a depth-2 child does not.
- D4 table test, one case per rule, including a brief that names a lower-RAM node (rule 3
  must win over capacity), a brief naming two nodes (falls through), a snapshot node
  missing from `nodes.yaml` (never chosen), and rule 2 under D5 option B.
- `Close()` while an async child runs ends its task as `cancelled`, returns its id under
  `Cancelled` and cancels a grandchild; a cancel during the child's last turn still ends as `cancelled`; a
  second `Close()` is a no-op. A console test spawns an async child after startup returns
  and sees it still running.
- A depth-1 child spawns an async grandchild and answers: the child's result holds the
  grandchild's receipt and result, and the child's store has no running task.
- cmd/axis tests assert the `cancelled N open sub-agent task(s)` line on the error writer
  for the one-shot path and for the startup console, the `still stopping` line when a task
  outlives the wait, and no line when nothing was open.
- A child whose stub backend ignores its context and blocks past the 3-second wait:
  Close returns after the wait with the task under `Stopping` and not under `Cancelled`.
  When the stub later returns, the task records no state change or result, the session log
  gets no event, the child starts no further model request or tool call, and nothing is
  written to Output.
- A `go test -race` case changes `/autonomy` on the root while an async child dispatches.
- A `-p` run with a sync child writes nothing of the child's to stdout, and the child's
  badges reach the observer on stderr tagged with depth. An async child writes nothing to
  Output after the turn returns. Child tool calls appear in the root's session log.
- After `Close()` returns, every task is either terminal and listed under `Cancelled`, or
  listed under `Stopping`; no task records a state change or writes a session-log event
  after that, and nothing further is written to Output.
- D4 table adds a config with both `worker` and `worker-1` and a brief naming `worker-1`;
  a node named only in `boundary` (falls through); a rule 1 name not in `nodes.yaml`
  (spawn refused); a nil or failing reload (rule 5).
- Cancelling the turn ctx while a sync child waits on a running grandchild returns at
  once, with the grandchild `cancelled` in the folded result. The folded text is pinned:
  `nested sync/bg-1: ...` with no `check_task` hint.
- An edit child writes a file, then the root's `undo_last` restores that write. A
  child's `todo init` leaves the root's list unchanged.
- Under default autonomy the child's scope is observe, so a sync or async child's
  `write_file` is refused as not available in this mode, before any confirm and with no
  prompt. Prompting is tested with a stub external MCP tool, visible at observe and not on
  `auto_approve_tools`: a sync child's call prompts the operator once and an async child's
  is refused with no prompt. Under edit autonomy a sync and an async child's `write_file`
  both run with no prompt.
  A plain-REPL test confirms no prompt is read while the REPL waits for input.
- With 6 sub-agents running across depths, a seventh spawn is refused with the no-retry
  text; after one finishes, a spawn succeeds.
- After the root drops from edit to observe, a running child's next round shows no edit
  tools in its `Defs()` and system message. A table over mode transitions (full to edit,
  edit to default, default to edit, edit to cluster to edit) checks the child's effective
  scope and that a sync child prompts through the root's current confirm.
- D4 table: a target name with no cluster config loaded keeps the name with reason
  `set by parent, cluster config not loaded`.
- A child that hits its ceiling returns the turn-ceiling error, sync and async, with the
  receipt line first; answered and failed paths keep their current text after the receipt.
- Existing subagent tests (`internal/agent/subagent_test.go`) stay green: they set
  `target_node` with `NewToolContext(&RuntimeView{}, nil)` (`:51`, `:77`, `:100`), the
  no-config path of rule 1; run the hermetic
  agent tests, `go vet`, `hack/verify-specs.go` and the full preflight.

## Review sequencing

Implementation order: D1 to D6 and D8 without the `outcome` field can land on `main` as it
is. D7 and the `outcome` field land after the `-p` exit-code change merges, in the same PR
if that change is merged first, or as a follow-up otherwise.

The reviews below ran with both decisions open and the recommended options written in.
The maintainer then picked option A for both, the text the reviews covered, so no new
spec review is needed before code (AGENTS.md §8, step 3).

## Review


Kind: spec
Reviewer: fresh-context subagent (Claude Code `general-purpose`, given only `docs/superpowers/review-prompt.md` and its five inputs)
Model: Anthropic Claude (fresh top-tier session, same family as the author)
Base: ab2adcc5429ba1025492eebc8dcfe7f6bc001ea4
Head: e6efa8d
Date: 2026-11-28
Verdict: revise

1. Major: D4 ranked every empty-target spawn by free capacity, so a brief that names a node could be sent elsewhere, and a brief that matches no workload profile was still ranked. Fixed in f5a7aed: D4 resolves set by parent, then no node-scoped tools, then one node named in the brief, then the ranker only when `InferRequirements` matched a profile, else no target.

2. Major: D2 copied approval state into the child, so "never" answered in a child blocked only the child and parent and child prompts could overlap (`dispatch.go:255-271`). Fixed in f5a7aed: children read and write the root's `sessionApproved`, `blockAll`, `dispatchMu` and confirm gate, and each child dispatch is capped by the root's current scope.

3. Major: D6 put `defer a.Close()` at the console `buildAgent` site, which returns at startup and would cancel children at once. Fixed in f5a7aed: Close runs after `prog.Run()` in `runAgentConsoleStartup` and in a deferred closure after `agent.New` in `cmd/axis/agent.go`, with a console test that spawns after startup.

4. Major: making six tools eager at observe breaks scope, deferral, approval and budget tests. Fixed in f5a7aed: D1 loads them deferred by default through a `deferredBuiltin` set in `eager`, and Validation lists every pin to update.

5. Minor: Research said no optimization applies though eager loading grows every request. Fixed in f5a7aed: Research states the measured 6565-byte observe baseline and the 3752-byte delta, removed by deferred loading.

6. Minor: the receipt reason restated a fixed phrase instead of the decision, and the "no node-scoped tools" branch was unreachable under D5 option A. Fixed in f5a7aed: the reason is built from class, decisive criterion and metric value, and rule 2 is marked as applying under D5 option B or C.

7. Minor: placement used the cached view while cluster tools reload under `dispatchMu`. Fixed in f5a7aed: D4 rule 4 reloads the view under `dispatchMu` first.

8. Minor: a cancel during the child's last turn returns nil at the ceiling and would be reported as turn_ceiling. Fixed in f5a7aed: a task whose context is done when `Run` returns finishes as cancelled whatever `err` and the outcome say.

Questions:

- Can the ranker pick a snapshot node that `nodes.yaml` does not list, so the child's remote tools cannot reach it? Verified: `runRemote` resolves only `view.Config.Nodes` (`tools_cluster.go:35-39`); f5a7aed restricts ranking to those nodes.
- Should the error paths carry the receipt? Became D7: every error row starts with the receipt line, pinned in Validation.
- Is `errors.Is(err, context.Canceled)` enough to classify a cancel? Verified: no, the ceiling path returns nil (`agent.go:462-518`); finding 8's fix replaces that check.

Kind: spec
Reviewer: fresh-context subagent (Claude Code `general-purpose`, given only `docs/superpowers/review-prompt.md` and its five inputs)
Model: Anthropic Claude (fresh top-tier session, same family as the author)
Base: ab2adcc5429ba1025492eebc8dcfe7f6bc001ea4
Head: c9a33e3
Date: 2026-11-28
Verdict: revise

1. Major: deferring the six tools turns on the `tool_search`/`tool_call` pair (`tools.go:206-217`, `:269-286`), so eager `Defs()` grows to 8086 bytes and four budget and deferral tests fail. Fixed in 7a39de3: Research states the measured 8086-byte figure and the comparison with eager loading, and Validation lists all four tests as pins to update.

2. Major: an async grandchild lands in its parent child's private store, which nothing reads once that child returns. Fixed in 7a39de3: D6 has a child wait for its own async tasks and fold their receipts and results into its answer, the interactive Claude Code behavior, with a Validation case.

3. Major: D7 depends on `RunOutcome`/`LastRunOutcome`, which exist on no ref. Fixed in 7a39de3: D7 names the unmerged `-p` exit-code change and DEA-31's spec, Review sequencing lands D7 and the receipt's `outcome` field after that change, and the unverified `pending_approvals` claim is removed.

4. Minor: rule 4's receipt read as a build placement the read-only child cannot run. Fixed in 7a39de3: the reason says `placement suggests for <class>` and D4 states the child only reads there.

5. Minor: whole-word matching counted `worker` inside `worker-1`. Fixed in 7a39de3: rule 3 tokenizes on whitespace and punctuation other than `-`, `_` and `.`, with a Validation case.

6. Minor: Problem 5 said children outlive the session, and the printed cancel line was untested. Fixed in 7a39de3: Problem 5 says dropped silently at exit, and Validation adds cmd/axis tests for the line on both surfaces.

7. Minor: `docs/decisions/agent-model-consult.md:13` goes stale under D1. Fixed in 7a39de3: added to the docs list.

Questions:

- Is the child's read of the root scope race-free against `SetAutonomy`? Became D2: the read is under the root's `dispatchMu`, with a `-race` case in Validation.
- Should a child's scope line tell its parent rather than the operator to elevate? Became D3: a child reports the mode it needs in its answer.
- Should `spawn_subagent`'s description and `target_node` text change? Became D3: rewritten in the same PR.

Kind: spec
Reviewer: fresh-context subagent (Claude Code `general-purpose`, given only `docs/superpowers/review-prompt.md` and its five inputs)
Model: Anthropic Claude (fresh top-tier session, same family as the author)
Base: ab2adcc5429ba1025492eebc8dcfe7f6bc001ea4
Head: 67cba12
Date: 2026-11-28
Verdict: revise

1. Major: D4 rule 3 read `boundary`, so a node the brief says to avoid became the target. Fixed in 386aca5: rule 3 tokenizes `objective` and `prompt` only, with a table case for a node named only in `boundary`.

2. Major: a sync child's wait on its grandchildren ignored the turn's cancel and the `-p` timeout. Fixed in 386aca5: the wait selects on the dispatch context and ends open grandchildren as `cancelled`, with a Validation case.

3. Major: folded grandchild receipts carried `check_task` ids from the child's own `bg-1` namespace. Fixed in 386aca5: folded results use `nested <parent-id>/<id>` with no `check_task` hint, pinned in Validation.

4. Major: an edit child's writes were invisible to the root's `undo_last` and `review_changes`, because every registry makes its own checkpointer. Fixed in 386aca5: D2 shares the root's checkpointer and todo store, with an undo Validation case.

5. Major: an async edit or exec child could prompt after the turn ended while the plain REPL reads stdin. Fixed in 386aca5: D2 refuses any async child call that needs a prompt, as `pipedConfirm` does; sync children prompt as today. Plain-REPL test added.

6. Minor: a child kept advertising tools the lowered root scope no longer allows. Fixed in 386aca5: a child lowers its registry scope and refreshes its visible-tools message at each round, and the refusal text names the lowered mode.

7. Minor: Research said A2A uses `cancelled`; A2A spells it `canceled`. Fixed in 386aca5: D6 and Research say the word matches MCP and DEA-31's `RunStateCancelled`.

8. Minor: a rule 1 target was never checked against `nodes.yaml`. Fixed in 386aca5: an unknown name refuses the spawn with the available-nodes error `runRemote` gives.

Questions:

- Does the async return keep "started in background"? Became D8: the existing return text follows the receipt line unchanged, so `subagent_test.go:87`, `:131` stay green.
- What happens when the view reload fails or `Reload` is nil? Became D4 rule 4: falls through to rule 5, `none (cluster view unavailable)`, with a table case.
- Does a running child's confirm follow the root's mode changes? Became D2: the child prompts through the root's current confirm, with a mode-transition table test.
- Is rule 3 case-sensitive? Became D4: yes, matching `findNode` (`tools_cluster.go:15-22`).
- What does `-p` do with async children at exit? Became D6: they are cancelled and reported, and the spawn description says so.
- Are tasks visible at cluster scope? Became D6: hidden with D1's tools and shown again when the mode is raised.

Kind: spec
Reviewer: fresh-context subagent (Claude Code `general-purpose`, given only `docs/superpowers/review-prompt.md` and its five inputs)
Model: Anthropic Claude (fresh top-tier session, same family as the author)
Base: ab2adcc5429ba1025492eebc8dcfe7f6bc001ea4
Head: d18174c
Date: 2026-11-28
Verdict: revise

1. Major: the async "never prompt" rule could not hold against `autonomyConfirm`, which approves edits under edit and full modes before any base confirm runs. Fixed in 863e1a7: an async child's confirm is `autonomyConfirm(<root mode>, refuse)`, D2 states it acts unattended within the mode's auto-approvals, and Validation tests default and edit modes separately.

2. Major: rule 1 refused every target with no cluster config, breaking three existing subagent tests. Fixed in 863e1a7: with no config loaded the name is used as given with reason `set by parent, cluster config not loaded`, with a table case.

3. Major: sharing the root's todo store let a child's `todo init` erase the root's plan. Fixed in 863e1a7: only the checkpointer is shared; each agent keeps its own todo store, with a Validation case.

4. Minor: nothing bounded how many sub-agents one session runs. Fixed in 863e1a7: D2 caps running sub-agents at 6 per session with a no-retry refusal, and Research cites Claude Code's limit and states the worst-case request count.

Questions:

- Does a lowered child climb back when the root's mode is raised? Became D2: its effective scope is the lower of its spawn scope and the root's current scope, with an edit to cluster to edit case.
- Does `LastRunOutcome` carry a turn count for the receipt? Verified: the `-p` exit-code change's output document carries `turns` and `max_turns` from it (`2026-11-28-exit-code-fix.patch`, outside the repo).

Kind: spec
Reviewer: fresh-context subagent (Claude Code `general-purpose`, given only `docs/superpowers/review-prompt.md` and its five inputs)
Model: Anthropic Claude (fresh top-tier session, same family as the author)
Base: ab2adcc5429ba1025492eebc8dcfe7f6bc001ea4
Head: 63b1cd8
Date: 2026-11-28
Verdict: approve-with-changes

1. Major: a child's badges and streamed text go to the parent's writer, which is stdout in `-p`, and never reach the observer or session log. Fixed in addeaff: D2 sets the child's writer to `io.Discard`, sends its events to the root's session log and, for a sync child, to the current turn's observer tagged with depth and task id, with Validation cases for `-p` stdout and async output.

Questions:

- Does Close wait for task goroutines before the exit line? Became D6: Close waits up to the console's 3-second shutdown wait, with a Validation case.
- Should rule 3 match a negated mention in `objective` or `prompt`? Became D4: it does; avoiding a node goes in `boundary` or `target_node`, and the receipt shows the choice.
- Should the cap refusal say "finish" when no tool cancels a sub-agent? Became D2: it says wait for a running one to end, and cancelling stays out of scope.
