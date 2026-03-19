# Copilot Instructions for AXIS

AXIS is a single-binary Go CLI that discovers hardware facts across clusters via SSH, builds deterministic task placement scores, and optionally integrates with LLMs (Ollama) for advisory chat. Phase 1 is read-only and advisory — no task execution.

## Build, Test & Lint

```bash
# Build the binary
go build -o axis ./cmd/axis/

# Run all tests
go test ./...

# Run tests for a single package
go test ./internal/placement/

# Run a specific test
go test -run TestBuildContextBlockPrefersNodeWithResources ./cmd/axis/

# Format code before committing
gofmt -w .
```

**Requirements:** Go 1.22+ (see go.mod), SSH key-based auth for remote nodes.

## High-Level Architecture

AXIS follows a **functional pipeline** architecture where each package is responsible for a single phase:

- **`cmd/axis/`** — Cobra CLI entry point; assembles commands (facts, status, task, chat, context, discover, scripts, skills)
- **`internal/config/`** — Loads `~/.axis/nodes.yaml` (node list, SSH credentials)
- **`internal/transport/`** — Raw SSH execution layer (command delivery, output capture)
- **`internal/facts/`** — Collects hardware facts (OS, RAM, CPU, GPU, installed tools) from local or remote nodes
- **`internal/snapshot/`** — Builds and caches a structured `ClusterSnapshot` of all nodes' facts
- **`internal/models/`** — Shared types: `NodeFacts`, `NodeStatus`, `TaskRequirements`, `ClusterSnapshot`
- **`internal/placement/`** — Filters + ranks nodes for task placement; computes deterministic fit scores (0–100)
- **`internal/state/`** — Caches/persists cluster state across commands (prevents re-querying all nodes)
- **`internal/chat/`** — Streams responses via local Ollama (localhost:11434); graceful fallback if unavailable
- **`internal/knowledge/`** — Knowledge base (skills, capabilities) stored locally
- **`internal/mcp/`** — Model Context Protocol (MCP) server integration for tool exposition
- **`internal/safety/`** — Safety checks and guardrails for task execution
- **`internal/llm/`** — LLM integration helpers (prompting, tokenization)
- **`internal/skills/`** — Skill definitions and registry
- **`internal/scripts/`** — Local script storage and execution
- **`internal/discovery/`** — Node discovery (currently static seed, planned: mesh peer discovery)

**Data flow:** Config → Transport (SSH) → Facts → Snapshot → State (cached) → Placement (filter + rank) → Placement score + advisory.

## Key Conventions

### Node Status Classification

All nodes report one of four statuses (see `models.NodeStatus`):

- `complete` — All facts collected successfully
- `partial` — Node reachable but some fact collectors failed
- `unreachable` — SSH/network failure; no facts available
- `error` — Internal parsing or command execution failure

A `ClusterSnapshot` is `healthy` only if all nodes are `complete`; otherwise `degraded`.

### Placement Scoring

The placement module uses **deterministic ranking** (no ML):

1. **Filtering** — Only `complete` nodes are candidates; hard requirements (tool availability, minimum RAM) must pass
2. **Ranking** — Candidates ranked by:
   - RAM pressure (none < low < medium < high)
   - Free RAM (higher is better)
   - Node name (stable tiebreaker, alphabetical)
3. **Bonuses** — GPU presence (+25 pts), local node (+10 pts)

Scores are always 0–100 and deterministic — same input always produces the same placement.

### Error Tolerance

Fact collectors are **failure-tolerant** — if a single collector fails (e.g., GPU detection on a non-GPU node), the node degrades to `partial` rather than failing entirely. This ensures cluster surveys continue even with platform-specific gaps.

### SSH & Remote Execution

- Uses `golang.org/x/crypto/ssh` for remote execution
- Per-node timeout is configurable in `nodes.yaml` (default 10 sec)
- SSH user, port, and hostname are configured per-node in `~/.axis/nodes.yaml`

### Output Formats

All command outputs support `--format json` (default) or `--format yaml`:

```go
// Typical pattern:
func printOutput(data interface{}, format string) error {
	switch format {
	case "yaml":
		out, err := yaml.Marshal(data)
		// ...
	default:
		out, err := json.MarshalIndent(data, "", "  ")
		// ...
	}
	return nil
}
```

### Testing Patterns

- Use `*testing.T` directly (no external test frameworks)
- Test file naming: `{module}_test.go` (e.g., `snapshot_test.go`)
- Table-driven tests for multiple scenarios (see `placement_test.go`)
- Prefer concrete assertions over reflection — test behavior, not implementation

Example:
```go
func TestBuild_AllComplete_Healthy(t *testing.T) {
	// Arrange
	// Act
	// Assert
}
```

### Dependency Philosophy

AXIS intentionally minimizes external dependencies. Current dependencies:

- `github.com/spf13/cobra` — CLI framework
- `golang.org/x/crypto` — SSH client
- `gopkg.in/yaml.v3` — YAML marshaling
- `github.com/mark3labs/mcp-go` — MCP server protocol

**Do not add heavy dependencies without strong justification** — the project values its small footprint.

### Configuration & State

- **Config source:** `~/.axis/nodes.yaml` (user-managed, version-controlled separately)
- **State persistence:** `internal/state/` caches results to avoid re-scanning all nodes
- **No hardcoding:** Cluster IPs, hostnames, and vendor-specific tool names are never hardcoded; all are user-configurable

### Phase Boundary

This is **Phase 1** — facts collection and advisory placement. Phase 2+ features (background coordinator, mesh networking, task execution) require discussion in GitHub issues before PRs.

## Scope Discipline

**Contributions should fit within Phase 1 and respect the existing architecture.**

**Do:**
- Fix bugs in fact collectors, discovery, snapshot assembly, or placement
- Improve error handling and robustness
- Add test coverage for existing behavior
- Improve documentation

**Avoid:**
- Adding a daemon or background coordinator (requires prior issue discussion)
- Adding mesh networking or peer discovery beyond static seed (requires discussion)
- Adding heavy dependencies without strong justification
- Overfitting to specific cluster topologies (hardcoded interface names, vendor-specific tools, private hostnames)

See [CONTRIBUTING.md](../CONTRIBUTING.md) for pull request guidelines.
