package agent

import (
	"bufio"
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

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/knowledge"
	"github.com/toasterbook88/axis/internal/mcpclient"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/placement"
	"github.com/toasterbook88/axis/internal/reservation"
	"github.com/toasterbook88/axis/internal/safety"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
	"sync/atomic"
)

// ToolRegistry maps tool names to their definitions and executors.
type ToolRegistry struct {
	defs      []chat.ToolDef
	executors map[string]ToolExecutor
	// todos is the session-scoped todo list, owned by the registry so the
	// todo tool persists state across turns without external plumbing.
	todos *todoStore
}

// ToolExecutor runs a tool and returns its string result.
type ToolExecutor func(ctx context.Context, args json.RawMessage) (string, error)

type RuntimeView struct {
	Config    *config.Config
	Snapshot  *models.ClusterSnapshot
	State     *state.ClusterState
	Ledger    *reservation.Ledger
	Skills    *skills.Store
	Knowledge *knowledge.ClusterKnowledge
}

// ToolContext holds runtime state available to tool executors. The current
// view is accessed atomically and reloaded on demand via the Reload callback.
type ToolContext struct {
	current atomic.Pointer[RuntimeView]
	Reload  func(context.Context) (*RuntimeView, error)
}

// NewToolContext creates a new ToolContext with an initial view.
func NewToolContext(initial *RuntimeView, reload func(context.Context) (*RuntimeView, error)) *ToolContext {
	if initial == nil {
		return nil
	}
	tc := &ToolContext{
		Reload: reload,
	}
	tc.current.Store(initial)
	return tc
}

// Current returns the current atomically replaceable RuntimeView.
func (tc *ToolContext) Current() *RuntimeView {
	if tc == nil {
		return nil
	}
	return tc.current.Load()
}

// ReloadCurrent reloads the current view using the Reload callback.
func (tc *ToolContext) ReloadCurrent(ctx context.Context) error {
	if tc == nil {
		return fmt.Errorf("nil tool context")
	}
	if tc.Reload == nil {
		return fmt.Errorf("no reload function defined")
	}
	view, err := tc.Reload(ctx)
	if err != nil {
		return err
	}
	if view == nil {
		return fmt.Errorf("reload returned nil view")
	}
	if view.Config == nil || view.Snapshot == nil || view.State == nil {
		return fmt.Errorf("reload returned incomplete view (Config, Snapshot, or State is nil)")
	}
	tc.current.Store(view)
	return nil
}

// GetView returns the current atomically replaceable RuntimeView.
func (tc *ToolContext) GetView() *RuntimeView {
	return tc.Current()
}

// ShellRunner executes an approved shell command and returns the tool-visible
// output. CLI callers can route this through guarded AXIS execution.
type ShellRunner func(context.Context, string) (string, error)

func NewToolRegistry(tc *ToolContext) *ToolRegistry {
	r := &ToolRegistry{executors: make(map[string]ToolExecutor), todos: newTodoStore()}
	r.registerStatus(tc)
	r.registerFacts(tc)
	r.registerPlace(tc)
	r.registerSummary(tc)
	r.registerReservations(tc)
	r.registerReadFile()
	r.registerWriteFile()
	r.registerEditFile()
	r.registerMultiEdit()
	r.registerListDirectory()
	r.registerGrepSearch()
	r.registerShell()
	r.registerGitTools()
	r.registerRemoteExecutionTool()
	r.registerTodo(r.todos)
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
			view := tc.GetView()
			if view == nil || view.Snapshot == nil {
				return "No cluster snapshot available — cluster may not be configured.", nil
			}
			return summarizeSnapshot(view.Snapshot), nil
		},
	)
}

// --- Tool: axis_facts ---

