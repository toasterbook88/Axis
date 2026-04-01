package placement

import (
	"testing"
	"time"

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

func nodeTurboQuant(name string, freeRAM int64, pressure string, backends ...string) models.NodeFacts {
	n := nodeComplete(name, freeRAM, pressure, "ollama")
	n.Ollama = &models.OllamaInfo{
		Installed: true,
		Listening: true,
		Models:    []string{"llama3:8b"},
	}
	n.TurboQuant = &models.TurboQuantInfo{
		Supported:    true,
		Verified:     true,
		Backends:     backends,
		Capabilities: []string{"long-context"},
	}
	return n
}

func nodeTurboQuantDetected(name string, freeRAM int64, pressure string, backends ...string) models.NodeFacts {
	n := nodeTurboQuant(name, freeRAM, pressure, backends...)
	n.TurboQuant.Verified = false
	return n
}

func nodeUnifiedMemory(name string, freeRAM int64, pressure string, class int, tools ...string) models.NodeFacts {
	n := nodeComplete(name, freeRAM, pressure, tools...)
	n.Resources.MemoryTopology = models.MemoryTopologyUnified
	n.Resources.MemoryClass = class
	return n
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

func TestFilterWarmOllamaDoesNotCollapseToUnsafeFloor(t *testing.T) {
	node := nodeComplete("m3", 1186, "low")
	node.Ollama = &models.OllamaInfo{
		Installed: true,
		Listening: true,
		Models:    []string{"qwen3:1.7b"},
	}
	reqs := models.TaskRequirements{RequiredTools: []string{"ollama"}, MinFreeRAMMB: 1536}

	result := FilterCandidates(reqs, []models.NodeFacts{node}, nil)
	if len(result) != 0 {
		t.Errorf("expected warm ollama node to stay blocked below safe floor, got %v", names(result))
	}
}

func TestFilterLongContextDoesNotCollapseToWarmOllamaFloor(t *testing.T) {
	node := nodeComplete("m3", 1186, "low")
	node.Ollama = &models.OllamaInfo{
		Installed: true,
		Listening: true,
		Models:    []string{"qwen3:1.7b"},
	}
	reqs := models.TaskRequirements{
		RequiredTools:       []string{"ollama"},
		MinFreeRAMMB:        4096,
		ContextWindowTokens: 128000,
		PrefersTurboQuant:   true,
	}

	result := FilterCandidates(reqs, []models.NodeFacts{node}, nil)
	if len(result) != 0 {
		t.Fatalf("expected warm ollama node to stay excluded for long-context task, got %v", names(result))
	}
}

func TestFilterLongContextUsesTurboQuantAdjustedRAM(t *testing.T) {
	node := nodeTurboQuant("mlx-node", 1536, "low", "mlx")
	reqs := models.TaskRequirements{
		RequiredTools:       []string{"ollama"},
		MinFreeRAMMB:        4096,
		ContextWindowTokens: 128000,
		PrefersTurboQuant:   true,
	}

	result := FilterCandidates(reqs, []models.NodeFacts{node}, nil)
	if len(result) != 1 || result[0].Name != "mlx-node" {
		t.Fatalf("expected turboquant node to qualify after adjusted RAM threshold, got %v", names(result))
	}
}

func TestFilterLongContextDoesNotUseDetectedOnlyTurboQuantForRAMReduction(t *testing.T) {
	node := nodeTurboQuantDetected("mlx-node", 1536, "low", "mlx")
	reqs := models.TaskRequirements{
		RequiredTools:       []string{"ollama"},
		MinFreeRAMMB:        4096,
		ContextWindowTokens: 128000,
		PrefersTurboQuant:   true,
	}

	result := FilterCandidates(reqs, []models.NodeFacts{node}, nil)
	if len(result) != 0 {
		t.Fatalf("expected detected-only turboquant node to stay excluded, got %v", names(result))
	}
}

func TestFilterExcludesHeavyInferenceOnCriticalLinuxPSI(t *testing.T) {
	node := nodeComplete("thrashing", 8192, "high", "ollama")
	node.Resources.PressureSource = "linux-psi"
	node.Resources.PressureStall10 = 16.4

	reqs := models.TaskRequirements{
		RequiredTools: []string{"ollama"},
		MinFreeRAMMB:  4096,
	}
	result := FilterCandidates(reqs, []models.NodeFacts{node}, nil)
	if len(result) != 0 {
		t.Fatalf("expected critical linux psi node to be filtered for heavy inference, got %v", names(result))
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

func TestFilterAppleFoundationModelsIsLocalOnly(t *testing.T) {
	local := nodeComplete("local-mac", 8192, "none", "apple-foundation-models")
	local.Hostname = "localhost"
	local.AppleFM = &models.AppleFoundationModelsInfo{Available: true, Verified: true, Version: "26.1"}

	remote := nodeComplete("remote-mac", 8192, "none", "apple-foundation-models")
	remote.Hostname = "remote-mac.local"
	remote.AppleFM = &models.AppleFoundationModelsInfo{Available: true, Verified: true, Version: "26.1"}

	reqs := models.TaskRequirements{
		RequiredTools:     []string{"apple-foundation-models"},
		PreferredBackends: []string{"apple-foundation-models"},
	}

	result := FilterCandidates(reqs, []models.NodeFacts{remote, local}, nil)
	if len(result) != 1 || result[0].Name != "local-mac" {
		t.Fatalf("expected only verified local apple node, got %v", names(result))
	}
}

// --- Rank Tests ---

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
		nodeComplete("gpu-node", 5000, "none"),
	}
	candidates[1].Resources.GPUs = []models.GPUInfo{{Model: "RTX 4090", Vendor: "nvidia", Capabilities: []string{"cuda"}}}

	ranked := RankCandidates(candidates, models.TaskRequirements{}, nil)
	if ranked[0].Name != "gpu-node" {
		t.Errorf("expected gpu-node first, got %s", ranked[0].Name)
	}
}

func TestRankDeterministicTiebreak(t *testing.T) {
	candidates := []models.NodeFacts{
		nodeComplete("zulu", 4000, "none"),
		nodeComplete("alpha", 4000, "none"),
		nodeComplete("mike", 4000, "none"),
	}
	ranked := RankCandidates(candidates, models.TaskRequirements{}, nil)
	if ranked[0].Name != "alpha" || ranked[1].Name != "mike" || ranked[2].Name != "zulu" {
		t.Errorf("expected [alpha, mike, zulu], got %v", names(ranked))
	}

	// Run again to confirm determinism
	ranked2 := RankCandidates(candidates, models.TaskRequirements{}, nil)
	for i := range ranked {
		if ranked[i].Name != ranked2[i].Name {
			t.Fatalf("non-deterministic: run1[%d]=%s, run2[%d]=%s",
				i, ranked[i].Name, i, ranked2[i].Name)
		}
	}
}

func TestRankPrefersLowerReservationRatioWhenAllocatableTied(t *testing.T) {
	candidates := []models.NodeFacts{
		nodeComplete("alpha", 5000, "none"),
		nodeComplete("beta", 3500, "none"),
	}
	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			"alpha": {ReservedMB: 2000},
			"beta":  {ReservedMB: 500},
		},
	}

	ranked := RankCandidates(candidates, models.TaskRequirements{}, st)
	if ranked[0].Name != "beta" {
		t.Fatalf("expected beta first on lower reservation ratio, got %s", ranked[0].Name)
	}
}

