package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/toasterbook88/axis/internal/chat"
)

// branchSnapshot is a saved conversation state that rollback_session can
// restore, letting the model try a risky approach and rewind the conversation
// if it goes sideways.
type branchSnapshot struct {
	label    string
	messages []chat.Message
}

// branchSession snapshots the current conversation onto the branch stack and
// returns a label the model can refer back to. The model can then take a risky
// path; if it fails, rollback_session restores the conversation to this point.
func (a *Agent) branchSession(label string) (string, error) {
	// Serialize against concurrent tool dispatch: branch_session/rollback_session
	// are read-only tools that run without the mutating-confirm gate, so they
	// must lock dispatchMu to avoid racing on a.branchStack and a.conv.
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()

	msgs := a.conv.Messages()
	// Deep copy: Message is mostly value types, but ToolCalls is a slice and
	// ToolCall.Function.Arguments is a json.RawMessage ([]byte) — copy both so
	// later mutations to the live conversation don't corrupt the snapshot.
	snap := make([]chat.Message, len(msgs))
	for i, m := range msgs {
		snap[i] = m
		if len(m.ToolCalls) > 0 {
			tc := make([]chat.ToolCall, len(m.ToolCalls))
			for j, call := range m.ToolCalls {
				tc[j] = call
				if len(call.Function.Arguments) > 0 {
					argsCopy := make(json.RawMessage, len(call.Function.Arguments))
					copy(argsCopy, call.Function.Arguments)
					tc[j].Function.Arguments = argsCopy
				}
			}
			snap[i].ToolCalls = tc
		}
	}
	if strings.TrimSpace(label) == "" {
		label = fmt.Sprintf("branch-%d", len(a.branchStack)+1)
	}
	a.branchStack = append(a.branchStack, branchSnapshot{label: label, messages: snap})
	n := len(a.conv.Messages())
	return fmt.Sprintf("Branched session as %q (snapshot of %d messages). You can now try a risky approach; call rollback_session with label %q to restore this point if it fails. Call branch_session again to drop the branch once you're happy with the path.", label, n, label), nil
}

// rollbackSession restores the conversation to the most recent branch
// (popping it), discarding all turns taken since. If a label is given, it
// restores that specific branch (and any branches above it). File changes are
// not auto-reverted — use undo_last for file-level rollback.
func (a *Agent) rollbackSession(label string) (string, error) {
	// Serialize against concurrent tool dispatch (see branchSession).
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()

	if len(a.branchStack) == 0 {
		return "", fmt.Errorf("no session branches to roll back to (call branch_session first)")
	}
	var idx int
	if label != "" {
		idx = -1
		for i, b := range a.branchStack {
			if b.label == label {
				idx = i
				break
			}
		}
		if idx < 0 {
			return "", fmt.Errorf("no branch labeled %q (branches: %s)", label, branchLabels(a.branchStack))
		}
	} else {
		idx = len(a.branchStack) - 1
	}
	snap := a.branchStack[idx]
	// Restore: replace all conversation messages with the snapshot.
	a.conv.RestoreAll(snap.messages)
	// Drop the restored branch and any above it.
	a.branchStack = a.branchStack[:idx]
	return fmt.Sprintf("Rolled back session to branch %q (restored %d messages). %d branch(es) remain.", snap.label, len(snap.messages), len(a.branchStack)), nil
}

func branchLabels(bs []branchSnapshot) string {
	labels := make([]string, 0, len(bs))
	for _, b := range bs {
		labels = append(labels, b.label)
	}
	return strings.Join(labels, ", ")
}

// --- Tool registrations (placeholder executors; real dispatch is special-cased) ---

func (r *ToolRegistry) registerSessionBranchTools() {
	r.add("branch_session",
		"Snapshot the current conversation as a named branch point so you can try a risky approach and rewind if it fails. Returns a label you can pass to rollback_session. Does NOT revert file changes — use undo_last for that. Read-only (no confirmation).",
		json.RawMessage(`{
			"type":"object",
			"properties":{
				"label":{"type":"string","description":"Optional name for the branch (auto-generated if omitted)"}
			}
		}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			return "", fmt.Errorf("branch_session must be dispatched through the agent")
		},
	)
	r.add("rollback_session",
		"Restore the conversation to a prior branch_session snapshot, discarding all turns taken since. Pass the label to restore a specific branch; omit to restore the most recent. File changes are not reverted (use undo_last). Read-only (no confirmation).",
		json.RawMessage(`{
			"type":"object",
			"properties":{
				"label":{"type":"string","description":"Optional branch label to restore (defaults to the most recent branch)"}
			}
		}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			return "", fmt.Errorf("rollback_session must be dispatched through the agent")
		},
	)
}
