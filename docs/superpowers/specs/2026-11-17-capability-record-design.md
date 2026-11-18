# Capability record: evidence-backed levels for what AXIS can be trusted to do

Date: 2026-11-17. Status: implemented on `feat/capability-record`; spec review approve-with-changes,
both findings fixed. This spec was written after the code, which is out of order under AGENTS.md §8;
the maintainer accepted that order in writing on PR #659 on 2026-11-18, because the spec has since had
fresh-context review that changed the design.

## Problem

AXIS reports facts, ranks placements and runs guarded tasks, and the operator widens what agents may
do by hand: autonomy modes in `internal/agent/autonomy.go`, A2A scope tiers in `internal/a2a/card.go`,
the approval queue in `internal/a2a/approval.go`. Nothing records how often a capability has been
right. An operator deciding whether to loosen a gate has no track record to read, only memory.

A live comparison of `axis status` against independent probes of each machine found the facts
mostly right but not entirely: one node reported a GPU whose model field was an `nvidia-smi` error
sentence tagged `cuda`. Without a record, a defect like that stays invisible until something places
work on it.

## Non-goals

- No surface reads a level to allow or refuse an action. The record is advisory: a ledger of what was
  observed, not an authority path. Wiring levels into autonomy or A2A is a later change with its own spec.
- No protection against a writer running as the same user. Anything that can run `axis capability`
  can rewrite the journal and the state together. `verify` detects cheaper tampering only.
- No per-agent records. The `actor` field exists in the schema, but only `axis` holds a record until
  an actor's identity can be verified.
- No new network listener and no daemon change. Node checks use the published snapshot and the existing
  SSH transport.
- `facts.gpu` checks NVIDIA GPUs only. Apple and other GPUs are out of scope.

## Design

Levels, in order: `unproven`, `advisory`, `supervised`, `trusted`. A record starts at `unproven` with
a ceiling of `advisory`, the highest level that cannot act.

Observations are `pass`, `fail` or `inconclusive`, with evidence (`live-probe`, `file`, `peer`, `none`)
and an origin the store sets:

- A pass counts only from one of AXIS's own checkers (`axis capability check`, origin `checker`) with
  grounded evidence: a named live probe, or a file with its content hash. A pass reported with
  `axis capability record` (origin `external`) is kept and counted as inconclusive, because a caller
  can name a probe it never ran.
- Every failure counts, whoever reports it. Only a checker failure with grounded evidence can be
  critical (demote to `unproven`); any other failure demotes one level. The operator drops a capability
  further by lowering its ceiling.
- Promotion is one level at a time, up to the ceiling, when all hold: the streak of counted passes
  reaches `promote_after` (default 20, floor 10); the pass rate over the last `window` outcomes is at
  least `min_rate` (default 0.98, floor 0.9); the streak covers at least `min_span` of time (default
  6h, floor 1h, a whole number of minutes); and no subject has an open failure. A failure from a checker opens its subject; a
  later checker pass on that same subject closes it, or the operator closes it with `axis capability
  resolve <capability> <subject> --reason ...` for a machine that was retired, renamed or will not be
  checked again. A resolution is a journal entry with its reason; it never raises a level. Any level
  change resets the streak.
- The store stamps every entry's time, so a caller cannot backdate observations to fake the span.

Storage, under `~/.axis/capability/`:

- `journal.jsonl`: every observation and ceiling change, one JSON line each, numbered from 1, each
  carrying the SHA-256 of the line before it and the thresholds in force. It is synced to disk before
  the state is written.
- `state.json`: a cache of replaying the journal, with `next_seq`, `head` and `journal_bytes`. A write
  that stopped after the journal is counted on the next write, without reusing a sequence number.
- `transitions.jsonl`: a derived log of level and ceiling changes for readers. It is appended after the
  state is saved, so a failed append can lose a transition from it; `verify` compares it, whole transition by
  whole transition, with the transitions a replay of the journal produces, and `verify --repair-transitions` rebuilds it from the
  journal without touching the journal or the state.
- Policy (thresholds and descriptions only) is read from `~/.axis/capabilities.yaml`; a policy below the
  floors is refused, and the state machine raises any thresholds it is given to the floors.

Checkers: `facts.hardware` compares OS, architecture, CPU count (the CPUs the collector may use, as
`nproc` counts them), RAM total and root disk total; a reading the probe did not take is inconclusive,
while a mismatch among the readings it took still fails. `facts.gpu` compares the NVIDIA GPU models
AXIS lists (ignoring order, case and spacing) with what the NVIDIA driver shows under
`/proc/driver/nvidia`, fails any GPU entry whose model is an error message, and passes when AXIS lists
no NVIDIA GPU and no NVIDIA driver is loaded. A driver GPU entry the probe cannot read makes the check
inconclusive, since the GPU count is then unknown. Without `--node`, a check runs the fact collector on this
machine against system calls; with `--node`/`--all-nodes` (and `--except` to leave machines out) it
compares the node's entry in the
published snapshot with a read-only shell probe run over the AXIS SSH transport. A node AXIS reports
unreachable, or that the probe cannot reach, is inconclusive. Subjects are `node:<snapshot name>`,
lowercased. A check without `--node` names this machine the same way, by the snapshot node whose
recorded hostname is this machine's, and falls back to the OS hostname only when there is no snapshot
or no such node.

