# Agent peer loop: reachable, scoped, placed sub-agents

Date: 2026-11-28. Linear: DEA-32. Base: `ab2adcc` (v0.29.0).

This spec amends the agent scope charter. Two decisions in it are marked
**pending maintainer pick**; the text describes the recommended option for each.

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
5. **Async children outlive their session.** They run under
   `context.WithCancel(context.Background())` (`subagent.go:115`). `backgroundTaskStore`
   has `nextID`, `add`, `get` and `snapshot` and no way to cancel everything
   ([`internal/agent/async_tasks.go:128-168`](../../../internal/agent/async_tasks.go)). `Agent`
   has no close hook; both surfaces build it with `agent.New` and drop it
   (`cmd/axis/agent.go:267`, `cmd/axis/agent_console_startup_job.go:343`). The task state
   machine has no cancelled state (`async_tasks.go:38-50`).
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
- Editing `docs/doctrine.md`, `AGENTS.md` or `README.md`.

## Design

### D1. Charter: which scopes show delegation (**pending maintainer pick, decision 1**)

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
  `--eager-tools` can still name them; `--no-defer-tools` sends them eagerly. This keeps
  the observe request size where it is (see Research).

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
- `buildChildAgent` sets `child.autonomy` to the parent's mode. It keeps the parent's
  confirm as is: that confirm is already wrapped for the parent's mode (`agent.go:206`,
  `:326`), so the child must not wrap it again.
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
  on their next call; raising it does not widen a child past its spawn scope.
- `consult_model` stays hidden in the child (no resolver, `tools.go:181-186`); a child
  seat is the seat-placement direction.
- At `maxSubAgentDepth` the child's registry withholds `spawn_subagent`: a small
  `withheld map[string]bool` on `ToolRegistry`, checked first in `callableNonBridge`
  (`tools.go:177`). Below the cap the child sees it and may spawn one level down. This
  matches the cap the code already enforces (`subagent.go:91`) and makes the prompt agree.

### D3. The child's prompt is built from its visible tools

- `buildSubAgentSystemPrompt` drops the hard-coded tool list (`subagent.go:267-273`) and
  the two "do not spawn" lines (`:103`, `:282`). The child gets `visibleToolsMessage(childTools)`
  (`agent.go:329-343`) as its own system message, as `New` does for the parent.
- The brief text (`subagent.go:100-107`) names the target node and drops the tool names:
  "Your target node is %q" when one is set, nothing when none is.
- When `spawn_subagent` is visible to the child, its prompt adds one line: it may delegate
  one level further with a full brief.

### D4. An empty `target_node` is resolved, and ranked only when the task has a profile

The child's model runs in this process (Non-goals), and under D5 its node-addressed tools
are bounded reads (`head -c 32000`, `ls`, `grep`, `tools_cluster.go:94-200`). Ranking by
free capacity optimizes nothing for a read. So, in `dispatchSubagent`, before building the
child, the target comes from the first rule that applies:

1. **Set by parent.** `strings.TrimSpace(sa.TargetNode)` is not empty: use it. Reason
   `set by parent`.
2. **No node-scoped tools.** The child's registry shows none of `remote_read_file`,
   `remote_grep` or `remote_list` (only possible under D5 option B or C): no target.
   Reason `none (no node-scoped tools at <scope>)`.
3. **Named in the brief.** Exactly one node name from `view.Config.Nodes` appears as a
   whole word in `objective`, `prompt`, `sources` or `boundary`: use it. Reason
   `named in brief`. Config names are the names `runRemote` resolves
   (`tools_cluster.go:35-39`), so the target is always reachable by the child's tools.
4. **Ranked.** `reqs := placement.InferRequirements(objective + "\n" + prompt)` matched a
   workload profile (`reqs.Workload.Class != models.ClassUnknown`, see
   `internal/workload/match.go:41-45`): reload the view under `dispatchMu` the way cluster
   tools do (`dispatch.go:133-148`), keep only snapshot nodes that also appear in
   `view.Config.Nodes`, and call `placement.ExplainPlacement(reqs, nodes, view.State)`, the
   call `axis_place` makes (`tools.go:499-513`). Decision OK: use `Decision.Node`, reason
   `placement: <class>, <decisive criterion>, <metric name> = <value><unit>` built from the
   decision's ranking. Not OK: no target, reason `placement: no eligible node (N excluded)`.
5. **Otherwise** no target. Reason `none (not set by parent)`. The child picks with its
   remote tools.

The target goes into the child's prompt (D3), the receipt (D8) and `backgroundTask.node`
(`subagent.go:118`).

### D5. Read-only remote tools leave the never set (**pending maintainer pick, decision 2**)

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
- `func (a *Agent) Close() []string` cancels `sessionCtx` and returns the ids of tasks that
  were still running. It is safe to call twice.
- Close runs where each session really ends, and its result is printed, one line on the
  surface's error writer when the list is not empty:
  `cancelled N open sub-agent task(s): bg-2, bg-3`.
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

