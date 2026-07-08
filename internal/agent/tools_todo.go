package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// todoStatus constants for todo item lifecycle.
const (
	todoPending    = "pending"
	todoInProgress = "in_progress"
	todoDone       = "done"
	todoDropped    = "dropped"
)

// todoItem is a single tracked task within a multi-step plan.
type todoItem struct {
	Content string `json:"content"`
	Status  string `json:"status"`
	Phase   string `json:"phase,omitempty"`
}

// todoStore holds the in-memory, session-scoped todo list. It is safe for
// concurrent use because tool calls dispatch in parallel.
type todoStore struct {
	mu    sync.Mutex
	items []todoItem
}

// newTodoStore returns an empty todo store.
func newTodoStore() *todoStore {
	return &todoStore{}
}

func (s *todoStore) init(items []todoItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = items
}

func (s *todoStore) append(phase string, contents []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range contents {
		s.items = append(s.items, todoItem{Content: c, Status: todoPending, Phase: phase})
	}
}

// setStatus finds the first item whose content matches `task` exactly and sets
// its status. Returns false if no match was found.
func (s *todoStore) setStatus(task, status string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].Content == task {
			s.items[i].Status = status
			return true
		}
	}
	return false
}

// render produces a compact text view of the current list grouped by phase.
func (s *todoStore) render() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.items) == 0 {
		return "Todo list is empty."
	}
	// Group by phase preserving first-seen order.
	var phaseOrder []string
	byPhase := map[string][]todoItem{}
	for _, it := range s.items {
		p := it.Phase
		if p == "" {
			p = "Tasks"
		}
		if _, ok := byPhase[p]; !ok {
			phaseOrder = append(phaseOrder, p)
		}
		byPhase[p] = append(byPhase[p], it)
	}
	var b strings.Builder
	for _, p := range phaseOrder {
		fmt.Fprintf(&b, "%s\n", p)
		for _, it := range byPhase[p] {
			mark := "[ ]"
			switch it.Status {
			case todoInProgress:
				mark = "[~]"
			case todoDone:
				mark = "[x]"
			case todoDropped:
				mark = "[-]"
			}
			fmt.Fprintf(&b, "  %s %s\n", mark, it.Content)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// todoArgs is the argument shape for the todo tool.
type todoArgs struct {
	Op    string `json:"op"`
	Items []struct {
		Content string `json:"content"`
		Phase   string `json:"phase,omitempty"`
	} `json:"items,omitempty"`
	Task  string `json:"task,omitempty"`
	Phase string `json:"phase,omitempty"`
}

// registerTodo registers the in-session todo tracking tool. The model uses it
// to break multi-step work into a tracked plan and update progress, so long
// tasks stay organized instead of drifting.
func (r *ToolRegistry) registerTodo(store *todoStore) {
	r.add("todo",
		"Track a multi-step plan for the current task. Use it to break work into steps, mark progress, and keep long tasks organized. "+
			"Ops: \"init\" (replace the whole list from items[]), \"append\" (add items[] to a phase), \"start\"/\"done\"/\"drop\" (update one task by exact content via \"task\"), \"view\" (read the current list).",
		json.RawMessage(`{
			"type":"object",
			"properties":{
				"op":{"type":"string","enum":["init","append","start","done","drop","view"],"description":"Operation to perform"},
				"items":{"type":"array","items":{"type":"object","properties":{"content":{"type":"string"},"phase":{"type":"string"}},"required":["content"]},"description":"Tasks for init/append"},
				"task":{"type":"string","description":"Exact task content for start/done/drop"},
				"phase":{"type":"string","description":"Phase label for append (optional)"}
			},
			"required":["op"]
		}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var a todoArgs
			if err := json.Unmarshal(args, &a); err != nil {
				return "", fmt.Errorf("invalid arguments for todo: %w", err)
			}
			switch a.Op {
			case "init":
				var items []todoItem
				for _, it := range a.Items {
					items = append(items, todoItem{Content: it.Content, Status: todoPending, Phase: it.Phase})
				}
				store.init(items)
				return "Todo list initialized:\n" + store.render(), nil
			case "append":
				var contents []string
				for _, it := range a.Items {
					contents = append(contents, it.Content)
				}
				store.append(a.Phase, contents)
				return "Appended to todo list:\n" + store.render(), nil
			case "start":
				if a.Task == "" {
					return "", fmt.Errorf("todo start requires \"task\"")
				}
				if !store.setStatus(a.Task, todoInProgress) {
					return "", fmt.Errorf("todo task not found: %q", a.Task)
				}
				return "Todo updated:\n" + store.render(), nil
			case "done":
				if a.Task == "" {
					return "", fmt.Errorf("todo done requires \"task\"")
				}
				if !store.setStatus(a.Task, todoDone) {
					return "", fmt.Errorf("todo task not found: %q", a.Task)
				}
				return "Todo updated:\n" + store.render(), nil
			case "drop":
				if a.Task == "" {
					return "", fmt.Errorf("todo drop requires \"task\"")
				}
				if !store.setStatus(a.Task, todoDropped) {
					return "", fmt.Errorf("todo task not found: %q", a.Task)
				}
				return "Todo updated:\n" + store.render(), nil
			case "view":
				return store.render(), nil
			default:
				return "", fmt.Errorf("unknown todo op %q (use init/append/start/done/drop/view)", a.Op)
			}
		},
	)
}
