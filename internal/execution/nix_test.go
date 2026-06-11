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

func TestCommandEntrypoint(t *testing.T) {
	tests := []struct {
		command  string
		expected string
	}{
		{"git status", "git"},
		{"FOO=bar python3 script.py", "python3"},
		{"  python3   script.py  ", "python3"},
		{"", ""},
		{"FOO=bar BAR=baz", ""},
		{"-flag value", "-flag"},
	}
	for _, tt := range tests {
		got := commandEntrypoint(tt.command)
		if got != tt.expected {
			t.Errorf("commandEntrypoint(%q) = %q, want %q", tt.command, got, tt.expected)
		}
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

func TestPlanNixWrapperPythonPip(t *testing.T) {
	node := models.NodeFacts{
		Name: "node-a",
		Tools: []models.ToolInfo{
			{Name: "nix"},
		},
		Resources: &models.Resources{Pressure: "low"},
	}

	// Test python command auto-wrap
	planPython := PlanNixWrapper(node, models.TaskRequirements{
		RequiredTools: []string{"python"},
	}, "python script.py")

	if !planPython.Enabled {
		t.Fatal("expected nix wrapper to be enabled for python")
	}
	if len(planPython.Packages) != 1 || planPython.Packages[0] != "nixpkgs#python3" {
		t.Fatalf("expected nixpkgs#python3 package, got %v", planPython.Packages)
	}

	// Test pip command auto-wrap
	planPip := PlanNixWrapper(node, models.TaskRequirements{
		RequiredTools: []string{"pip"},
	}, "pip install -r requirements.txt")

	if !planPip.Enabled {
		t.Fatal("expected nix wrapper to be enabled for pip")
	}
	if len(planPip.Packages) != 1 || planPip.Packages[0] != "nixpkgs#python3" {
		t.Fatalf("expected nixpkgs#python3 package, got %v", planPip.Packages)
	}
}
