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
	}
	for _, tt := range tests {
		if got := llmrouter.RoleFromTaskDescription(tt.in); got != tt.want {
			t.Errorf("RoleFromTaskDescription(%q)=%q want %q", tt.in, got, tt.want)
		}
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
	for _, need := range []string{"inference_role=default", "inference_backend=hub", "inference_model=coder:latest", "inference_healthy=true"} {
		if !strings.Contains(joined, need) {
			t.Fatalf("missing %q in %q", need, joined)
		}
	}
}
