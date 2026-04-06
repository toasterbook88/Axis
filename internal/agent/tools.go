package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/knowledge"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/placement"
	"github.com/toasterbook88/axis/internal/safety"
	"github.com/toasterbook88/axis/internal/state"
)

// ToolRegistry maps tool names to their definitions and executors.
type ToolRegistry struct {
	defs      []chat.ToolDef
	executors map[string]ToolExecutor
}

// ToolExecutor runs a tool and returns its string result.
type ToolExecutor func(ctx context.Context, args json.RawMessage) (string, error)

// ToolContext holds runtime state available to tool executors.
type ToolContext struct {
	Snapshot *models.ClusterSnapshot
	State    *state.ClusterState
}

// ShellRunner executes an approved shell command and returns the tool-visible
// output. CLI callers can route this through guarded AXIS execution.
type ShellRunner func(context.Context, string) (string, error)

// NewToolRegistry creates the default set of agent tools.
func NewToolRegistry(tc *ToolContext) *ToolRegistry {
	r := &ToolRegistry{executors: make(map[string]ToolExecutor)}
	r.registerStatus(tc)
	r.registerFacts(tc)
	r.registerPlace(tc)
	r.registerShell()
	return r
}

// Defs returns Ollama-compatible tool definitions for the /api/chat request.
func (r *ToolRegistry) Defs() []chat.ToolDef {
	return r.defs
}

// Execute dispatches a tool call. Returns an error message string (not a Go
// error) so the agent loop can feed it back to the model for self-correction.
func (r *ToolRegistry) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	exec, ok := r.executors[name]
	if !ok {
		return "", fmt.Errorf("unknown tool %q — available tools: %s", name, r.availableNames())
	}
	return exec(ctx, args)
}

// HasTool returns true if the named tool is registered.
func (r *ToolRegistry) HasTool(name string) bool {
	_, ok := r.executors[name]
	return ok
}

func (r *ToolRegistry) availableNames() string {
	names := make([]string, 0, len(r.executors))
	for n := range r.executors {
		names = append(names, n)
	}
	return strings.Join(names, ", ")
}

func (r *ToolRegistry) add(name, description string, params json.RawMessage, exec ToolExecutor) {
	r.defs = append(r.defs, chat.ToolDef{
		Type: "function",
		Function: chat.ToolDefFunction{
			Name:        name,
			Description: description,
			Parameters:  params,
		},
	})
	r.executors[name] = exec
}

// --- Tool: axis_status ---

func (r *ToolRegistry) registerStatus(tc *ToolContext) {
	r.add("axis_status",
		"Return the current AXIS cluster status snapshot as JSON. Use this to answer questions about node health, resources, and cluster state.",
		json.RawMessage(`{"type":"object","properties":{}}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			if tc.Snapshot == nil {
				return `{"error":"no snapshot available — cluster may not be configured"}`, nil
			}
			out, err := json.Marshal(tc.Snapshot)
			if err != nil {
				return "", fmt.Errorf("marshal snapshot: %w", err)
			}
			return string(out), nil
		},
	)
}

// --- Tool: axis_facts ---

func (r *ToolRegistry) registerFacts(tc *ToolContext) {
	r.add("axis_facts",
		"Return local hardware facts for the current machine (CPU, RAM, disk, GPUs, installed tools).",
		json.RawMessage(`{"type":"object","properties":{}}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			if tc.Snapshot != nil {
				if n, ok := models.FindLocalNode(tc.Snapshot.Nodes); ok {
					out, err := json.Marshal(n)
					if err != nil {
						return "", fmt.Errorf("marshal facts: %w", err)
					}
					return string(out), nil
				}
			}
			return `{"error":"local node not found in snapshot"}`, nil
		},
	)
}

// --- Tool: axis_place ---

type placeArgs struct {
	Description string `json:"description"`
}

func (r *ToolRegistry) registerPlace(tc *ToolContext) {
	r.add("axis_place",
		"Select the best node for a task description. Returns a placement decision with node, fit score, and reasoning.",
		json.RawMessage(`{"type":"object","properties":{"description":{"type":"string","description":"What the task needs to do"}},"required":["description"]}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var a placeArgs
			if err := json.Unmarshal(args, &a); err != nil {
				return "", fmt.Errorf("invalid arguments for axis_place: expected {\"description\": \"...\"}, got: %s", string(args))
			}
			if a.Description == "" {
				return "", fmt.Errorf("axis_place requires a non-empty \"description\" argument")
			}
			if tc.Snapshot == nil || len(tc.Snapshot.Nodes) == 0 {
				return `{"ok":false,"reasoning":["no nodes available in snapshot"]}`, nil
			}
			reqs := placement.InferRequirements(a.Description)
			decision := placement.SelectBestNode(reqs, tc.Snapshot.Nodes, tc.State)
			out, err := json.Marshal(decision)
			if err != nil {
				return "", fmt.Errorf("marshal decision: %w", err)
			}
			return string(out), nil
		},
	)
}

// --- Tool: run_shell ---

type shellArgs struct {
	Command string `json:"command"`
}

// ShellSafetyGate is called before executing any shell command. It receives
// the command string and returns (allow bool, reason string, score int).
// If allow is false, the command is not executed and reason is returned to the model.
type ShellSafetyGate func(command string) (allow bool, reason string, score int)

// DefaultSafetyGate uses the safety package to gate shell commands.
func DefaultSafetyGate(k *knowledge.ClusterKnowledge) ShellSafetyGate {
	return func(command string) (bool, string, int) {
		result := safety.Check(k, command, nil)
		if result.Blocked {
			return false, fmt.Sprintf("blocked (score %d/100): %s", result.Score, result.Reason), result.Score
		}
		return true, "", result.Score
	}
}

// shellTimeout is the maximum duration for a single shell command.
const shellTimeout = 30 * time.Second

func (r *ToolRegistry) registerShell() {
	r.add("run_shell",
		"Execute a shell command through the agent execution gate. CLI callers may route this through guarded AXIS execution with placement and reservation protection; destructive commands will still be blocked.",
		json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"The shell command to execute"}},"required":["command"]}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var a shellArgs
			if err := json.Unmarshal(args, &a); err != nil {
				return "", fmt.Errorf("invalid arguments for run_shell: expected {\"command\": \"...\"}, got: %s", string(args))
			}
			if a.Command == "" {
				return "", fmt.Errorf("run_shell requires a non-empty \"command\" argument")
			}
			// Shell execution is handled by the agent loop after safety + confirmation.
			// This executor is a placeholder — the Agent dispatches shell commands specially.
			return "", fmt.Errorf("run_shell must be dispatched through the agent safety gate")
		},
	)
}

// ExecuteShell runs a local shell command with timeout and captures output.
// This is the fallback shell runner when no guarded AXIS executor is wired in.
func ExecuteShell(ctx context.Context, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, shellTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	output := stdout.String()
	if stderr.Len() > 0 {
		output += "\n[stderr] " + stderr.String()
	}

	if ctx.Err() == context.DeadlineExceeded {
		return output + "\n[timeout after 30s]", fmt.Errorf("command timed out after 30s")
	}
	if err != nil {
		return output + "\n[exit error] " + err.Error(), nil
	}

	// Cap output to prevent blowing up the context window.
	const maxOutput = 4000
	if len(output) > maxOutput {
		output = output[:maxOutput] + "\n... [truncated to 4000 chars]"
	}
	return output, nil
}
