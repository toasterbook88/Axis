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
  around it.

## Non-goals

- The interactive REPL, transcript console, slash commands, subagents and A2A keep calling `Run`
  and keep its return values. `Run` still returns `nil` at the ceiling.
- The positional one-shot without `-p` keeps its exit 0 at the ceiling; its output contract is
  pinned by `TestCharOneShotLegacyContractUnchanged`.
- The loop does not stop at the first denied confirm. The model still sees the denial and may
  carry on with other tools; stopping early would change every `-p` run.
- No streaming event format (`stream-json`), no resume of a needs-input run, no question protocol
  back to the caller beyond listing the denied requests.

## Design

1. `internal/agent`: `runLocked` records a `RunOutcome{Stop, Turns, MaxTurns}` on every return,
   read through `(*Agent).LastRunOutcome()`. `Stop` is `answered` (a round with no tool calls),
   `turn_ceiling` (loop exhausted) or `failed` (`Run` returned an error). `Turns` counts model
   rounds that returned a response. The field has its own mutex so it can be read without
   `runMu`.
2. `cmd/axis`: `pipedConfirm(w, rec)` keeps its notices and also appends each denial to a
   `pipedApprovals` recorder: tool, the confirm text clipped to 400 runes, risk score, and what
   clears it (`--autonomy full`, or `interactive approval` for consult_model and a specific safety
   rule's ask, scores 75 to 79).
3. `classifyPipedRun(err, stop, pending)` maps the run to an outcome and exit code, first match
   wins:

   | outcome | exit | when |
   |---|---|---|
   | `failed` | 4 (`ExitErrCommandFail`, unchanged) | `Run` returned an error |
   | `needs_input` | 9 (`ExitAgentNeedsInput`, new) | at least one confirm was denied |
   | `turn_ceiling` | 8 (`ExitAgentTurnCeiling`, new) | loop used every round |
   | `answered` | 0 | otherwise |

   `needs_input` outranks the others because a rerun with approval may do more, even when the
   model answered around the denial. 8 and 9 are the next free codes after `ExitErrModelUnlisted`
   (7).
4. `--format text|json` on `axis agent`, following the repo's existing `--format` flags and the
   `axis.output/v1` envelope (`writeMachineOutput`, `docs/machine-output.md`). `json` requires
   `-p`; `yaml` is refused. With `json` the assistant stream goes to `io.Discard` and stdout carries
   one document: `command` `agent`, `status` the outcome, `ok` true only for `answered`, and
   `data` with `outcome`, `exit_code`, `turns`, `max_turns`, `final_text` (newest assistant
   message; empty on `failed`), `pending_approvals` (always an array), `error` (on `failed`),
   `usage` (`tokens_in`, `tokens_out`, `estimated`) and `elapsed_ms`. The document is written for
   every outcome, failures included; stderr keeps the badges and notices.
5. In text mode, non-zero outcomes return an `ExitCodeError` whose message names the cause
   (`agent stopped at the N-turn ceiling without a final answer`, `agent needs input: N step(s)
   need an approval a piped run cannot give`).
6. Docs: `docs/machine-output.md` gains a section with the payload and the table;
   `docs/current-state.md` gains one bullet; `-p` help lists the codes.

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
  resets between runs.
- `cmd/axis/agent_oneshot_outcome_test.go`: classification table including precedence; exit codes
  distinct; denial recording and `clears`; `runOneShotPiped` end to end with a scripted backend for
  answered (text and json, stdout holds exactly one document), ceiling (text and json, exit 8,
  `pending_approvals` serialised as `[]`), needs input (`--autonomy full` plus `git push`, exit 9,
  request listed), failed (exit 4, `error` set); `--format` validation.
- Manual: the built binary against a stub Ollama for the ceiling, answer and git-push cases.

## Review