The word `cancelled` matches A2A and MCP task states. If DEA-31 lands a shared state
vocabulary first, D6 uses its word.

### D7. The child's result uses `LastRunOutcome`

Depends on the approved exit-code change (`RunOutcome`, `RunStop`, `LastRunOutcome`; see
DEA-31). After `child.Run`, `dispatchSubagent` reads `child.LastRunOutcome()`:

| Stop | Child text | Sync result | Async task |
|---|---|---|---|
| answered | non-empty | receipt line, then the answer | succeeded, receipt line plus answer |
| answered | empty | existing no-answer error (`subagent.go:169-171`) | failed, same line |
| turn_ceiling | any | error: `sub-agent stopped at its N-turn ceiling without a final answer; the parent must not invent one` | failed, same line |
| failed | any | existing `sub-agent failed: %w` (`subagent.go:155`) | failed, error line |

Every error row starts with the receipt line (D8), then a newline, then the error text, so
the parent sees where the child ran even when it failed. The no-invented-answer rule stays. Under the exit-code change a piped run's confirm records
denials (`pipedConfirm(w, rec)`); the child holds the parent's confirm (`confirm.go:147`),
so a child's denied call lands in the parent's `pending_approvals` (inferred from the
patch, not run).

### D8. The spawn receipt

Every spawn returns, and verbose mode prints, one receipt line before anything else.
Illustrative values:

```text
sub-agent bg-3 async | node: builder (named in brief) | scope: observe | depth: 1 | max_turns: 6
sub-agent sync | node: worker-1 (placement: go-build, allocatable_capacity, allocatable_ram = 7168MB) | scope: edit | depth: 1 | max_turns: 6 | outcome: answered 4/6
```

Async receipts end with the existing `check_task` hint (`subagent.go:145-146`). Sync
receipts carry `outcome: <stop> <turns>/<max_turns>` from D7. The line is text in a tool
result, read by the parent model; no JSON shape changes.

### Docs in the implementing PR

- `docs/current-state.md`: one bullet for the reachable peer loop.
- `docs/release-notes.md`: the scope change, the receipt, and `cancelled`.
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
  the same shape: inherit by default, withhold the spawn tool at the cap.
- A2A `TaskState` ([`specification/a2a.proto`](https://raw.githubusercontent.com/a2aproject/A2A/main/specification/a2a.proto))
  and MCP tasks ([2025-11-25 tasks](https://raw.githubusercontent.com/modelcontextprotocol/modelcontextprotocol/main/docs/specification/2025-11-25/basic/utilities/tasks.mdx))
  both have a cancelled terminal state; D6 uses the same word.

Optimization: request size. Measured at `ab2adcc` with a throwaway test marshalling
`Defs()`: observe sends 13 tools in 6565 bytes. Made eager, the six tools this spec
reveals add 3752 bytes (57%), `spawn_subagent` alone 1553. That cost lands on every model
round of every default session and moves compaction timing. Loading them deferred (D1)
keeps eager `Defs()` at 6565 bytes; the deferred catalog adds one summary line per tool.
Placement adds at most one view reload and one `ExplainPlacement` call per spawn, and only
for briefs that match a workload profile.

## Validation

- Scope pins to update under the chosen D1 and D5 options: `scope_test.go:59`, `:80`,
  `:98`, `:141`, `:178-180`, `:337`; `deferred_tools_test.go:165-183`, `:352-365`, `:502-508`
  (pick another never-set tool, such as `fleet_exec`, for the bridge refusal case);
  `agent_test.go:136-140`; `autonomy_test.go:38`; `session_approval_test.go:249`. New
  pins: the tools are visible at exactly the chosen scopes, hidden at cluster, and absent
  from eager `Defs()` by default; `run_on_node`, `fleet_exec`, `remote_write_file`,
  `remote_tail_logs` and `run_background` stay hidden at every scope and denied on the
  direct path.
- Budget and compaction tests (`agent_backend_turn_test.go:122`, `request_test.go:347`,
  `sessionlog_compaction_test.go:44`) stay green unchanged. They failed in the spec review's
  probe with the tools eager; deferred loading must keep them green without resizing their
  fixtures.
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
- `Close()` while an async child runs ends its task as `cancelled`, returns its id and
  cancels a grandchild; a cancel during the child's last turn still ends as `cancelled`; a
  second `Close()` is a no-op. A console test spawns an async child after startup returns
  and sees it still running.
- A child that hits its ceiling returns the turn-ceiling error, sync and async, with the
  receipt line first; answered and failed paths keep their current text after the receipt.
- Existing subagent tests (`internal/agent/subagent_test.go`) stay green; run the hermetic
  agent tests, `go vet`, `hack/verify-specs.go` and the full preflight.

## Review sequencing

The reviews below ran with both decisions open and the recommended options written in. If the maintainer picks a non-recommended option for either decision, the spec
changes substantially and needs a new spec review before code (AGENTS.md §8, step 3).

## Review