func TestRankPrefersTurboQuantForLongContextTasks(t *testing.T) {
	candidates := []models.NodeFacts{
		nodeComplete("plain", 4096, "none", "ollama"),
		nodeTurboQuant("mlx", 4096, "none", "mlx"),
	}
	reqs := models.TaskRequirements{
		RequiredTools:       []string{"ollama"},
		MinFreeRAMMB:        4096,
		ContextWindowTokens: 128000,
		PrefersTurboQuant:   true,
	}

	ranked := RankCandidates(candidates, reqs, nil)
	if ranked[0].Name != "mlx" {
		t.Fatalf("expected turboquant-capable node to rank first, got %s", ranked[0].Name)
	}
}

func TestRankPrefersVerifiedTurboQuantOverDetected(t *testing.T) {
	candidates := []models.NodeFacts{
		nodeTurboQuantDetected("detected", 4096, "none", "mlx"),
		nodeTurboQuant("verified", 4096, "none", "mlx"),
	}
	reqs := models.TaskRequirements{
		RequiredTools:       []string{"ollama"},
		MinFreeRAMMB:        4096,
		ContextWindowTokens: 128000,
		PrefersTurboQuant:   true,
	}

	ranked := RankCandidates(candidates, reqs, nil)
	if ranked[0].Name != "verified" {
		t.Fatalf("expected verified turboquant node to rank first, got %s", ranked[0].Name)
	}
}

func TestRankPrefersUnifiedMemoryForMLXLongContext(t *testing.T) {
	candidates := []models.NodeFacts{
		nodeComplete("standard", 4096, "none", "ollama"),
		nodeUnifiedMemory("unified", 4096, "none", 3, "ollama"),
	}
	reqs := models.TaskRequirements{
		RequiredTools:       []string{"ollama"},
		MinFreeRAMMB:        4096,
		ContextWindowTokens: 128000,
		PrefersTurboQuant:   true,
		PreferredBackends:   []string{"mlx"},
	}

	ranked := RankCandidates(candidates, reqs, nil)
	if ranked[0].Name != "unified" {
		t.Fatalf("expected unified-memory node to rank first, got %s", ranked[0].Name)
	}
}

