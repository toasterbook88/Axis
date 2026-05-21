package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
	r.registerSummary(tc)
	r.registerReservations(tc)
	r.registerReadFile()
	r.registerListDirectory()
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
		"Return a compact human-readable summary of the current AXIS cluster status. Includes node count, health, resources, and warnings. Use this for cluster overview questions.",
		json.RawMessage(`{"type":"object","properties":{}}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			if tc.Snapshot == nil {
				return "No cluster snapshot available — cluster may not be configured.", nil
			}
			return summarizeSnapshot(tc.Snapshot), nil
		},
	)
}

// --- Tool: axis_facts ---

func (r *ToolRegistry) registerFacts(tc *ToolContext) {
	r.add("axis_facts",
		"Return a compact human-readable summary of local hardware facts for the current machine (CPU, RAM, disk, GPUs, installed tools, Ollama status).",
		json.RawMessage(`{"type":"object","properties":{}}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			if tc.Snapshot != nil {
				if n, ok := models.FindLocalNode(tc.Snapshot.Nodes); ok {
					return summarizeNodeFacts(n), nil
				}
			}
			return "Local node not found in snapshot.", nil
		},
	)
}

// --- Tool: axis_place ---

type placeArgs struct {
	Description string `json:"description"`
}

func (r *ToolRegistry) registerPlace(tc *ToolContext) {
	r.add("axis_place",
		"Select the best node for a task description. Returns a human-readable placement decision with node name, fit score, and reasoning.",
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
				return "Placement: no nodes available in snapshot.", nil
			}
			reqs := placement.InferRequirements(a.Description)
			decision := placement.SelectBestNode(reqs, tc.Snapshot.Nodes, tc.State)
			return summarizePlacementDecision(decision), nil
		},
	)
}

// --- Tool: axis_summary ---

func (r *ToolRegistry) registerSummary(tc *ToolContext) {
	r.add("axis_summary",
		"Return an ultra-compact one-line summary of the cluster (node count, health, total RAM). Good for quick status checks.",
		json.RawMessage(`{"type":"object","properties":{}}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			if tc.Snapshot == nil {
				return "No cluster snapshot available.", nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%d nodes (%d reachable), status: %s",
				tc.Snapshot.Summary.TotalNodes, tc.Snapshot.Summary.ReachableNodes, tc.Snapshot.Status)
			if tc.Snapshot.Summary.TotalRAMMB > 0 {
				fmt.Fprintf(&b, ", %d MB RAM total, %d MB free",
					tc.Snapshot.Summary.TotalRAMMB, tc.Snapshot.Summary.TotalFreeRAMMB)
			}
			if len(tc.Snapshot.Warnings) > 0 {
				fmt.Fprintf(&b, ", %d warnings", len(tc.Snapshot.Warnings))
			}
			return b.String(), nil
		},
	)
}

// --- Tool: axis_reservations ---

func (r *ToolRegistry) registerReservations(tc *ToolContext) {
	r.add("axis_reservations",
		"List active reservations and task assignments across the cluster.",
		json.RawMessage(`{"type":"object","properties":{}}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			if tc.State == nil || len(tc.State.Nodes) == 0 {
				return "No reservation state available.", nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "Active reservations for %d nodes:\n", len(tc.State.Nodes))
			for name, ns := range tc.State.Nodes {
				if ns.ActiveTasks == 0 && ns.ReservedMB == 0 {
					continue
				}
				fmt.Fprintf(&b, "- %s: %d active tasks, %d MB reserved\n", name, ns.ActiveTasks, ns.ReservedMB)
				if ns.LastTask != "" {
					fmt.Fprintf(&b, "  Last task: %s\n", truncate(ns.LastTask, 60))
				}
				if len(ns.ActiveExecs) > 0 {
					fmt.Fprintf(&b, "  Active execs: %s\n", strings.Join(ns.ActiveExecs, ", "))
				}
			}
			return b.String(), nil
		},
	)
}

