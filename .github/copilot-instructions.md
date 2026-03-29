# Copilot Instructions for AXIS

AXIS is a snapshot-first Go CLI for cluster fact discovery, deterministic
placement, and explicit local control surfaces.

## Truth Rule

No generated output may present itself as cluster truth unless it is backed by a
real snapshot or live probe.

- `axis facts`, `axis status`, `axis task place`, and `axis task context` are
  the primary operator truth surfaces.
- `axis chat` and `axis discover` are experimental helpers and must stay
  subordinate to observed state.
- Optional HTTP, MCP, and execution surfaces must not weaken the fact plane.

## Build And Test

```bash
go build -o axis ./cmd/axis/
go test ./...
go test -race ./...
./hack/coverage-check.sh
gofmt -w .
```

Requires Go 1.26.1+ and SSH key-based auth for remote nodes.

## High-Level Shape

Stable operator path:

- `cmd/axis/` wires the CLI surface
- `internal/config/` loads `~/.axis/nodes.yaml`
- `internal/discovery/`, `internal/facts/`, and `internal/snapshot/` build the
  observed cluster state
- `internal/placement/` ranks nodes deterministically
- `internal/runtimectx/` assembles the live runtime view used by read surfaces
- `internal/transport/` is the SSH execution layer and must keep host-key
  verification on

Secondary surfaces:

- `internal/daemon/` powers explicit cached reads
- `internal/api/` provides the optional local HTTP surface
- `internal/mcp/` provides read-only MCP access
- `internal/chat/`, `internal/scripts/`, and `internal/skills/` are useful only
  if they remain truthful and explicit

## Scope Discipline

Prefer changes that:

- improve fact quality, snapshot quality, or placement quality
- reduce operator confusion
- remove dead or duplicate complexity
- strengthen explicitness, determinism, and test coverage

Avoid changes that:

- create new hidden authority paths
- guess at cluster truth instead of surfacing uncertainty
- add duplicate control surfaces without a strong operator reason
- add heavy dependencies without strong justification
