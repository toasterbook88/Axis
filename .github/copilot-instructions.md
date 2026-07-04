# Copilot Instructions for AXIS

**Read `AGENTS.md` first.** That file is the single orientation entry point and authoritative source of truth for repository facts, architecture, and constraints.

## Copilot-Specific Behavior

When assisting with AXIS, Copilot should adhere to these critical guardrails:

- **Truth Rule**: Do not make release or state claims without code/current-state proof. No generated output may present itself as cluster truth unless backed by a real snapshot or live probe.
- **Surgical Changes**: Prefer small, explicit changes. Do not touch adjacent code that isn't broken.
- **Verification**: Run Makefile gates (`make test`, `make lint`) to verify any changes before proposing them.
