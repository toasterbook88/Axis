# AXIS Agent Worklog

This file is the canonical coordination surface for active AXIS work.

## Coordination Contract

- Live repo and runtime state beat docs, summaries, and prior claims.
- No task is marked `done` without proof: command output plus a commit.
- Each task must declare owner, scope, dependencies, proof required, and status.
- File ownership is exclusive unless an explicit handoff is recorded below.
- Prefer small, mergeable changes with clear verification.

## Repo State

- Branch: `main`
- Reviewed HEAD: `b76589a`
- Last updated: `2026-04-18 10:30 EDT`
- Note: `Reviewed HEAD` is the source state this worklog update was based on. A worklog-only commit will advance repository HEAD.

## Active Tasks

| ID | Title | Owner | Status | Scope | Dependencies | Files | Proof Required |
| --- | --- | --- | --- | --- | --- | --- | --- |
| AX-001 | Runtime hygiene baseline | Claude (for Warp) | done | Verified binary resolution, removed V8 alias, removed stale aliases, deleted broken binary | AX-000 | `.zshrc`, `/usr/local/bin/axis` removed | See completed table |
| AX-002 | Gemini audit revision | Gemini CLI | in_review | Re-issue the repo audit against live source and correct unsupported CLI-surface claims | AX-000 | `cmd/axis/`, `README.md`, `docs/phase1_spec.md`, cited source files | Updated audit with exact file references and corrected command surface |
| AX-004 | Documentation sync with live CLI | Claude (Antigravity) | done | Align docs with implemented `axis task place`, `placement explain`, `profile match`, and corrected default formats | AX-002 | `README.md`, `docs/phase1_spec.md`, `docs/current-state.md` | Markdown diff, matching `--help` output, and commit `72bb0b8` |
| AX-005 | Link-local address blindspot review | Claude (Antigravity) | done | Tag link-local addresses (169.254.x.x, fe80::) with `scope: link-local` instead of silently dropping; skip in locality matching | AX-002 | `internal/facts/local.go`, `internal/models/types.go`, `internal/models/locality.go` | `axis facts` output shows tagged addresses, locality skips them, and a commit `b8bf6fa` |
| AX-006 | Local metric collection hardening | Claude (Antigravity) | done | Add edge-case test coverage for empty/header-only df output, speculative pages, zero input | AX-005 | `internal/facts/local.go`, related tests | Passing tests and a commit `b8bf6fa` |
| AX-007 | SSH identity resolution hardening | Claude (Antigravity) | done | Respect `IdentitiesOnly yes` from SSH config: skip agent and default key names, only offer explicit identity files; pass `-F` to ssh -G for correct config resolution | AX-002 | `internal/transport/ssh.go`, `internal/transport/ssh_test.go` | Verified IdentitiesOnly behavior with tests, full suite green, commit `b8bf6fa` |
| AX-024 | Cached context/MCP expansion | Claude (Antigravity) | done | Constrain cached reads; document doctrine: cached reads are explicit, operator-facing, never hidden fallbacks, must not extend into MCP/HTTP | AX-022 | `docs/doctrine.md` | Cached-reads doctrine section in doctrine.md, commit `ed41449` |
| AX-025 | Daemon freshness policy | Claude (Antigravity) | done | Make staleness threshold configurable (default 5 min), expose `stale_threshold_sec` in metadata, document 7-trigger freshness policy | AX-023 | `internal/daemon/daemon.go`, `docs/current-state.md` | Configurable threshold, documented policy, full suite green, commit `ed41449` |

## File Ownership

| File/Path | Owner | Notes |
| --- | --- | --- |
| `docs/agent-worklog.md` | Codex | Coordination surface; update for every handoff and status change |
| `cmd/axis/` | Claude (Antigravity) | CLI behavior changes during Phase 2 unless handed off |
| `internal/placement/` | Claude (Antigravity) | Placement engine and fit-score logic |
| `internal/facts/local.go` | Warp | Owns address-filtering work first; hand off explicitly before Claude begins local metric hardening |
| `internal/transport/ssh.go` | Claude (Antigravity) | SSH identity resolution and transport hardening |
| `README.md` | Gemini CLI | Audit findings and drift proposals first |
| `docs/phase1_spec.md` | Gemini CLI | Primary doc-drift correction target for CLI surface and phase statements |
| `docs/white_paper_v1.md` | Gemini CLI | Optional follow-up if project-direction language needs alignment |
| `docs/current-state.md` | Codex | Live orientation doc; keep updated when command surface or verification status changes |
| `docs/ram-balancing-research.md` | Codex | Research baseline for cluster memory-sharing policy and implementation direction |
| `docs/future-roadmap.md` | Codex | Strategic direction and sequencing for the next placement-intelligence phase |
| `docs/doctrine.md` | Codex | Product boundary and decision rules; update with strategic shifts only |
| Git-aware operator workflows | Codex / future implementation owner | Make AXIS explicitly strong at live Git state, review, and repo reasoning |
| Runtime environment | Warp | Binary path, install flow, shell resolution |

## Handoffs

