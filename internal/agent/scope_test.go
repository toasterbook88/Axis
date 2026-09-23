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

func TestEditScopeAddsWriteTools(t *testing.T) {
	r := NewToolRegistry(NewToolContext(&RuntimeView{}, nil))
	r.SetScope(ScopeEdit)
	names := defNames(r)
	for _, want := range []string{"write_file", "edit_file", "multi_edit", "undo_last", "review_changes", "read_file"} {
		if !containsName(names, want) {
			t.Errorf("edit defs missing %s", want)
		}
	}
	// run_shell and axis_run_task are PR2-hidden: not advertised at ANY
	// scope in v1, including edit and exec.
	for _, hidden := range []string{"run_shell", "axis_run_task", "spawn_subagent"} {
		if containsName(names, hidden) {
			t.Fatalf("edit defs advertised a PR2-hidden or never tool: %v", names)
		}
	}
}

func TestExecScopeMatchesEditInV1(t *testing.T) {
	// The exec tier exists as the guarded-exec grant, but PR2-hidden tools
	// (run_shell, axis_run_task) are not advertised at any scope in v1, so
	// exec's advertised set equals edit's.
	r := NewToolRegistry(NewToolContext(&RuntimeView{}, nil))
	r.SetScope(ScopeExec)
	names := defNames(r)
	for _, hidden := range []string{"run_shell", "axis_run_task", "fleet_exec", "run_on_node", "spawn_subagent"} {
		if containsName(names, hidden) {
			t.Fatalf("exec defs advertised a PR2-hidden or never tool: %v", names)
		}
	}
	if !containsName(names, "write_file") || !containsName(names, "undo_last") {
		t.Fatalf("exec defs should include the edit set, got: %v", names)
	}
}

// TestPr2HiddenDeniedAtEveryScopeAndDirect pins the v1 contract: run_shell
// and axis_run_task are rejected by Execute (operator direct path) at every
// scope, and never dump the registry.
func TestPr2HiddenDeniedAtEveryScopeAndDirect(t *testing.T) {
	for _, scope := range []ToolScope{ScopeObserve, ScopeEdit, ScopeExec} {
		r := NewToolRegistry(NewToolContext(&RuntimeView{}, nil))
		r.SetScope(scope)
		for _, name := range []string{"run_shell", "axis_run_task"} {
			_, err := r.Execute(context.Background(), name, json.RawMessage(`{}`))
			if err == nil || !strings.Contains(err.Error(), "not available in this mode") {
				t.Errorf("scope %s: Execute(%q) = %v, want 'not available in this mode'", scope, name, err)
			}
			if r.Visible(name) {
				t.Errorf("scope %s: %q must not be visible", scope, name)
			}
		}
	}
}

// TestNeverSetDeniedOnDirectPath pins that the operator-facing Execute also
// hard-denies the never set — no path reaches those executors.
func TestNeverSetDeniedOnDirectPath(t *testing.T) {
	r := NewToolRegistry(NewToolContext(&RuntimeView{}, nil))
	for _, name := range []string{"spawn_subagent", "fleet_exec", "run_on_node", "remote_write_file", "run_background"} {
		_, err := r.Execute(context.Background(), name, json.RawMessage(`{}`))
		if err == nil || !strings.Contains(err.Error(), "not available in this mode") {
			t.Errorf("Execute(%q) = %v, want hard-deny", name, err)
		}
	}
}

// TestUnknownNameFailsClosed pins that a name unknown to the static scope
// lists is not advertised or callable unless explicitly granted.
func TestUnknownNameFailsClosed(t *testing.T) {
	r := NewToolRegistry(NewToolContext(&RuntimeView{}, nil))
	r.add("custom_probe", "test probe",
		json.RawMessage(`{"type":"object","properties":{}}`),
		func(ctx context.Context, args json.RawMessage) (string, error) { return "ok", nil })

	if r.Visible("custom_probe") {
		t.Fatal("unknown dynamic tool must fail closed until granted")
	}
	for _, d := range r.Defs() {
		if d.Function.Name == "custom_probe" {
			t.Fatal("unknown dynamic tool must not appear in Defs")
		}
	}

	// After an explicit grant it is visible and callable; scope changes
	// keep the grant.
	r.allowExtra("custom_probe")
	if !r.Visible("custom_probe") {
		t.Fatal("granted dynamic tool must be visible")
	}
	r.SetScope(ScopeEdit)
	if !r.Visible("custom_probe") {
		t.Fatal("dynamic grant must survive scope changes")
	}

	// A grant cannot resurrect a never-set name.
	r.allowExtra("spawn_subagent")
	if r.Visible("spawn_subagent") {
		t.Fatal("allowExtra must not override the never set")
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
