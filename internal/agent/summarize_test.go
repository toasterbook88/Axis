package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
)

func TestSummarizePlacementDecisionUsesStructuredRanking(t *testing.T) {
	dec := models.PlacementDecision{
		OK:   true,
		Node: "node-a",
		Ranking: &models.PlacementRanking{
			Objective: models.PlacementObjectiveCapacity,
			Source:    models.ObjectiveSourceDefault,
			Metric: models.RankingMetric{
				Name:       "allocatable_ram",
				Value:      0,
				Unit:       "MB",
				Provenance: models.MetricProvenanceDerived,
			},
			DecisiveBy: models.RankingCriterionOnlyCandidate,
		},
	}

	got := summarizePlacementDecision(dec)
	for _, want := range []string{"allocatable_ram = 0MB", "provenance: derived", "objective: capacity", "only_candidate"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary missing %q: %s", want, got)
		}
	}
}

func TestSummarizeSnapshotIncludesAge(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Timestamp: time.Now().Add(-35 * time.Second),
		Summary: models.ClusterSummary{
			TotalNodes:     2,
			ReachableNodes: 2,
		},
		Status: models.SnapshotHealthy,
	}

	got := summarizeSnapshot(snap)
	if !strings.Contains(got, "age: 35s") {
		t.Fatalf("expected snapshot summary to contain 'age: 35s', got: %s", got)
	}
}

func TestSummarizePlacementExplanationIncludesRunnerUpsAndExcluded(t *testing.T) {
	exp := models.PlacementExplanation{
		Decision: models.PlacementDecision{
			OK:       true,
			Node:     "cranium",
			FitScore: 90,
			IsLocal:  true,
			Ranking: &models.PlacementRanking{
				Objective:  models.PlacementObjectiveCapacity,
				Source:     models.ObjectiveSourceDefault,
				DecisiveBy: "allocatable_ram",
				Metric: models.RankingMetric{
					Name:       "allocatable_ram",
					Value:      29398,
					Unit:       "MB",
					Provenance: models.MetricProvenanceDerived,
				},
			},
			Reasoning: []string{"Local node fits without network transfer"},
		},
		Eligible: []models.PlacementCandidateExplanation{
			{
				Node:       "cranium",
				FitScore:   90,
				IsLocal:    true,
				HeadroomMB: 29398,
				Metric: models.RankingMetric{
					Name:  "allocatable_ram",
					Value: 29398,
					Unit:  "MB",
				},
			},
			{
				Node:       "cachyos",
				FitScore:   95,
				IsLocal:    false,
				HeadroomMB: 146038,
				Metric: models.RankingMetric{
					Name:  "allocatable_ram",
					Value: 146038,
					Unit:  "MB",
				},
			},
		},
		Excluded: []models.PlacementExclusion{
			{
				Node:    "axis5",
				Reasons: []string{"insufficient RAM: 3122 MB free < 16384 MB required"},
			},
		},
	}

	got := summarizePlacementExplanation(exp)
	for _, want := range []string{
		"Placement: cranium (allocatable_ram = 29398MB",
		"Runner-up candidates:",
		"- cachyos: allocatable_ram = 146038MB",
		"Excluded nodes:",
		"- axis5: insufficient RAM",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary missing %q:\n%s", want, got)
		}
	}
}

func TestSummarizePlacementExplanationRendersExclusionsWhenNoNodeFits(t *testing.T) {
	exp := models.PlacementExplanation{
		Decision: models.PlacementDecision{
			OK: false,
		},
		Excluded: []models.PlacementExclusion{
			{
				Node:    "axis5",
				Reasons: []string{"insufficient RAM: 3122 MB free < 16384 MB required"},
			},
			{
				Node:    "nixos",
				Reasons: []string{"missing required tool: docker"},
			},
		},
	}

	got := summarizePlacementExplanation(exp)
	for _, want := range []string{
		"Placement: no suitable node found for this task.",
		"Excluded nodes:",
		"- axis5: insufficient RAM",
		"- nixos: missing required tool: docker",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Runner-up candidates:") {
		t.Fatalf("summary should not contain runner-ups when no node fits:\n%s", got)
	}
}

