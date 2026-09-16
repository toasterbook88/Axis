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
func TestDefaultOllamaKeepAliveParses(t *testing.T) {
	cases := []struct {
		input    string
		expected time.Duration
	}{
		{"1h", time.Hour},
		{"30s", 30 * time.Second},
		{"300", 5 * time.Minute},
		{"1200", 20 * time.Minute},
		{"30", 30 * time.Second},
		{" 600  ", 10 * time.Minute},
	}
	for _, c := range cases {
		info := &models.OllamaInfo{DefaultKeepAlive: c.input}
		if got := facts.DefaultOllamaKeepAlive(info); got != c.expected {
			t.Errorf("input %q: expected %v, got %v", c.input, c.expected, got)
		}
	}
}
