# `axis agent -p` exit outcomes

Status: proposed. Written after a draft patch existed (the patch was built first as a local
proposal); the code commit follows this one and is reviewed against it.

## Problem

`axis agent -p` is the entry point a script or a supervising agent uses. It cannot tell the caller
how the run ended:

- The turn loop returns `nil` when it uses every `--max-turns` round without a final answer
  (`internal/agent/agent.go:517-518`, `emitMaxTurnsReached` then `return nil`), the same value a
  finished answer returns (`agent.go:439-440`).
- `runOneShotPiped` (`cmd/axis/agent_oneshot.go`) only checks `Run`'s error, so the ceiling exits
  0. Reproduced on v0.29.0 (`ab2adcc`) against a stub Ollama whose model always calls a tool:
  `axis agent -p --max-turns 2 "…"` prints `stopped at the 2-turn ceiling` to stderr and exits 0.
- `pipedConfirm` denies every confirm a piped run reaches (it cannot prompt) and records nothing,
  so a run that could not do a step for want of approval also exits 0 when the model answers
  around it. So does a call to a tool the `--autonomy` mode hides (`run_shell` in the default
  observe mode): dispatch fails it, and the run exits 0.

## Non-goals

- The interactive REPL, transcript console, slash commands, subagents and A2A keep calling `Run`
  and keep its return values. `Run` still returns `nil` at the ceiling.
- The positional one-shot without `-p` keeps its exit 0 at the ceiling
  (`TestAgentCmdPositionalCeilingKeepsExitZero`; `TestCharOneShotLegacyContractUnchanged` pins its
  banner and confirm).
- The loop does not stop at the first denied confirm. The model still sees the denial and may
  carry on with other tools; stopping early would change every `-p` run.
- No streaming event format (`stream-json`), no resume of a needs-input run, no question protocol
  back to the caller beyond listing the denied requests.

## Design

1. `internal/agent`: `runLocked` records a `RunOutcome{Stop, Turns, MaxTurns, ScopeRefused}` on
   every return, read through `(*Agent).LastRunOutcome()`. `Stop` is `answered` (a round with no
   tool calls), `turn_ceiling` (loop exhausted) or `failed` (`Run` returned an error). `Turns`
   counts model rounds that returned a response. `ScopeRefused` lists, once each, the tools the
   model called that the mode hides but `--autonomy full` would expose; never-tools are not listed.
   The fields have their own mutex so they can be read without `runMu`.