// --- Selector Tests ---

func TestSelectBestNode(t *testing.T) {
	nodes := []models.NodeFacts{
		nodeComplete("m3", 800, "medium", "git", "go"),
		nodeComplete("m1", 5200, "none", "git", "python3"),
		nodeUnreachable("m2"),
	}
	reqs := models.TaskRequirements{
		Description:   "analyze repo",
		RequiredTools: []string{"git"},
	}

	d := SelectBestNode(reqs, nodes, nil)
	if !d.OK {
		t.Fatal("expected OK=true")
	}
	if d.Node != "m1" {
		t.Errorf("expected m1 (most free RAM, no pressure), got %s", d.Node)
	}
	if d.Tool != "git" {
		t.Errorf("expected tool=git, got %q", d.Tool)
	}
	if len(d.Reasoning) == 0 {
		t.Error("expected non-empty reasoning")
	}
}

// --- Failure Reasoning Tests ---

func TestSelectFailure_RAMGap(t *testing.T) {
	nodes := []models.NodeFacts{
		nodeComplete("m3", 900, "low"),
		nodeComplete("m1", 1400, "low"),
	}
	reqs := models.TaskRequirements{
		Description:  "run a 70b model",
		MinFreeRAMMB: 4096,
	}

	d := SelectBestNode(reqs, nodes, nil)
	if d.OK {
		t.Fatal("expected OK=false")
	}
	// Should explain the RAM shortfall for each node
	foundM3 := false
	foundM1 := false
	for _, r := range d.Reasoning {
		if contains(r, "m3") && contains(r, "short") {
			foundM3 = true
		}
		if contains(r, "m1") && contains(r, "short") {
			foundM1 = true
		}
	}
	if !foundM3 {
		t.Errorf("expected RAM gap reasoning for m3, got: %v", d.Reasoning)
	}
	if !foundM1 {
		t.Errorf("expected RAM gap reasoning for m1, got: %v", d.Reasoning)
	}
}

func TestSelectFailure_MissingTool(t *testing.T) {
	nodes := []models.NodeFacts{
		nodeComplete("m3", 5000, "none", "git", "go"),
	}
	reqs := models.TaskRequirements{
		Description:   "inference with ollama",
		RequiredTools: []string{"ollama"},
	}

	d := SelectBestNode(reqs, nodes, nil)
	if d.OK {
		t.Fatal("expected OK=false")
	}
	foundToolGap := false
	for _, r := range d.Reasoning {
		if contains(r, "missing") && contains(r, "ollama") {
			foundToolGap = true
		}
	}
	if !foundToolGap {
		t.Errorf("expected missing-tool reasoning, got: %v", d.Reasoning)
	}
}

func TestSelectSuccess_RunnerUpComparison(t *testing.T) {
	nodes := []models.NodeFacts{
		nodeComplete("m3", 2000, "none", "git"),
		nodeComplete("m1", 5000, "none", "git"),
	}
	reqs := models.TaskRequirements{RequiredTools: []string{"git"}}

	d := SelectBestNode(reqs, nodes, nil)
	if !d.OK || d.Node != "m1" {
		t.Fatalf("expected OK=true, node=m1, got OK=%v node=%s", d.OK, d.Node)
	}
	foundRunnerUp := false
	for _, r := range d.Reasoning {
		if contains(r, "runner-up") && contains(r, "m3") {
			foundRunnerUp = true
		}
	}
	if !foundRunnerUp {
		t.Errorf("expected runner-up comparison, got: %v", d.Reasoning)
	}
}

func TestSelectSuccess_SingleCandidate(t *testing.T) {
	nodes := []models.NodeFacts{
		nodeComplete("solo", 4000, "none", "git"),
		nodeUnreachable("down"),
	}
	reqs := models.TaskRequirements{RequiredTools: []string{"git"}}

	d := SelectBestNode(reqs, nodes, nil)
	if !d.OK || d.Node != "solo" {
		t.Fatalf("expected OK=true node=solo, got OK=%v node=%s", d.OK, d.Node)
	}
	// Single candidate: should NOT have runner-up line
	for _, r := range d.Reasoning {
		if contains(r, "runner-up") {
			t.Errorf("single candidate should not have runner-up line: %v", d.Reasoning)
		}
	}
}

// --- Fit Score Tests ---

func TestFitScore_GPUNodeScoresHigher(t *testing.T) {
	noGPU := nodeComplete("cpu-only", 4000, "none")
	withGPU := nodeComplete("gpu-node", 4000, "none")
	withGPU.Resources.GPUs = []models.GPUInfo{{Model: "MX250", Vendor: "nvidia", VRAMMB: 4096, Capabilities: []string{"cuda"}}}

	scoreNoGPU := ComputeFitScore(noGPU, false, nil)
	scoreGPU := ComputeFitScore(withGPU, false, nil)

	if scoreGPU <= scoreNoGPU {
		t.Errorf("GPU node (%d) should score higher than non-GPU (%d)", scoreGPU, scoreNoGPU)
	}
	// GPU score: base 10 + 4 (4GB VRAM) = 14
	delta := scoreGPU - scoreNoGPU
	if delta < 10 || delta > 25 {
		t.Errorf("GPU delta should be 10-25, got %d", delta)
	}
}

