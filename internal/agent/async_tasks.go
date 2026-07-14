package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/toasterbook88/axis/internal/ui"
)

// backgroundTask tracks one asynchronously-running command started by the
// run_background tool. Output is captured incrementally so check_task can
// report partial progress while the command is still running.
type backgroundTask struct {
	id        string
	command   string
	node      string // empty = local
	startedAt time.Time
	mu        sync.Mutex
	output    bytes.Buffer
	done      bool
	failed    bool
	cancel    context.CancelFunc
}

// status returns a snapshot of the task's state for the check_task tool.
func (t *backgroundTask) status() (running bool, finished bool, errored bool, output string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.done, t.done, t.failed && t.done, t.output.String()
}

// appendOutput appends command output under the lock.
func (t *backgroundTask) appendOutput(p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.output.Write(p)
}

// backgroundTaskStore holds all background tasks started in a session. Safe
// for concurrent use (tasks are checked from the agent loop while running).
type backgroundTaskStore struct {
	mu    sync.Mutex
	tasks map[string]*backgroundTask
	seq   int
}

func newBackgroundTaskStore() *backgroundTaskStore {
	return &backgroundTaskStore{tasks: make(map[string]*backgroundTask)}
}

func (s *backgroundTaskStore) nextID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return fmt.Sprintf("bg-%d", s.seq)
}

func (s *backgroundTaskStore) add(t *backgroundTask) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[t.id] = t
}

func (s *backgroundTaskStore) get(id string) (*backgroundTask, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	return t, ok
}

// snapshot returns the tasks in creation order for list_background_tasks.
func (s *backgroundTaskStore) snapshot() []*backgroundTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*backgroundTask, 0, len(s.tasks))
	for _, t := range s.tasks {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].startedAt.Before(out[j].startedAt) })
	return out
}

// --- run_background / check_task / list_background_tasks args ---

type runBackgroundArgs struct {
	Command string `json:"command"`
	Node    string `json:"node,omitempty"`
}

type checkTaskArgs struct {
	ID      string `json:"id"`
	WaitFor string `json:"wait_for,omitempty"`
}

// runBackgroundTask launches the command in a goroutine and returns its task
// ID immediately. The command runs via the agent's shell runner (local) or
// remote SSH (when node is set). Output is captured for later polling.
func (a *Agent) runBackgroundTask(ctx context.Context, args runBackgroundArgs) (string, error) {
	if strings.TrimSpace(args.Command) == "" {
		return "", fmt.Errorf("run_background requires a non-empty \"command\" argument")
	}
	taskCtx, cancel := context.WithCancel(ctx)
	task := &backgroundTask{
		command:   args.Command,
		node:      args.Node,
		startedAt: time.Now(),
		cancel:    cancel,
	}
	task.id = a.backgroundTasks.nextID()
	a.backgroundTasks.add(task)

	go func() {
		defer cancel()
		var out string
		var runErr error
		if args.Node != "" {
			out, runErr = runRemote(taskCtx, a.toolContext, args.Node, args.Command)
		} else {
			out, runErr = a.runShell(taskCtx, args.Command)
		}
		if out != "" {
			task.appendOutput([]byte(out))
		}
		if runErr != nil {
			task.appendOutput(fmt.Appendf(nil, "\n[error: %s]\n", runErr.Error()))
		}
		task.mu.Lock()
		task.done = true
		task.failed = runErr != nil
		task.mu.Unlock()
	}()

	loc := "locally"
	if args.Node != "" {
		loc = "on " + args.Node
	}
	if a.verbose {
		fmt.Fprintf(a.output, "%s Started background task %s (%s)\n", ui.Cyan("⤴"), task.id, loc)
	}
	return fmt.Sprintf("Background task %s started: running %s. Command: %s\nUse check_task with id %q to poll for results.", task.id, loc, truncateForLog(args.Command, 120), task.id), nil
}

func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

// checkBackgroundTask returns the status and captured output of a task. If
// wait_for is set and the task is still running, it blocks up to that duration
// for completion before reporting (useful for "run, then wait a bit").
func (a *Agent) checkBackgroundTask(ctx context.Context, args checkTaskArgs) (string, error) {
	if args.ID == "" {
		return "", fmt.Errorf("check_task requires a non-empty \"id\" argument")
	}
	task, ok := a.backgroundTasks.get(args.ID)
	if !ok {
		return "", fmt.Errorf("no background task with id %q", args.ID)
	}

	// wait_for arrives as a string (e.g. "30s") since encoding/json cannot
	// unmarshal a JSON string into time.Duration directly.
	var waitFor time.Duration
	if strings.TrimSpace(args.WaitFor) != "" {
		parsed, err := time.ParseDuration(args.WaitFor)
		if err != nil {
			return "", fmt.Errorf("invalid wait_for duration %q: %w", args.WaitFor, err)
		}
		waitFor = parsed
	}

	running, _, _, _ := task.status()
	if running && waitFor > 0 {
		deadline := time.Now().Add(waitFor)
		// Use a ticker instead of time.After to avoid accumulating leaked
		// timers across loop iterations.
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return formatTaskStatus(task), nil
			case <-ticker.C:
			}
			running, _, _, _ = task.status()
			if !running {
				break
			}
		}
	}
	return formatTaskStatus(task), nil
}

