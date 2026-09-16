package placement

import (
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/failures"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

// --- Test Helpers ---

func nodeComplete(name string, freeRAM int64, pressure string, tools ...string) models.NodeFacts {
	var toolInfos []models.ToolInfo
	for _, t := range tools {
		toolInfos = append(toolInfos, models.ToolInfo{Name: t, Path: "/usr/bin/" + t, Class: models.ToolClassBuild})
	}
	return models.NodeFacts{
		Name:   name,
		Status: models.StatusComplete,
		Resources: &models.Resources{
			CPUCores:   8,
			RAMTotalMB: 8192,
			RAMFreeMB:  freeRAM,
			Pressure:   pressure,
		},
		Tools:       toolInfos,
		CollectedAt: time.Now().UTC(),
	}
}

func nodeUnreachable(name string) models.NodeFacts {
	return models.NodeFacts{
		Name:        name,
		Status:      models.StatusUnreachable,
		Error:       "connection refused",
		CollectedAt: time.Now().UTC(),
	}
}

// --- Filter Tests ---

func TestFilterExcludesUnreachable(t *testing.T) {
	nodes := []models.NodeFacts{
		nodeComplete("a", 4000, "none", "git"),
		nodeUnreachable("b"),
	}
	reqs := models.TaskRequirements{}
	result := FilterCandidates(reqs, nodes, nil)
	if len(result) != 1 || result[0].Name != "a" {
		t.Errorf("expected [a], got %v", names(result))
	}
}

func TestFilterExcludesLowRAM(t *testing.T) {
	nodes := []models.NodeFacts{
		nodeComplete("a", 2000, "none"),
		nodeComplete("b", 5000, "none"),
	}
	reqs := models.TaskRequirements{MinFreeRAMMB: 4096}
	result := FilterCandidates(reqs, nodes, nil)
	if len(result) != 1 || result[0].Name != "b" {
		t.Errorf("expected [b], got %v", names(result))
	}
}

func TestFilterExcludesMissingTool(t *testing.T) {
	nodes := []models.NodeFacts{
		nodeComplete("a", 4000, "none", "python3"),
		nodeComplete("b", 4000, "none", "git", "go"),
	}
	reqs := models.TaskRequirements{RequiredTools: []string{"git"}}
	result := FilterCandidates(reqs, nodes, nil)
	if len(result) != 1 || result[0].Name != "b" {
		t.Errorf("expected [b], got %v", names(result))
	}
}

func TestFilterPassesAllQualified(t *testing.T) {
	nodes := []models.NodeFacts{
		nodeComplete("a", 5000, "none", "git"),
		nodeComplete("b", 6000, "none", "git"),
	}
	reqs := models.TaskRequirements{RequiredTools: []string{"git"}, MinFreeRAMMB: 4096}
	result := FilterCandidates(reqs, nodes, nil)
	if len(result) != 2 {
		t.Errorf("expected 2 candidates, got %d", len(result))
	}
}

func TestFilterRequiresAllTools(t *testing.T) {
	nodes := []models.NodeFacts{
		nodeComplete("docker-only", 5000, "none", "docker"),
		nodeComplete("docker-and-go", 5000, "none", "docker", "go"),
	}
	reqs := models.TaskRequirements{RequiredTools: []string{"docker", "go"}}

	result := FilterCandidates(reqs, nodes, nil)
	if len(result) != 1 || result[0].Name != "docker-and-go" {
		t.Errorf("expected [docker-and-go], got %v", names(result))
	}
}

func TestFilterAllowsLightTaskOnCriticalLinuxPSI(t *testing.T) {
	node := nodeComplete("thrashing", 8192, "high", "git")
	node.Resources.PressureSource = "linux-psi"
	node.Resources.PressureStall10 = 16.4

	reqs := models.TaskRequirements{
		RequiredTools: []string{"git"},
	}
	result := FilterCandidates(reqs, []models.NodeFacts{node}, nil)
	if len(result) != 1 || result[0].Name != "thrashing" {
		t.Fatalf("expected light task to stay eligible, got %v", names(result))
	}
}

func TestRankByPressure(t *testing.T) {
	candidates := []models.NodeFacts{
		nodeComplete("high-node", 4000, "high"),
		nodeComplete("none-node", 4000, "none"),
	}
	ranked := RankCandidates(candidates, models.TaskRequirements{}, nil)
	if ranked[0].Name != "none-node" {
		t.Errorf("expected none-node first, got %s", ranked[0].Name)
	}
}

func TestRankByFreeRAM(t *testing.T) {
	candidates := []models.NodeFacts{
		nodeComplete("low-ram", 2000, "none"),
		nodeComplete("high-ram", 6000, "none"),
	}
	ranked := RankCandidates(candidates, models.TaskRequirements{}, nil)
	if ranked[0].Name != "high-ram" {
		t.Errorf("expected high-ram first, got %s", ranked[0].Name)
	}
}

func TestRankByGPU(t *testing.T) {
	candidates := []models.NodeFacts{
		nodeComplete("cpu-only", 6000, "none"),
		nodeComplete("gpu-node", 6000, "none"),
	}
	candidates[1].Resources.GPUs = []models.GPUInfo{{Model: "RTX 4090", Vendor: "nvidia", Capabilities: []string{"cuda"}}}

	ranked := RankCandidates(candidates, models.TaskRequirements{}, nil)
	if ranked[0].Name != "gpu-node" {
		t.Errorf("expected gpu-node first, got %s", ranked[0].Name)
	}
}

func TestRankPrefersLowerReservationRatioWhenAllocatableTied(t *testing.T) {
	candidates := []models.NodeFacts{
		nodeComplete("alpha", 5000, "none"),
		nodeComplete("beta", 3500, "none"),
	}
	candidates[0].RAMReservedMB = 2000
	candidates[1].RAMReservedMB = 500

	ranked := RankCandidates(candidates, models.TaskRequirements{}, nil)
	if ranked[0].Name != "beta" {
		t.Fatalf("expected beta first on lower reservation ratio, got %s", ranked[0].Name)
	}
}

func TestSelectFailure_CriticalRuntimePressureReasoning(t *testing.T) {
	node := nodeComplete("thrashing", 8192, "high", "ollama")
	node.Resources.PressureSource = "linux-psi"
	node.Resources.PressureStall10 = 19.2

	reqs := models.TaskRequirements{
		RequiredTools: []string{"ollama"},
		MinFreeRAMMB:  4096,
	}
	d := SelectBestNode(reqs, []models.NodeFacts{node}, nil)
	if d.OK {
		t.Fatal("expected placement failure")
	}
	found := false
	for _, reason := range d.Reasoning {
		if contains(reason, "linux-psi") && contains(reason, "critical runtime memory pressure") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected critical pressure reasoning, got %v", d.Reasoning)
	}
}

func TestReservedRAMAffectsSelection(t *testing.T) {
	nodes := []models.NodeFacts{
		nodeComplete("alpha", 5000, "none", "git"),
		nodeComplete("beta", 4200, "none", "git"),
	}
	nodes[0].RAMReservedMB = 2000
	reqs := models.TaskRequirements{RequiredTools: []string{"git"}, MinFreeRAMMB: 3000}

	d := SelectBestNode(reqs, nodes, nil)
	if !d.OK || d.Node != "beta" {
		t.Fatalf("expected OK=true node=beta, got OK=%v node=%s reasoning=%v", d.OK, d.Node, d.Reasoning)
	}
}

func TestAllocatableRAMOutweighsClusterPressureSharePenalty(t *testing.T) {
	nodes := []models.NodeFacts{
		nodeComplete("alpha", 5000, "none", "git"),
		nodeComplete("beta", 3500, "none", "git"),
	}
	nodes[0].RAMReservedMB = 1000
	reqs := models.TaskRequirements{RequiredTools: []string{"git"}, MinFreeRAMMB: 1000}

	d := SelectBestNode(reqs, nodes, nil)
	if !d.OK || d.Node != "alpha" {
		t.Fatalf("expected OK=true node=alpha when allocatable RAM leads, got OK=%v node=%s reasoning=%v", d.OK, d.Node, d.Reasoning)
	}
}

func TestReservedRAMAppearsInFailureReasoning(t *testing.T) {
	nodes := []models.NodeFacts{
		nodeComplete("alpha", 5000, "none", "git"),
	}
	nodes[0].RAMReservedMB = 2500
	reqs := models.TaskRequirements{RequiredTools: []string{"git"}, MinFreeRAMMB: 3000}

	d := SelectBestNode(reqs, nodes, nil)
	if d.OK {
		t.Fatal("expected OK=false")
	}
	foundEffective := false
	for _, r := range d.Reasoning {
		if contains(r, "allocatable") && contains(r, "free") {
			foundEffective = true
		}
	}
	if !foundEffective {
		t.Errorf("expected allocatable RAM reasoning, got: %v", d.Reasoning)
	}
}

func TestFilter_LowBatteryBlocksHeavyTask(t *testing.T) {
	batteryLow := 15
	n := nodeComplete("laptop", 8000, "none", "ollama")
	n.Resources.BatteryPercent = &batteryLow
	n.Ollama = &models.OllamaInfo{Running: true, Installed: true}

	reqs := models.TaskRequirements{
		RequiredTools: []string{"ollama"},
		MinFreeRAMMB:  4096,
	}
	candidates := FilterCandidates(reqs, []models.NodeFacts{n}, nil)
	if len(candidates) != 0 {
		t.Error("low battery node should be filtered out for heavy inference")
	}
}

func TestFilter_LowBatteryAllowsLightTask(t *testing.T) {
	batteryLow := 15
	n := nodeComplete("laptop", 8000, "none", "git")
	n.Resources.BatteryPercent = &batteryLow

	reqs := models.TaskRequirements{RequiredTools: []string{"git"}}
	candidates := FilterCandidates(reqs, []models.NodeFacts{n}, nil)
	if len(candidates) != 1 {
		t.Error("low battery should not block non-inference tasks")
	}
}

func TestFilter_ThermalCriticalBlocksHeavyTask(t *testing.T) {
	n := nodeComplete("hot-box", 8000, "none", "ollama")
	n.Resources.ThermalState = "critical"
	n.Ollama = &models.OllamaInfo{Running: true, Installed: true}

	reqs := models.TaskRequirements{
		RequiredTools: []string{"ollama"},
		MinFreeRAMMB:  4096,
	}
	candidates := FilterCandidates(reqs, []models.NodeFacts{n}, nil)
	if len(candidates) != 0 {
		t.Error("thermally critical node should be filtered out for heavy inference")
	}
}

func TestFilter_FailureNodeExcluded(t *testing.T) {
	n := nodeComplete("cursed-node", 8000, "none", "git")
	reqs := models.TaskRequirements{
		Description:   "llama3:8b",
		RequiredTools: []string{"git"},
		Workload: models.WorkloadProfileMatch{
			Class: models.ClassLocalLLMInference,
		},
	}

	st := &state.ClusterState{
		Nodes: make(map[string]state.NodeState),
		Failures: failures.Store{
			"hash123": models.FailureRecord{
				ID:        "hash123",
				Class:     models.FailureExecCrash,
				Scope:     models.FailureScope{Node: "cursed-node", Workload: models.ClassLocalLLMInference},
				Count:     2,
				ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
			},
		},
	}

	candidates := FilterCandidates(reqs, []models.NodeFacts{n}, st)
	if len(candidates) != 0 {
		t.Error("tombstoned node should be filtered out")
	}
}

func TestFilter_ExpiredFailureAllowed(t *testing.T) {
	n := nodeComplete("recovered-node", 8000, "none", "git")
	reqs := models.TaskRequirements{
		Description:   "llama3:8b",
		RequiredTools: []string{"git"},
		Workload: models.WorkloadProfileMatch{
			Class: models.ClassLocalLLMInference,
		},
	}

	st := &state.ClusterState{
		Nodes: make(map[string]state.NodeState),
		Failures: failures.Store{
			"hash456": models.FailureRecord{
				ID:        "hash456",
				Class:     models.FailureExecCrash,
				Scope:     models.FailureScope{Node: "recovered-node", Workload: models.ClassLocalLLMInference},
				Count:     1,
				ExpiresAt: time.Now().UTC().Add(-1 * time.Hour),
			},
		},
	}

	candidates := FilterCandidates(reqs, []models.NodeFacts{n}, st)
	if len(candidates) != 1 {
		t.Error("expired tombstone should not block node")
	}
}

func TestFilter_NilStateSkipsFailureCheck(t *testing.T) {
	n := nodeComplete("any-node", 8000, "none", "git")
	reqs := models.TaskRequirements{
		Description:   "some-task",
		RequiredTools: []string{"git"},
	}

	candidates := FilterCandidates(reqs, []models.NodeFacts{n}, nil)
	if len(candidates) != 1 {
		t.Error("nil state should not filter any nodes")
	}
}

// --- PeakRAMMB empirical filter tests ---

func TestRankerRespectsCustomSystemReserve(t *testing.T) {
	nodeA := nodeComplete("nodeA", 6000, "none")
	nodeA.SystemReserveMB = 3000

	nodeB := nodeComplete("nodeB", 5500, "none")
	nodeB.SystemReserveMB = 0

	candidates := []models.NodeFacts{nodeA, nodeB}
	ranked := RankCandidates(candidates, models.TaskRequirements{}, nil)
	if ranked[0].Name != "nodeB" {
		t.Fatalf("expected nodeB first, got %s", ranked[0].Name)
	}

	nodeB.SystemReserveMB = 4500
	candidates = []models.NodeFacts{nodeA, nodeB}
	ranked = RankCandidates(candidates, models.TaskRequirements{}, nil)
	if ranked[0].Name != "nodeA" {
		t.Fatalf("expected nodeA first after Node B reserve increased, got %s", ranked[0].Name)
	}
}

func TestFilterCandidatesRespectsCustomSystemReserve(t *testing.T) {
	node := nodeComplete("nodeA", 6000, "none")
	node.SystemReserveMB = 3000

	reqs := models.TaskRequirements{MinFreeRAMMB: 5500}
	filtered := FilterCandidates(reqs, []models.NodeFacts{node}, nil)
	if len(filtered) != 0 {
		t.Fatalf("expected node to be filtered out, got %v", names(filtered))
	}

	reqs.MinFreeRAMMB = 4500
	filtered = FilterCandidates(reqs, []models.NodeFacts{node}, nil)
	if len(filtered) != 1 || filtered[0].Name != "nodeA" {
		t.Fatalf("expected node to qualify, got %v", names(filtered))
	}
}