func TestFitScore_LocalBonus(t *testing.T) {
	n := nodeComplete("test", 2000, "low")
	remote := ComputeFitScore(n, false, nil)
	local := ComputeFitScore(n, true, nil)

	if local-remote != 10 {
		t.Errorf("local bonus should be 10, got %d", local-remote)
	}
}

func TestFitScore_TurboQuantLongContextBonus(t *testing.T) {
	n := nodeTurboQuant("mlx", 4000, "none", "mlx")
	reqs := models.TaskRequirements{
		ContextWindowTokens: 256000,
		PrefersTurboQuant:   true,
	}

	base := ComputeFitScore(n, false, nil)
	boost := ComputeTaskFitScore(n, false, nil, reqs)
	if boost <= base {
		t.Fatalf("expected turboquant long-context score boost, got base=%d boost=%d", base, boost)
	}
	if boost-base != 20 {
		t.Fatalf("expected turboquant bonus 20, got %d", boost-base)
	}
}

func TestFitScore_DetectedTurboQuantGetsSmallerBonus(t *testing.T) {
	n := nodeTurboQuantDetected("mlx", 4000, "none", "mlx")
	reqs := models.TaskRequirements{
		ContextWindowTokens: 256000,
		PrefersTurboQuant:   true,
	}

	base := ComputeFitScore(n, false, nil)
	boost := ComputeTaskFitScore(n, false, nil, reqs)
	if boost-base != 8 {
		t.Fatalf("expected detected turboquant bonus 8, got %d", boost-base)
	}
}

func TestFitScore_UnifiedMemoryBonusForMLXLongContext(t *testing.T) {
	standard := nodeComplete("standard", 4096, "none", "ollama")
	unified := nodeUnifiedMemory("unified", 4096, "none", 3, "ollama")

	reqs := models.TaskRequirements{
		ContextWindowTokens: 128000,
		PrefersTurboQuant:   true,
		PreferredBackends:   []string{"mlx"},
	}

	base := ComputeTaskFitScore(standard, false, nil, reqs)
	boost := ComputeTaskFitScore(unified, false, nil, reqs)
	if boost <= base {
		t.Fatalf("expected unified-memory node to score higher, got standard=%d unified=%d", base, boost)
	}
}

func TestInferRequirementsAddsPreferredBackends(t *testing.T) {
	reqs := InferRequirements("run mlx long-context inference on apple silicon")
	if len(reqs.PreferredBackends) == 0 || reqs.PreferredBackends[0] != "mlx" {
		t.Fatalf("expected mlx preferred backend, got %v", reqs.PreferredBackends)
	}
	if !reqs.PrefersTurboQuant {
		t.Fatal("expected long-context hint to keep turboquant preference")
	}
}

func TestInferRequirementsDetectsAppleFoundationModels(t *testing.T) {
	reqs := InferRequirements("summarize this with apple-intelligence via apple-foundation-models")
	if len(reqs.RequiredTools) != 1 || reqs.RequiredTools[0] != "apple-foundation-models" {
		t.Fatalf("expected apple foundation models requirement, got %v", reqs.RequiredTools)
	}
	if len(reqs.PreferredBackends) == 0 || reqs.PreferredBackends[0] != "apple-foundation-models" {
		t.Fatalf("expected apple foundation models preferred backend, got %v", reqs.PreferredBackends)
	}
	if reqs.MinFreeRAMMB != 0 {
		t.Fatalf("expected apple foundation models helper path to avoid generic model RAM floor, got %d", reqs.MinFreeRAMMB)
	}
}

func TestInferRequirementsExplicitLlamaServerUsesObservedRuntime(t *testing.T) {
	reqs := InferRequirements("llama-server -m /models/qwen.gguf")
	if len(reqs.RequiredTools) != 1 || reqs.RequiredTools[0] != "llama-server" {
		t.Fatalf("expected llama-server tool requirement, got %v", reqs.RequiredTools)
	}
	if reqs.MinFreeRAMMB != 6144 {
		t.Fatalf("expected 6144MB floor, got %d", reqs.MinFreeRAMMB)
	}
	if len(reqs.PreferredBackends) == 0 || reqs.PreferredBackends[0] != "llama.cpp" {
		t.Fatalf("expected llama.cpp preferred backend, got %v", reqs.PreferredBackends)
	}
}

