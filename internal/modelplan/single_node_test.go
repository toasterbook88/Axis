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

// TestPlanSingleNodeNeverSumsVRAMAcrossGPUs pins the per-device VRAM contract:
// a model larger than the best single GPU must never qualify for full offload
// just because two smaller GPUs sum past the requirement. llama.cpp cannot
// pool VRAM across devices for a single model without explicit tensor-split,
// which the planner does not model.
func TestPlanSingleNodeNeverSumsVRAMAcrossGPUs(t *testing.T) {
	// 12 GiB required; two 7 GiB GPUs. Sum = 14 GiB (old bug: "full offload"),
	// best single device = 7 GiB (correct: cannot fully offload).
	spec := models.ModelSpec{
		ID:     "ms-vram-sum",
		Name:   "twelve-gb-model",
		Format: models.ModelFormatGGUF,
		Memory: models.ModelMemoryRequirements{
			WeightSizeMB:      12288,
			ContextOverheadMB: 512,
			RuntimeOverheadMB: 256,
		},
		Accelerators: []models.AcceleratorType{models.AcceleratorCUDA},
	}

	snap := &models.ClusterSnapshot{
		Timestamp: time.Now().UTC(),
		Nodes: []models.NodeFacts{
			{
				Name:   "dual-gpu-node",
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMFreeMB:  32000,
					RAMTotalMB: 64000,
					GPUs: []models.GPUInfo{
						{Model: "NVIDIA A", Vendor: "nvidia", VRAMMB: 7168, Capabilities: []string{"cuda"}},
						{Model: "NVIDIA B", Vendor: "nvidia", VRAMMB: 7168, Capabilities: []string{"cuda"}},
					},
				},
			},
			{
				Name:   "single-big-gpu-node",
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMFreeMB:  32000,
					RAMTotalMB: 64000,
					GPUs: []models.GPUInfo{
						{Model: "NVIDIA C", Vendor: "nvidia", VRAMMB: 24576, Capabilities: []string{"cuda"}},
					},
				},
			},
		},
	}

	plan, err := PlanSingleNode(snap, spec, 8080)
	if err != nil {
		t.Fatalf("PlanSingleNode: %v", err)
	}

	for _, c := range plan.Candidates {
		var reasoning string
		for _, r := range c.Reasoning {
			if strings.Contains(r, "offload") {
				reasoning = r
			}
		}
		switch c.Node {
		case "dual-gpu-node":
			// Best single device is 7168 MiB < 13056 required: must NOT claim full offload.
			if strings.Contains(reasoning, "full accelerator offload") {
				t.Errorf("dual-gpu-node claimed full offload: %q (VRAM was pooled across GPUs)", reasoning)
			}
		case "single-big-gpu-node":
			// 24576 MiB single device >= 13056 required: full offload is genuinely possible.
			if !strings.Contains(reasoning, "full accelerator offload") {
				t.Errorf("single-big-gpu-node lost full offload verdict: %q", reasoning)
			}
		}
	}

	// The reported VRAM figures must describe the best single device, not the pool.
	for _, c := range plan.Candidates {
		if c.Node == "dual-gpu-node" && c.VRAMTotalMB > 7168 {
			t.Errorf("dual-gpu-node VRAMTotalMB = %d, want best single device (7168); pooled values leak into output", c.VRAMTotalMB)
		}
	}
}