func (r *ToolRegistry) registerFacts(tc *ToolContext) {
	r.add("axis_facts",
		"Return a compact human-readable summary of local hardware facts for the current machine (CPU, RAM, disk, GPUs, installed tools, Ollama status).",
		json.RawMessage(`{"type":"object","properties":{}}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			view := tc.GetView()
			if view != nil && view.Snapshot != nil {
				if n, ok := models.FindLocalNode(view.Snapshot.Nodes); ok {
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
			view := tc.GetView()
			if view == nil || view.Snapshot == nil || len(view.Snapshot.Nodes) == 0 {
				return "Placement: no nodes available in snapshot.", nil
			}
			reqs := placement.InferRequirements(a.Description)
			decision := placement.SelectBestNode(reqs, view.Snapshot.Nodes, view.State)
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
			view := tc.GetView()
			if view == nil || view.Snapshot == nil {
				return "No cluster snapshot available.", nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%d nodes (%d reachable), status: %s",
				view.Snapshot.Summary.TotalNodes, view.Snapshot.Summary.ReachableNodes, view.Snapshot.Status)
			if view.Snapshot.Summary.TotalRAMMB > 0 {
				fmt.Fprintf(&b, ", %d MB RAM total, %d MB free",
					view.Snapshot.Summary.TotalRAMMB, view.Snapshot.Summary.TotalFreeRAMMB)
			}
			if len(view.Snapshot.Warnings) > 0 {
				fmt.Fprintf(&b, ", %d warnings", len(view.Snapshot.Warnings))
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
			view := tc.GetView()
			if view == nil || view.State == nil || len(view.State.Nodes) == 0 {
				return "No reservation state available.", nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "Active reservations for %d nodes:\n", len(view.State.Nodes))
			for name, ns := range view.State.Nodes {
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

			const maxFileSize = 32000
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
				b.WriteString(name)
				b.WriteByte('\n')
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
	Cwd     string `json:"cwd,omitempty"`
}

// ShellSafetyGate is called before executing any shell command. It receives
// the command string and returns (allow bool, reason string, score int).
// If allow is false, the command is not executed and reason is returned to the model.
type ShellSafetyGate func(command string) (allow bool, reason string, score int)

// DefaultSafetyGate uses the safety package to gate shell commands.
func DefaultSafetyGate(tc *ToolContext) ShellSafetyGate {
	return func(command string) (bool, string, int) {
		var k *knowledge.ClusterKnowledge
		if tc != nil {
			if view := tc.GetView(); view != nil {
				k = view.Knowledge
			}
		}
		result := safety.Check(k, command, nil)
		if result.Blocked {
			return false, fmt.Sprintf("blocked (score %d/100): %s", result.Score, result.Reason), result.Score
		}
		return true, "", result.Score
	}
}

func (r *ToolRegistry) registerShell() {
	r.add("run_shell",
		"Execute a shell command through the agent execution gate. CLI callers may route this through guarded AXIS execution with placement and reservation protection; destructive commands will still be blocked.",
		json.RawMessage(`{
			"type":"object",
			"properties":{
				"command":{"type":"string","description":"The shell command to execute"},
				"cwd":{"type":"string","description":"Optional working directory to execute the command in"}
			},
			"required":["command"]
		}`),
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

// ExecuteShell runs a local shell command with a default 30s timeout and captures output.
// This is kept for backward compatibility.
func ExecuteShell(ctx context.Context, command string) (string, error) {
	return ExecuteShellWithTimeout(30*time.Second)(ctx, command)
}

// ExecuteShellWithTimeout returns a ShellRunner configured with a specific command timeout.
func ExecuteShellWithTimeout(timeout time.Duration) ShellRunner {
	return func(ctx context.Context, command string) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
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
			return output + fmt.Sprintf("\n[timeout after %v]", timeout), fmt.Errorf("command timed out after %v", timeout)
		}
		if err != nil {
			return output + "\n[exit error] " + err.Error(), nil
		}

		// Cap output to prevent blowing up the context window.
		const maxOutput = 16000
		if len([]rune(output)) > maxOutput {
			output = truncateRune(output, maxOutput) + fmt.Sprintf("\n... [truncated to %d chars]", maxOutput)
		}
		return output, nil
	}
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

// validateToolPathForWrite validates and resolves a path for writing files,
// supporting files and directories that do not exist yet.
func validateToolPathForWrite(p string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("cannot determine working directory: %w", err)
	}
	clean := filepath.Clean(p)
	if !filepath.IsAbs(clean) {
		clean = filepath.Join(cwd, clean)
	}

	// Since the file/directory may not exist, resolve the closest existing ancestor.
	dir := filepath.Dir(clean)
	for {
		if _, err := os.Stat(dir); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("cannot resolve path %q: %w", p, err)
	}

	relDir, err := filepath.Rel(dir, clean)
	if err != nil {
		return "", fmt.Errorf("invalid path %q: %w", p, err)
	}
	resolved := filepath.Join(resolvedDir, relDir)

	rel, err := filepath.Rel(cwd, resolved)
	if err != nil {
		return "", fmt.Errorf("invalid path %q: %w", p, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes working directory", p)
	}
	return resolved, nil
}

// --- Tool: write_file ---

type writeFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (r *ToolRegistry) registerWriteFile() {
	r.add("write_file",
		"Create a new file or overwrite an existing file with the specified content. Paths are restricted to the current working directory and its subdirectories for safety.",
		json.RawMessage(`{
			"type":"object",
			"properties":{
				"path":{"type":"string","description":"Relative or absolute file path"},
				"content":{"type":"string","description":"The contents to write to the file"}
			},
			"required":["path","content"]
		}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var a writeFileArgs
			if err := json.Unmarshal(args, &a); err != nil {
				return "", fmt.Errorf("invalid arguments for write_file: %w", err)
			}
			if a.Path == "" {
				return "", fmt.Errorf("write_file requires a non-empty \"path\" argument")
			}
			clean, err := validateToolPathForWrite(a.Path)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(clean), 0755); err != nil {
				return "", fmt.Errorf("cannot create parent directory: %w", err)
			}
			if err := os.WriteFile(clean, []byte(a.Content), 0644); err != nil {
				return "", fmt.Errorf("cannot write file %q: %w", clean, err)
			}
			return fmt.Sprintf("Successfully wrote %d bytes to %s", len(a.Content), a.Path), nil
		},
	)
}

