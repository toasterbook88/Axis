package main

import (
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

// The context block is a paste-as-system-prompt artifact for external AI
// agents. Its best-node line must carry GPU identity, VRAM, and capabilities
// so an agent can reason about accelerators without a second round-trip.
func TestBuildContextBlockIncludesGPUVRAMOnBestNodeLine(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name:   "cachyos",
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMFreeMB: 16384,
					Pressure:  "none",
					GPUs: []models.GPUInfo{
						{Vendor: "nvidia", Model: "NVIDIA GeForce RTX 5060", VRAMMB: 8188, Capabilities: []string{"cuda", "vulkan"}},
					},
				},
				Tools: []models.ToolInfo{{Name: "ollama"}},
			},
		},
		Summary: models.ClusterSummary{TotalNodes: 1, TotalReservableMB: 16384, TotalAllocatableMB: 16384},
	}

	out := buildContextBlock(snap, models.TaskRequirements{}, "run 13B inference", "live", nil, nil)

	if !strings.Contains(out, "Best node: cachyos") {
		t.Fatalf("missing best node name:\n%s", out)
	}
	if !strings.Contains(out, "GPU: NVIDIA GeForce RTX 5060 8188MB [cuda,vulkan]") {
		t.Fatalf("missing GPU segment with VRAM and capabilities:\n%s", out)
	}
}

// A node reporting no GPUs must not gain a fabricated GPU segment.
func TestBuildContextBlockOmitsGPUSegmentWhenAbsent(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name:      "cpu-only",
				Status:    models.StatusComplete,
				Resources: &models.Resources{RAMFreeMB: 4096, Pressure: "none"},
			},
		},
		Summary: models.ClusterSummary{TotalNodes: 1, TotalReservableMB: 4096, TotalAllocatableMB: 4096},
	}

	out := buildContextBlock(snap, models.TaskRequirements{}, "compile code", "live", nil, nil)
	if strings.Contains(out, "GPU:") {
		t.Fatalf("fabricated GPU segment for GPU-less node:\n%s", out)
	}
}

// Resident models on the best node are the warm-start signal for inference
// tasks; they must appear with runtime, port, and VRAM when known.
func TestBuildContextBlockIncludesResidentModels(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name:      "axis5",
				Status:    models.StatusComplete,
				Resources: &models.Resources{RAMFreeMB: 1993, Pressure: "none"},
				ResidentModels: []models.ResidentModel{
					{Name: "bitnet-2B", Runtime: "llama.cpp", Port: 8080, SizeRAMMB: 1132},
				},
			},
		},
		Summary: models.ClusterSummary{TotalNodes: 1, TotalReservableMB: 1993, TotalAllocatableMB: 1993},
	}

	out := buildContextBlock(snap, models.TaskRequirements{}, "prompt the local model", "live", nil, nil)
	if !strings.Contains(out, "Resident models here: bitnet-2B on llama.cpp:8080") {
		t.Fatalf("missing resident models line:\n%s", out)
	}
}

// Disk weights tell the agent what can be started locally without a
// download; they are distinct from resident (loaded) models and must be
// rendered with format when present, truncated beyond three entries.
func TestBuildContextBlockIncludesDiskWeightsTruncated(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name:      "storage",
				Status:    models.StatusComplete,
				Resources: &models.Resources{RAMFreeMB: 150000, Pressure: "none"},
				DiskWeights: []models.DiskWeight{
					{Name: "qwen3.8-27b", Path: "/mnt/ssd/qwen.gguf", Bytes: 55000000000, Format: "gguf"},
					{Name: "ornith-1.5", Path: "/mnt/ssd/ornith", Bytes: 21000000000, Format: "safetensors"},
					{Name: "jack-coder", Path: "/mnt/ssd/jack.gguf", Bytes: 11700000000, Format: "gguf"},
					{Name: "phi3-mini", Path: "/mnt/ssd/phi3.gguf", Bytes: 2400000000, Format: "gguf"},
				},
			},
		},
		Summary: models.ClusterSummary{TotalNodes: 1, TotalReservableMB: 150000, TotalAllocatableMB: 150000},
	}

	out := buildContextBlock(snap, models.TaskRequirements{}, "start a 27B model", "live", nil, nil)
	if !strings.Contains(out, "Weights on disk here: qwen3.8-27b (gguf), ornith-1.5 (safetensors), jack-coder (gguf), +1 more") {
		t.Fatalf("missing or malformed disk weights line:\n%s", out)
	}
}

// The next-action guidance must name the real execution paths (guarded task
// run, model lifecycle) instead of implying a single generic MCP command.
func TestBuildContextBlockNextActionNamesRealPaths(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name:      "cachyos",
				Status:    models.StatusComplete,
				Resources: &models.Resources{RAMFreeMB: 16384, Pressure: "none"},
				Tools:     []models.ToolInfo{{Name: "ollama"}},
			},
		},
		Summary: models.ClusterSummary{TotalNodes: 1, TotalReservableMB: 16384, TotalAllocatableMB: 16384},
	}

	out := buildContextBlock(snap, models.TaskRequirements{}, "run task", "live", nil, nil)
	for _, want := range []string{
		"axis task run --script/--exec",
		"axis model start --node",
		"axis mcp serve",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("next-action guidance missing %q:\n%s", want, out)
		}
	}
}