// TestPlanSingleNodePrefersMeasuredFreeVRAM pins the free-VRAM contract: when
// the fact plane reports a measured VRAMFreeMB, fit math uses it instead of
// total; when it reports 0 (unmeasured), the total is used as the fallback and
// never presented as a measurement.
func TestPlanSingleNodePrefersMeasuredFreeVRAM(t *testing.T) {
	// GPU with 24576 MiB total but only 2048 MiB measured free (9 GiB resident
	// model loaded). A 12 GiB model must NOT claim full offload.
	spec := models.ModelSpec{
		ID:     "ms-free-vram",
		Name:   "twelve-gb-model",
		Format: models.ModelFormatGGUF,
		Memory: models.ModelMemoryRequirements{
			WeightSizeMB:      12288,
			ContextOverheadMB: 512,
			RuntimeOverheadMB: 256,
		},
		Accelerators: []models.AcceleratorType{models.AcceleratorCUDA},
	}
	snap := &models.ClusterSnapshot{
		Timestamp: time.Now().UTC(),
		Nodes: []models.NodeFacts{
			{
				Name:   "busy-gpu-node",
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMFreeMB:  32000,
					RAMTotalMB: 64000,
					GPUs: []models.GPUInfo{
						{Model: "NVIDIA Busy", Vendor: "nvidia", VRAMMB: 24576, VRAMFreeMB: 2048, Capabilities: []string{"cuda"}},
					},
				},
			},
			{
				Name:   "unmeasured-gpu-node",
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMFreeMB:  32000,
					RAMTotalMB: 64000,
					GPUs: []models.GPUInfo{
						{Model: "NVIDIA Idle", Vendor: "nvidia", VRAMMB: 24576, Capabilities: []string{"cuda"}},
					},
				},
			},
		},
	}

	plan, err := PlanSingleNode(snap, spec, 8080)
	if err != nil {
		t.Fatalf("PlanSingleNode: %v", err)
	}

	for _, c := range plan.Candidates {
		switch c.Node {
		case "busy-gpu-node":
			if c.VRAMFreeMB != 2048 {
				t.Errorf("busy-gpu-node VRAMFreeMB = %d, want measured 2048 (total-as-free leak)", c.VRAMFreeMB)
			}
			for _, r := range c.Reasoning {
				if strings.Contains(r, "full accelerator offload") {
					t.Errorf("busy-gpu-node claimed full offload with 2048 MiB free: %q", r)
				}
			}
		case "unmeasured-gpu-node":
			// No measurement: falls back to total; full offload verdict is the
			// planner's honest best estimate, not a false claim.
			if c.VRAMFreeMB != 24576 {
				t.Errorf("unmeasured-gpu-node VRAMFreeMB = %d, want total fallback 24576", c.VRAMFreeMB)
			}
		}
	}
}

// TestPlanSingleNode_OverloadedZeroVRAM ensures a fully exhausted card reporting
// 0 MiB free (with VRAMFreeMeasured=true) is never treated as empty (total fallback).
func TestPlanSingleNode_OverloadedZeroVRAM(t *testing.T) {
	spec := models.ModelSpec{
		ID:     "ms-zero-vram",
		Name:   "small-model",
		Format: models.ModelFormatGGUF,
		Memory: models.ModelMemoryRequirements{
			WeightSizeMB:      4096,
			ContextOverheadMB: 512,
			RuntimeOverheadMB: 256,
		},
		Accelerators: []models.AcceleratorType{models.AcceleratorCUDA},
	}
	snap := &models.ClusterSnapshot{
		Timestamp: time.Now().UTC(),
		Nodes: []models.NodeFacts{
			{
				Name:   "exhausted-gpu-node",
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMFreeMB:  32000,
					RAMTotalMB: 64000,
					GPUs: []models.GPUInfo{
						{
							Model:            "NVIDIA RTX 4090",
							Vendor:           "nvidia",
							VRAMMB:           24576,
							VRAMFreeMB:       0,
							VRAMFreeMeasured: true,
							Capabilities:     []string{"cuda"},
						},
					},
				},
			},
		},
	}

	plan, err := PlanSingleNode(snap, spec, 8080)
	if err != nil {
		t.Fatalf("PlanSingleNode: %v", err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(plan.Candidates))
	}
	cand := plan.Candidates[0]
	if cand.VRAMFreeMB != 0 {
		t.Errorf("cand.VRAMFreeMB = %d, want 0 (must not fall back to TotalMB 24576)", cand.VRAMFreeMB)
	}
	for _, r := range cand.Reasoning {
		if strings.Contains(r, "offload possible") {
			t.Errorf("exhausted-gpu-node must not claim VRAM offload with 0 free: %q", r)
		}
	}
}

