# AXIS Agent Worklog

> Single source of truth for multi-agent coordination on the AXIS project.

## Repo State

- **Branch:** main
- **HEAD:** 56dfab0
- **Last updated:** 2026-03-19T14:40 EDT

## Active Tasks

| ID | Title | Owner | Status | Scope | Files | Proof Required |
|:---|:------|:------|:-------|:------|:------|:---------------|
| P0-1 | Seed coordination worklog | Claude | complete | Create `docs/agent-worklog.md` | `docs/agent-worklog.md` | Commit + push |
| P1-1 | Runtime hygiene verified | Warp | pending | Confirm bare `axis` resolves correctly, install path clean | — | `which axis` + `axis version` output |
| P2-1 | LLM fit scoring | Claude | complete | Fit score 0-100, local detection, improved reasoning | `ranker.go`, `selector.go`, `task.go`, `types.go`, `placement_test.go` | 16/16 tests + live queries |
| P3-1 | Initial codebase audit | Gemini CLI | pending | Full codebase map, source-to-doc drift, gap analysis | — | Written audit report |
| P4-1 | Docs alignment | — | pending | Update README + white paper to reflect Phase 2 | `README.md`, `docs/white_paper_v1.md` | Diff showing new commands documented |

## File Ownership

| File/Path | Owner | Notes |
|:----------|:------|:------|
| `cmd/axis/*.go` | Claude | CLI commands and inference logic |
| `internal/placement/*.go` | Claude | Placement engine + tests |
| `internal/models/types.go` | Claude | Shared data types |
| `internal/facts/*.go` | Claude | Fact collectors (touch with care) |
| `internal/config/config.go` | Claude | Config loader + tests |
| `internal/snapshot/snapshot.go` | Claude | Snapshot builder + tests |
| `internal/transport/ssh.go` | Claude | SSH executor (stable, avoid churn) |
| `internal/discovery/discovery.go` | Claude | Discovery fan-out (stable) |
| `docs/agent-worklog.md` | Codex | Coordination surface |
| `docs/*.md` (other) | Gemini CLI | Architecture docs |
| `README.md` | Gemini CLI | Project README |
| `nodes.example.yaml` | — | Example config |

## Handoffs

_None active._

## Completed

| ID | Owner | Commit | Proof |
|:---|:------|:-------|:------|
| — | Claude | `632ebde` | Phase 1 scaffold: go module, models, config, transport, facts, discovery, snapshot |
| — | Claude | `07524b2` | CLI entry point: axis version, facts, status |
| — | Claude | `bd9612f` | Phase 2: deterministic task placement engine |
| — | Claude | `25653e0` | Test suites: snapshot 100%, config 96.3% |
| — | Claude | `a91266c` | Fix: keyword inference false positives |
| — | Claude | `3392267` | Fix: diagnostic placement reasoning |
| — | Claude | `56dfab0` | Feat: LLM fit scoring, local-node detection, improved reasoning |
