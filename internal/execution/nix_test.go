package execution

import (
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestPlanNixWrapperRequiresObservedNix(t *testing.T) {
	node := models.NodeFacts{
		Name: "node-a",
		Tools: []models.ToolInfo{
			{Name: "git"},
			{Name: "jq"},
		},
		Resources: &models.Resources{Pressure: "low"},
	}

	plan := PlanNixWrapper(node, models.TaskRequirements{
		RequiredTools: []string{"git", "jq"},
	}, `git status --short`)

	if plan.Enabled {
		t.Fatalf("expected nix wrapper to stay disabled without observed nix: %#v", plan)
	}
}

func TestPlanNixWrapperWrapsTrustedToolSet(t *testing.T) {
	node := models.NodeFacts{
		Name: "node-a",
		Tools: []models.ToolInfo{
			{Name: "git"},
			{Name: "jq"},
			{Name: "nix"},
		},
		Resources: &models.Resources{Pressure: "low"},
	}

	plan := PlanNixWrapper(node, models.TaskRequirements{
		RequiredTools: []string{"git", "jq"},
	}, `cat "$AXIS_CONTEXT_FILE" | jq -r '.snapshot.summary' && git status --short`)

	if !plan.Enabled {
		t.Fatal("expected nix wrapper to enable")
	}
	for _, want := range []string{
		"nix --extra-experimental-features 'nix-command flakes' shell",
		"nixpkgs#git",
		"nixpkgs#jq",
		"--command bash -lc",
	} {
		if !strings.Contains(plan.Command, want) {
			t.Fatalf("expected %q in wrapped command %q", want, plan.Command)
		}
	}
	if got := strings.Join(plan.Packages, ","); got != "nixpkgs#git,nixpkgs#jq" {
		t.Fatalf("packages = %q, want nixpkgs#git,nixpkgs#jq", got)
	}
	if !containsEnv(plan.Env, "AXIS_NIX_WRAPPER=1") {
		t.Fatalf("expected AXIS_NIX_WRAPPER env, got %#v", plan.Env)
	}
	if !containsEnv(plan.Env, "AXIS_NIX_PACKAGES=nixpkgs#git,nixpkgs#jq") {
		t.Fatalf("expected AXIS_NIX_PACKAGES env, got %#v", plan.Env)
	}
}

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

func TestPlanNixWrapperSkipsHighPressureNode(t *testing.T) {
	node := models.NodeFacts{
		Name: "node-a",
		Tools: []models.ToolInfo{
			{Name: "git"},
			{Name: "jq"},
			{Name: "nix"},
		},
		Resources: &models.Resources{Pressure: "high"},
	}

	plan := PlanNixWrapper(node, models.TaskRequirements{
		RequiredTools: []string{"git", "jq"},
	}, `git status --short`)

	if plan.Enabled {
		t.Fatalf("expected high pressure node to skip nix wrapper: %#v", plan)
	}
}

func containsEnv(env []string, want string) bool {
	for _, item := range env {
		if item == want {
			return true
		}
	}
	return false
}

func containsReason(reasoning []string, want string) bool {
	for _, reason := range reasoning {
		if reason == want {
			return true
		}
	}
	return false
}
