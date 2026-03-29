# AXIS Documentation

## Start Here

- [Current State](current-state.md) — current orientation doc for the live repo, command surface, coverage snapshot, and known weak spots
- [Agent Worklog](agent-worklog.md) — active coordination surface, file ownership, and current task tracking
- [RAM Balancing Research](ram-balancing-research.md) — research-backed design note on how AXIS should model cluster RAM sharing and balancing

## Design Docs

- [Phase 1 Spec](phase1_spec.md) — detailed design for Phase 1: CLI, fact collection, and snapshot assembly
- [White Paper v1](white_paper_v1.md) — project motivation, architecture overview, and planned direction
- [Doctrine](doctrine.md) — product boundary, decision rules, and execution principles
- [Future Roadmap](future-roadmap.md) — strategic options, phased direction, and feature guardrails

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

| Phase | Status | Description |
|---|---|---|
| Phase 1 | Core complete; retained as the historical spec boundary | CLI bootstrap, local/remote fact collection, and `ClusterSnapshot` output remain the foundation |
| Phase 2 | **Active on `main`** | Reservation-aware placement, execution surfaces, MCP, daemon-backed cached reads, and utility layers are shipped; current effort is hardening and truth-alignment |
