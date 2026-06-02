package placement

import (
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/facts"
	"github.com/toasterbook88/axis/internal/models"
)

// TestRankCandidatesWarmthLosesToAllocatableRAM verifies the v2 critical-fix
// invariant: warmth is a bounded tiebreaker, never a primary signal. A small
// node with a hot model must not outrank a large node with a cold model.
func TestRankCandidatesWarmthLosesToAllocatableRAM(t *testing.T) {
	// 4GB total, 2GB free → only just passes a 1GB requirement.
	// Hot ollama model loaded (warmth=1.0).
	hot := nodeComplete("hot-small", 2000, "none", "ollama")
	hot.Ollama = &models.OllamaInfo{Installed: true, Running: true}
	hot.ResidentModels = []models.ResidentModel{
		{Name: "llama3:8b", Runtime: "ollama", Source: "ollama-ps", WarmthScore: 1.0},
	}

	// 16GB total, 14GB free, no resident model.
	cold := nodeComplete("cold-large", 14000, "none", "ollama")
	cold.Ollama = &models.OllamaInfo{Installed: true, Running: true}

	reqs := models.TaskRequirements{
		RequiredTools: []string{"ollama"},
		MinFreeRAMMB:  1024,
		Workload:      models.WorkloadProfileMatch{Class: models.ClassLocalLLMInference},
	}

	ranked := RankCandidates([]models.NodeFacts{hot, cold}, reqs, nil)
	if ranked[0].Name != "cold-large" {
		t.Fatalf("expected cold-large to win on allocatable RAM regardless of warmth, got %s", ranked[0].Name)
	}
}

// TestRankCandidatesWarmthBreaksTieOnEqualAllocatableRAM verifies the v2
// bounded-tiebreaker behavior: two equally-RAM-eligible nodes differ only on
// warmth, and the warmer node wins.
func TestRankCandidatesWarmthBreaksTieOnEqualAllocatableRAM(t *testing.T) {
	alpha := nodeComplete("alpha", 8000, "none", "ollama")
	alpha.Ollama = &models.OllamaInfo{Installed: true, Running: true}
	alpha.ResidentModels = []models.ResidentModel{
		{Name: "llama3:8b", Runtime: "ollama", Source: "ollama-ps", WarmthScore: 0.0},
	}

	beta := nodeComplete("beta", 8000, "none", "ollama")
	beta.Ollama = &models.OllamaInfo{Installed: true, Running: true}
	beta.ResidentModels = []models.ResidentModel{
		{Name: "llama3:8b", Runtime: "ollama", Source: "ollama-ps", WarmthScore: 1.0},
	}

	reqs := models.TaskRequirements{
		RequiredTools: []string{"ollama"},
		MinFreeRAMMB:  1024,
		Workload:      models.WorkloadProfileMatch{Class: models.ClassLocalLLMInference},
	}

	ranked := RankCandidates([]models.NodeFacts{alpha, beta}, reqs, nil)
	if ranked[0].Name != "beta" {
		t.Fatalf("expected warm beta to win on warmth tiebreaker, got %s", ranked[0].Name)
	}
}

// TestRankCandidatesWarmthFilteredBeforeRanking verifies the v2 safety
// invariant: warmth is never consulted on a node that fails FilterCandidates
// due to RAM shortfall.
func TestRankCandidatesWarmthFilteredBeforeRanking(t *testing.T) {
	// Hot model, but only 100MB free → fails the 1GB filter.
	hot := nodeComplete("hot-tiny", 100, "none", "ollama")
	hot.Resources.RAMTotalMB = 4096 // keep total small so reservable is small too
	hot.Ollama = &models.OllamaInfo{Installed: true, Running: true}
	hot.ResidentModels = []models.ResidentModel{
		{Name: "llama3:8b", Runtime: "ollama", Source: "ollama-ps", WarmthScore: 1.0},
	}

	cold := nodeComplete("cold-large", 8000, "none", "ollama")
	cold.Ollama = &models.OllamaInfo{Installed: true, Running: true}

	reqs := models.TaskRequirements{
		RequiredTools: []string{"ollama"},
		MinFreeRAMMB:  1024,
		Workload:      models.WorkloadProfileMatch{Class: models.ClassLocalLLMInference},
	}

	filtered := FilterCandidates(reqs, []models.NodeFacts{hot, cold}, nil)
	if len(filtered) != 1 || filtered[0].Name != "cold-large" {
		t.Fatalf("expected FilterCandidates to drop hot-tiny (insufficient RAM), got %v", names(filtered))
	}
}

// TestWarmthToRankBoundaries pins the bucket boundaries: cold (0), warm
// (>0.5), hot (>0.9). Exact thresholds must behave predictably.
func TestWarmthToRankBoundaries(t *testing.T) {
	cases := []struct {
		score float64
		want  int
	}{
		{-0.1, 0}, // negative → cold
		{0.0, 0},  // zero → cold
		{0.5, 0},  // exactly threshold → cold (not >)
		{0.51, 1}, // just above → warm
		{0.9, 1},  // exactly threshold → warm (not >)
		{0.91, 2}, // just above → hot
		{1.0, 2},  // max → hot
		{2.0, 2},  // above 1 (defensive) → hot
	}
	for _, c := range cases {
		got := warmthToRank(c.score)
		if got != c.want {
			t.Errorf("warmthToRank(%v) = %d, want %d", c.score, got, c.want)
		}
	}
}

