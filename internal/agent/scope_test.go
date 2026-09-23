package agent

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/chat"
)

func defNames(r *ToolRegistry) []string {
	defs := r.Defs()
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Function.Name)
	}
	return out
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func TestObserveDefsHideProhibitedTools(t *testing.T) {
	r := NewToolRegistry(NewToolContext(&RuntimeView{}, nil))
	names := defNames(r)
	for _, want := range []string{"read_file", "todo", "axis_status", "axis_facts", "axis_place"} {
		if !containsName(names, want) {
			t.Errorf("observe defs missing %s", want)
		}
	}
	for _, hidden := range []string{"spawn_subagent", "fleet_exec", "run_on_node", "remote_write_file", "run_shell", "axis_run_task", "write_file", "run_background", "undo_last"} {
		if containsName(names, hidden) {
			t.Errorf("observe defs advertised %s: %v", hidden, names)
		}
	}
	prompt := VisibleToolPrompt(names)
	if strings.Contains(prompt, "spawn_subagent") || strings.Contains(prompt, "fleet: 10/10") {
		t.Fatalf("prompt leaked a hidden name or fleet fraction: %s", prompt)
	}
}

func TestEditScopeAddsWriteAndShell(t *testing.T) {
	r := NewToolRegistry(NewToolContext(&RuntimeView{}, nil))
	r.SetScope(ScopeEdit)
	names := defNames(r)
	for _, want := range []string{"write_file", "edit_file", "multi_edit", "run_shell", "read_file"} {
		if !containsName(names, want) {
			t.Errorf("edit defs missing %s", want)
		}
	}
	if containsName(names, "axis_run_task") || containsName(names, "spawn_subagent") {
		t.Fatalf("edit defs advertised a guarded or never tool: %v", names)
	}
}

func TestExecScopeAddsRunTaskOnly(t *testing.T) {
	r := NewToolRegistry(NewToolContext(&RuntimeView{}, nil))
	r.SetScope(ScopeExec)
	names := defNames(r)
	if !containsName(names, "axis_run_task") || !containsName(names, "run_shell") {
		t.Fatalf("exec defs = %v", names)
	}
	if containsName(names, "fleet_exec") || containsName(names, "run_on_node") {
		t.Fatalf("exec defs advertised a never tool: %v", names)
	}
}

func TestDefaultSafetyGateKeepsPromptReason(t *testing.T) {
	allow, reason, score := DefaultSafetyGate(nil)("git push origin main")
	if !allow {
		t.Fatal("git push should stay in the prompt band, not block")
	}
	if reason == "" || score == 0 {
		t.Fatalf("allow path dropped the gate reason: allow=%v reason=%q score=%d", allow, reason, score)
	}
	var got string
	a := &Agent{
		safety: DefaultSafetyGate(nil),
		output: io.Discard,
		tools:  NewToolRegistry(NewToolContext(&RuntimeView{}, nil)),
		confirm: func(tool, desc string, sc int) ConfirmResult {
			got = desc
			if sc != score {
				t.Errorf("confirm score = %d, gate score = %d", sc, score)
			}
			return ConfirmNo
		},
	}
	a.tools.SetScope(ScopeEdit)
	_, err := a.dispatchShell(context.Background(), json.RawMessage(`{"command":"git push origin main"}`))
	if err == nil {
		t.Fatal("expected the operator denial to return")
	}
	if !strings.Contains(got, "safety why: "+strings.TrimSpace(reason)) || !strings.Contains(got, "git push origin main") {
		t.Fatalf("confirm description = %q, want the gate reason %q and the command", got, reason)
	}
	if strings.Contains(got, "no gate finding") {
		t.Fatalf("confirm description invented an empty finding: %q", got)
	}
}

func TestHiddenToolDoesNotRun(t *testing.T) {
	a := &Agent{
		tools:  NewToolRegistry(NewToolContext(&RuntimeView{}, nil)),
		output: io.Discard,
	}
	called := false
	a.tools.executors["spawn_subagent"] = func(context.Context, json.RawMessage) (string, error) {
		called = true
		return "ran", nil
	}
	_, err := a.dispatchToolCall(context.Background(), chat.ToolCall{
		Function: chat.ToolCallFunction{Name: "spawn_subagent"},
	})
	if err == nil {
		t.Fatal("expected hidden tool to fail")
	}
	if called {
		t.Fatal("hidden tool ran")
	}
	if strings.Contains(err.Error(), "fleet_exec") || strings.Contains(err.Error(), "run_on_node") {
		t.Fatalf("error listed a hidden tool: %v", err)
	}
}