Commands: `axis capability list|show|check|record|ceiling|resolve|verify|export`. Each takes
`--format text|json|yaml` and refuses any other value; `export` writes JSON unless it is `yaml`. Level changes are
emitted as `capability.promoted`, `capability.demoted`, `capability.ceiling.changed` and
`capability.failure.resolved` events.

Tests: `internal/capability/*_test.go` (state machine edges, floors, span, open failures, journal
chain, catch-up after an interrupted write, verify against edited, removed and truncated lines, torn
last line, a lost transitions write found and repaired, four concurrent writers through the store lock
in TestConcurrentWritersNeverLoseOrRepeatAnEntry), `cmd/axis/capability*_test.go` (each command,
structured output, event emission, checker verdicts, node mode with a stubbed snapshot and probe).

## Alternatives

- **Hand-set levels only (today).** No evidence behind a level; rejected because it is the problem.
- **Count every observation a caller reports.** Rejected: an audit raised a capability to `advisory`
  with twenty invented probe names.
- **Rate gate without a time span.** Rejected: twenty back-to-back local checks promoted in 83 seconds.
- **One record per (capability, subject).** Simpler to reason about per machine, but a capability that
  is right on nine machines and wrong on one would still reach a level for nine of them. Open
  failures keep one record per capability and block it while any machine fails.
- **Merkle log (RFC 9162 style) instead of a linear hash chain.** Gives inclusion proofs a single
  operator does not need; a linear chain is enough to detect edits and is simpler to replay.
- **A separate service account as the writer.** Would stop same-user rewrites, but is access hardening
  this project does not do. The stated precondition is to anchor the journal head in backups instead.

## Research

Prior art:

- Progressive delivery gates promotion on repeated analysis runs with success thresholds and failure
  limits, which is the shape used here: Argo Rollouts analysis,
  https://argo-rollouts.readthedocs.io/en/stable/features/analysis/
- Append-only, hash-linked logs make tampering detectable without trusting the writer at read time:
  Certificate Transparency, RFC 9162, https://www.rfc-editor.org/rfc/rfc9162
- In this repo: `internal/failures` (scoped failure memory with expiry), `internal/state`
  observations (exact-scope execution records), and `internal/repairs` (advisory receipts) are records
  that inform without granting authority; this record follows the same rule.

Measurement, from runs against a live cluster in throwaway `AXIS_HOME` directories:

- Before the span and open-failure rules, twenty local checks reached `advisory` in 83 s, and 47 checks
  of one healthy node reached `advisory` while another node's `facts.gpu` failure stood. After them,
  25 rapid checks and 60 single-node checks did not promote.
- A sweep of ten nodes runs in under two minutes; each entry costs one fsync. Replay in `verify` is linear
  in the journal; no optimization applies at hourly sweeps.

## Review

Kind: spec
Reviewer: fresh-context session (Claude Code, new session on another machine)
Model: Anthropic Claude
Base: 4465fa4a81a14c19fbfbdca00feed2f976da0e0e
Head: 8c1509f72af4599b1aa81e8480d7bb4b11b90cd2
Date: 2026-11-17
Verdict: approve-with-changes

1. Major: a checker failure on a machine that is never checked again blocked promotion for good, with no operator path to clear it, since an inconclusive check never closes a failure and editing the journal breaks verify. Fixed in e55568cf: `axis capability resolve` journals an operator entry that closes one open subject with its reason and never raises a level; Design names it; TestResolveClosesOneOpenFailureForAMachineThatIsGone and TestCapabilityResolveCommand cover it.

2. Minor: a check without `--node` named its subject after the OS hostname while `--node` checks use the snapshot name, so on a machine whose names differ the two never met. Fixed in e55568cf: local checks take the snapshot name of the node whose recorded hostname is this machine's and fall back to the hostname; TestALocalCheckUsesTheSnapshotNameOfThisMachine covers both paths.

Questions:

