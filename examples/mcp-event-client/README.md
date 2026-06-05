# Reference MCP Event Client for AXIS

This is a minimal example showing how an external AI agent (Grok, Claude, etc.)
can:

- Discover available lifecycle events (`list_lifecycle_events`)
- Register interest in specific events (`register_event_interest`)
- Poll recent events (`get_recent_events`)

## Usage

```bash
cd examples/mcp-event-client
go run main.go
```

In a real integration the agent would:

1. Connect to AXIS as an MCP client (stdio or HTTP).
2. Register interest in events it cares about.
3. Either poll `get_recent_events` or (in future) receive callbacks.

This is **advisory/observational only**. It does not grant execution control.
