package agent

import (
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
)

func TestFindNode(t *testing.T) {
	nodes := []config.NodeConfig{
		{Name: "nixos", Hostname: "192.168.1.219", SSHUser: "axis"},
		{Name: "foundry", Hostname: "192.168.1.249", SSHUser: "axis"},
	}
	if n := findNode(nodes, "nixos"); n == nil || n.Hostname != "192.168.1.219" {
		t.Fatalf("findNode nixos: got %+v", n)
	}
	if n := findNode(nodes, "missing"); n != nil {
		t.Fatalf("findNode missing should be nil, got %+v", n)
	}
}

func TestNodeNames(t *testing.T) {
	nodes := []config.NodeConfig{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	got := nodeNames(nodes)
	if got != "a, b, c" {
		t.Fatalf("got %q", got)
	}
}

func TestRunOnNodeRegistryRequiresAgentDispatch(t *testing.T) {
	// Direct registry execution must not open raw SSH — agent dispatch + guarded runner only.
	tc := NewToolContext(&RuntimeView{
		Config: &config.Config{Nodes: []config.NodeConfig{{Name: "only", Hostname: "h"}}},
	}, nil)
	r := NewToolRegistry(tc)
	_, err := execTool(t, r, "run_on_node", mustJSON(t, map[string]any{"node": "ghost", "command": "ls"}))
	if err == nil || !strings.Contains(err.Error(), "safety gate") {
		t.Fatalf("expected safety-gate dispatch error, got %v", err)
	}
	_, err = execTool(t, r, "run_on_node", mustJSON(t, map[string]any{"node": "any", "command": "ls"}))
	if err == nil || !strings.Contains(err.Error(), "safety gate") {
		t.Fatalf("expected safety-gate dispatch error, got %v", err)
	}
}

func TestRemoteReadFileValidation(t *testing.T) {
	tc := NewToolContext(&RuntimeView{Config: &config.Config{}}, nil)
	r := NewToolRegistry(tc)
	// missing path
	_, err := execTool(t, r, "remote_read_file", mustJSON(t, map[string]any{"node": "x"}))
	if err == nil || !strings.Contains(err.Error(), "requires") {
		t.Fatalf("expected validation error, got %v", err)
	}
	// unknown node → not found (no SSH attempt)
	_, err = execTool(t, r, "remote_read_file", mustJSON(t, map[string]any{"node": "ghost", "path": "/etc/hostname"}))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found, got %v", err)
	}
}

func TestRemoteGrepAndListValidation(t *testing.T) {
	tc := NewToolContext(&RuntimeView{Config: &config.Config{}}, nil)
	r := NewToolRegistry(tc)
	_, err := execTool(t, r, "remote_grep", mustJSON(t, map[string]any{"node": "x"}))
	if err == nil || !strings.Contains(err.Error(), "requires") {
		t.Fatalf("remote_grep validation: %v", err)
	}
	_, err = execTool(t, r, "remote_list", mustJSON(t, map[string]any{}))
	if err == nil || !strings.Contains(err.Error(), "requires") {
		t.Fatalf("remote_list validation: %v", err)
	}
}

func TestClusterContextSnippetEmpty(t *testing.T) {
	a := New(Config{Backend: noopChatBackend{}, ToolContext: NewToolContext(&RuntimeView{}, nil), Output: discardWriter()})
	if s := a.clusterContextSnippet(); s != "" {
		t.Fatalf("expected empty snippet with no snapshot, got %q", s)
	}
}

func TestClusterContextSnippetPresent(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Status:  "healthy",
		Summary: models.ClusterSummary{TotalNodes: 2, ReachableNodes: 2, TotalRAMMB: 16000, TotalFreeRAMMB: 8000},
		Nodes: []models.NodeFacts{
			{Name: "nixos", Status: "complete"},
			{Name: "foundry", Status: "complete"},
		},
	}
	tc := NewToolContext(&RuntimeView{Snapshot: snap}, nil)
	a := New(Config{Backend: noopChatBackend{}, ToolContext: tc, Output: discardWriter()})
	s := a.clusterContextSnippet()
	if s == "" || !strings.Contains(s, "live_cluster_context") || !strings.Contains(s, "nixos") {
		t.Fatalf("expected cluster context with nixos, got %q", s)
	}
}

func TestShellQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"simple", "'simple'"},
		{"it's", "'it'\\''s'"},
		{"a'b'c", "'a'\\''b'\\''c'"},
	}
	for _, c := range cases {
		if got := shellQuote(c.in); got != c.want {
			t.Errorf("shellQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// discardWriter returns a writer that discards output for tests.
func discardWriter() *strings.Builder { return &strings.Builder{} }
