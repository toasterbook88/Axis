package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/chat"
)

func TestParseAutonomyModeAcceptsCluster(t *testing.T) {
	mode, err := ParseAutonomyMode("cluster")
	if err != nil || mode != AutonomyCluster {
		t.Fatalf("ParseAutonomyMode(cluster) = %q, %v", mode, err)
	}
	if ScopeFor(AutonomyCluster) != ScopeCluster || DisplayScope(AutonomyCluster) != "cluster" {
		t.Fatalf("cluster maps to scope %q display %q", ScopeFor(AutonomyCluster), DisplayScope(AutonomyCluster))
	}
}

// TestClusterScopeAdvertisesOnlyClusterReads pins the cluster grant: the
// model sees AXIS fact-plane reads and nothing from the workspace.
func TestClusterScopeAdvertisesOnlyClusterReads(t *testing.T) {
	r := NewToolRegistry(NewToolContext(&RuntimeView{}, nil))
	r.SetScope(ScopeCluster)
	var got []string
	for _, def := range r.Defs() {
		got = append(got, def.Function.Name)
	}
	sort.Strings(got)
	want := []string{"axis_facts", "axis_models", "axis_place", "axis_reservations", "axis_status"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("cluster scope advertises %v, want %v", got, want)
	}
	for _, name := range []string{"read_file", "list_directory", "grep_search", "symbol_search", "git_status", "git_diff", "git_log", "todo", "write_file", "run_shell"} {
		if r.Visible(name) {
			t.Errorf("cluster scope must hide %s", name)
		}
	}
}

// TestClusterScopeHidesMCPAndExtraGrants pins fail-closed: an MCP tool or a
// dynamically granted extra is visible in observe but not in cluster.
func TestClusterScopeHidesMCPAndExtraGrants(t *testing.T) {
	r := NewToolRegistry(NewToolContext(&RuntimeView{}, nil))
	noop := func(context.Context, json.RawMessage) (string, error) { return "ok", nil }
	params := json.RawMessage(`{"type":"object","properties":{}}`)
	r.add("mcp_files_read", "mcp probe", params, noop)
	r.add("custom_probe", "extra probe", params, noop)
	r.allowExtra("custom_probe")

	r.SetScope(ScopeObserve)
	if !r.Visible("mcp_files_read") || !r.Visible("custom_probe") {
		t.Fatal("observe should show the MCP tool and the granted extra")
	}
	r.SetScope(ScopeCluster)
	if r.Visible("mcp_files_read") || r.Visible("custom_probe") {
		t.Fatal("cluster scope must hide MCP tools and extra grants")
	}
}

// TestClusterAutonomyRejectsWorkspaceToolAtDispatch pins that the model
// cannot call a hidden tool even if it names one.
func TestClusterAutonomyRejectsWorkspaceToolAtDispatch(t *testing.T) {
	a := New(Config{Confirm: alwaysConfirm(), Output: &bytes.Buffer{}, Autonomy: AutonomyCluster})
	_, err := a.dispatchToolCall(context.Background(), chat.ToolCall{
		Function: chat.ToolCallFunction{Name: "read_file", Arguments: json.RawMessage(`{"path":"go.mod"}`)},
	})
	if err == nil || !strings.Contains(err.Error(), "not available in this mode") {
		t.Fatalf("cluster autonomy must reject read_file at dispatch, got %v", err)
	}

	a.SetAutonomy(AutonomyDefault)
	if !a.tools.Visible("read_file") {
		t.Fatal("switching back to default should restore workspace reads")
	}
	a.SetAutonomy(AutonomyCluster)
	if a.tools.Visible("read_file") {
		t.Fatal("/autonomy cluster should hide workspace reads again")
	}
}

func TestVisibleToolPromptNamesClusterScope(t *testing.T) {
	p := VisibleToolPrompt([]string{"axis_status"}, ScopeCluster)
	if !strings.Contains(p, "Current autonomy scope: cluster") {
		t.Fatalf("prompt does not name the cluster scope: %q", p)
	}
}