// --- Tool: edit_file ---

type editFileArgs struct {
	Path               string `json:"path"`
	TargetContent      string `json:"target_content"`
	ReplacementContent string `json:"replacement_content"`
	ReplaceAll         bool   `json:"replace_all,omitempty"`
}

// applyStrReplace performs a single string replacement on content. When
// replaceAll is false the old string must occur exactly once (anchor-unique),
// otherwise an error is returned so the caller can self-correct rather than
// silently editing the wrong location.
func applyStrReplace(content, old, new string, replaceAll bool) (string, error) {
	if old == "" {
		return "", fmt.Errorf("old_string/target_content is empty")
	}
	count := strings.Count(content, old)
	if count == 0 {
		return "", fmt.Errorf("target text not found in file (0 occurrences)")
	}
	if !replaceAll && count > 1 {
		return "", fmt.Errorf("target text is not unique (found %d occurrences); set replace_all=true to replace all, or provide more surrounding context to make it unique", count)
	}
	if replaceAll {
		return strings.ReplaceAll(content, old, new), nil
	}
	return strings.Replace(content, old, new, 1), nil
}

func (r *ToolRegistry) registerEditFile() {
	r.add("edit_file",
		"Replace a specific block of text in an existing file. The target_content must match exactly. By default it must be unique in the file; set replace_all=true to replace every occurrence. For multiple edits to the same file, prefer multi_edit.",
		json.RawMessage(`{
			"type":"object",
			"properties":{
				"path":{"type":"string","description":"Relative or absolute file path"},
				"target_content":{"type":"string","description":"The exact block of text to be replaced"},
				"replacement_content":{"type":"string","description":"The new text to replace the target block"},
				"replace_all":{"type":"boolean","description":"Replace every occurrence (default false, requires unique match)","default":false}
			},
			"required":["path","target_content","replacement_content"]
		}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var a editFileArgs
			if err := json.Unmarshal(args, &a); err != nil {
				return "", fmt.Errorf("invalid arguments for edit_file: %w", err)
			}
			if a.Path == "" {
				return "", fmt.Errorf("edit_file requires a non-empty \"path\" argument")
			}
			clean, err := validateToolPath(a.Path)
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(clean)
			if err != nil {
				return "", fmt.Errorf("cannot read file %q: %w", clean, err)
			}
			newContent, err := applyStrReplace(string(data), a.TargetContent, a.ReplacementContent, a.ReplaceAll)
			if err != nil {
				return "", fmt.Errorf("edit_file %q: %w", clean, err)
			}
			if err := os.WriteFile(clean, []byte(newContent), 0644); err != nil {
				return "", fmt.Errorf("cannot write file %q: %w", clean, err)
			}
			n := strings.Count(string(data), a.TargetContent)
			if a.ReplaceAll {
				return fmt.Sprintf("Replaced %d occurrence(s) in %s", n, a.Path), nil
			}
			return fmt.Sprintf("Successfully replaced target content in %s", a.Path), nil
		},
	)
}

// --- Tool: multi_edit ---

type multiEditArgs struct {
	Path  string `json:"path"`
	Edits []struct {
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all,omitempty"`
	} `json:"edits"`
}

func (r *ToolRegistry) registerMultiEdit() {
	r.add("multi_edit",
		"Apply multiple text replacements to a single file in one call. Each edit replaces old_string with new_string; edits apply in order to the evolving content. By default each old_string must be unique at apply time; set replace_all=true on an edit to replace all occurrences. The file is written once after all edits succeed. Prefer this over repeated edit_file calls to cut round-trips.",
		json.RawMessage(`{
			"type":"object",
			"properties":{
				"path":{"type":"string","description":"Relative or absolute file path"},
				"edits":{"type":"array","items":{
					"type":"object",
					"properties":{
						"old_string":{"type":"string","description":"Exact text to find"},
						"new_string":{"type":"string","description":"Replacement text"},
						"replace_all":{"type":"boolean","description":"Replace all occurrences (default false, unique match required)","default":false}
					},
					"required":["old_string","new_string"]
				}}
			},
			"required":["path","edits"]
		}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var a multiEditArgs
			if err := json.Unmarshal(args, &a); err != nil {
				return "", fmt.Errorf("invalid arguments for multi_edit: %w", err)
			}
			if a.Path == "" {
				return "", fmt.Errorf("multi_edit requires a non-empty \"path\" argument")
			}
			if len(a.Edits) == 0 {
				return "", fmt.Errorf("multi_edit requires at least one edit")
			}
			clean, err := validateToolPath(a.Path)
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(clean)
			if err != nil {
				return "", fmt.Errorf("cannot read file %q: %w", clean, err)
			}
			content := string(data)
			for i, e := range a.Edits {
				next, err := applyStrReplace(content, e.OldString, e.NewString, e.ReplaceAll)
				if err != nil {
					return "", fmt.Errorf("multi_edit %q edit #%d: %w", clean, i+1, err)
				}
				content = next
			}
			if err := os.WriteFile(clean, []byte(content), 0644); err != nil {
				return "", fmt.Errorf("cannot write file %q: %w", clean, err)
			}
			return fmt.Sprintf("Applied %d edit(s) to %s", len(a.Edits), a.Path), nil
		},
	)
}

// --- Tool: grep_search ---

type grepArgs struct {
	Query string `json:"query"`
	Path  string `json:"path,omitempty"`
}

func (r *ToolRegistry) registerGrepSearch() {
	r.add("grep_search",
		"Search recursively for a pattern in text files within a directory. Skips binary files and hidden directories.",
		json.RawMessage(`{
			"type":"object",
			"properties":{
				"query":{"type":"string","description":"The search term or pattern to look for"},
				"path":{"type":"string","description":"Directory or file to search (defaults to '.')"}
			},
			"required":["query"]
		}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var a grepArgs
			if err := json.Unmarshal(args, &a); err != nil {
				return "", fmt.Errorf("invalid arguments for grep_search: %w", err)
			}
			if a.Query == "" {
				return "", fmt.Errorf("grep_search requires a non-empty \"query\" argument")
			}
			searchPath := a.Path
			if searchPath == "" {
				searchPath = "."
			}
			clean, err := validateToolPath(searchPath)
			if err != nil {
				return "", err
			}

			var matches []string
			maxMatches := 50
			limitErr := fmt.Errorf("match limit reached")
			err = filepath.Walk(clean, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if len(matches) >= maxMatches {
					return limitErr
				}
				if info.IsDir() {
					name := info.Name()
					if name != "." && name != ".." && strings.HasPrefix(name, ".") {
						return filepath.SkipDir
					}
					return nil
				}
				name := info.Name()
				if strings.HasPrefix(name, ".") {
					return nil
				}

				f, err := os.Open(path)
				if err != nil {
					return nil
				}
				defer f.Close()

				buf := make([]byte, 512)
				n, _ := f.Read(buf)
				for i := 0; i < n; i++ {
					if buf[i] == 0 {
						return nil
					}
				}

				_, _ = f.Seek(0, 0)
				scanner := bufio.NewScanner(f)
				lineNum := 0
				for scanner.Scan() {
					lineNum++
					line := scanner.Text()
					if strings.Contains(line, a.Query) {
						rel, _ := filepath.Rel(clean, path)
						if rel == "" || rel == "." {
							rel = filepath.Base(path)
						}
						matches = append(matches, fmt.Sprintf("%s:%d: %s", rel, lineNum, strings.TrimSpace(line)))
						if len(matches) >= maxMatches {
							break
						}
					}
				}
				if err := scanner.Err(); err != nil {
					return nil
				}
				return nil
			})
			if err != nil && err != limitErr {
				return "", fmt.Errorf("search error: %w", err)
			}
			if len(matches) == 0 {
				return fmt.Sprintf("No matches found for %q", a.Query), nil
			}
			return strings.Join(matches, "\n"), nil
		},
	)
}

