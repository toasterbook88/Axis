package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/ui"
)

// maxSubAgentDepth caps nested spawn_subagent calls to prevent runaway
// recursion. The root agent is depth 0; a depth-2 agent cannot spawn further
// children.
const maxSubAgentDepth = 2

// defaultSubAgentMaxTurns is the per-sub-agent turn budget. Sub-agents are
// scoped to a single delegated sub-task, so a tighter budget than the root.
const defaultSubAgentMaxTurns = 6

// subAgentArgs is the argument shape for the spawn_subagent tool.
type subAgentArgs struct {
	Prompt     string `json:"prompt"`
	TargetNode string `json:"target_node,omitempty"`
	MaxTurns   int    `json:"max_turns,omitempty"`
}

// registerSubAgent registers the spawn_subagent tool definition. Execution is
// dispatched through Agent.dispatchSubagent (special-cased in dispatchToolCall)
// because it needs access to the active backend and tool context.
func (r *ToolRegistry) registerSubAgent() {
	r.add("spawn_subagent",
		"Delegate a focused sub-task to a child agent that runs its own tool-calling loop, "+
			"scoped to a target cluster node. The sub-agent can investigate and run commands on that node "+
			"(via run_on_node, remote_read_file, remote_grep, remote_list) and returns a final summary. "+
			"Use this to parallelize work across nodes — e.g. one sub-agent runs tests on nixos while another "+
			"validates a build on foundry. The sub-agent inherits the active model and auto-approve setting. "+
			"Nesting is capped (sub-agents cannot spawn their own sub-agents beyond depth 2).",
		json.RawMessage(`{
			"type":"object",
			"properties":{
				"prompt":{"type":"string","description":"The delegated sub-task instruction for the child agent"},
				"target_node":{"type":"string","description":"Cluster node the sub-agent should focus on (optional; if omitted the sub-agent chooses via placement)"},
				"max_turns":{"type":"integer","description":"Max tool-calling turns for the sub-agent (default 6)","default":6}
			},
			"required":["prompt"]
		}`),
		// Placeholder executor — real dispatch happens in Agent.dispatchSubagent.
		func(ctx context.Context, args json.RawMessage) (string, error) {
			return "", fmt.Errorf("spawn_subagent must be dispatched through the agent safety gate")
		},
	)
}

// dispatchSubagent builds and runs a scoped child agent for a spawn_subagent
// tool call. The child shares the parent's backend (LLM) but gets a fresh,
// focused conversation and a toolset biased toward the target node. It runs
// to completion (or its turn budget) and returns the child's final answer.
func (a *Agent) dispatchSubagent(ctx context.Context, args json.RawMessage) (string, error) {
	var sa subAgentArgs
	if err := json.Unmarshal(args, &sa); err != nil {
		return "", fmt.Errorf("invalid arguments for spawn_subagent: %w", err)
	}
	if strings.TrimSpace(sa.Prompt) == "" {
		return "", fmt.Errorf("spawn_subagent requires a non-empty \"prompt\" argument")
	}
	if a.subAgentDepth >= maxSubAgentDepth {
		return "", fmt.Errorf("spawn_subagent recursion limit reached (depth %d >= %d); the sub-agent cannot spawn further children", a.subAgentDepth, maxSubAgentDepth)
	}

	maxTurns := sa.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultSubAgentMaxTurns
	}

	sysExtra := fmt.Sprintf("You are a focused sub-agent (depth %d) delegated a single sub-task. "+
		"Operate primarily on the cluster node %q using run_on_node / remote_read_file / remote_grep / remote_list. "+
		"Investigate, run what's needed, then return a concise final answer to the parent agent. "+
		"Do not spawn further sub-agents.",
		a.subAgentDepth+1, firstNonEmpty(sa.TargetNode, "chosen by placement"))

	child := a.buildChildAgent(maxTurns, sa.TargetNode, sysExtra)

	if a.verbose {
		fmt.Fprintf(a.output, "\n%s Spawning sub-agent (depth %d, target=%s, max_turns=%d)\n",
			ui.Cyan("⤷"), child.subAgentDepth, firstNonEmpty(sa.TargetNode, "auto"), maxTurns)
	}

	if err := child.Run(ctx, sa.Prompt); err != nil {
		return "", fmt.Errorf("sub-agent failed: %w", err)
	}

	// Extract the child's final assistant text answer.
	answer := finalAssistantText(child.conv)
	if strings.TrimSpace(answer) == "" {
		return "(sub-agent completed without a final text answer)", nil
	}
	return answer, nil
}