// TestPlanSingleNode_EligibilityGatesOnFreeVRAM ensures that a card with large total
// VRAM but insufficient free VRAM and insufficient RAM is properly excluded.
func TestPlanSingleNode_EligibilityGatesOnFreeVRAM(t *testing.T) {
	spec := models.ModelSpec{
		ID:     "ms-tight-vram",
		Name:   "twelve-gb-model",
		Format: models.ModelFormatGGUF,
		Memory: models.ModelMemoryRequirements{
			WeightSizeMB:      10240,
			ContextOverheadMB: 1024,
			RuntimeOverheadMB: 1024, // total required: 12288 MiB
		},
		Accelerators: []models.AcceleratorType{models.AcceleratorCUDA},
	}
	// Node has 24 GiB total VRAM, but only 2 GiB free, and only 1 GiB free RAM.
	// Total available (2048 + 1024 = 3072 MiB) is far below 12288 MiB required.
	snap := &models.ClusterSnapshot{
		Timestamp: time.Now().UTC(),
		Nodes: []models.NodeFacts{
			{
				Name:   "tight-node",
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMFreeMB:  1024,
					RAMTotalMB: 64000,
					GPUs: []models.GPUInfo{
						{
							Model:            "NVIDIA RTX 4090",
							Vendor:           "nvidia",
							VRAMMB:           24576,
							VRAMFreeMB:       2048,
							VRAMFreeMeasured: true,
							Capabilities:     []string{"cuda"},
						},
					},
				},
			},
		},
	}

	plan, err := PlanSingleNode(snap, spec, 8080)
	if err != nil {
		t.Fatalf("PlanSingleNode: %v", err)
	}
	if len(plan.Candidates) != 0 {
		t.Fatalf("expected 0 candidates, got %d (tight node should have been excluded)", len(plan.Candidates))
	}
	if len(plan.Excluded) != 1 {
		t.Fatalf("expected 1 excluded node, got %d", len(plan.Excluded))
	}
	if plan.Excluded[0].Node != "tight-node" {
		t.Errorf("excluded node = %q, want tight-node", plan.Excluded[0].Node)
	}
}

// TestPlanSingleNode_BestDevicePrefersFreeVRAM ensures that when a node has multiple GPUs,
// the device with the most free VRAM is selected as BestDevice, not merely the largest total.
func TestPlanSingleNode_BestDevicePrefersFreeVRAM(t *testing.T) {
	spec := models.ModelSpec{
		ID:     "ms-multi-gpu",
		Name:   "eight-gb-model",
		Format: models.ModelFormatGGUF,
		Memory: models.ModelMemoryRequirements{
			WeightSizeMB:      7000,
			ContextOverheadMB: 512,
			RuntimeOverheadMB: 512, // total required: 8024 MiB
		},
		Accelerators: []models.AcceleratorType{models.AcceleratorCUDA},
	}
	// GPU 0: 24 GiB total, but only 1 GiB free (busy).
	// GPU 1: 16 GiB total, 15 GiB free (idle).
	snap := &models.ClusterSnapshot{
		Timestamp: time.Now().UTC(),
		Nodes: []models.NodeFacts{
			{
				Name:   "dual-gpu-node",
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMFreeMB:  32000,
					RAMTotalMB: 64000,
					GPUs: []models.GPUInfo{
						{
							Model:            "NVIDIA RTX 4090",
							Vendor:           "nvidia",
							VRAMMB:           24576,
							VRAMFreeMB:       1024,
							VRAMFreeMeasured: true,
							Capabilities:     []string{"cuda"},
						},
						{
							Model:            "NVIDIA RTX 4080",
							Vendor:           "nvidia",
							VRAMMB:           16384,
							VRAMFreeMB:       15360,
							VRAMFreeMeasured: true,
							Capabilities:     []string{"cuda"},
						},
					},
				},
			},
		},
	}

	plan, err := PlanSingleNode(snap, spec, 8080)
	if err != nil {
		t.Fatalf("PlanSingleNode: %v", err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(plan.Candidates))
	}
	cand := plan.Candidates[0]
	if cand.VRAMFreeMB != 15360 {
		t.Errorf("cand.VRAMFreeMB = %d, want 15360 (RTX 4080 with 15 GiB free must be chosen over busy RTX 4090)", cand.VRAMFreeMB)
	}
	if !strings.Contains(cand.Accelerator, "RTX 4080") {
		t.Errorf("cand.Accelerator = %q, want RTX 4080", cand.Accelerator)
	}
}

// TestFormatModelPlacementPlanText_PrintsSnapshotSource ensures that text plan
// output displays Snapshot Source prominently.
func TestFormatModelPlacementPlanText_PrintsSnapshotSource(t *testing.T) {
	plan := ModelPlacementPlan{
		Spec: models.ModelSpec{
			ID:   "test-spec",
			Name: "test-model",
		},
		TargetPort:     8080,
		PublicationID:  "pub-12345",
		SnapshotSource: "daemon-cache",
	}

	out := FormatModelPlacementPlanText(plan)
	if !strings.Contains(out, "Snapshot Source: daemon-cache") {
		t.Errorf("FormatModelPlacementPlanText missing Snapshot Source: got %q", out)
	}
	if !strings.Contains(out, "Snapshot Publication: pub-12345") {
		t.Errorf("FormatModelPlacementPlanText missing Snapshot Publication: got %q", out)
	}
}