2. `cmd/axis`: `pipedConfirm(w, rec)` keeps its notices and also appends each denial to a
   mutex-guarded `pipedApprovals` recorder: tool, the confirm text clipped to 400 runes, risk
   score, and `clears`, what lets a rerun through. `clears` follows `autonomyConfirm`
   (`internal/agent/autonomy.go`): `auto_approve_tools` for an `mcp_` tool, which asks in every mode
   unless `nodes.yaml` allowlists it; `interactive approval` for consult_model and any score of 75
   or more (a safety rule's ask, or a safety-blocked override at 80 and up), which full autonomy
   still sends to the confirm; `--autonomy full` otherwise. The stderr notice names the same thing.
   Each `ScopeRefused` tool becomes an entry with `clears` `--autonomy full`.
3. `classifyPipedRun(err, stop, pending)` maps the run to an outcome and exit code, first match
   wins:

   | outcome | exit | when |
   |---|---|---|
   | `failed` | 4 (`ExitErrCommandFail`, unchanged) | `Run` returned an error, or the deadline or a signal ended a run that reached the ceiling |
   | `needs_input` | 9 (`ExitErrAgentNeedsInput`, new) | a confirm was denied, or the mode hid a tool that full would expose |
   | `turn_ceiling` | 8 (`ExitErrAgentTurnCeiling`, new) | loop used every round |
   | `answered` | 0 | otherwise |

   `needs_input` outranks the others because a rerun with approval may do more, even when the
   model answered around the denial. 8 and 9 are the next free codes after `ExitErrModelUnlisted`
   (7). The `ExitErr` prefix keeps them inside the exit-code check of `hack/verify-doc-facts.sh`.
4. `--format text|json` on `axis agent`, following the repo's existing `--format` flags and the
   `axis.output/v1` envelope (`writeMachineOutput`, `docs/machine-output.md`). `json` requires
   `-p`; `yaml` is refused. With `json` the assistant stream, the model picker and every notice go
   to stderr, and stdout carries one document: `command` `agent`, `status` the outcome, `ok` true
   only for `answered`, and `data` with `outcome`, `exit_code`, `turns`, `max_turns`, `final_text`
   (newest assistant message, which at the ceiling is the last round's text beside its tool calls;
   empty on `failed`), `pending_approvals` (always an array), `error` (on `failed`), `usage`
   (`tokens_in`, `tokens_out`, `estimated`) and `elapsed_ms`. The document is written for every
   outcome, failures included. A failure before the agent runs (flag conflict, unknown role, config
   or backend startup) writes a `failed` document from the command, with `exit_code` the code the
   process exits with (1 or 2) and `turns` 0.
5. In text mode, non-zero outcomes return an `ExitCodeError` whose message names the cause
   (`agent stopped at the N-turn ceiling without a final answer`, `agent needs input: N step(s)
   need an approval a piped run cannot give`).
6. `-p` refuses `--max-turns` below 1, which would otherwise report a ceiling with no round run.
7. Docs: `docs/machine-output.md` gains a section with the payload and the table;
   `docs/current-state.md` gains one bullet; the exit-code tables in AGENTS.md §5 and
   `docs/troubleshooting.md` gain 8 and 9; `-p` help lists the codes.

## Alternatives

- Return a sentinel error from `Run` at the ceiling. Every caller treats a non-nil error as a
  failed turn (REPL prints it, the console marks the turn failed, slash commands and subagents
  propagate it), so this changes behavior outside `-p`. Rejected.
- A bare `--json` boolean with a flat object. The repo already standardises on `--format` and a
  versioned envelope for automation output; a second convention would split scripts. Rejected.
- sysexits.h codes (`EX_TEMPFAIL` 75 for the ceiling, `EX_NOPERM` 77 for needs input). The CLI's
  own table is 0 to 7 and documented in `cmd/axis/exit.go`; jumping into the sysexits range would
  mix two schemes in one binary. Rejected.
- Stop the run at the first denied confirm and return needs_input immediately. Cheaper for the
  caller, but it removes the model's chance to finish with read tools, and changes every
  existing `-p` run. Left for a later flag if wanted.

## Research

- Claude Code's headless mode (`claude -p`) exits 0 on success and non-zero when the run fails,
  offers `--output-format json` with the answer in a `result` field, and lists denied permission
  requests in the final result's `permission_denials`
  (<https://code.claude.com/docs/en/headless>). This design takes the same split: exit status for
  the coarse branch, a JSON result for detail, denials listed for the caller.
- Optimization: none applies. The change adds one mutex-guarded struct write per run and, in json
  mode, one encode at exit.

## Tests

- `internal/agent/run_outcome_test.go`: zero before a run; answered (1 turn); ceiling returns nil
  and records `turn_ceiling` with 2 rounds; failing backend records `failed` with 0 turns; outcome
  resets between runs; a mode-hidden `run_shell` is listed once, a never-tool is not, and the list
  resets on the next run.
- `cmd/axis/agent_oneshot_outcome_test.go`: classification table including precedence; exit codes
  distinct; denial recording and `clears`, including score 100 and an `mcp_` tool;
  `runOneShotPiped` end to end with a scripted backend for answered (text and json, stdout holds
  exactly one document), ceiling (text and json, exit 8, `pending_approvals` serialised as `[]`),
  needs input (`--autonomy full` plus `git push`, exit 9, request listed), a mode-hidden tool
  (default mode, exit 9, `clears` `--autonomy full`), failed (exit 4, `error` set), and a deadline
  during the last round (exit 4); `--format` validation.
- `cmd/axis/agent_cmd_outcome_test.go` drives `agentCmd` with a stubbed startup backend: json
  stdout is one document and the stream reaches stderr; the recorder is wired (exit 9); a startup
  failure writes a `failed` document with its real exit code; text mode writes none; `-p
  --max-turns 0` is refused; the positional run at the ceiling exits 0. Removing the stream
  routing or the recorder from `agentCmd` fails these (checked by mutation).
- Manual: the built binary against a stub Ollama for the ceiling, answer and git-push cases, and
  with no backend for the startup and mid-run failure documents.

## Review

Kind: spec
Reviewer: fresh-context subagent (Claude Code `general-purpose`, given only `docs/superpowers/review-prompt.md` and its five inputs)
Model: Anthropic Claude Opus 5.5 (same family as the author)
Base: ab2adcc5429ba1025492eebc8dcfe7f6bc001ea4
Head: b52eaf06ef4658116d40d9ff1824de1c10a9880b
Date: 2026-11-28
Verdict: approve-with-changes

1. Major: `clears` named `--autonomy full` for denials that flag cannot clear (unlisted `mcp_` tools at score 0, safety-blocked overrides at 80 and up, both sent to the confirm by `internal/agent/autonomy.go:80-89`). Fixed in e2958c0: `clears` is `auto_approve_tools` for `mcp_` tools and `interactive approval` for consult_model and any score of 75 or more, the stderr notices match, and `TestPipedConfirmRecordsDenials` covers score 100 and `mcp_files_delete`.

2. Major: the JSON document was promised for every outcome, but startup failures returned before `Run` with empty stdout and exit 1 or 2. Fixed in e2958c0: `agentCmd` writes a `failed` document with the real exit code once `-p --format json` is validated; `TestAgentCmdJSONStartupFailureWritesDocument`, and the built binary gives exit 1 with a document for `--model x --role y`.

3. Minor: the exit-code tables in AGENTS.md and `docs/troubleshooting.md` lacked 8 and 9, and `ExitAgent*` names slipped past `hack/verify-doc-facts.sh`. Fixed in e2958c0: renamed to `ExitErrAgentTurnCeiling` and `ExitErrAgentNeedsInput`, added both tables, and the gate passes with the constants in scope.

4. Minor: the non-goal cited a test that does not pin the positional exit code at the ceiling. Fixed in e2958c0: `TestAgentCmdPositionalCeilingKeepsExitZero` drives `agentCmd` without `-p` to the ceiling and asserts a nil error.

Questions:

- Is the recorder reached concurrently? Verified: `pipedApprovals` and the outcome fields are mutex-guarded, `spawn_subagent` and `run_background` are never-tools (`internal/agent/scope.go:96-100`) so a model cannot start a child that confirms through it, and `hack/hermetic-go-test.sh -race ./internal/agent ./cmd/axis` reports no race.
- What should `final_text` be at the ceiling? Verified: it is the newest assistant message, which `lastAssistantText` never skips past, so `--resume` cannot surface an earlier session's answer; Design §4 and `docs/machine-output.md` say it is the last round's text beside its tool calls.
- Should `--format json` keep the model picker off stdout? Verified: since e2958c0, in json mode the startup backend's `Out` is stderr.

Kind: diff
Reviewer: fresh-context subagent A (Claude Code `general-purpose`, given only `docs/superpowers/review-prompt.md` and its five inputs)
Model: Anthropic Claude Opus 5.5 (same family as the author)
Base: ab2adcc5429ba1025492eebc8dcfe7f6bc001ea4
Head: 58e1faa6a3555051d15ae1bfab8accaa2efc28f0
Date: 2026-11-28
Verdict: approve-with-changes

1. Major: `pending_approvals[].clears` said `--autonomy full` for denials that flag does not clear. Fixed in e2958c0, as spec review item 1.

2. Major: the spec gate failed with no review recorded. Fixed in this section.

3. Minor: exit codes 8 and 9 were missing from AGENTS.md and `docs/troubleshooting.md` and escaped the doc-facts gate. Fixed in e2958c0, as spec review item 3.

4. Minor: the json-mode stdout wiring in `agentCmd` was untested. Fixed in e2958c0: `TestAgentCmdJSONStdoutIsOneDocument` runs `agentCmd` with a stubbed startup backend; pointing the agent's Output back at stdout makes it fail.

5. Minor: a call to a tool the mode hides (default mode, `run_shell`) exited 0 `answered`. Fixed in e2958c0: dispatch records it in `RunOutcome.ScopeRefused` when `--autonomy full` would expose the tool, and the run reports `needs_input` with `clears` `--autonomy full`; `TestLastRunOutcomeScopeRefused` and `TestPipedRunScopeHiddenToolIsNeedsInput`.

Questions:

- Startup failures write no document. Became item 2 of the spec review.
- Can the model picker draw on stdout ahead of the document? Verified: fixed in e2958c0, as the spec review's third question.
- Is the `permission_denials` citation right? Verified: the spec reviewer and diff reviewer B both fetched https://code.claude.com/docs/en/headless and found it.
- `hack/verify-repo-truth.sh` could not reach the GitHub API. Open: the sandbox gets 403 from the API; CI runs it.

Kind: diff
Reviewer: fresh-context subagent B (Claude Code `general-purpose`, independent fresh session given only `docs/superpowers/review-prompt.md` and its five inputs)
Model: Anthropic Claude Opus 5.5 (same family as the author)
Base: ab2adcc5429ba1025492eebc8dcfe7f6bc001ea4
Head: 58e1faa6a3555051d15ae1bfab8accaa2efc28f0
Date: 2026-11-28
Verdict: revise

1. Major: `clears` told the caller to pass `--autonomy full` when that flag cannot clear the denial. Fixed in e2958c0, as spec review item 1.

2. Major: `--format json` wrote no document when the command failed before the run. Fixed in e2958c0, as spec review item 2.

3. Minor: the AGENTS.md exit-code list omitted 8 and 9, and the doc-facts gate could not see them. Fixed in e2958c0, as spec review item 3.

4. Minor: the json stream routing and the recorder could be removed from `agentCmd` without a test failing. Fixed in e2958c0: `TestAgentCmdJSONStdoutIsOneDocument` and `TestAgentCmdRecordsDeniedStep`; each mutation now fails at least one of them.

5. Minor: notices written to the agent's Output vanished in json mode. Fixed in e2958c0: json mode sends the agent's Output to stderr instead of `io.Discard`; `TestAgentCmdJSONStdoutIsOneDocument` checks the streamed text reaches stderr.

6. Major: the spec gate failed and the reviews were not recorded. Fixed in this section.

Questions:

- `--max-turns 0` reported `turn_ceiling` with 0 turns. Verified: since e2958c0, `-p` refuses `--max-turns` below 1; `TestAgentCmdPrintRejectsZeroMaxTurns`.
- A deadline during the last round's tools reported `turn_ceiling`. Verified: since e2958c0, a ceiling stop with the request context done is `failed`; `TestPipedRunDeadlineOnLastRoundIsFailed`.
- Background work that confirms after `pending_approvals` is read would be missed. Verified: `run_background` and `spawn_subagent` are never-tools (`internal/agent/scope.go:96-100`), so a `-p` run cannot start such work.
