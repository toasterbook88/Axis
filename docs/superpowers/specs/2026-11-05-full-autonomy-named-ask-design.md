# Full autonomy asks before commands a named rule flags

Date: 2026-11-05. Status: revision 2, after spec review 1 (revise); awaiting spec review 2.

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
- Default, edit and `--auto-approve` modes already ask at 70, and they keep asking. The two new
  rules turn some commands that were allowed (score 0) into asks in every mode: `git` inside
  `node_modules`, and any command with a disabled SSH host-key check. Both are the logged
  risks, so this is accepted (spec review 1, F2).
- No new flag, config key, state file or session-log field.
- No complete list of risky commands. The two new rules cover the logged cases; other unknown
  risky commands still auto-run under full autonomy, as today.

## Design

1. **Mark the rules that always ask.** `safety.Rule` gains `AskAlways bool`, and
   `safety.Decision` carries it from the matched rule. It is set on `git-push`,
   `git-force-push`, `curl-post`, `service-mgmt`, `ollama-run` and the two new rules. It is not
   set on `unknown-fallback` or `legacy-hardblock-dev-null`, so a null redirect alone never
   asks (spec review 1, F4). An explicit flag, not a rule-name comparison, so renaming a rule
   cannot change the scoring (spec review 1, question 2).
2. **Two scores for a prompt verdict.**
   - `safety.Check` gives an `AskAlways` prompt score 75 (`safety.ScoreAskAlways`); any other
     prompt keeps 70. Scores of 80 and above still mean safety-blocked.
   - Guarded execution sets `DumbScore` to 75 for an `AskAlways` prompt and leaves it
     unchanged otherwise, so `axis_run_task` carries the same signal (spec review 1, F1).
3. **Full autonomy asks at 75.** `autonomyConfirm` in full mode auto-approves only scores below
   75 (`autonomyAskThreshold`) and sends 75 to 79 to the operator, the same fallback 80 and
   above use. This applies to `axis_run_task` too.
   - The other scores in that band (75 "no GPU node", 78 "best node low on RAM") come with
     `Blocked: true` and are refused before confirmation, so the threshold moves only
     `AskAlways` prompts.
   - Edit mode already asks at 70; for `axis_run_task` it now also asks on an `AskAlways`
     prompt, where it used to see score 0.
4. **An `AskAlways` prompt beats other prompts across segments.** `Evaluate` returns the first
   segment (in command order) whose prompt is `AskAlways`. Only when no segment has one does it
   return the first other prompt. Deny still wins over both.
5. **Two new rules, shaped to the logged cases** (spec review 1, F3 and F5). Both are prompt
   verdicts with `AskAlways`.
   - `ssh-host-key-off`: any program, raw patterns (case-insensitive, as all raw patterns are)
     `stricthostkeychecking=no`, `stricthostkeychecking no`, `stricthostkeychecking=off`,
     `stricthostkeychecking off`, `stricthostkeychecking=false`, `stricthostkeychecking false`,
     `userknownhostsfile=/dev/null` and `userknownhostsfile /dev/null`. Category
     network-mutating, priority 160. It covers `ssh`, `scp`, `rsync -e`, `GIT_SSH_COMMAND` and
     writing the option into an SSH config. A `grep` for the option also asks; accepted.
   - `dependency-tree`: programs `cd`, `pushd`, `git`, `cp`, `mv`, `tee`, `patch`, `ln`, raw
     pattern `node_modules`. Category modify, priority 90, which is above `git-safe` (85) and
     `git-modify` (80), so `git` inside the tree asks. Other programs are unchanged, so
     `./node_modules/.bin/eslint .`, `grep -rn foo node_modules/`, `npm test`, `ls` and `cat`
     keep their current verdicts. `vendor/`, `.venv/` and `site-packages/` are not covered: no
     logged case needs them, and Go and PHP `vendor/` is checked-in source.
6. **Session "always".** An operator who answers "always" to a confirmation approves that tool
   for the rest of the session (`dispatch.go`), and later `AskAlways` prompts for that tool run
   without asking. That is the operator's explicit choice and is kept (spec review 1,
   question 1).
7. **Text.** These places say full autonomy auto-approves everything except safety-blocked
   commands, commands a specific safety rule flags, and unlisted MCP tools:
   - the `--autonomy` flag help;
   - the `AutonomyFull` and threshold comments in `autonomy.go`;
   - the agent banner (`cmd/axis/agent_banner.go`);
   - the CHANGELOG (Unreleased) and `docs/current-state.md`.

   The `-p` denial notice (`cmd/axis/agent_oneshot.go`) says that a specific safety rule
   flagged the command and that it needs an interactive approval, instead of pointing at
   `--autonomy full` (spec review 1, F8). Earlier release notes stay as they were.

## Tests

Change tests assert `MatchedRule` and fail without the change. Regression guards are labelled
so.

- `Check`: `git push origin main` scores 75. `foo --bar` and `ls > /dev/null` score 70. A deny
  scores 100. `ls` scores 0.
- `Evaluate`:
  - `cd /tmp && git push` returns `git-push` (change).
  - `cd /tmp && foo` returns `unknown-fallback`, and `git push && rm -rf /` is denied
    (regression guards).
- New rules:
  - `ssh -o StrictHostKeyChecking=no h true`, `ssh -o "StrictHostKeyChecking no" h true`,
    `ssh -o StrictHostKeyChecking=off h true`, `rsync -e "ssh -o UserKnownHostsFile=/dev/null" a
    b:`, `cd node_modules/x && git checkout v1`, `cd node_modules && git -C x checkout v1` and
    `git -C node_modules/x checkout v1` all match their new rule (change).
  - `ls node_modules/x`, `cat vendor/a.go`, `grep -rn foo node_modules/`,
    `./node_modules/.bin/eslint .`, `.venv/bin/python -m pytest` and `git add vendor/ go.mod`
    keep their current verdict and rule (regression guards against false positives).
- `autonomyConfirm`, full mode:
  - Score 70 is approved without the fallback; 75 and 80 call the fallback.
  - `axis_run_task` at 75 calls the fallback, and at 0 it is approved (change).
  - Edit and default modes are unchanged at 70.
- Guarded execution: `git push` gets `DumbScore` 75, and an unknown command keeps 0.
- End to end under `AutonomyFull`:
  - `git push` and the two logged shapes reach the fallback confirm, through `run_shell` and
    through `axis_run_task`.
  - `foo --bar`, `ls > /dev/null` and `ls` do not.
- Session "always": after "always" on one `git push`, a second `git push` in the same session
  runs without asking (documents Design 6).
- The `-p` notice names the safety rule; the banner text matches the new wording.

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
