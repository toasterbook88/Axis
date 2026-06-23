# MCP Network Tools Runbook

AXIS exposes a read-only MCP surface over stdio so external MCP clients can inspect cluster state without adding a daemon.

## Start

```bash
go run ./cmd/axis mcp serve
```

## Available surfaces

- Resource: `cluster://snapshot`

**Read-only diagnostic tools (14, all carry `readOnlyHint: true`):**

- Tool: `cluster_snapshot`
- Tool: `placement_decision`
- Tool: `axis_health`
- Tool: `axis_tools`
- Tool: `ip_addr`
- Tool: `tailscale_status`
- Tool: `tailscale_ping`
- Tool: `wireguard_status`
- Tool: `docker_ps`
- Tool: `ssh_connectivity_test`
- Tool: `git_status`
- Tool: `list_lifecycle_events`
- Tool: `get_recent_events`
- Tool: `register_event_interest`

**Advisory lease tools (3, not marked read-only — they write to the local reservation ledger):**

- Tool: `triangle_request_lease`
- Tool: `triangle_release_lease`
- Tool: `triangle_heartbeat_lease`

Registrations live in `internal/mcp/server.go` (`registerTools`, 14) and `internal/mcp/triangle.go` (`registerTriangleTools`, 3).

## Verify

```bash
go test ./...
go run ./cmd/axis task context "set up wireguard between two nodes"
go run ./cmd/axis mcp serve --transport stdio
```

## Safety

- 14 of 17 tools are read-only; the 3 `triangle_*_lease` tools are advisory lease primitives that write to the local reservation ledger and are not marked read-only.
- No secrets are exposed.
- AXIS still uses the same discovery, snapshot, placement, and SSH transport code paths.