func TestSelectBestNodePrefersObservedLlamaServerForLlamaCppTask(t *testing.T) {
	llama := nodeComplete("gpu-llama", 8192, "low", "llama-server")
	llama.TurboQuant = &models.TurboQuantInfo{
		Supported: true,
		Verified:  true,
		Backends:  []string{"llama.cpp"},
	}
	ollama := nodeComplete("gpu-ollama", 12288, "low", "ollama")

	reqs := models.TaskRequirements{
		RequiredTools:     []string{"llama-server"},
		MinFreeRAMMB:      6144,
		PreferredBackends: []string{"llama.cpp"},
	}

	d := SelectBestNode(reqs, []models.NodeFacts{ollama, llama}, nil)
	if !d.OK || d.Node != "gpu-llama" {
		t.Fatalf("expected llama.cpp-capable node, got OK=%v node=%s reasoning=%v", d.OK, d.Node, d.Reasoning)
	}
}

func TestFitScore_NilResources(t *testing.T) {
	n := models.NodeFacts{Name: "empty", Status: models.StatusComplete}
	score := ComputeFitScore(n, false, nil)
	if score != 0 {
		t.Errorf("nil resources should score 0, got %d", score)
	}
}

func TestSelectFailure_HeavyRAMScopeNote(t *testing.T) {
	nodes := []models.NodeFacts{
		nodeComplete("m3", 900, "low"),
	}
	reqs := models.TaskRequirements{
		Description:  "run 70b model",
		MinFreeRAMMB: 4096,
	}

	d := SelectBestNode(reqs, nodes, nil)
	if d.OK {
		t.Fatal("expected OK=false")
	}
	foundScope := false
	for _, r := range d.Reasoning {
		if contains(r, "assistive") && contains(r, "70B") {
			foundScope = true
		}
	}
	if !foundScope {
		t.Errorf("expected AXIS scope note about small models, got: %v", d.Reasoning)
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
	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			"alpha": {ReservedMB: 2000},
		},
	}
	reqs := models.TaskRequirements{RequiredTools: []string{"git"}, MinFreeRAMMB: 3000}

	d := SelectBestNode(reqs, nodes, st)
	if !d.OK || d.Node != "beta" {
		t.Fatalf("expected OK=true node=beta, got OK=%v node=%s reasoning=%v", d.OK, d.Node, d.Reasoning)
	}
}

func TestClusterPressureSharePenalizesDominantNode(t *testing.T) {
	nodes := []models.NodeFacts{
		nodeComplete("alpha", 5000, "none", "git"),
		nodeComplete("beta", 3500, "none", "git"),
	}
	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			"alpha": {ReservedMB: 1000},
		},
	}
	reqs := models.TaskRequirements{RequiredTools: []string{"git"}, MinFreeRAMMB: 1000}

	d := SelectBestNode(reqs, nodes, st)
	if !d.OK || d.Node != "beta" {
		t.Fatalf("expected OK=true node=beta after cluster-share penalty, got OK=%v node=%s reasoning=%v", d.OK, d.Node, d.Reasoning)
	}
}

func TestReservedRAMAppearsInFailureReasoning(t *testing.T) {
	nodes := []models.NodeFacts{
		nodeComplete("alpha", 5000, "none", "git"),
	}
	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			"alpha": {ReservedMB: 2500},
		},
	}
	reqs := models.TaskRequirements{RequiredTools: []string{"git"}, MinFreeRAMMB: 3000}

	d := SelectBestNode(reqs, nodes, st)
	if d.OK {
		t.Fatal("expected OK=false")
	}
	foundEffective := false
	for _, r := range d.Reasoning {
		if contains(r, "effective") && contains(r, "base 5000MB") {
			foundEffective = true
		}
	}
	if !foundEffective {
		t.Errorf("expected effective RAM reasoning, got: %v", d.Reasoning)
	}
}

func TestEffectiveRAMNeverGoesNegative(t *testing.T) {
	n := nodeComplete("alpha", 400, "high", "git")
	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			"alpha": {ReservedMB: 4096},
		},
	}

	if got := freeRAMWithState(n, st); got != 0 {
		t.Fatalf("expected effective RAM to clamp at 0, got %d", got)
	}
}

func TestFitScoreUsesEffectiveRAM(t *testing.T) {
	n := nodeComplete("alpha", 5000, "none", "git")
	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			"alpha": {ReservedMB: 2048},
		},
	}

	base := ComputeFitScore(n, false, nil)
	effective := ComputeFitScore(n, false, st)

	if effective >= base {
		t.Fatalf("expected reserved RAM to lower fit score, got base=%d effective=%d", base, effective)
	}
	if effective != 44 {
		t.Fatalf("expected effective fit score 44, got %d", effective)
	}
}

