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
- Reviewed HEAD: `6a2cb99`
- Last updated: `2026-03-24 12:27 EDT`
- Note: `Reviewed HEAD` is the source state this worklog update was based on. A worklog-only commit will advance repository HEAD.

## Active Tasks

| ID | Title | Owner | Status | Scope | Dependencies | Files | Proof Required |
| --- | --- | --- | --- | --- | --- | --- | --- |
| AX-001 | Runtime hygiene baseline | Claude (for Warp) | done | Verified binary resolution, removed V8 alias, removed stale aliases, deleted broken binary | AX-000 | `.zshrc`, `/usr/local/bin/axis` removed | See completed table |
| AX-002 | Gemini audit revision | Gemini CLI | in_review | Re-issue the repo audit against live source and correct unsupported CLI-surface claims | AX-000 | `cmd/axis/`, `README.md`, `docs/phase1_spec.md`, cited source files | Updated audit with exact file references and corrected command surface |
| AX-004 | Documentation sync with live CLI | Gemini CLI | pending | Align docs with implemented `axis task place`; do not document unsupported flags like `-f` or `--config` | AX-002 | `README.md`, `docs/phase1_spec.md`, optionally `docs/white_paper_v1.md` | Markdown diff, matching `--help` output, and a commit |
| AX-005 | Link-local address blindspot review | Warp | pending | Decide whether `169.254.x.x` and similar link-local addresses should be exposed, filtered, or tagged explicitly | AX-002 | `internal/facts/local.go` | `axis facts` output demonstrating intended address behavior and a commit |
| AX-006 | Local metric collection hardening | Claude (Antigravity) | pending | Reduce brittle local disk/RAM command parsing while preserving current behavior and adding coverage where practical | AX-005 | `internal/facts/local.go`, related tests | Passing tests, runtime verification, and a commit |
| AX-007 | SSH identity resolution hardening | Claude (Antigravity) | pending | Respect non-default SSH identity configuration or explicitly document the current limitation | AX-002 | `internal/transport/ssh.go`, related tests/docs | Verified non-default identity behavior or documented limitation, plus a commit |
| AX-022 | Cached read surface expansion | Codex | pending | Decide whether explicit cached reads should extend beyond `axis status` into placement, context, or MCP without introducing hidden fallback behavior | AX-020 | `cmd/axis/status.go`, `cmd/axis/task.go`, `internal/api/`, `internal/mcp/`, docs | Design note or scoped implementation with explicit verification |
| AX-023 | Daemon freshness policy | Codex | pending | Define whether daemon refresh should stay timer-only or gain additional explicit/manual refresh semantics beyond invalidate | AX-020 | `internal/daemon/`, `cmd/axis/daemon.go`, runbooks/docs | Clear operator-facing policy, tests if code changes, and a commit |

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
