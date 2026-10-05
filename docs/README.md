# AXIS Documentation

## Start Here

- [Current State](current-state.md) — current orientation doc for the live repo, command surface, coverage snapshot, and known weak spots
- [Agent Worklog](agent-worklog.md) — historical coordination log and file-ownership notes; not the current queue
- [RAM Balancing Research](ram-balancing-research.md) — research-backed design note on how AXIS should model cluster RAM sharing and balancing

## Design Docs

- [Phase 1 Spec](phase1_spec.md) — detailed design for Phase 1: CLI, fact collection, and snapshot assembly
- [White Paper v1](white_paper_v1.md) — project motivation, architecture overview, and planned direction
- [Doctrine](doctrine.md) — product boundary, decision rules, and execution principles
- [Future Roadmap](future-roadmap.md) — strategic options, phased direction, and feature guardrails
- [Apple Foundation Models Roadmap](plans/apple-foundation-models-roadmap.md) — planning only: AFM probe rigor, remote placement, guarded helper exec, ai.yaml cascade. Not shipped behavior.
- [Emergency GPU Eviction & Observability Proposal](plans/emergency-gpu-eviction-and-observability-proposal.md) — proposal. Evict and resume on the eviction branch include an observed VRAM delta and a generation and owner guard. `--drain-timeout`, Cortex, journald, and zero-bloat log rotation are not shipped.

## Runbooks

- [Local Assist](runbooks/local-assist.md) — operator verification guide for the local assist surface
- [MCP Network Tools](runbooks/mcp-network-tools.md) — diagnostics and operator notes for MCP network tooling

## Source Of Truth

When docs disagree:

1. Live code
2. `docs/current-state.md`
3. `docs/agent-worklog.md`
4. Older design docs and white paper

## Phase Tracking

See [phase-tracking.md](phase-tracking.md) for current status. Phases 1–7 are complete.
