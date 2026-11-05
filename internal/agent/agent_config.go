package agent

import (
	"context"
	"io"
	"time"

	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/knowledge"
	"github.com/toasterbook88/axis/internal/mcpclient"
)

// Config configures an Agent.
type Config struct {
	Endpoint                string               // Ollama endpoint (default: chat.DefaultEndpoint)
	Model                   string               // Ollama model name
	Backend                 ChatBackend          // Optional custom backend override
	MaxTurns                int                  // Maximum agent loop iterations (default: 10)
	MaxTokens               int                  // Conversation token budget (default: 32768)
	ContextWindow           int                  // Served context of the active model in tokens; 0 = unknown
	AutoApprove             bool                 // Auto-approve safe commands (score < 70)
	Autonomy                AutonomyMode         // Autonomy mode: cluster, default, edit, or full (controls tool scope and auto-approval)
	SystemExtra             string               // Extra text appended to system prompt
	Verbose                 bool                 // Emit trace output for tool calls and turns
	DryRun                  bool                 // Plan tool calls without executing them
	ShellTimeout            time.Duration        // Timeout for run_shell commands (default: 5m)
	AllowRawCommandEvidence bool                 // Include raw command text in local evidence
	BackendSecurityClass    BackendSecurityClass // Local or remote backend trust classification
	// Cluster is optional. If non-nil, the agent injects a cluster summary
	// into the system prompt and uses it for safety checks.
	Cluster *chat.ClusterSummaryForPrompt

	// Knowledge is optional. Used for safety gating shell commands.
	Knowledge *knowledge.ClusterKnowledge

	// Snapshot and State are used to initialize tools.
	ToolContext *ToolContext

	// Output is where the agent writes assistant text and traces.
	Output io.Writer

	// Observer, when non-nil, receives structured loop events (tool calls,
	// results, turns, shell execs) instead of having them formatted to
	// Output. Surfaces that render their own transcript set this; the plain
	// CLI leaves it nil. See observer.go.
	Observer Observer

	// SessionLog, when non-nil, appends a private daily JSONL record for
	// each Run. The CLI sets this. Nil leaves tests and other callers
	// quiet. The logger is not an Observer and does not replace one.
	SessionLog *DailySessionLog

	// Confirm is the confirmation function. If nil, StdinConfirm() is used.
	Confirm ConfirmFunc

	// RunShell executes an approved shell command. If nil, the agent falls back
	// to a direct local shell helper.
	RunShell ShellRunner

	// RunOnNode executes an approved shell command on a named cluster node.
	// If nil, run_on_node fails with a configuration error (no raw SSH bypass).
	RunOnNode NodeShellRunner

	// RunTask executes an approved remote/cluster task.
	RunTask TaskRunner

	// MCPRegistry is a connected registry of external MCP servers (optional).
	MCPRegistry *mcpclient.Registry
}

// NodeShellRunner runs an approved command on a named cluster node.
type NodeShellRunner func(ctx context.Context, node, command string) (string, error)
