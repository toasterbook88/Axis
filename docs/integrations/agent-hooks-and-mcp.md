# Integrating AI Agents with AXIS (Hooks, MCP, and Lifecycle Events)

This document describes how AI coding/operations agents (Grok, Claude Code, Cursor, Gemini CLI, Hermes, OpenCode, Aider, etc.) can deeply integrate with the AXIS daemon.

## Design Goals

- Make AXIS a first-class participant in agentic workflows, not just a passive data source.
- Use open standards (primarily MCP) so any compliant agent can integrate.
- Provide both high-power (MCP) and simple (shell hooks) integration paths.
- Respect Axis philosophy: snapshot-first, truth-order, advisory surfaces, strong safety.

## Recommended Integration Layer: MCP + Lifecycle Events

As of 2026, the **Model Context Protocol (MCP)** is the emerging standard for extending AI agents with external capabilities.

AXIS should evolve from a mostly read-only MCP server into one that also exposes **lifecycle events** and accepts **hook registrations**.

### Current Axis Lifecycle Events

The canonical event names live in `internal/events/events.go`. They follow the naming convention `<domain>.<action>.<qualifier>`.

Current defined events (as of this writing):

- `task.placement.requested`
- `task.execution.pre`
- `task.execution.reserved`
- `task.execution.started`
- `task.execution.post`
- `task.execution.finished`
- `reservation.requested`
- `reservation.granted`
- `reservation.released`
- `daemon.refresh.pre`
- `daemon.refresh.post`
- `snapshot.collected`

These names are the source of truth. When adding new events, update the constants in `internal/events/events.go` first.

**Important Boundary**: All events are **advisory and observational only**. External agents may observe these events and provide advisory input via MCP, but they must never be granted direct control over execution, placement, or reservations. This respects the core Axis principle: "Optional HTTP, MCP, and execution surfaces must not weaken the fact plane." (See AGENTS.md)

### Two-Way Integration Model

1. **Agent calls AXIS** (already well supported)
   - Via MCP tools (`cluster_snapshot`, `placement_decision`, `triangle_request_lease`, etc.)
   - Via HTTP API

2. **AXIS calls back into the Agent** (the new "hooks" direction)
   - Agent registers callback tools with AXIS (or connects as an MCP client itself).
   - AXIS invokes those tools at key lifecycle points (especially around execution and reservations).

This model lets agents stay in control while still getting deep, real-time integration with the cluster daemon.

## Lightweight Shell Hooks (Local Only)

For simpler tools and local scripting, AXIS can also support a `.axis/hooks/` directory (similar to Claude/Grok hooks).

Example structure:
```
.axis/hooks/
├── pre-task-execution.sh
├── post-task-execution.sh
└── on-reservation-requested.sh
```

These would be executed by the `axis` binary/daemon when the corresponding events fire.

**Note**: Shell hooks are secondary. MCP is the primary recommended path for serious agent integration.

## Implementation Roadmap (Suggested)

1. **Define and document** the set of lifecycle events (this doc).
2. **Expose events via MCP**:
   - Add new tools for event subscription/registration.
   - Allow agents to register callback tool names.
3. **Instrument key paths** in the codebase:
   - Guarded task execution (`internal/execution/guarded.go`)
   - Reservation/lease operations
   - Daemon refresh cycle
4. **Add optional `.axis/hooks/` shell hook support** (lower priority).
5. **Provide reference implementations**:
   - Example MCP client for Grok
   - Example for Claude Code
   - Example hook scripts

## Current State (as of late May 2026)

- AXIS already exposes a solid read-only + advisory MCP server.
- Triangle advisory leases (`triangle_request_lease` etc.) are a good early example of two-way advisory interaction.
- Guarded execution already has strong safety (`safety.Check` + structured evaluator).
- No formal lifecycle event system or callback hooks yet.

## Contributing

When adding new hook points or MCP surfaces:
- Keep them **advisory** where possible (respect the "no execution authority" principle).
- Document them here.
- Add corresponding tests and examples.
- Update `internal/mcp/server.go` and related files.

---

**See also**:
- `docs/runbooks/mcp-network-tools.md`
- `AGENTS.md`

## Current Implementation Status

- `internal/events/events.go` — Skeleton package defining canonical event names.
- Natural hook points have been annotated in `internal/execution/guarded.go`.
- The existing Triangle advisory lease tools (`triangle_request_lease` etc.) serve as an early example of two-way advisory integration via MCP.

See also the `axis-pr-responder` patterns being developed for handling review feedback on the project itself.

