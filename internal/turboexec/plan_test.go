package turboexec

import (
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func turboNode(verified bool, caps ...string) models.NodeFacts {
	return models.NodeFacts{
		Name: "node-a",
		TurboQuant: &models.TurboQuantInfo{
			Supported:    true,
			Verified:     verified,
			Backends:     []string{"llama.cpp"},
			Capabilities: caps,
		},
	}
}

func TestPrepare_NoTurboQuantLeavesCommandUntouched(t *testing.T) {
	plan := Prepare(models.NodeFacts{Name: "plain"}, models.TaskRequirements{}, "echo hi")
	if plan.Command != "echo hi" {
		t.Fatalf("command = %q, want unchanged", plan.Command)
	}
	if len(plan.Env) != 0 {
		t.Fatalf("env = %v, want none", plan.Env)
	}
}

func TestPrepare_ExportsTurboQuantEnv(t *testing.T) {
	plan := Prepare(turboNode(false), models.TaskRequirements{
		ContextWindowTokens: 128000,
		PrefersTurboQuant:   true,
	}, "echo hi")
	for _, want := range []string{
		"AXIS_TURBOQUANT=1",
		"AXIS_TURBOQUANT_STATUS=detected",
		"AXIS_TURBOQUANT_REQUESTED=1",
		"AXIS_TURBOQUANT_CONTEXT_TOKENS=128000",
	} {
		if !contains(plan.Env, want) {
			t.Fatalf("expected env %q in %v", want, plan.Env)
		}
	}
}

func TestPrepare_InjectsLlamaFlagsForVerifiedBackend(t *testing.T) {
	plan := Prepare(turboNode(true, "flash-attn-flag"), models.TaskRequirements{
		ContextWindowTokens: 128000,
		PrefersTurboQuant:   true,
	}, "llama-server -m model.gguf")
	if !strings.Contains(plan.Command, "--ctx-size 128000") {
		t.Fatalf("expected ctx-size injection, got %q", plan.Command)
	}
	if !strings.Contains(plan.Command, "--flash-attn") {
		t.Fatalf("expected flash-attn injection, got %q", plan.Command)
	}
}

func TestPrepare_DoesNotInjectFlagsForDetectedOnlyBackend(t *testing.T) {
	plan := Prepare(turboNode(false, "flash-attn-flag"), models.TaskRequirements{
		ContextWindowTokens: 128000,
		PrefersTurboQuant:   true,
	}, "llama-server -m model.gguf")
	if strings.Contains(plan.Command, "--ctx-size") || strings.Contains(plan.Command, "--flash-attn") {
		t.Fatalf("did not expect flag injection for detected-only backend, got %q", plan.Command)
	}
}

func TestPrepare_DoesNotDuplicateExistingFlags(t *testing.T) {
	plan := Prepare(turboNode(true, "flash-attn-flag"), models.TaskRequirements{
		ContextWindowTokens: 128000,
		PrefersTurboQuant:   true,
	}, "llama-cli --ctx-size 8192 --flash-attn")
	if strings.Count(plan.Command, "--ctx-size") != 1 {
		t.Fatalf("expected single ctx-size flag, got %q", plan.Command)
	}
	if strings.Count(plan.Command, "--flash-attn") != 1 {
		t.Fatalf("expected single flash-attn flag, got %q", plan.Command)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
