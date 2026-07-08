package agent

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/toasterbook88/axis/internal/chat"
)

// RouterClassifier decides, for a given message list, whether the cheap
// (fast/low-cost) backend is appropriate or the strong (primary) backend
// should be used. Returning false routes to the primary backend.
type RouterClassifier func(msgs []chat.Message) (useCheap bool, reason string)

// RoutingBackend is a ChatBackend that routes each ChatStream call to one of
// two backends — a strong (primary) model for hard work (code generation,
// planning, edits) and a cheap (fast) model for simple work (status reads,
// summarization, classification, short lookups). This makes long autonomous
// agent runs economically viable: most turns are cheap, only the hard ones use
// the expensive model.
type RoutingBackend struct {
	primary    ChatBackend
	cheap      ChatBackend
	classifier RouterClassifier
	verbose    bool
	output     io.Writer
}

// NewRoutingBackend creates a routing backend. If cheap is nil, all calls go
// to primary (a safe no-op when no cheap model is configured).
func NewRoutingBackend(primary, cheap ChatBackend, classifier RouterClassifier) *RoutingBackend {
	if classifier == nil {
		classifier = DefaultRouterClassifier
	}
	return &RoutingBackend{primary: primary, cheap: cheap, classifier: classifier}
}

// SetVerbose enables routing-decision trace output to w.
func (r *RoutingBackend) SetVerbose(w io.Writer) {
	r.verbose = true
	r.output = w
}

// ChatStream routes to the chosen backend and streams its response.
func (r *RoutingBackend) ChatStream(ctx context.Context, msgs []chat.Message, tools []chat.ToolDef, w io.Writer) (chat.Message, error) {
	if r.cheap == nil {
		resp, err := r.primary.ChatStream(ctx, msgs, tools, w)
		if err == nil {
			resp.RouterChoice = "primary"
		}
		return resp, err
	}
	useCheap, reason := r.classifier(msgs)
	backend := r.primary
	which := "primary"
	if useCheap {
		backend = r.cheap
		which = "cheap"
	}
	if r.verbose && r.output != nil {
		fmt.Fprintf(r.output, "  %s routing → %s (%s)\n", dimLabel("⇄"), which, reason)
	}
	resp, err := backend.ChatStream(ctx, msgs, tools, w)
	if err == nil {
		// Tag the response so Stats() can attribute cost if backends support it.
		resp.RouterChoice = which
	}
	return resp, err
}

// DefaultRouterClassifier is the heuristic turn classifier. It routes to the
// cheap backend only when the turn looks simple: a short user prompt with no
// code/implementation keywords and no recent code-editing tool calls. Anything
// ambiguous or code-shaped goes to the primary backend (safer to over-use the
// strong model than to fumble hard work on a weak one).
func DefaultRouterClassifier(msgs []chat.Message) (bool, string) {
	// Find the last user message.
	var lastUser string
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == chat.RoleUser {
			lastUser = msgs[i].Content
			break
		}
	}
	// Find the most recent assistant message and inspect its tool calls.
	var lastAssistantToolCalls []chat.ToolCall
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == chat.RoleAssistant {
			lastAssistantToolCalls = msgs[i].ToolCalls
			break
		}
	}

	// If the previous assistant turn invoked a code-editing tool or a sub-agent,
	// the model is mid-flow on hard work → keep it on the strong model.
	for _, tc := range lastAssistantToolCalls {
		switch tc.Function.Name {
		case "write_file", "edit_file", "multi_edit", "spawn_subagent", "axis_run_task", "run_on_node":
			return false, "previous turn used a mutating/complex tool"
		}
	}

	// Long prompts usually carry substantial context the cheap model can't hold.
	if len(lastUser) > 600 {
		return false, "long user prompt"
	}

	// Code/implementation keywords in the prompt signal hard work.
	lower := strings.ToLower(lastUser)
	for _, kw := range routerHardKeywords {
		if strings.Contains(lower, kw) {
			return false, "prompt contains code/implementation keyword"
		}
	}

	// Short, keyword-free prompt → cheap is fine.
	if strings.TrimSpace(lastUser) == "" {
		// No user message (e.g. continuation after tool results) — keep strong.
		return false, "no user message in this turn"
	}
	return true, "short, non-code prompt"
}

// routerHardKeywords are terms that signal the turn needs the strong model.
var routerHardKeywords = []string{
	"implement", "refactor", "rewrite", "migrate", "architect", "design",
	"debug", "fix the bug", "fix bug", "optimize", "review the code",
	"write a function", "write a method", "write a class", "write a test",
	"write a script", "create a", "build a", "deploy", "structure",
	"algorithm", "concurrency", "race condition", "deadlock",
	"pull request", "pr ", "commit", "branch", "merge",
}

// dimLabel is a minimal styling helper kept local to avoid a ui import cycle
// in the routing path.
func dimLabel(s string) string { return "\x1b[2m" + s + "\x1b[0m" }