func TestSummarizeClusterModels(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name: "node-a",
				ResidentModels: []models.ResidentModel{
					{
						Name:        "qwen3-coder",
						Runtime:     "llama.cpp",
						Port:        8082,
						SizeVRAMMB:  8192,
						WarmthScore: 90,
					},
				},
				Ollama: &models.OllamaInfo{
					Installed: true,
					Version:   "0.5.12",
					Models:    []string{"llama3.2:latest"},
				},
			},
		},
	}
	cfg := &config.AIConfig{
		Backends: []config.AIBackendConfig{
			{
				Name:    "local-hub",
				Kind:    "openai-compatible",
				BaseURL: "http://127.0.0.1:4000/v1",
				Node:    "node-a",
			},
		},
		Roles: map[string]config.AIRoleConfig{
			"coder": {
				Model:  "qwen3-coder",
				Prefer: []string{"local-hub"},
			},
		},
	}

	got := summarizeClusterModels(snap, cfg, "")
	for _, want := range []string{
		"Active Resident Models",
		"qwen3-coder on node-a (llama.cpp, port 8082, 8192 MB VRAM, warmth: 90/100)",
		"Local Ollama Models",
		"node-a (1 models): llama3.2:latest",
		"Configured AI Roles",
		`role "coder" -> model "qwen3-coder"`,
		"AI Backends",
		"local-hub (openai-compatible, node: node-a, enabled)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("summarizeClusterModels missing %q:\n%s", want, got)
		}
	}

	// Filter by non-existent node
	filtered := summarizeClusterModels(snap, cfg, "non-existent")
	if !strings.Contains(filtered, "None currently resident") {
		t.Fatalf("expected 'None currently resident' when filtered, got:\n%s", filtered)
	}
}

func TestSummarizeNodeFactsWithResidentAndOllama(t *testing.T) {
	node := models.NodeFacts{
		Name:     "node-a",
		OS:       "linux",
		Arch:     "amd64",
		Hostname: "node-a.local",
		Role:     "primary",
		Resources: &models.Resources{
			CPUCores:    8,
			CPUModel:    "AMD Ryzen",
			RAMTotalMB:  16384,
			RAMFreeMB:   8192,
			DiskTotalGB: 500,
			DiskFreeGB:  250,
			GPUs: []models.GPUInfo{
				{Model: "RTX 5060", Vendor: "NVIDIA", VRAMMB: 8192},
			},
		},
		ResidentModels: []models.ResidentModel{
			{
				Name:       "bonsai-2-27b",
				Runtime:    "llama.cpp",
				Port:       8082,
				SizeVRAMMB: 16384,
			},
		},
		Ollama: &models.OllamaInfo{
			Installed: true,
			Version:   "0.5.12",
			Models:    []string{"llama3.2:latest"},
		},
		Tools: []models.ToolInfo{
			{Name: "docker"},
			{Name: "git"},
		},
		Status: models.StatusComplete,
	}

	got := summarizeNodeFacts(node)
	for _, want := range []string{
		"Node: node-a (linux/amd64, node-a.local)",
		"Role: primary",
		"CPU: 8 cores (AMD Ryzen)",
		"RAM: 16384 MB total, 8192 MB free",
		"GPU: RTX 5060 (NVIDIA, 8192 MB VRAM)",
		"Resident models (1):",
		"- bonsai-2-27b (llama.cpp, port 8082, 16384 MB VRAM)",
		"Ollama: 0.5.12 (1 models: llama3.2:latest)",
		"Tools: docker, git",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("summarizeNodeFacts missing %q:\n%s", want, got)
		}
	}
}
