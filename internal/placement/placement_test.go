package placement

import (
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
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
	result := FilterCandidates(reqs, nodes)
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
	result := FilterCandidates(reqs, nodes)
	if len(result) != 1 || result[0].Name != "b" {
		t.Errorf("expected [b], got %v", names(result))
	}
}

func TestFilterExcludesMissingTool(t *testing.T) {
	nodes := []models.NodeFacts{
		nodeComplete("a", 4000, "none", "python3"),
		nodeComplete("b", 4000, "none", "git", "go"),
	}
	reqs := models.TaskRequirements{RequiredTool: "git"}
	result := FilterCandidates(reqs, nodes)
	if len(result) != 1 || result[0].Name != "b" {
		t.Errorf("expected [b], got %v", names(result))
	}
}

func TestFilterPassesAllQualified(t *testing.T) {
	nodes := []models.NodeFacts{
		nodeComplete("a", 5000, "none", "git"),
		nodeComplete("b", 6000, "none", "git"),
	}
	reqs := models.TaskRequirements{RequiredTool: "git", MinFreeRAMMB: 4096}
	result := FilterCandidates(reqs, nodes)
	if len(result) != 2 {
		t.Errorf("expected 2 candidates, got %d", len(result))
	}
}

// --- Rank Tests ---

func TestRankByPressure(t *testing.T) {
	candidates := []models.NodeFacts{
		nodeComplete("high-node", 4000, "high"),
		nodeComplete("none-node", 4000, "none"),
	}
	ranked := RankCandidates(candidates)
	if ranked[0].Name != "none-node" {
		t.Errorf("expected none-node first, got %s", ranked[0].Name)
	}
}

func TestRankByFreeRAM(t *testing.T) {
	candidates := []models.NodeFacts{
		nodeComplete("low-ram", 2000, "none"),
		nodeComplete("high-ram", 6000, "none"),
	}
	ranked := RankCandidates(candidates)
	if ranked[0].Name != "high-ram" {
		t.Errorf("expected high-ram first, got %s", ranked[0].Name)
	}
}

func TestRankDeterministicTiebreak(t *testing.T) {
	candidates := []models.NodeFacts{
		nodeComplete("zulu", 4000, "none"),
		nodeComplete("alpha", 4000, "none"),
		nodeComplete("mike", 4000, "none"),
	}
	ranked := RankCandidates(candidates)
	if ranked[0].Name != "alpha" || ranked[1].Name != "mike" || ranked[2].Name != "zulu" {
		t.Errorf("expected [alpha, mike, zulu], got %v", names(ranked))
	}

	// Run again to confirm determinism
	ranked2 := RankCandidates(candidates)
	for i := range ranked {
		if ranked[i].Name != ranked2[i].Name {
			t.Fatalf("non-deterministic: run1[%d]=%s, run2[%d]=%s",
				i, ranked[i].Name, i, ranked2[i].Name)
		}
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
		Description:  "analyze repo",
		RequiredTool: "git",
	}

	d := SelectBestNode(reqs, nodes)
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

	d := SelectBestNode(reqs, nodes)
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
		Description:  "inference with ollama",
		RequiredTool: "ollama",
	}

	d := SelectBestNode(reqs, nodes)
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
	reqs := models.TaskRequirements{RequiredTool: "git"}

	d := SelectBestNode(reqs, nodes)
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
	reqs := models.TaskRequirements{RequiredTool: "git"}

	d := SelectBestNode(reqs, nodes)
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
