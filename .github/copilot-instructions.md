# Copilot Instructions for AXIS

**Read [`AGENTS.md`](../AGENTS.md) first.** That file is the canonical repository
knowledge and architecture source for AI agents working in this repository
(architecture, repository facts, scope rules, and verification requirements).

This file is a **thin repository-wide entry point** for GitHub Copilot surfaces
that load `.github/copilot-instructions.md`. It does not replace `AGENTS.md`.

## Truth Rule

No generated output may present itself as cluster truth unless it is backed by
a real snapshot or live probe.

- `axis facts`, `axis status`, `axis task place`, and `axis task context` are
  the primary operator truth surfaces.
- `axis chat` and `axis agent` are experimental helpers subordinate to observed
  state.
- Optional HTTP, MCP, and execution surfaces must not weaken the fact plane.

## Copilot-Specific Behavior

When assisting with AXIS, Copilot should adhere to these critical guardrails:

- **Truth Rule**: Do not make release or state claims without code/current-state
  proof. No generated output may present itself as cluster truth unless backed
  by a real snapshot or live probe.
- **Surgical Changes**: Prefer small, explicit changes. Do not touch adjacent
  code that is not broken.
- **Verification**: Run Makefile gates (`make test`, `make lint`) to verify any
  changes before proposing them.

For full architecture, CLI inventory, testing patterns, and scope discipline,
see root [`AGENTS.md`](../AGENTS.md).