// TestModelWarmthRankPicksHighestRelevant verifies that when a node has
// multiple relevant resident models, the highest warmth wins.
func TestModelWarmthRankPicksHighestRelevant(t *testing.T) {
	n := nodeComplete("n", 8000, "none", "ollama")
	n.Ollama = &models.OllamaInfo{Installed: true, Running: true}
	n.ResidentModels = []models.ResidentModel{
		{Name: "llama3:8b", Runtime: "ollama", Source: "ollama-ps", WarmthScore: 0.0},
		{Name: "qwen2:7b", Runtime: "ollama", Source: "ollama-ps", WarmthScore: 0.6},
	}
	reqs := models.TaskRequirements{
		RequiredTools: []string{"ollama"},
		Workload:      models.WorkloadProfileMatch{Class: models.ClassLocalLLMInference},
	}
	if got := modelWarmthRank(n, reqs); got != 1 {
		t.Fatalf("expected rank 1 (warm) from best of {0.0, 0.6}, got %d", got)
	}
}

// TestModelWarmthRankIgnoresOtherRuntimes verifies that warmth on a
// non-relevant runtime (e.g. llama.cpp) does not affect an ollama task's
// ranking — only ollama resident models count.
func TestModelWarmthRankIgnoresOtherRuntimes(t *testing.T) {
	n := nodeComplete("n", 8000, "none", "ollama")
	n.Ollama = &models.OllamaInfo{Installed: true, Running: true}
	// Resident model is llama.cpp, but task requires ollama. The warmth
	// on the llama.cpp entry must be ignored.
	n.ResidentModels = []models.ResidentModel{
		{Name: "llama3:8b", Runtime: "llama.cpp", Source: "proc-cmdline", WarmthScore: 1.0},
	}
	reqs := models.TaskRequirements{
		RequiredTools: []string{"ollama"},
		Workload:      models.WorkloadProfileMatch{Class: models.ClassLocalLLMInference},
	}
	if got := modelWarmthRank(n, reqs); got != 0 {
		t.Fatalf("expected rank 0 (no relevant model), got %d", got)
	}
}

// TestApplyOllamaWarmthTimeZero verifies the fact-layer helper: when
// ExpiresAt is zero, WarmthScore stays zero. This is the "older Ollama
// or no keep_alive" graceful-degradation path.
func TestApplyOllamaWarmthTimeZero(t *testing.T) {
	rms := []models.ResidentModel{
		{Name: "m1", Runtime: "ollama", Source: "ollama-ps"},
	}
	info := &models.OllamaInfo{Installed: true}
	facts.ApplyOllamaWarmth(info, rms)
	if rms[0].WarmthScore != 0 {
		t.Fatalf("expected WarmthScore=0 for zero ExpiresAt, got %v", rms[0].WarmthScore)
	}
}

// TestApplyOllamaWarmthInFuturePopulates verifies that a future ExpiresAt
// yields a non-zero WarmthScore.
func TestApplyOllamaWarmthInFuturePopulates(t *testing.T) {
	rms := []models.ResidentModel{
		{Name: "m1", Runtime: "ollama", Source: "ollama-ps", ExpiresAt: time.Now().Add(2 * time.Minute)},
	}
	info := &models.OllamaInfo{Installed: true, DefaultKeepAlive: "5m"}
	facts.ApplyOllamaWarmth(info, rms)
	if rms[0].WarmthScore <= 0 {
		t.Fatalf("expected positive WarmthScore, got %v", rms[0].WarmthScore)
	}
	if rms[0].WarmthScore > 1 {
		t.Fatalf("expected WarmthScore ≤ 1, got %v", rms[0].WarmthScore)
	}
}

// TestApplyOllamaWarmthPastExpiresAtIsCold verifies that an already-expired
// ExpiresAt is treated as cold (WarmthScore=0), not negative.
func TestApplyOllamaWarmthPastExpiresAtIsCold(t *testing.T) {
	rms := []models.ResidentModel{
		{Name: "m1", Runtime: "ollama", Source: "ollama-ps", ExpiresAt: time.Now().Add(-1 * time.Minute)},
	}
	info := &models.OllamaInfo{Installed: true, DefaultKeepAlive: "5m"}
	facts.ApplyOllamaWarmth(info, rms)
	if rms[0].WarmthScore != 0 {
		t.Fatalf("expected WarmthScore=0 for past ExpiresAt, got %v", rms[0].WarmthScore)
	}
}

// TestDefaultOllamaKeepAliveFallbacks verifies the helper resolves 5m when
// DefaultKeepAlive is empty, unparseable, or negative.
func TestDefaultOllamaKeepAliveFallbacks(t *testing.T) {
	cases := []struct {
		name string
		info *models.OllamaInfo
	}{
		{"nil", nil},
		{"empty", &models.OllamaInfo{DefaultKeepAlive: ""}},
		{"garbage", &models.OllamaInfo{DefaultKeepAlive: "not-a-duration"}},
		{"negative", &models.OllamaInfo{DefaultKeepAlive: "-30s"}},
		{"zero", &models.OllamaInfo{DefaultKeepAlive: "0s"}},
	}
	for _, c := range cases {
		got := facts.DefaultOllamaKeepAlive(c.info)
		if got != 5*time.Minute {
			t.Errorf("%s: expected 5m fallback, got %v", c.name, got)
		}
	}
}

// TestDefaultOllamaKeepAliveParses verifies the helper accepts valid
// duration strings and returns them unchanged.
func TestDefaultOllamaKeepAliveParses(t *testing.T) {
	info := &models.OllamaInfo{DefaultKeepAlive: "1h"}
	if got := facts.DefaultOllamaKeepAlive(info); got != time.Hour {
		t.Fatalf("expected 1h, got %v", got)
	}
	info.DefaultKeepAlive = "30s"
	if got := facts.DefaultOllamaKeepAlive(info); got != 30*time.Second {
		t.Fatalf("expected 30s, got %v", got)
	}
}
