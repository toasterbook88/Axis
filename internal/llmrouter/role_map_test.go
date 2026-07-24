package llmrouter_test

import (
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/llmrouter"
)

func TestRoleFromTaskDescription(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"go build ./...", ""},
		{"role=default run inference", "default"},
		{"use role:fast for chat", "fast"},
		{"--role long analyze", "long"},
		{"run ollama inference on 7b", "default"},
		{"need long-context summarization", "long"},
		{"fast chat responses please", "fast"},
		{"mlx inference on apple silicon", "metal"},
		{"sheet metal fabrication", ""}, // bare metal must not match
		// Non-ASCII ToLower must not panic or mis-slice.
		{"İ role=fast", "fast"},
	}
	for _, tt := range tests {
		got := llmrouter.RoleFromTaskDescription(tt.in)
		if got != tt.want {
			t.Errorf("RoleFromTaskDescription(%q)=%q want %q", tt.in, got, tt.want)
		}
	}
}

func TestRoleFromTaskDescription_NonASCIINoPanic(t *testing.T) {
	// Ⱥ (2 bytes) lowercases to ⱥ (3 bytes) — previously paniced when slicing desc.
	const heavy = "ȺȺȺȺȺȺȺȺȺȺȺȺȺȺȺ role=x"
	got := llmrouter.RoleFromTaskDescription(heavy)
	if got != "x" {
		t.Fatalf("got %q want x", got)
	}
}

func TestFormatRouteReasoning(t *testing.T) {
	lines := llmrouter.FormatRouteReasoning(llmrouter.RoleRouteDecision{
		Role:         "default",
		Backend:      "hub",
		Model:        "coder:latest",
		Endpoint:     "http://127.0.0.1:4000/v1",
		Kind:         "openai-compatible",
		Healthy:      true,
		ModelPresent: true,
	})
	joined := strings.Join(lines, "\n")
	for _, need := range []string{
		"inference_role=default",
		"inference_backend=hub",
		"inference_model=coder:latest",
		"inference_endpoint_class=loopback",
		"inference_healthy=true",
	} {
		if !strings.Contains(joined, need) {
			t.Fatalf("missing %q in %q", need, joined)
		}
	}
	if strings.Contains(joined, "127.0.0.1") {
		t.Fatal("raw endpoint must not appear in placement reasoning")
	}
}

func TestEndpointIsClusterLocal(t *testing.T) {
	if !llmrouter.EndpointIsClusterLocal("http://127.0.0.1:4000/v1") {
		t.Fatal("loopback should be local")
	}
	if !llmrouter.EndpointIsClusterLocal("http://192.168.1.10:11434") {
		t.Fatal("private LAN should be local")
	}
	if llmrouter.EndpointIsClusterLocal("https://api.example.com/v1") {
		t.Fatal("public host should be remote")
	}
}