func TestCachedAllocatableRAMAffectsSelection(t *testing.T) {
	alpha := nodeComplete("alpha", 5000, "none", "git")
	alpha.Resources.RAMReservedMB = 3000
	alpha.Resources.RAMAllocatableMB = 2000

	beta := nodeComplete("beta", 4200, "none", "git")
	beta.Resources.RAMReservedMB = 512
	beta.Resources.RAMAllocatableMB = 3688

	reqs := models.TaskRequirements{RequiredTools: []string{"git"}, MinFreeRAMMB: 3000}
	d := SelectBestNode(reqs, []models.NodeFacts{alpha, beta}, nil)

	if !d.OK || d.Node != "beta" {
		t.Fatalf("expected cached allocatable RAM to prefer beta, got OK=%v node=%s reasoning=%v", d.OK, d.Node, d.Reasoning)
	}
}

func TestSuccessReasoningShowsAllocatableRAM(t *testing.T) {
	alpha := nodeComplete("alpha", 5000, "none", "git")
	alpha.Resources.RAMReservedMB = 2048
	alpha.Resources.RAMAllocatableMB = 2952

	d := SelectBestNode(models.TaskRequirements{RequiredTools: []string{"git"}}, []models.NodeFacts{alpha}, nil)
	if !d.OK {
		t.Fatalf("expected OK=true, got reasoning=%v", d.Reasoning)
	}

	foundAllocatable := false
	for _, r := range d.Reasoning {
		if contains(r, "allocatable") && contains(r, "reserved") {
			foundAllocatable = true
		}
	}
	if !foundAllocatable {
		t.Fatalf("expected allocatable RAM reasoning, got %v", d.Reasoning)
	}
}

func TestSuccessReasoningShowsTurboQuantAvailability(t *testing.T) {
	n := nodeTurboQuant("mlx", 4096, "none", "mlx")
	reqs := models.TaskRequirements{
		RequiredTools:       []string{"ollama"},
		MinFreeRAMMB:        4096,
		ContextWindowTokens: 128000,
		PrefersTurboQuant:   true,
	}

	d := SelectBestNode(reqs, []models.NodeFacts{n}, nil)
	if !d.OK {
		t.Fatalf("expected OK=true, got reasoning=%v", d.Reasoning)
	}
	foundTurboQuant := false
	for _, r := range d.Reasoning {
		if contains(r, "turboquant-aware backend verified") && contains(r, "mlx") {
			foundTurboQuant = true
		}
	}
	if !foundTurboQuant {
		t.Fatalf("expected turboquant reasoning, got %v", d.Reasoning)
	}
}

func TestSuccessReasoningShowsTurboQuantCapabilities(t *testing.T) {
	n := nodeTurboQuant("mlx", 4096, "none", "mlx")
	n.TurboQuant.Capabilities = []string{"apple-silicon", "long-context"}
	reqs := models.TaskRequirements{
		RequiredTools:       []string{"ollama"},
		MinFreeRAMMB:        4096,
		ContextWindowTokens: 128000,
		PrefersTurboQuant:   true,
	}

	d := SelectBestNode(reqs, []models.NodeFacts{n}, nil)
	if !d.OK {
		t.Fatalf("expected OK=true, got reasoning=%v", d.Reasoning)
	}
	foundCaps := false
	for _, r := range d.Reasoning {
		if contains(r, "turboquant capabilities") && contains(r, "apple-silicon") {
			foundCaps = true
		}
	}
	if !foundCaps {
		t.Fatalf("expected turboquant capabilities reasoning, got %v", d.Reasoning)
	}
}

// --- Helpers ---

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func names(nodes []models.NodeFacts) []string {
	var out []string
	for _, n := range nodes {
		out = append(out, n.Name)
	}
	return out
}

func TestInferRequirements(t *testing.T) {
	tests := []struct {
		desc       string
		wantTool   string
		wantRAM    int64
		wantTokens int
		wantTQ     bool
	}{
		{"Run a 70b inference model", "ollama", 12288, 0, false},
		{"clone this repo and analyze it", "git", 0, 0, false},
		{"compile the go binary", "go", 0, 0, false},
		{"spin up a docker container", "docker", 0, 0, false},
		{"just a simple task", "", 0, 0, false},
		{"deploy using gpu", "ollama", 6144, 0, false},
		{"run a small local model with ollama inference", "ollama", 6144, 0, false},
		{"ollama run llama3", "ollama", 6144, 0, false},
		{"run a 7b model locally", "", 4096, 0, false},
		{"llama-server -m /models/qwen.gguf", "llama-server", 6144, 0, false},
		{"run 128k book-length ollama inference", "ollama", 6144, 128000, true},
		{"needle in a haystack 1m tokens", "", 12288, 1000000, true},
	}
	for _, tt := range tests {
		got := InferRequirements(tt.desc)
		gotTool := ""
		if len(got.RequiredTools) > 0 {
			gotTool = got.RequiredTools[0]
		}
		if gotTool != tt.wantTool {
			t.Errorf("InferRequirements(%q) TOOL = %q, want %q", tt.desc, gotTool, tt.wantTool)
		}
		if got.MinFreeRAMMB != tt.wantRAM {
			t.Errorf("InferRequirements(%q) RAM = %d, want %d", tt.desc, got.MinFreeRAMMB, tt.wantRAM)
		}
		if got.ContextWindowTokens != tt.wantTokens {
			t.Errorf("InferRequirements(%q) TOKENS = %d, want %d", tt.desc, got.ContextWindowTokens, tt.wantTokens)
		}
		if got.PrefersTurboQuant != tt.wantTQ {
			t.Errorf("InferRequirements(%q) PrefersTurboQuant = %v, want %v", tt.desc, got.PrefersTurboQuant, tt.wantTQ)
		}
	}
}