func formatTaskStatus(t *backgroundTask) string {
	running, _, errored, output := t.status()
	state := "running"
	if !running {
		if errored {
			state = "failed"
		} else {
			state = "completed"
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Task %s: %s\n", t.id, state)
	if t.node != "" {
		fmt.Fprintf(&b, "Node: %s\n", t.node)
	}
	fmt.Fprintf(&b, "Command: %s\n", truncateForLog(t.command, 200))
	fmt.Fprintf(&b, "Started: %s ago\n", time.Since(t.startedAt).Round(time.Second))
	fmt.Fprintf(&b, "Output (%d chars):\n", len(output))
	if output == "" {
		b.WriteString("(no output yet)\n")
	} else {
		b.WriteString(output)
		if !strings.HasSuffix(output, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// listBackgroundTasks returns a one-line summary of every background task in
// the session.
func (a *Agent) listBackgroundTasks() string {
	tasks := a.backgroundTasks.snapshot()
	if len(tasks) == 0 {
		return "No background tasks in this session."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Background tasks (%d):\n", len(tasks))
	for _, t := range tasks {
		running, _, errored, _ := t.status()
		state := "running"
		if !running {
			if errored {
				state = "failed"
			} else {
				state = "done"
			}
		}
		where := "local"
		if t.node != "" {
			where = t.node
		}
		fmt.Fprintf(&b, "  %s  [%s]  %s  (%s)  %s\n", t.id, state, where, time.Since(t.startedAt).Round(time.Second), truncateForLog(t.command, 60))
	}
	return b.String()
}

// dispatchRunBackground is the confirmation-gated entry point for the
// run_background tool. run_background is mutating (it runs a command), so it
// confirms like run_shell before launching the async task.
func (a *Agent) dispatchRunBackground(ctx context.Context, args json.RawMessage) (string, error) {
	var a0 runBackgroundArgs
	if err := json.Unmarshal(args, &a0); err != nil {
		return "", fmt.Errorf("invalid arguments for run_background: %w", err)
	}
	if strings.TrimSpace(a0.Command) == "" {
		return "", fmt.Errorf("run_background requires a non-empty \"command\" argument")
	}
	// Session-level block.
	a.dispatchMu.Lock()
	if a.blockAll {
		a.dispatchMu.Unlock()
		return "", fmt.Errorf("operator has blocked all tool execution for this session")
	}
	a.dispatchMu.Unlock()
	// Safety gate + confirmation (mirrors run_shell's gating).
	allowed, reason, score := a.safety(a0.Command)
	forceConfirm := false
	if !allowed {
		forceConfirm = true
		if score < 80 {
			score = 80
		}
	}
	a.dispatchMu.Lock()
	needsConfirm := !a.autoApproveAll || forceConfirm
	a.dispatchMu.Unlock()
	if needsConfirm {
		desc := a0.Command
		if a0.Node != "" {
			desc = fmt.Sprintf("[on %s] %s", a0.Node, a0.Command)
		}
		if forceConfirm {
			desc = fmt.Sprintf("[OVERRIDE SAFETY - BLOCKED REASON: %s] %s", reason, desc)
		}
		a.dispatchMu.Lock()
		decision := a.confirm("run_background", desc, score)
		switch decision {
		case ConfirmNo:
			a.dispatchMu.Unlock()
			return "", fmt.Errorf("operator declined to run background: %s", a0.Command)
		case ConfirmAlways:
			if !forceConfirm {
				a.autoApproveAll = true
			}
			a.dispatchMu.Unlock()
		case ConfirmNever:
			a.blockAll = true
			a.dispatchMu.Unlock()
			return "", fmt.Errorf("operator has blocked all tool execution for this session")
		case ConfirmYes:
			a.dispatchMu.Unlock()
		}
	}
	return a.runBackgroundTask(ctx, a0)
}

// dispatchCheckTask is the read-only entry point for check_task.
func (a *Agent) dispatchCheckTask(ctx context.Context, args json.RawMessage) (string, error) {
	var a0 checkTaskArgs
	if err := json.Unmarshal(args, &a0); err != nil {
		return "", fmt.Errorf("invalid arguments for check_task: %w", err)
	}
	return a.checkBackgroundTask(ctx, a0)
}

// registerBackgroundTools registers run_background, check_task, and
// list_background_tasks. Execution is dispatched through Agent methods
// (special-cased in dispatchToolCall) because they need access to the agent's
// shell runner and the session's background task store.
func (r *ToolRegistry) registerBackgroundTools() {
	r.add("run_background",
		"Start a shell command running in the background (locally or on a named cluster node via SSH) and return a task id immediately without blocking. Poll the result with check_task. Use for long-running work (builds, tests, training) so the agent can continue other work meanwhile. Requires confirmation (it runs a command).",
		json.RawMessage(`{
			"type":"object",
			"properties":{
				"command":{"type":"string","description":"Shell command to run in the background"},
				"node":{"type":"string","description":"Optional cluster node to run on via SSH (omit for local)"}
			},
			"required":["command"]
		}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			return "", fmt.Errorf("run_background must be dispatched through the agent safety gate")
		},
	)
	r.add("check_task",
		"Check the status and captured output of a background task started by run_background. Returns running/completed/failed plus the output so far. Optional wait_for (e.g. \"30s\") blocks up to that duration for completion before reporting. Read-only.",
		json.RawMessage(`{
			"type":"object",
			"properties":{
				"id":{"type":"string","description":"Background task id (e.g. bg-1)"},
				"wait_for":{"type":"string","description":"Optional duration to wait for completion (e.g. 30s)"}
			},
			"required":["id"]
		}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			return "", fmt.Errorf("check_task must be dispatched through the agent")
		},
	)
	r.add("list_background_tasks",
		"List all background tasks in this session with their status (running/done/failed), node, elapsed time, and command. Read-only.",
		json.RawMessage(`{"type":"object","properties":{}}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			return "", fmt.Errorf("list_background_tasks must be dispatched through the agent")
		},
	)
}
