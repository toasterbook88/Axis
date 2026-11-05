# Full autonomy asks before commands a named rule flags

Date: 2026-11-05. Status: draft, awaiting spec review.

## Problem

`axis agent --autonomy full` is documented to "auto-approve all but safety-blocked and unlisted
MCP tools" (`cmd/axis/agent.go` flag help; `internal/agent/autonomy.go`). The structured safety
evaluator (`internal/safety/structured.go`) has three verdicts: allow, deny and prompt ("ask the
operator"). `safety.Check` (`internal/safety/blocker.go`) turns every prompt verdict into score
70, and full autonomy approves any score below 80. So in full autonomy no "ask" verdict ever
asks.

The evaluator has two kinds of prompt verdict:

- **Named rules** that flag a specific risk: `git-push`, `git-force-push`, `curl-post` (POST,
  PUT, DELETE, data uploads), `service-mgmt` (`systemctl`/`launchctl`/`brew` start, stop,
  install), `ollama-run` (run, pull, serve) and the `> /dev/null` rule.
- **`unknown-fallback`** (priority 0), which prompts for any program no rule recognizes.

Measured on the private agent session log (14 days): 11 distinct `run_shell` commands ran under
full autonomy. 4 were allowed by a rule, 1 was denied (unparseable), and 6 got a prompt verdict.
All 6 ran without asking. All 6 came from `unknown-fallback`. Two of them are the risks the log
review named: an `ssh` with `StrictHostKeyChecking=no`, and commands that start with `cd` into a
`node_modules/` tree and then run git there. No named rule covers either today.

A second gap: for `a && b`, `Evaluate` returns the first prompt segment. When `a` is unknown
and `b` matches a named rule, the fallback verdict wins.

The operator chose a targeted fix: named risks ask under full autonomy, and unknown commands
keep auto-approving so unattended runs keep working.

## Non-goals

- No change to unknown commands under full autonomy: `unknown-fallback` keeps score 70 and
  stays auto-approved. Making them ask is the stricter alternative the operator declined.
- No change to default, edit or cluster autonomy, to `--auto-approve`, or to deny rules.
- No change to `axis_run_task`: it reads the evaluator's verdict directly, acts only on deny,
  and always asks (`internal/agent/dispatch.go`).
- No new flag, config key, state file or session-log field.
- No complete list of risky commands. The two new rules cover the logged cases; other unknown
  risky commands still auto-run under full autonomy, as today.

## Design

1. **Two scores for a prompt verdict.** `safety.Check` gives a prompt verdict from a named rule
   score 75 (`ScoreNamedPrompt`). A prompt verdict from `unknown-fallback` keeps 70. Scores at
   80 and above still mean "safety-blocked" and are unchanged.
2. **Full autonomy asks at 75.** `autonomyConfirm` in full mode auto-approves only scores below
   75 (`autonomyAskThreshold`). Scores from 75 to 79 go to the operator, the same fallback that
   80 and above use today. Edit mode already asks at 70 and is unchanged. The other scores in
   that band (75 "no GPU node", 78 "best node low on RAM") come with `Blocked: true` and are
   refused before confirmation, so the threshold moves only named prompts.
3. **A named prompt beats the fallback across segments.** `Evaluate` returns a named rule's
   prompt verdict when any segment has one, and the fallback prompt only when no segment does.
   Deny still wins over both.
4. **Two named prompt rules.**
   - `ssh-host-key-off`: raw patterns `StrictHostKeyChecking=no` and
     `UserKnownHostsFile=/dev/null` (case-insensitive), any program. Category
     network-mutating, priority 160. It covers `ssh`, `scp`, `rsync -e`, `GIT_SSH_COMMAND` and
     writing those options into an SSH config.
   - `dependency-tree`: raw patterns `node_modules/`, `vendor/`, `site-packages/` and
     `.venv/`, any program. Category modify, priority 90. It sits below the read-only allow rules
     (`ls`, `cat`, `head`, `find`, …, priority 100), so reading a dependency tree still runs. It sits
     above `git-modify` and `build-commands` (80), so `cd node_modules/x`, `git -C vendor/x
     checkout` and `cp patch node_modules/` ask. `git-safe` (85) also sits below it, so
     `git -C node_modules/x status` asks too; accepted, since reads through git inside a
     dependency tree are rare.
5. **Text.** The `--autonomy` flag help, the `AutonomyFull` comment and the CHANGELOG say full
   autonomy auto-approves everything except safety-blocked commands, commands a named safety
   rule flags, and unlisted MCP tools.

## Tests

- `safety.Check`: a named prompt (`git push origin main`) scores 75, an unknown command scores
  70, a deny scores 100, and an allowed command scores 0.
- `Evaluate`: `cd /tmp && git push` returns the `git-push` rule; `cd /tmp && foo` returns
  `unknown-fallback`; `git push && rm -rf /` is denied.
- New rules: `ssh -o StrictHostKeyChecking=no host true`, `rsync -e "ssh -o
  UserKnownHostsFile=/dev/null" a b:` and `cd node_modules/x && git checkout v1` prompt from the
  named rule; `ls node_modules/x` and `cat vendor/a.go` are allowed.
- `autonomyConfirm` in full mode: score 70 → approved without the fallback, 75 → fallback
  called, 80 → fallback called. Edit and default modes are unchanged at 70 and 75.
- End to end through the agent's shell gate under `AutonomyFull`: `git push` and the two logged
  shapes reach the fallback confirm; an unknown command (`foo --bar`) and `ls` do not.
- Each test fails without its change.

## Alternatives

- **Strict: every prompt verdict asks under full autonomy.** It closes the gap completely, but 6
  of the 11 logged full-autonomy commands would have stopped. In unattended runs no one
  answers, so they would be refused at the 600 s deadline. Declined by the operator.
- **Score named prompts 80.** No threshold change, but `axis_run_task` treats a score of 80 or
  more as a hard block, and `BlockResult` documents 80 or more as "instant block". A named risk
  would then be refused, not asked.
- **Pass the verdict to the confirm function.** Exact, but it changes `ConfirmFunc` and every
  confirm path. One more score band carries the same information.

## Research

- OpenCode resolves bash permissions by pattern, with the last matching rule winning, so a
  specific `"git push *": "ask"` overrides a catch-all `"*": "allow"`. Its `--auto` mode,
  however, approves every request that would ask and enforces only deny
  (https://opencode.ai/docs/permissions/). That matches AXIS full autonomy today. This spec
  keeps specific asks under full autonomy and lets only the catch-all through.
- Measurement: the 11-command replay above, with the evaluator at the current `main` (6 prompt
  verdicts, all `unknown-fallback`). Success after install: over 14 days of new logs, no
  full-autonomy `run_shell` that a named prompt rule matches runs without an approval decision.
  Unknown commands keep running.

## Review