- Is writing this spec after the code acceptable under AGENTS.md section 8? Open: only the maintainer can rule on it; the PR asks.
- Does any real node have an OS hostname that differs from its snapshot name? Verified: in the cluster this was built against, two of eleven nodes do (compared the names in `axis status` with each node's recorded hostname), so finding 2 was reachable.
- Are capability events written when no daemon runs, and do they reach Cortex? Verified: `emitCapabilityTransition` enqueues to the event worker and `main` flushes it on exit, which writes the event log (TestCapabilityTransitionsReachTheEventLog); the root command registers the Cortex client when one is configured, and `capability.*` names are not on the telemetry block list in `internal/events`, so they are forwarded. Delivery to a live Cortex has not been observed yet, because no live promotion has happened.

Kind: diff
Reviewer: the author, not fresh-context (at the maintainer's direction, while the requested fresh-context reviews had not started)
Model: Anthropic Claude
Base: ab1461a385311d5cb18e4ba43db76647c0092cbb
Head: 7b80538c27ddd350e49addee0c21406106c61f9e
Date: 2026-11-18
Verdict: approve-with-changes

1. Minor: `--node` naming this machine dialed it over SSH when its OS hostname matched neither its config name nor its config hostname, which is true of two nodes in the cluster this was built against; the check then depended on SSH to itself. Fixed in 452535db: the probe also uses a local shell when the snapshot recorded this machine's hostname for the node; TestProbeRunnerUsesALocalShellForThisMachine.

2. Minor: `resolve` matched subjects exactly, so `node:ALPHA` did not close an open `node:alpha` (a probe test returned "has no open failure"). Fixed in 452535db: the command writes node subjects in their canonical form; TestCapabilityResolveCommand resolves with the other case.

3. Minor: no test asserted that `resolve` emits `capability.failure.resolved`. Fixed in 452535db: TestCapabilityResolveCommand reads it from the event log.

4. Minor: `--except` was in the code and its help but not in this spec. Fixed in this file: Design names it.

Questions:

- Does an author review count toward the diff reviews section 8 asks of a change this size? Open: it is not independent; it is recorded because the maintainer asked for it while the requested fresh-context diff reviews had not started, and the maintainer decides whether it counts.

Kind: spec
Reviewer: fresh-context session (Hermes agent)
Model: not stated by the reviewer
Base: 4465fa4a81a14c19fbfbdca00feed2f976da0e0e
Head: 8c1509f72af4599b1aa81e8480d7bb4b11b90cd2
Date: 2026-11-17
Verdict: revise

1. Blocker: the spec had no review record and failed the spec gate. Fixed in 99b76538: the first spec review is recorded above and verify-specs passes.

2. Blocker: the spec was written after the code, which needs the maintainer's written acceptance. Fixed in this file: the maintainer accepted the order on PR #659 on 2026-11-18, and the Status line cites it.

3. Major: local checks named the subject by hostname and node checks by snapshot name, so an open failure could never close on a node whose names differ. Fixed in e55568cf: local checks take the snapshot name of this machine; the same finding as the first spec review's second.

4. Major: a failed `transitions.jsonl` append after the state save lost a level or ceiling change from the derived log for good, silently. Fixed in 7436eca8: `verify` compares the log with the journal replay and `verify --repair-transitions` rebuilds it; TestVerifyFindsAndRepairsALostTransition injects the failed write.

5. Major: the spec claimed a concurrent-writers test that did not exist. Fixed in 7436eca8: TestConcurrentWritersNeverLoseOrRepeatAnEntry runs four stores on one directory and checks 100 unique sequence numbers and a clean verify, under the race detector.

Questions:

- Should a driver GPU entry the probe cannot read make `facts.gpu` fail? Verified: it should not; the probe now reports it and the check is inconclusive, fixed in 7436eca8 with a TestJudgeGPU case.
- Does raising the ceiling after lowering it keep the old open failures? Verified: yes, by design, since open failures record evidence rather than level; TestCeilingChanges now asserts it.

Kind: spec
Reviewer: the author, re-reviewing the Hermes findings only (Hermes out of quota; at the maintainer's direction)
Model: Anthropic Claude
Base: 4465fa4a81a14c19fbfbdca00feed2f976da0e0e
Head: 6ace14182fa9e99fc6b40cfdc6c396e48e4508c9
Date: 2026-11-18
Verdict: approve

Note: In a fresh clone at Head, each Hermes finding and question was rerun: verify-specs failed only on this missing re-review, the Status line cites the maintainer's acceptance, and the tests for findings 3 to 5 and both questions pass, the concurrency test under the race detector. Two mutations confirmed the new tests can fail: with verify no longer checking transitions.jsonl, TestVerifyFindsAndRepairsALostTransition fails; with the store lock removed, TestConcurrentWritersNeverLoseOrRepeatAnEntry fails at once. This re-review is not independent.

Kind: diff
Reviewer: fresh-context subagent run by the Codex agent (reviewer A)
Model: OpenAI GPT (version not stated by the reviewer)
Base: 60eba8a04a8cd79a6bfcf612c293ff3a4f645a4f
Head: b7afa9361806f239613ceda19246469a0a72bec8
Date: 2026-11-17
Verdict: revise

1. Major: when the local probe stopped early, the check threw away the readings it had taken, so a measured CPU mismatch next to a failed statfs was inconclusive instead of a failure. Fixed in d3b57c85: the taken readings are compared, a mismatch among them fails, and otherwise the result is inconclusive and names the probe error; two TestFactsHardwareCheckerVerdicts cases, both failing on the old code.

2. Minor: check --format json printed the observation before the store stamped it, so its time, actor, origin and counted were empty while the journal held them. Fixed in d3b57c85: Record and RecordCheck also return the observation as committed and the command prints it; TestCapabilityCheckJSONMatchesTheJournal.

Kind: diff
Reviewer: the author, re-reviewing the two fresh-context findings only (at the maintainer's direction; the reviewer has also been asked)
Model: Anthropic Claude
Base: 60eba8a04a8cd79a6bfcf612c293ff3a4f645a4f
Head: 28501fc3e1f1b21da18b4cb4cf98d8356c96b39a
Date: 2026-11-18
Verdict: approve

Note: In a fresh clone, the reviewer's own two probe tests, copied in unchanged (TestReviewPartialLocalProbeKeepsMeasuredMismatch and TestReviewCheckJSONMatchesJournalObservation), pass at Head and fail at the reviewed head b7afa936. With the old capability_check.go restored, the two new TestFactsHardwareCheckerVerdicts cases fail. The full hermetic suite passes at Head. This re-review is not independent.

Kind: diff
Reviewer: fresh-context subagent run by the Codex agent (reviewer A), re-review
Model: OpenAI GPT (version not stated by the reviewer)
Base: 60eba8a04a8cd79a6bfcf612c293ff3a4f645a4f
Head: 28501fc3e1f1b21da18b4cb4cf98d8356c96b39a
Date: 2026-11-17
Verdict: approve

Note: Both of reviewer A's probes pass unchanged at Head; every file changed since b7afa936 was inspected and no new defect found; the capability and command suites and the capability race tests pass.

Kind: diff
Reviewer: fresh-context subagent run by the Codex agent (reviewer B), independent of reviewer A
Model: OpenAI GPT (version not stated by the reviewer)
Base: 60eba8a04a8cd79a6bfcf612c293ff3a4f645a4f
Head: b7afa9361806f239613ceda19246469a0a72bec8
Date: 2026-11-17
Verdict: revise

1. Major: with the NVIDIA GPU directory present but not listable, the probe's glob matched nothing and reported the driver with zero GPUs, so `facts.gpu` earned a counted pass without seeing the list. Fixed in 8b198bfa: an unlistable directory, a GPU directory without a readable information file, or a file naming no model marks the list unreadable and the check inconclusive; TestTheNodeProbeNeverPassesAGPUListItCannotRead runs the real probe script on each case, and all three fail on the old script.

2. Major: a local probe that stopped early discarded the readings it had taken. Fixed in d3b57c85: reviewer A's first finding.

3. Minor: structured check output printed the unstamped observation. Fixed in d3b57c85: reviewer A's second finding.

Kind: spec
Reviewer: fresh-context session (Hermes agent), re-review of its own findings
Model: not stated by the reviewer
Base: 60eba8a04a8cd79a6bfcf612c293ff3a4f645a4f
Head: 6ace14182fa9e99fc6b40cfdc6c396e48e4508c9
Date: 2026-11-17
Verdict: approve

Note: Each Hermes finding and both questions were checked at Head in a read-only clone (spec review record, the maintainer's acceptance, local and node subjects, transitions repair, concurrent writers under the race detector, unreadable driver entry, ceiling raise); no findings; the full hermetic suite passes. This is the reviewer's own re-review, independent of the author's re-review above.

Kind: diff
Reviewer: fresh-context subagent run by the Codex agent (reviewer A), final re-review
Model: OpenAI GPT (version not stated by the reviewer)
Base: 60eba8a04a8cd79a6bfcf612c293ff3a4f645a4f
Head: b7b19338e61c5311252a8a624847941208ee9ec9
Date: 2026-11-17
Verdict: approve

Note: Approves the cumulative diff; every file changed since 28501fc3 inspected with no new defect; the original probes, the new GPU-listing test and an extra probe with an inaccessible child GPU directory pass; capability and command suites pass.

Kind: diff
Reviewer: fresh-context subagent run by the Codex agent (reviewer B), final re-review
Model: OpenAI GPT (version not stated by the reviewer)
Base: 60eba8a04a8cd79a6bfcf612c293ff3a4f645a4f
Head: b7b19338e61c5311252a8a624847941208ee9ec9
Date: 2026-11-17
Verdict: approve

Note: All three findings resolved; every file changed since b7afa936 inspected with no new defect; all three original probes pass in a fresh clone, changed only for the new RecordCheck return values; capability, command and race suites and the repository gates pass.
