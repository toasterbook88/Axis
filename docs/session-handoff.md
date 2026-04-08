# AXIS Session Handoff

This file is the lightweight coordination surface for concurrent AXIS work
across multiple machines and editor sessions.

## Current Base

- Shared base branch: `main`
- Shared base commit: `46b29f6238ee7b8790067c76e78deff2748d8cb0`
- Rule: do not assume another session's unstaged or staged work is complete
  until it is committed and explicitly handed off here.

## Active Sessions

| Session | Machine | Repo Path | Branch | Owner | Scope | Avoid Touching |
| --- | --- | --- | --- | --- | --- | --- |
| `codex-runtime` | local macOS | `/Users/smithanator/axis-1` | `session-codex-runtime-core` | Codex | SSH transport, `axis doctor`, workload inference, related tests/goldens | install/distribution files unless required |
| `gemini-distribution` | NixOS / Antigravity | `/home/axis/Github-Axis/axis` | `session-gemini-install-distribution` | Gemini | installer, flake, updater policy, install docs | `internal/workload/*`, transport/runtime-core files |

## File Ownership

- `internal/transport/*`: `codex-runtime`
- `cmd/axis/doctor*`: `codex-runtime`
- `internal/workload/*`: `codex-runtime`
- `internal/placement/placement_test.go`: `codex-runtime`
- `internal/api/server_test.go`: `codex-runtime`
- `internal/mcp/testdata/placement_decision_turboquant.golden`: `codex-runtime`
- `install.sh`: `gemini-distribution`
- `flake.nix`: `gemini-distribution`
- `flake.lock`: `gemini-distribution`
- `cmd/axis/update.go`: `gemini-distribution`
- `internal/buildinfo/version.go`: `gemini-distribution`
- `README.md`: `gemini-distribution`
- `docs/current-state.md`: `gemini-distribution`
- `docs/hybrid-ai-router-plan.md`: `gemini-distribution`

## Handoff Format

When a session pauses or wants the other session to integrate work, append a new
entry under `Handoffs` with:

- date/time
- session name
- branch and commit
- files changed
- verification run
- blockers or merge notes

Example:

```text
- 2026-04-08 16:10 EDT | codex-runtime
  branch: session-codex-runtime-core
  commit: <hash>
  files: internal/transport/ssh.go, cmd/axis/doctor.go
  verified: env HOME=/tmp/axis-test-home GOCACHE=/tmp/axis-go-build-cache go test ./... -count=1
  notes: workload files intentionally owned by codex-runtime
```

## Integration Rules

1. Commit on the owning session branch before asking for integration.
2. Integrate by commit (`cherry-pick`, PR, or patch), not by manual file copy.
3. If a non-owner must touch an owned file, record that here before doing it.
4. Large design docs should be separate from runtime behavior commits.
5. `docs/current-state.md` should only be updated by the session that changes
   the live operator surface it describes.

## Current Risks

- The local runtime branch should keep SSH, `doctor`, and workload changes in a
  single reviewable change set.
- The NixOS branch should keep packaging/distribution work separate from
  doc-only design material.
- The NixOS branch must not integrate incidental `internal/workload/*` edits
  over the runtime branch.
- Antigravity on NixOS is live on the AXIS repo, but session state itself is
  not the source of truth; git plus this file is.

## Current Session Snapshot

- `codex-runtime`
  branch: `session-codex-runtime-core`
  state: runtime/core fixes validated green locally
  verify: `env HOME=/tmp/axis-test-home GOCACHE=/tmp/axis-go-build-cache go test ./... -count=1`
  notes: keep staged state aligned with the tested working tree

- `gemini-distribution`
  branch: `session-gemini-install-distribution`
  state: install/update/flake/docs lane active in Antigravity on NixOS
  verify: pending Nix-focused validation on that branch
  notes: keep `docs/hybrid-ai-router-plan.md` separate from shipping install/update changes

## Handoffs

- 2026-04-08 16:00 EDT | setup
  branch: `session-codex-runtime-core` and `session-gemini-install-distribution`
  commit: pending
  files: `docs/session-handoff.md`
  verified: branch split established on both machines
  notes: use this file for future cross-session handoffs

- 2026-04-08 16:10 EDT | organization
  branch: `session-codex-runtime-core`
  commit: pending
  files: `internal/transport/*`, `cmd/axis/doctor*`, `internal/workload/*`, related tests/goldens, `docs/session-handoff.md`
  verified: `env HOME=/tmp/axis-test-home GOCACHE=/tmp/axis-go-build-cache go test ./... -count=1`
  notes: local staged set should match the full validated runtime fix set

- 2026-04-08 16:10 EDT | organization
  branch: `session-gemini-install-distribution`
  commit: pending
  files: install/update/flake/docs lane plus `docs/session-handoff.md`
  verified: pending
  notes: keep `internal/workload/*` overlap and `docs/hybrid-ai-router-plan.md` out of the packaging commit
