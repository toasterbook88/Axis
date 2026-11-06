# Full autonomy asks before commands a named rule flags

Date: 2026-11-05. Status: revision 4, implemented on this branch; spec review 2 and diff reviews A
and B approved with changes, all findings resolved.

## Problem

`axis agent --autonomy full` is documented to "auto-approve all but safety-blocked and unlisted
MCP tools" (`cmd/axis/agent.go` flag help; `internal/agent/autonomy.go`). The structured safety
evaluator (`internal/safety/structured.go`) has three verdicts: allow, deny and prompt ("ask the
operator").

- `safety.Check` (`internal/safety/blocker.go`) turns every prompt verdict into score 70, and
  full autonomy approves any score below 80, so for `run_shell` and `run_on_node` no "ask"
  verdict ever asks.
- `axis_run_task` is worse: guarded execution (`internal/execution/prepare.go`) acts only on
  deny and leaves the score at 0, and `autonomyConfirm` has no exception for it, so full
  autonomy approves every `axis_run_task` (spec review 1, F1).

The evaluator has rules that flag a specific risk with a prompt verdict:

- `git-push` and `git-force-push`;
- `curl-post` (POST, PUT, DELETE, data uploads);
- `service-mgmt` (`systemctl`/`launchctl`/`brew` start, stop, install);
- `ollama-run` (run, pull, serve).

Two prompt rules flag no specific risk: `unknown-fallback` (priority 0, any program no rule
recognizes) and `legacy-hardblock-dev-null` (any `> /dev/null` redirect, which #613 already
removed from deny matching).

Measured on the private agent session log (14 days). Selection: `kind == "tool"`,
`autonomy == "full"`, `tool.name == "run_shell"`, with the `command` argument de-duplicated. Each
command was replayed through `Evaluate(cmd, "agent-run-shell")` at `b80fcb5b`, printing only the
verdict and matched rule. Result: 11 distinct commands; 4 allowed, 1 denied (unparseable), 6
prompt. All 6 prompts ran without asking, and all 6 matched `unknown-fallback`. Two of them are
the risks the log review named:

- an `ssh` with `StrictHostKeyChecking=no`;
- commands that `cd` into a `node_modules/` tree and run git there.

No specific rule covers either. None of the 4 allowed commands contains `node_modules` or a
host-key option, so the new rules below do not change them (checked with the same replay).

A second gap: for `a && b`, `Evaluate` returns the first prompt segment, so an unknown `a` hides
a specific risk in `b`.

The operator chose a targeted fix: specific risks ask under full autonomy, and other commands
keep auto-approving so unattended runs keep working.

## Non-goals

- No change for commands that only `unknown-fallback` or `legacy-hardblock-dev-null` flag: they
  keep score 70 and stay auto-approved under full autonomy. Making every prompt ask is the
  stricter alternative the operator declined.
- No change to deny rules or to `--autonomy cluster`.
- Default, edit and `--auto-approve` modes already ask at 70, and they keep asking. The new
  rules turn some commands that were allowed (score 0) into asks in every mode: `git -C`,
  `--git-dir` or `--work-tree` into `node_modules`, and any command with a disabled SSH host-key
  check (for example `GIT_SSH_COMMAND="ssh -o StrictHostKeyChecking=no" git fetch`). Both are
  the logged risks, so this is accepted (spec review 1, F2). A `git` command that only mentions
  `node_modules` (a pathspec exclusion such as `git diff -- . ':!node_modules'`) is unchanged
  (spec review 2, F3).
- `dumb_score` in guarded-execution results becomes 75 for an `AskAlways` prompt where it was
  absent (0, omitted), and `block_reason` carries the rule's reason on a result that is not
  blocked. Both reach the `/v2` run response, A2A results, `axis task run` JSON and the agent's
  run payload. The MCP `verify_execution_safety` output does not gain `AskAlways`, but its
  `matched_rule` now names the `AskAlways` rule where it used to name the fallback or the
  null-redirect rule (`cd /tmp && git push`, `git push > /dev/null`). These value changes in
  existing optional fields are listed here and in `docs/current-state.md` (spec review 2, F5;
  diff review A, F4).
- Only these spellings are matched (revision 4, diff review B F1). The safety parser does not
  look through:
  - command wrappers (`env`, `command`, `exec`, `nice`, `timeout`, `xargs`);
  - `sh -c` / `bash -c` strings;
  - globs and shell variables;
  - an uppercase program name;
  - tab separators;
  - an SSH config file written with `write_file` and used with `ssh -F`.

  These still auto-run under full autonomy when spelled that way. Tab separators also bypass
  every program and argument rule, deny rules included; that predates this change (diff
  review B, F5). Fixing them changes how every command is parsed, which is follow-up work.
- Skipping git's global options lets `git -C <repo> status` and `git -C <repo> commit` match
  `git-safe` and `git-modify` like their plain forms. Edit mode and `--auto-approve` now
  auto-approve them, as they already do the plain forms, where they used to ask as unknown
  commands.
- No new flag, config key, state file or session-log field.
- No complete list of risky commands. The three new rules cover the logged cases; other unknown
  risky commands still auto-run under full autonomy, as today.

## Design

1. **Mark the rules that always ask.** `safety.Rule` gains `AskAlways bool`, and
   `safety.Decision` carries it from the matched rule. It is set on `git-push`,
   `git-force-push`, `curl-post`, `service-mgmt`, `ollama-run` and the three new rules. It is not
   set on `unknown-fallback` or `legacy-hardblock-dev-null`, so a null redirect alone never
   asks (spec review 1, F4). An explicit flag, not a rule-name comparison, so renaming a rule
   cannot change the scoring (spec review 1, question 2). It carries the tags
   `yaml:"ask_always" json:"ask_always,omitempty"` like the other fields; rules are not loaded
   from config today, so an absent field is `false`.
   - **Within a segment**, when the highest-priority match is a prompt without `AskAlways`, the
     evaluator also searches the lower-priority rules for a matching `AskAlways` prompt and
     returns it, the way `firstDeny` searches for a deny (#613). Deny still wins. So
     `git push origin main > /dev/null` returns `git-push`, not the null-redirect rule (spec
     review 2, F1).
2. **Two scores for a prompt verdict.**
   - `safety.Check` gives an `AskAlways` prompt score 75 (`safety.ScoreAskAlways`); any other
     prompt keeps 70. Scores of 80 and above still mean safety-blocked.
   - Guarded execution sets `DumbScore` to 75 and `BlockReason` to the rule's reason, with
     `Blocked` false, for an `AskAlways` prompt, and leaves both unchanged otherwise. So
     `axis_run_task` carries the same signal, and its confirm shows the reason instead of an
     empty one (spec review 1, F1; spec review 2, F5).
3. **Full autonomy asks at 75.** `autonomyConfirm` in full mode auto-approves only scores below
   75 (`autonomyAskThreshold`) and sends 75 to 79 to the operator, the same fallback 80 and
   above use. This applies to `axis_run_task` too.
   - The other scores in that band (75 "no GPU node", 78 "best node low on RAM") come with
     `Blocked: true`. The shell gates send a blocked result to an override confirm with the score
     raised to 80, so it already reaches the operator, and the threshold moves only `AskAlways`
     prompts (spec review 2, question 2).
   - `run_shell`, `run_on_node`, `fleet_exec` and `run_background` all take their score from
     `safety.Check`, so all four inherit the ask (spec review 2, question 4).
   - Edit mode already asks at 70; for `axis_run_task` it now also asks on an `AskAlways`
     prompt, where it used to see score 0.
4. **An `AskAlways` prompt beats other prompts across segments.** `Evaluate` returns the first
   segment (in command order) whose prompt is `AskAlways`. Only when no segment has one does it
   return the first other prompt. Deny still wins over both.
5. **Three new rules, shaped to the logged cases** (spec review 1, F3 and F5; spec review 2,
   F3). All are prompt verdicts with `AskAlways`.
   - `ssh-host-key-off`: any program; raw patterns `stricthostkeychecking` followed by `=` or a
     space and then `no`, `off` or `false`, and `userknownhostsfile` followed by `=` or a space
     and then `/dev/null` (eight patterns). Category network-mutating, priority 160. `AskAlways`
     raw patterns are matched against the lowercased segment with quote characters removed,
     whitespace runs collapsed, and spaces around `=` dropped. So `StrictHostKeyChecking no`,
     `='no'`, `="no"`, `= no` and `'StrictHostKeyChecking  no'` all match (spec review 2, F2;
     revision 4: the space form is a pattern, not rewritten to `=`). It covers `ssh`, `scp`,
     `rsync -e`, `GIT_SSH_COMMAND` and writing the option into an SSH config. A `grep` for the
     option also asks; accepted.
   - `dependency-tree`: programs `cd`, `pushd`, `cp`, `mv`, `tee`, `patch`, `ln`; raw pattern
     `node_modules`. Category modify, priority 90.
   - `dependency-tree-git`: program `git`, and a directory git runs in contains
     `node_modules`. The directories are the values of `-C` (separate or attached),
     `--git-dir` and `--work-tree` (separate or `=`), and the `GIT_DIR` and `GIT_WORK_TREE`
     environment prefixes (`Rule.WorkDirPatterns`). Category modify, priority 90, above
     `git-safe` (85) and `git-modify` (80). So git run inside the tree asks. A git command that
     only names `node_modules` as a pathspec, in a message or after `git log -C` (copy
     detection) does not (spec review 2, F3; revision 4, diff reviews A F1 and B F2).
   - **Spellings** (revision 4, diff review B F1):
     - For git, argument patterns see the subcommand after git's global options (`-C`, `-c`,
       `--git-dir`, `--work-tree`, `--no-pager` and other value-less flags), so
       `git -C /repo push` matches `git-push`.
     - For `systemctl`, `launchctl` and `service`, leading options are skipped, so
       `systemctl --user restart x` matches `service-mgmt`, which also covers
       `brew services start|stop|restart|run`.
     - `curl-post` matches its upload and method options anywhere in the arguments:
       `-X`/`-XPOST`/`--request` with POST, PUT, DELETE or PATCH, `--data`, `-d`, `-F`/`--form`,
       `-T`/`--upload-file`, and wget's `--post-data`, `--post-file`, `--method`, `--body-*`.
       A GET request keeps `curl-get`.
     - `AskAlways` raw patterns also drop backslashes, so `StrictHostKeyChecking\=no` and
       `cd node\_modules` match.
     - The five existing `AskAlways` rules gain a description, so the confirm shows a reason
       (diff review A, F3).
   - Other programs are unchanged, so `./node_modules/.bin/eslint .`, `grep -rn foo
     node_modules/`, `npm test`, `ls` and `cat` keep their current verdicts. `vendor/`, `.venv/`
     and `site-packages/` are not covered: no logged case needs them, and Go and PHP `vendor/`
     is checked-in source.
6. **Session "always".** For `run_shell`, `run_on_node`, `fleet_exec` and `run_background`, an
   operator who answers "always" approves that tool for the rest of the session
   (`dispatch.go`, `async_tasks.go`), and later `AskAlways` prompts for it run without asking.
   That is the operator's explicit choice and is kept (spec review 1, question 1).
   `axis_run_task` has no session approval and asks every time (spec review 2, F4).
7. **Text.** These places say full autonomy auto-approves everything except safety-blocked
   commands, commands a specific safety rule flags, and unlisted MCP tools:
   - the `--autonomy` flag help;
   - the `AutonomyFull` and threshold comments in `autonomy.go`;
   - the agent banner (`cmd/axis/agent_banner.go`);
   - the CHANGELOG (Unreleased) and `docs/current-state.md`.

   The `-p` denial notice (`cmd/axis/agent_oneshot.go`) branches on the score (spec review 2,
   F6):
   - from 75 to 79, it says a specific safety rule flagged the command and that it needs an
     interactive approval;
   - every other score keeps today's text, including the `--autonomy full` hint.

   Earlier release notes stay as they were (spec review 1, F8).

## Tests

Change tests assert `MatchedRule` and fail without the change. Regression guards are labelled
so.

- `Check`: `git push origin main` scores 75. `foo --bar` and `ls > /dev/null` score 70. A deny
  scores 100. `ls` scores 0.
- `Evaluate`:
  - `cd /tmp && git push` returns `git-push` (change).
  - `git push origin main > /dev/null`, `curl -X POST http://example.com > /dev/null` and the
    ssh form with `> /dev/null` return `git-push`, `curl-post` and `ssh-host-key-off`, and
    `git push origin main > /dev/null` scores 75 (change, spec review 2 F1).
  - `cd /tmp && foo` returns `unknown-fallback`, and `git push && rm -rf /` and
    `git push > /dev/null && rm -rf /` are denied (regression guards).
- New rules:
  - Match their new rule (change):
    - `ssh -o StrictHostKeyChecking=no h true`, `ssh -o "StrictHostKeyChecking no" h true`,
      `ssh -o StrictHostKeyChecking='no' h true`, `ssh -o StrictHostKeyChecking="no" h true`,
      `ssh -o 'StrictHostKeyChecking  no' h true` and `ssh -o StrictHostKeyChecking=off h true`;
    - `rsync -e "ssh -o UserKnownHostsFile=/dev/null" a b:`;
    - `cd node_modules/x && git checkout v1` and `cd -- node_modules/x`;
    - `cd node_modules && git -C x checkout v1`, `git -C node_modules/x checkout v1` and
      `FOO=1 git -C node_modules/x checkout v1`.
  - Keep their current verdict and rule (regression guards against false positives):
    `ls node_modules/x`, `cat vendor/a.go`, `grep -rn foo node_modules/`,
    `./node_modules/.bin/eslint .`, `.venv/bin/python -m pytest`, `git add vendor/ go.mod` and
    `git diff -- . ':!node_modules'`.
- `autonomyConfirm`, full mode:
  - Score 70 is approved without the fallback; 75, 79 and 80 call the fallback.
    `TestAutonomyFullAutoApprovesAllButSafetyBlocked` (`autonomy_test.go`), which pins 79 as
    approved, is updated and renamed, because this change reverses it (spec review 2, F7).
  - `axis_run_task` at 75 calls the fallback, and at 0 it is approved (change).
  - Edit and default modes are unchanged at 70.
- Guarded execution: `git push` gets `DumbScore` 75 and its rule reason in `BlockReason`, and an
  unknown command keeps 0; the `axis_run_task` confirm text shows the reason.
- End to end under `AutonomyFull`:
  - `git push` and the two logged shapes reach the fallback confirm, through `run_shell` and
    through `axis_run_task`.
  - `foo --bar`, `ls > /dev/null` and `ls` do not.
- Session "always":
  - after "always" on one `run_shell` `git push`, a second one in the same session runs without
    asking;
  - a second `AskAlways` `axis_run_task` still calls the fallback (Design 6).
- The `-p` notice: score 75 gives the safety-rule text, score 0 keeps today's text. The banner
  text matches the new wording.

## Alternatives

- **Strict: every prompt verdict asks under full autonomy.** It closes the gap completely, but
  6 of the 11 logged full-autonomy commands would have stopped, and in unattended runs they
  would be refused at the 600 s deadline. Declined by the operator.
- **Score `AskAlways` prompts 80.** No threshold change, but `axis_run_task` treats 80 or more
  as a hard block and `BlockResult` documents it as "instant block", so a specific risk would be
  refused, not asked.
- **Pass the verdict to the confirm function.** Exact, but it changes `ConfirmFunc` and every
  confirm path; one more score band carries the same information.
- **Broad dependency-tree patterns** (`vendor/`, `.venv/`, `site-packages/`, any program).
  Spec review 1 showed they stop `pytest` through a venv, `grep` into dependencies and
  `git add vendor/`, which breaks unattended runs.

## Research

- OpenCode resolves bash permissions by pattern, with the last matching rule winning, so a
  specific `"git push *": "ask"` overrides a catch-all `"*": "allow"`. Its `--auto` mode,
  however, approves every request that would ask and enforces only deny
  (https://opencode.ai/docs/permissions/). That matches AXIS full autonomy today. This spec
  keeps specific asks under full autonomy and lets only the catch-all through.
- Measurement: the replay above (selection and procedure in Problem). Success after install:
  over 14 days of new logs, every full-autonomy `run_shell` or `axis_run_task` whose command
  matches an `AskAlways` rule has an operator decision recorded in the log (`decision`); unknown
  commands keep running.

## Review

Kind: spec
Reviewer: fresh-context subagent (Claude Code)
Model: Anthropic Claude (same family as the author; fresh session)
Base: b80fcb5b3925b49621e914c35080826e9cceb527
Head: 8af3b3f0d5b183656a602b57d50d1262d1fca402
Date: 2026-11-05
Verdict: revise

1. Major: the non-goal that `axis_run_task` always asks is false; full autonomy approves it with
   score 0, so a specific risk could go through it. Fixed: guarded execution scores an
   `AskAlways` prompt 75 and full autonomy asks at 75 for every tool (Design 2-3); tests added.
2. Major: the new rules turn allowed commands into asks in edit and `--auto-approve` modes too
   (`git add vendor/`, `go test ./vendor/...`, `git diff vendor/`). Fixed: `vendor/` dropped
   and the dependency rule limited to eight programs; the remaining cross-mode effect (git
   inside `node_modules`, a disabled host-key check) is stated in Non-goals.
3. Major: dependency-tree false positives (`.venv/bin/python`, `grep` into `node_modules/`,
   `./node_modules/.bin/eslint`) would stop unattended runs. Fixed: rule limited to `cd`,
   `pushd`, `git`, `cp`, `mv`, `tee`, `patch`, `ln` and `node_modules`; negative tests added.
4. Major: `> /dev/null` was counted as a specific risk, so `go test ./... > /dev/null` would ask.
   Fixed: `AskAlways` is not set on `legacy-hardblock-dev-null`; `ls > /dev/null` stays score 70
   and auto-approved (Design 1, Tests).
5. Minor: false negatives (`StrictHostKeyChecking no`, `=off`, `=false`; `cd node_modules`
   without a slash). Fixed: patterns for each spelling and a bare `node_modules` match; tests
   for each.
6. Minor: the measurement could not be reproduced. Fixed: selection fields and replay
   procedure stated in Problem, and the 4 allowed commands were checked against the new rules.
7. Minor: several listed tests already pass at Base, and key tests were missing. Fixed: change
   tests assert `MatchedRule`, regression guards are labelled, and tests added for F1-F4 and the
   session "always" behaviour.
8. Minor: the text changes missed the banner, the `-p` notice, release notes and current-state.
   Fixed: Design 7 lists them; past release notes stay as released.

Questions:
- Does a session "always" answer cover later `AskAlways` prompts? Became Design 6: yes, kept as
  the operator's explicit choice; test added.
- How does `Check` tell the two kinds of prompt apart? Became Design 1: an explicit `AskAlways`
  field, not a rule-name comparison.
- With several `AskAlways` segments, which is reported? Became Design 4: the first in command
  order.
- Do any of the 4 allowed logged commands now ask? Verified: replayed against the new rules;
  none contains `node_modules` or a host-key option.

Kind: spec
Reviewer: fresh-context subagent (Claude Code)
Model: Anthropic Claude (same family as the author; fresh session)
Base: b80fcb5b3925b49621e914c35080826e9cceb527
Head: 76fc0917a8ad178eaac73943ab03d20def267c92
Date: 2026-11-05
Verdict: approve-with-changes

1. Major: a null-redirect prompt in the same segment hides an `AskAlways` rule, so
   `git push origin main > /dev/null` still auto-runs. Fixed: within a segment the evaluator
   searches below a non-`AskAlways` prompt for an `AskAlways` one, as `firstDeny` does (Design
   1); tests added.
2. Minor: quoted values and repeated spaces in the ssh option are missed. Fixed: `AskAlways` raw
   patterns are matched with quotes removed, whitespace collapsed and spaces around `=` dropped,
   and the space form is its own pattern (Design 5, revision 4); the three spellings added to
   the tests.
3. Minor: the dependency rule matched any git command mentioning `node_modules`, including
   pathspec exclusions. Fixed: git is a separate rule matching only `-C`, `--git-dir` and
   `--work-tree` into the tree; `git diff -- . ':!node_modules'` added as a guard; Non-goals
   updated.
4. Minor: Design 6 was wrong for `axis_run_task`, which has no session approval. Fixed: Design 6
   limited to the four session-approvable tools; test added that a second `axis_run_task` asks.
5. Minor: the `axis_run_task` score 75 had no reason, and the `dumb_score` output change was not
   listed. Fixed: `BlockReason` carries the rule reason with `Blocked` false (Design 2); the output
   change is listed in Non-goals and current-state; confirm-text test added.
6. Minor: the new `-p` notice would be wrong for most denials. Fixed: it branches on score, and
   only 75 to 79 get the safety-rule text (Design 7); both branches tested.
7. Minor: `TestAutonomyFullAutoApprovesAllButSafetyBlocked` pins 79 as approved and contradicts
   Design 3. Fixed: listed under Tests as updated and renamed.

Questions:
- The 14-day replay and the 4 allowed commands. Open: they need the private session log; the
  author ran the replay at `b80fcb5b` (Problem).
- Are the other 75-79 scores refused before confirmation? Became Design 3: they go to an
  override confirm at score 80 on the shell paths; only `AskAlways` prompts move.
- Should `AskAlways` get yaml/json tags, and should `verify_execution_safety` expose it? Became
  Design 1 (tags added) and Non-goals (not exposed).
- Do `fleet_exec` and `run_background` inherit the ask? Became Design 3: yes, all four shell
  gates take their score from `safety.Check`.

Note: every spec-review finding is resolved; code starts.

Kind: diff
Reviewer: fresh-context subagent A (Claude Code)
Model: Anthropic Claude (same family as the author; fresh session)
Base: b80fcb5b3925b49621e914c35080826e9cceb527
Head: 6fe1a988857e39af694306fc3895c8348a02686b
Date: 2026-11-06
Verdict: approve-with-changes

1. Minor: `dependency-tree-git` matched any git command with `-C` and a later `node_modules`
   (`git -C /repo diff -- . ':!node_modules'`, `git log -C -- node_modules/x`). Fixed: the
   rule matches the directories git runs in (`WorkDirPatterns`), not the joined arguments; both
   commands are regression guards.
2. Minor: spec-listed tests were missing (the two logged shapes and `ls` through
   `axis_run_task`; edit and default at 70). Fixed: added to
   `TestFullAutonomyRunTaskAsksForSpecificRisks` and `TestEditAndDefaultModesAskAtSeventy`.
3. Minor: the `axis_run_task` confirm showed an empty reason, twice. Fixed: the five existing
   `AskAlways` rules have descriptions, and the score warning carries the `safety why:` label
   so the reason appears once; the test checks the count.
4. Minor: `block_reason` on non-blocked results and the MCP `matched_rule` changes were not
   listed. Fixed: both are in Non-goals and `docs/current-state.md`.

Questions:
- Is the attached `git -Cnode_modules/x` form worth covering? Became Design 5: attached `-C`
  values are read; tested.
- Do the change tests fail without their change? Verified: diff review B's mutation harness
  found every change test failing under its mutation, except the three `AskAlways` flags with
  no test, which review B F3 fixed.

Kind: diff
Reviewer: fresh-context subagent B (Claude Code)
Model: Anthropic Claude (same family as the author; fresh session)
Base: b80fcb5b3925b49621e914c35080826e9cceb527
Head: 6fe1a988857e39af694306fc3895c8348a02686b
Date: 2026-11-06
Verdict: approve-with-changes

1. Major: other spellings of the `AskAlways` rules still auto-run (`git -C /repo push`,
   `curl -XPOST`, `systemctl --user restart`, `GIT_DIR=node_modules/...`, backslash escapes).
   Fixed for the matcher-level forms: git global options and env, curl/wget options anywhere,
   service options, `brew services`, backslashes (Design 5, revision 4); each tested. Declined
   for wrappers (`env`, `command`, `nice`, `xargs`), `sh -c`, globs, variables, uppercase names
   and `ssh -F`: they need a change to how every command is parsed, so they are listed in
   Non-goals as not matched, per review B's own alternative.
2. Minor: `dependency-tree-git` asked for read-only git commands that only name `node_modules`.
   Fixed with A F1: work-directory matching; pathspec, message and `log -C` guards added.
3. Minor: three `AskAlways` flags and two end-to-end cases had no test. Fixed: `systemctl
   restart`, `ollama run`, `git push --force` assert their rule and `AskAlways`; the two shapes
   run through `axis_run_task`.
4. Minor: the spec status was stale. Fixed: revision 4, and the review-2 F2 resolution text
   corrected.
5. Major (pre-existing): tab-separated words bypass every program and argument rule, deny
   rules included. Declined: the same at Base and outside this spec; listed in Non-goals as
   follow-up parser work and in the PR body.

Questions:
- Edit autonomy approves `axis_run_task` at score 0 even with `--auto-approve`. Open:
  predates this change and is outside it; the spec keeps edit mode unchanged at 0.
- `persistA2ARun` falls back to `BlockReason` when `Error` is empty, so a failed `AskAlways` run
  could report the rule as its failure reason. Open: needs a guarded-execution failure fixture;
  noted in the PR body.
- Should `write_file` of an SSH config followed by `ssh -F` count? Became Non-goals: not matched.

Note: every diff-review finding is resolved or declined with a reason; merge needs the maintainer's go.