// --- Tool: read_file ---

type readFileArgs struct {
	Path string `json:"path"`
}

func (r *ToolRegistry) registerReadFile() {
	r.add("read_file",
		"Read the contents of a file at a given path. Returns the file contents as text. Paths are restricted to the current working directory and its subdirectories for safety.",
		json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Relative or absolute file path"}},"required":["path"]}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var a readFileArgs
			if err := json.Unmarshal(args, &a); err != nil {
				return "", fmt.Errorf("invalid arguments for read_file: expected {\"path\": \"...\"}, got: %s", string(args))
			}
			if a.Path == "" {
				return "", fmt.Errorf("read_file requires a non-empty \"path\" argument")
			}
			clean, err := validateToolPath(a.Path)
			if err != nil {
				return "", err
			}
			f, err := os.Open(clean)
			if err != nil {
				return "", fmt.Errorf("cannot read file %q: %w", clean, err)
			}
			defer f.Close()

			const maxFileSize = 8000
			// Read up to maxFileSize+1 to detect truncation.
			limited := io.LimitReader(f, int64(maxFileSize)+1)
			data, err := io.ReadAll(limited)
			if err != nil {
				return "", fmt.Errorf("cannot read file %q: %w", clean, err)
			}
			content := string(data)
			if len(data) > maxFileSize {
				content = truncateRune(content, maxFileSize) + "\n... [truncated due to size limit]"
			}
			return content, nil
		},
	)
}

// --- Tool: list_directory ---

type listDirArgs struct {
	Path string `json:"path"`
}

func (r *ToolRegistry) registerListDirectory() {
	r.add("list_directory",
		"List files and directories at a given path. Returns a human-readable directory listing. Paths are restricted to the current working directory and its subdirectories for safety.",
		json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Relative or absolute directory path"}},"required":["path"]}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var a listDirArgs
			if err := json.Unmarshal(args, &a); err != nil {
				return "", fmt.Errorf("invalid arguments for list_directory: expected {\"path\": \"...\"}, got: %s", string(args))
			}
			if a.Path == "" {
				a.Path = "."
			}
			clean, err := validateToolPath(a.Path)
			if err != nil {
				return "", err
			}
			entries, err := os.ReadDir(clean)
			if err != nil {
				return "", fmt.Errorf("cannot read directory %q: %w", clean, err)
			}
			var b strings.Builder
			const maxDirEntries = 100
			fmt.Fprintf(&b, "Directory: %s (%d entries)\n", clean, len(entries))
			for i, e := range entries {
				if i >= maxDirEntries {
					fmt.Fprintf(&b, "... and %d more entries\n", len(entries)-i)
					break
				}
				name := e.Name()
				if e.IsDir() {
					name += "/"
				}
				b.WriteString(name + "\n")
			}
			return b.String(), nil
		},
	)
}

// validateToolPath validates and resolves a path for file tools, preventing
// directory traversal outside the current working directory. Symlinks are
// resolved before the bounds check to prevent symlink-based escapes.
func validateToolPath(p string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("cannot determine working directory: %w", err)
	}
	clean := filepath.Clean(p)
	if !filepath.IsAbs(clean) {
		clean = filepath.Join(cwd, clean)
	}
	// Resolve symlinks to their real destination.
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", fmt.Errorf("cannot resolve path %q: %w", p, err)
	}
	// Ensure the resolved path is within cwd.
	rel, err := filepath.Rel(cwd, resolved)
	if err != nil {
		return "", fmt.Errorf("invalid path %q: %w", p, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes working directory", p)
	}
	return resolved, nil
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
	if len([]rune(output)) > maxOutput {
		output = truncateRune(output, maxOutput) + "\n... [truncated to 4000 chars]"
	}
	return output, nil
}

// truncateRune truncates a string to maxLen runes, appending "..." if truncated.
func truncateRune(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}
