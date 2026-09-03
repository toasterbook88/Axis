package modelplan

import (
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func TestPlanSingleNodeEvaluatesCandidatesAndSortsByScore(t *testing.T) {
	spec := models.ModelSpec{
		Schema: "axis.model-spec/v1",
		ID:     "ms-test-qwen",
		Name:   "qwen2.5-7b",
		Format: models.ModelFormatGGUF,
		Memory: models.ModelMemoryRequirements{
			WeightSizeMB:      4096,
			ContextOverheadMB: 512,
			RuntimeOverheadMB: 256,
		},
		Accelerators: []models.AcceleratorType{models.AcceleratorCUDA, models.AcceleratorMetal, models.AcceleratorCPU},
	}

	snap := &models.ClusterSnapshot{
		Timestamp: time.Now().UTC(),
		Publication: &models.PublicationEnvelope{
			ID: "pub-test-plan-1",
		},
		Nodes: []models.NodeFacts{
			{
				Name:   "gpu-node",
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMFreeMB:  32000,
					RAMTotalMB: 64000,
					GPUs: []models.GPUInfo{
						{
							Model:        "NVIDIA RTX 4090",
							Vendor:       "nvidia",
							VRAMMB:       24576,
							Capabilities: []string{"cuda"},
						},
					},
				},
				DiskWeights: []models.DiskWeight{
					{
						Name:   "qwen2.5-7b",
						Path:   "/mnt/models/qwen2.5-7b.gguf",
						Bytes:  4 * 1024 * 1024 * 1024,
						Format: "gguf",
					},
				},
			},
			{
				Name:   "cpu-node",
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMFreeMB:  16000,
					RAMTotalMB: 32000,
				},
			},
			{
				Name:   "occupied-node",
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMFreeMB:  32000,
					RAMTotalMB: 64000,
				},
				ResidentModels: []models.ResidentModel{
					{
						Name:    "other-model",
						Runtime: "llama.cpp",
						Port:    8080,
					},
				},
			},
			{
				Name:   "low-ram-node",
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMFreeMB:  1000, // < 4864 MiB required
					RAMTotalMB: 4000,
				},
			},
			{
				Name:   "degraded-node",
				Status: models.StatusPartial,
				Resources: &models.Resources{
					RAMFreeMB:  64000,
					RAMTotalMB: 128000,
				},
			},
		},
	}

	plan, err := PlanSingleNode(snap, spec, 8080)
	if err != nil {
		t.Fatalf("PlanSingleNode failed: %v", err)
	}

	if plan.BestCandidate != "gpu-node" {
		t.Fatalf("BestCandidate = %q, want gpu-node", plan.BestCandidate)
	}
	if len(plan.Candidates) != 2 {
		t.Fatalf("candidates count = %d, want 2", len(plan.Candidates))
	}
	if plan.Candidates[0].Node != "gpu-node" || plan.Candidates[0].Score < 80 {
		t.Fatalf("expected gpu-node to score >= 80, got %+v", plan.Candidates[0])
	}
	if !plan.Candidates[0].HasLocalWeights {
		t.Errorf("expected gpu-node to have local weights")
	}
	if plan.Candidates[1].Node != "cpu-node" {
		t.Fatalf("expected second candidate to be cpu-node, got %+v", plan.Candidates[1])
	}

	// Verify exclusions
	if len(plan.Excluded) != 3 {
		t.Fatalf("excluded count = %d, want 3", len(plan.Excluded))
	}

	excludedMap := make(map[string][]string)
	for _, ex := range plan.Excluded {
		excludedMap[ex.Node] = ex.Reasons
	}

	if _, ok := excludedMap["occupied-node"]; !ok {
		t.Errorf("occupied-node not excluded")
	}
	if _, ok := excludedMap["low-ram-node"]; !ok {
		t.Errorf("low-ram-node not excluded")
	}
	if _, ok := excludedMap["degraded-node"]; !ok {
		t.Errorf("degraded-node not excluded")
	}

	text := FormatModelPlacementPlanText(plan)
	if !strings.Contains(text, "MODEL PLACEMENT PLAN: qwen2.5-7b") {
		t.Errorf("formatted text missing title: %s", text)
	}
	if !strings.Contains(text, "gpu-node") || !strings.Contains(text, "cpu-node") {
		t.Errorf("formatted text missing candidate nodes: %s", text)
	}
	if !strings.Contains(text, "occupied-node") {
		t.Errorf("formatted text missing excluded node: %s", text)
	}
}

func TestPlanSingleNodeValidation(t *testing.T) {
	validSpec := models.ModelSpec{
		ID:     "ms-1",
		Name:   "m",
		Format: models.ModelFormatGGUF,
		Memory: models.ModelMemoryRequirements{WeightSizeMB: 100},
	}
	snap := &models.ClusterSnapshot{}

	if _, err := PlanSingleNode(nil, validSpec, 8080); err == nil {
		t.Fatal("expected error for nil snapshot")
	}
	if _, err := PlanSingleNode(snap, models.ModelSpec{}, 8080); err == nil {
		t.Fatal("expected error for empty spec")
	}
	if _, err := PlanSingleNode(snap, validSpec, 0); err == nil {
		t.Fatal("expected error for port 0")
	}
	if _, err := PlanSingleNode(snap, validSpec, 70000); err == nil {
		t.Fatal("expected error for port > 65535")
	}
}