// buildChildAgent constructs a scoped child Agent for spawn_subagent.
// The child gets its own backgroundTasks store (registry exposes async tools)
// and a snapshot of the parent's shell runners under runnerMu.
func (a *Agent) buildChildAgent(maxTurns int, targetNode, sysExtra string) *Agent {
	tc := a.toolContext
	if tc == nil {
		tc = NewToolContext(&RuntimeView{}, nil)
	}
	childTools := NewToolRegistry(tc)

	confirm := a.confirm
	if a.autoApproveAll {
		confirm = func(toolName, description string, score int) ConfirmResult { return ConfirmYes }
	}

	// Snapshot parent runners under lock so child construction does not race
	// with a concurrent /model runner refresh on the parent.
	a.runnerMu.RLock()
	parentShell := a.runShell
	parentOnNode := a.runOnNode
	a.runnerMu.RUnlock()

	child := &Agent{
		client:                  a.client,
		conv:                    chat.NewConversation(a.maxTokens),
		tools:                   childTools,
		confirm:                 confirm,
		runShell:                parentShell,
		runOnNode:               parentOnNode,
		runTask:                 a.runTask,
		safety:                  a.safety,
		output:                  a.output,
		maxTurns:                maxTurns,
		maxTokens:               a.maxTokens,
		verbose:                 a.verbose,
		dryRun:                  a.dryRun, // cascade --dry-run so children do not execute for real
		toolContext:             tc,
		model:                   a.model,
		allowRawCommandEvidence: a.allowRawCommandEvidence,
		securityClass:           a.securityClass,
		mcpRegistry:             a.mcpRegistry,
		autoApproveAll:          a.autoApproveAll,
		subAgentDepth:           a.subAgentDepth + 1,
		// Own store: child registry exposes run_background/check_task/list_*.
		// Sharing the parent's store would mix IDs across agent scopes.
		backgroundTasks: newBackgroundTaskStore(),
	}
	child.conv.Append(chat.Message{Role: chat.RoleSystem, Content: buildSubAgentSystemPrompt(targetNode, sysExtra)})
	return child
}

// finalAssistantText returns the content of the last assistant message in the
// conversation that has non-tool-call text content.
func finalAssistantText(conv *chat.Conversation) string {
	msgs := conv.Messages()
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role == chat.RoleAssistant && len(m.ToolCalls) == 0 && strings.TrimSpace(m.Content) != "" {
			return m.Content
		}
	}
	return ""
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func buildSubAgentSystemPrompt(targetNode, extra string) string {
	var b strings.Builder
	b.WriteString("You are an AXIS sub-agent: a focused, delegated agent running inside a parent agent session. ")
	b.WriteString("Your output is advisory and must be grounded in tool results — never fabricate.\n\n")
	b.WriteString("You have tools to operate across the cluster:\n")
	b.WriteString("- `run_on_node` to run a shell command on a named cluster node (requires confirmation).\n")
	b.WriteString("- `remote_read_file` / `remote_grep` / `remote_list` to inspect files and directories on remote nodes (read-only).\n")
	b.WriteString("- `read_file` / `list_directory` / `grep_search` / `symbol_search` for local inspection.\n")
	b.WriteString("- `run_shell` for a local shell command.\n")
	b.WriteString("- `axis_status` / `axis_facts` / `axis_place` for cluster state and placement.\n")
	b.WriteString("- `git_status` / `git_diff` / `git_log` if a repository is present.\n\n")
	if targetNode = strings.TrimSpace(targetNode); targetNode != "" {
		fmt.Fprintf(&b, "Your assigned target node is %q. Prefer remote tools scoped to that node unless observed state requires another choice.\n\n", targetNode)
	}
	if extra != "" {
		b.WriteString(extra)
		b.WriteString("\n\n")
	}
	b.WriteString("When done, produce a concise final text answer summarizing what you found or did. ")
	b.WriteString("Do not call spawn_subagent (nesting is blocked).\n")
	return b.String()
}