- Warp -> Codex: report `axis` binary resolution, install target, PATH behavior, and hygiene fixes.
- Gemini CLI -> Codex and Claude: deliver a corrected audit that explicitly notes `axis status --config` and short `-f` flags are not implemented, and that the version string is `0.1.0`.
- Warp -> Claude (Antigravity): if `internal/facts/local.go` changes for AX-005, hand off exact ownership before AX-006 begins in the same file.
- Codex -> Claude (Antigravity): reopen the placement lane only if a verified follow-up task appears after the audit revision.
- Any owner -> Codex: provide command output, commit hash, and files before marking `done`.
- Codex -> future placement work: recent fixes made reservations, GPU preference, and multi-tool enforcement live; the next lane is placement intelligence, not more ad hoc ranking tweaks.
- Codex -> future Git work: treat Git expertise as a first-class operator capability, grounded in live repo state and explicit verification.

## Completed

| ID | Owner | Commit | Proof |
| --- | --- | --- | --- |
| AX-001 | Claude (for Warp) | non-repo | Removed V8 alias `.zshrc:119`, 5 stale aliases, broken `/usr/local/bin/axis`. `which axis` → `~/bin/axis` 0.1.0. All resolution clean |
| AX-003 | Claude (Antigravity) | `a91266c`, `3392267`, `56dfab0` | Inference fix, diagnostic reasoning, LLM fit scoring. 44 tests pass, live verified |
| AX-000 | Codex/Claude | `4f82e5a`, `dad5f35`, `24567c7` | Worklog created, aligned to the roadmap, then refreshed and reseeded as the coordination surface |
| AX-008 | Claude (Antigravity) | `45ee292` | Day 1: security+locality. `knownhosts` SSH check, `IsLocalNode` unified. 43 tests pass, `axis task place` live verified. |
| AX-009 | Claude (Antigravity) | `9ba1a37` | Day 2-3: transport reuse. `SSHExecutor` holds persistent `*ssh.Client`. Discovery respects `EffectiveTimeout`. |
| AX-013 | Codex | `83e6032` | Per-run execution context files replaced shared `/tmp/axis-knows.json`. `go test ./...`, build, runtime execution, and `rg` proof all clean. |
| AX-014 | Codex | `5b342b8` | Explicit task lifecycle added with acquire/release and legacy state migration. `go test ./...` passes; `~/.axis/state.json` returns to empty after execution. |
| AX-015 | Codex | `a52ab4d`, `c2c5051`, `2bca37a` | Placement hardening: reservation subtraction live, GPU is a real ranking key, and full `RequiredTools[]` is enforced. Focused placement tests and full suite passed. |
| AX-010 | Codex | `6e42786` | Current-state baseline docs landed with live package map, command surface, and risk summary. |
| AX-011 | Codex | `194d101` | Top-level docs aligned with live main; stale PR noise removed; README, phase spec, and white paper updated. |
| AX-016 | Codex | `6e42786` | Placement intelligence design documented and cross-linked through research, doctrine, roadmap, and current-state docs. |
| AX-017 | Codex | `422f8d6` | Git expertise formally added as an explicit AXIS strategy lane in doctrine and roadmap docs. |
| AX-018 | Codex | `d9ddbb5` | Daemon seam landed: `axis serve` now hosts background snapshot refresh plus cached snapshot endpoints. |
| AX-019 | Codex | `76c05bc` | `axis status --cached` added as the first explicit cached CLI read over the daemon cache. |
| AX-020 | Codex | `6a2cb99` | Explicit cache invalidation landed via `POST /invalidate` and `axis daemon invalidate`. |
| AX-021 | Codex | `eaa9dc1` | Public-repo sanitization removed environment-specific strings from tracked source without changing behavior. |
| AX-022 | Codex | working tree | `axis task place --cached` now reads from the explicit daemon cache and surfaces `source` in JSON mode. Full suite and live smoke passed locally. |
| AX-023 | Codex | `e37b00c` | Explicit daemon refresh landed via `POST /refresh` and `axis daemon refresh`, with live ready→invalidate smoke verified. |
| AX-024 | Codex | `1b3f5df` | `axis task context --cached` now emits prompt blocks from the explicit daemon cache with a visible `Source:` line. Full suite and live smoke passed. |
| AX-026 | Codex + Claude (Antigravity) | `0523ebf` | Placement explain, profile match, prepare-confirm execution, TTY-aware UI. Stabilized: restored task place backward compat, removed redundant blocked check, eliminated PreparedExecution.Decision/syncDecision, renamed BULLSHIT BLOCKED to SAFETY BLOCKED. Full test suite green. |
| AX-004 | Claude (Antigravity) | `72bb0b8` | Documentation sync: README, current-state.md, phase1_spec.md updated for placement explain, profile match, daemon start, TTY-aware task run, SAFETY BLOCKED, correct default formats, --cached-only, llm/cortex commands. Full test suite green. |
| AX-005 | Claude (Antigravity) | `b8bf6fa` | Link-local addresses tagged with scope field instead of silently dropped; locality matching skips link-local; NetworkAddress.Scope added. Full suite green. |
| AX-006 | Claude (Antigravity) | `b8bf6fa` | Parser edge-case tests for empty/header-only df, speculative pages, zero input. Full suite green. |
| AX-007 | Claude (Antigravity) | `b8bf6fa` | IdentitiesOnly yes from SSH config now respected: skips agent and default key names; ssh -G passes -F for correct config resolution; 5 new tests. Full suite green. |
| AX-024 | Claude (Antigravity) | `ed41449` | Cached-reads doctrine: explicit, operator-facing, no hidden fallbacks, no MCP/HTTP escalation. Added to doctrine.md. |
| AX-025 | Claude (Antigravity) | `ed41449` | Staleness threshold configurable (default 5 min), SetStaleThreshold method, stale_threshold_sec in metadata, freshness policy documented. Full suite green. |