// --- GPU Capability-Aware Placement Tests ---

func TestGpuScore_NvidiaWithCUDABackend(t *testing.T) {
	n := nodeComplete("gpu-node", 8000, "none")
	n.Resources.GPUs = []models.GPUInfo{{
		Model: "NVIDIA RTX 4090", Vendor: "nvidia", VRAMMB: 24576, Capabilities: []string{"cuda"},
	}}
	reqs := models.TaskRequirements{PreferredBackends: []string{"cuda"}}
	score := gpuScore(n, reqs)
	// base 10 + capability match 10 + VRAM 5 (24GB, capped) = 25
	if score != 25 {
		t.Errorf("gpuScore = %d, want 25", score)
	}
}

func TestGpuScore_AppleWithMetalBackend(t *testing.T) {
	n := nodeComplete("mac-node", 16000, "none")
	n.Resources.GPUs = []models.GPUInfo{{
		Model: "Apple M3 Pro", Vendor: "apple", Capabilities: []string{"metal"},
	}}
	reqs := models.TaskRequirements{PreferredBackends: []string{"mlx"}}
	score := gpuScore(n, reqs)
	// base 10 + capability match 10 = 20 (no VRAM for unified)
	if score != 20 {
		t.Errorf("gpuScore = %d, want 20", score)
	}
}

func TestGpuScore_IntelIntegratedReduced(t *testing.T) {
	n := nodeComplete("intel-node", 8000, "none")
	n.Resources.GPUs = []models.GPUInfo{{
		Model: "Intel UHD 630", Vendor: "intel",
	}}
	reqs := models.TaskRequirements{}
	score := gpuScore(n, reqs)
	// base 10, halved for integrated Intel with no capabilities = 5
	if score != 5 {
		t.Errorf("gpuScore = %d, want 5", score)
	}
}

func TestGpuScore_NoGPU(t *testing.T) {
	n := nodeComplete("cpu-node", 8000, "none")
	score := gpuScore(n, models.TaskRequirements{})
	if score != 0 {
		t.Errorf("gpuScore = %d, want 0", score)
	}
}

func TestRank_CUDAPreferNvidiaOverApple(t *testing.T) {
	apple := nodeComplete("mac", 8000, "none")
	apple.Resources.GPUs = []models.GPUInfo{{
		Model: "Apple M3", Vendor: "apple", Capabilities: []string{"metal"},
	}}
	nvidia := nodeComplete("gpu-box", 8000, "none")
	nvidia.Resources.GPUs = []models.GPUInfo{{
		Model: "RTX 4090", Vendor: "nvidia", VRAMMB: 24576, Capabilities: []string{"cuda"},
	}}

	reqs := models.TaskRequirements{PreferredBackends: []string{"cuda"}}
	ranked := RankCandidates([]models.NodeFacts{apple, nvidia}, reqs, nil)
	if ranked[0].Name != "gpu-box" {
		t.Errorf("CUDA task should prefer nvidia node, got %s first", ranked[0].Name)
	}
}

func TestRank_MLXPreferAppleOverNvidia(t *testing.T) {
	apple := nodeComplete("mac", 8000, "none")
	apple.Resources.GPUs = []models.GPUInfo{{
		Model: "Apple M3 Pro", Vendor: "apple", Capabilities: []string{"metal"},
	}}
	apple.Resources.MemoryTopology = models.MemoryTopologyUnified
	nvidia := nodeComplete("gpu-box", 8000, "none")
	nvidia.Resources.GPUs = []models.GPUInfo{{
		Model: "RTX 3080", Vendor: "nvidia", VRAMMB: 10240, Capabilities: []string{"cuda"},
	}}

	reqs := models.TaskRequirements{PreferredBackends: []string{"mlx"}}
	ranked := RankCandidates([]models.NodeFacts{nvidia, apple}, reqs, nil)
	if ranked[0].Name != "mac" {
		t.Errorf("MLX task should prefer apple node, got %s first", ranked[0].Name)
	}
}

