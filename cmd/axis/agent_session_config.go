package main

import (
	"context"
	"io"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/knowledge"
	"github.com/toasterbook88/axis/internal/mcpclient"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

// agentSessionParams holds the live operator inputs used to construct agent.Config.
// Extracted so tests can prove RunShell/RunOnNode/SystemExtra wiring without
// driving the full Cobra RunE path.
type agentSessionParams struct {
	Endpoint                string
	Model                   string
	Backend                 agent.ChatBackend
	MaxTurns                int
	MaxTokens               int
	ContextWindow           int // the active target's served window; 0 = unknown
	AutoApprove             bool
	Autonomy                agent.AutonomyMode
	SystemExtra             string
	Verbose                 bool
	DryRun                  bool
	AllowRawCommandEvidence bool
	BackendSecurityClass    agent.BackendSecurityClass
	Cluster                 *chat.ClusterSummaryForPrompt
	Knowledge               *knowledge.ClusterKnowledge
	ToolContext             *agent.ToolContext
	Output                  io.Writer
	Observer                agent.Observer
	Confirm                 agent.ConfirmFunc
	MCPRegistry             *mcpclient.Registry
	RuntimeLoader           func(context.Context) (*runtimectx.Context, error)
	SessionLog              *agent.DailySessionLog
}

// buildAgentSessionConfig assembles agent.Config for axis agent sessions.
// Always injects guarded shell runners so Layer 4 cannot be accidentally dropped.
func buildAgentSessionConfig(p agentSessionParams) agent.Config {
	model := p.Model
	loader := p.RuntimeLoader
	if loader == nil {
		loader = loadAgentShellRuntime
	}
	return agent.Config{
		Endpoint:                p.Endpoint,
		Model:                   p.Model,
		Backend:                 p.Backend,
		MaxTurns:                p.MaxTurns,
		MaxTokens:               p.MaxTokens,
		ContextWindow:           p.ContextWindow,
		AutoApprove:             p.AutoApprove,
		Autonomy:                p.Autonomy,
		SystemExtra:             p.SystemExtra,
		Verbose:                 p.Verbose,
		DryRun:                  p.DryRun,
		AllowRawCommandEvidence: p.AllowRawCommandEvidence,
		BackendSecurityClass:    p.BackendSecurityClass,
		Cluster:                 p.Cluster,
		Knowledge:               p.Knowledge,
		ToolContext:             p.ToolContext,
		Output:                  p.Output,
		Observer:                p.Observer,
		Confirm:                 p.Confirm,
		RunShell:                guardedAgentShellRunner(model, loader),
		RunOnNode: func(ctx context.Context, node, command string) (string, error) {
			return guardedAgentCommandRunner(model, node, loader)(ctx, command)
		},
		RunTask:     guardedAgentTaskRunner(),
		MCPRegistry: p.MCPRegistry,
		SessionLog:  p.SessionLog,
	}
}