// RegisterMCPTools registers all tools from connected MCP servers.
func (r *ToolRegistry) RegisterMCPTools(mcpRegistry *mcpclient.Registry) {
	if mcpRegistry == nil {
		return
	}
	for _, entry := range mcpRegistry.ListAllTools() {
		server := entry.Server
		toolName := entry.Tool.Name
		agentToolName := fmt.Sprintf("mcp_%s_%s", server, toolName)

		paramSchema, err := json.Marshal(entry.Tool.InputSchema)
		if err != nil {
			continue
		}

		r.add(agentToolName, entry.Tool.Description, json.RawMessage(paramSchema), func(ctx context.Context, args json.RawMessage) (string, error) {
			var mcpArgs map[string]any
			if len(args) > 0 {
				if err := json.Unmarshal(args, &mcpArgs); err != nil {
					return "", fmt.Errorf("invalid JSON arguments for MCP tool: %w", err)
				}
			}
			res := mcpRegistry.CallTool(ctx, server, toolName, mcpArgs)
			if res.Err != nil {
				return "", res.Err
			}

			var parts []string
			for _, content := range res.Result.Content {
				if tc, ok := content.(mcp.TextContent); ok {
					parts = append(parts, tc.Text)
				} else if ic, ok := content.(mcp.ImageContent); ok {
					parts = append(parts, fmt.Sprintf("[image: %s %d bytes]", ic.MIMEType, len(ic.Data)))
				} else {
					b, _ := json.MarshalIndent(content, "", "  ")
					parts = append(parts, string(b))
				}
			}
			return strings.Join(parts, "\n"), nil
		})
	}
}
