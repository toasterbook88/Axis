# MCP Network Tools Runbook

AXIS exposes a read-only MCP surface over stdio so external MCP clients can inspect cluster state without adding a daemon.

## Start

```bash
go run ./cmd/axis mcp serve
```

## Available surfaces

- Resource: `cluster://snapshot`
- Tool: `cluster_snapshot`
- Tool: `placement_decision`
- Tool: `ip_addr`
- Tool: `tailscale_status`
- Tool: `tailscale_ping`
- Tool: `wireguard_status`
- Tool: `docker_ps`
- Tool: `ssh_connectivity_test`

## Verify

```bash
go test ./...
go run ./cmd/axis task context "set up wireguard between two nodes"
go run ./cmd/axis mcp serve --transport stdio
```

## Safety

- All tools are read-only.
- No secrets are exposed.
- AXIS still uses the same discovery, snapshot, placement, and SSH transport code paths.