func TestFitScore_HighVRAMScoresHigher(t *testing.T) {
	lowVRAM := nodeComplete("low", 8000, "none")
	lowVRAM.Resources.GPUs = []models.GPUInfo{{
		Model: "MX250", Vendor: "nvidia", VRAMMB: 2048, Capabilities: []string{"cuda"},
	}}
	highVRAM := nodeComplete("high", 8000, "none")
	highVRAM.Resources.GPUs = []models.GPUInfo{{
		Model: "RTX 4090", Vendor: "nvidia", VRAMMB: 24576, Capabilities: []string{"cuda"},
	}}

	scoreLow := ComputeFitScore(lowVRAM, false, nil)
	scoreHigh := ComputeFitScore(highVRAM, false, nil)
	if scoreHigh <= scoreLow {
		t.Errorf("high VRAM (%d) should score higher than low VRAM (%d)", scoreHigh, scoreLow)
	}
}

// --- Storage & Thermal Placement Tests ---

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

func TestFilter_ThermalNominalAllowed(t *testing.T) {
	n := nodeComplete("cool-box", 8000, "none", "ollama")
	n.Resources.ThermalState = "nominal"
	n.Ollama = &models.OllamaInfo{Running: true, Installed: true}

	reqs := models.TaskRequirements{
		RequiredTools: []string{"ollama"},
		MinFreeRAMMB:  4096,
	}
	candidates := FilterCandidates(reqs, []models.NodeFacts{n}, nil)
	if len(candidates) != 1 {
		t.Error("nominal thermal node should not be filtered")
	}
}

func TestFitScore_HDDPenaltyForInference(t *testing.T) {
	ssd := nodeComplete("ssd-node", 8000, "none")
	ssd.Resources.StorageClass = "ssd"

	hdd := nodeComplete("hdd-node", 8000, "none")
	hdd.Resources.StorageClass = "hdd"

	reqs := models.TaskRequirements{
		RequiredTools: []string{"ollama"},
		MinFreeRAMMB:  4096,
	}
	scoreSSD := ComputeTaskFitScore(ssd, false, nil, reqs)
	scoreHDD := ComputeTaskFitScore(hdd, false, nil, reqs)

	if scoreHDD >= scoreSSD {
		t.Errorf("HDD (%d) should score lower than SSD (%d) for inference", scoreHDD, scoreSSD)
	}
}

func TestFitScore_HDDNoPenaltyForLightTask(t *testing.T) {
	ssd := nodeComplete("ssd-node", 8000, "none")
	ssd.Resources.StorageClass = "ssd"

	hdd := nodeComplete("hdd-node", 8000, "none")
	hdd.Resources.StorageClass = "hdd"

	reqs := models.TaskRequirements{RequiredTools: []string{"git"}}
	scoreSSD := ComputeTaskFitScore(ssd, false, nil, reqs)
	scoreHDD := ComputeTaskFitScore(hdd, false, nil, reqs)

	if scoreHDD != scoreSSD {
		t.Errorf("HDD (%d) and SSD (%d) should score same for light tasks", scoreHDD, scoreSSD)
	}
}

// --- Tombstone Placement Tests ---

func TestFilter_TombstonedNodeExcluded(t *testing.T) {
	n := nodeComplete("cursed-node", 8000, "none", "git")
	reqs := models.TaskRequirements{
		Description:   "llama3:8b",
		RequiredTools: []string{"git"},
	}

	st := &state.ClusterState{
		Nodes: make(map[string]state.NodeState),
		Tombstones: map[string]state.TombstoneEntry{
			"llama3:8b@cursed-node": {
				TaskPattern: "llama3:8b",
				NodeName:    "cursed-node",
				FailCount:   2,
				ExpiresAt:   time.Now().UTC().Add(24 * time.Hour),
			},
		},
	}

	candidates := FilterCandidates(reqs, []models.NodeFacts{n}, st)
	if len(candidates) != 0 {
		t.Error("tombstoned node should be filtered out")
	}
}

func TestFilter_ExpiredTombstoneAllowed(t *testing.T) {
	n := nodeComplete("recovered-node", 8000, "none", "git")
	reqs := models.TaskRequirements{
		Description:   "llama3:8b",
		RequiredTools: []string{"git"},
	}

	st := &state.ClusterState{
		Nodes: make(map[string]state.NodeState),
		Tombstones: map[string]state.TombstoneEntry{
			"llama3:8b@recovered-node": {
				TaskPattern: "llama3:8b",
				NodeName:    "recovered-node",
				FailCount:   1,
				ExpiresAt:   time.Now().UTC().Add(-1 * time.Hour),
			},
		},
	}

	candidates := FilterCandidates(reqs, []models.NodeFacts{n}, st)
	if len(candidates) != 1 {
		t.Error("expired tombstone should not block node")
	}
}

func TestFilter_NilStateSkipsTombstoneCheck(t *testing.T) {
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
