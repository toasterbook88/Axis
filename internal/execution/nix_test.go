package execution

import (
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestPlanNixWrapperSkipsUnsupportedToolMapping(t *testing.T) {
	node := models.NodeFacts{
		Name: "node-a",
		Tools: []models.ToolInfo{
			{Name: "docker"},
			{Name: "nix"},
		},
		Resources: &models.Resources{Pressure: "low"},
	}

	plan := PlanNixWrapper(node, models.TaskRequirements{
		RequiredTools: []string{"docker"},
	}, `docker ps`)

	if plan.Enabled {
		t.Fatalf("expected unsupported tool mapping to disable nix wrapper: %#v", plan)
	}
}

func TestPlanNixWrapperPreservesVerifiedTurboQuantBackend(t *testing.T) {
	node := models.NodeFacts{
		Name: "mlx-node",
		Tools: []models.ToolInfo{
			{Name: "git"},
			{Name: "jq"},
			{Name: "nix"},
		},
		Resources: &models.Resources{
			Pressure:       "low",
			MemoryTopology: models.MemoryTopologyUnified,
		},
		TurboQuant: &models.TurboQuantInfo{
			Supported: true,
			Verified:  true,
			Backends:  []string{"mlx"},
		},
	}

	plan := PlanNixWrapper(node, models.TaskRequirements{
		RequiredTools:     []string{"git", "jq"},
		PrefersTurboQuant: true,
		PreferredBackends: []string{"mlx"},
	}, `cat "$AXIS_CONTEXT_FILE" | jq -r '.snapshot.summary' && git status --short`)

	if !plan.Enabled {
		t.Fatal("expected nix wrapper to enable for helper tools on verified turboquant node")
	}
	if !containsReason(plan.Notes, "verified turboquant backend preserved natively (mlx); nix wrapper limited to helper tools") {
		t.Fatalf("expected turboquant preservation note, got %#v", plan.Notes)
	}
	if !containsReason(plan.Notes, "unified-memory node kept on native backend; nix wrapper adds only helper tools") {
		t.Fatalf("expected unified memory note, got %#v", plan.Notes)
	}
	if !containsEnv(plan.Env, "AXIS_NIX_NATIVE_BACKEND=mlx") {
		t.Fatalf("expected AXIS_NIX_NATIVE_BACKEND env, got %#v", plan.Env)
	}
}

func TestFirstTurboBackend(t *testing.T) {
	if got := firstTurboBackend(models.NodeFacts{}); got != "" {
		t.Errorf("firstTurboBackend(empty) = %q, want empty", got)
	}
	node := models.NodeFacts{TurboQuant: &models.TurboQuantInfo{Supported: true, Verified: true, Backends: []string{"mlx", "cuda"}}}
	if got := firstTurboBackend(node); got != "mlx" {
		t.Errorf("firstTurboBackend(node) = %q, want mlx", got)
	}
}

func TestPrefersBackend(t *testing.T) {
	if prefersBackend(nil, "mlx") {
		t.Error("prefersBackend(nil, mlx) = true, want false")
	}
	if !prefersBackend([]string{"mlx", "cuda"}, "mlx") {
		t.Error("prefersBackend([mlx,cuda], mlx) = false, want true")
	}
	if prefersBackend([]string{"cuda"}, "mlx") {
		t.Error("prefersBackend([cuda], mlx) = true, want false")
	}
}
