package modelplan

import (
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func TestPlanSingleNodeSelectedReadyProfile(t *testing.T) {
	spec := models.ModelSpec{
		Schema:       "axis.model-spec/v1",
		ID:           "ms-ready",
		Name:         "qwen",
		Format:       models.ModelFormatGGUF,
		Source:       "disk-weight",
		Quantization: "Q4_K_M",
		WeightsPath:  "/mnt/models/qwen.gguf",
		Memory: models.ModelMemoryRequirements{
			WeightSizeMB:      4096,
			ContextOverheadMB: 512,
			RuntimeOverheadMB: 256,
		},
		Accelerators: []models.AcceleratorType{models.AcceleratorCUDA},
	}
	snap := &models.ClusterSnapshot{
		Timestamp:   time.Now().UTC(),
		Publication: &models.PublicationEnvelope{ID: "pub-selected"},
		Nodes: []models.NodeFacts{{
			Name:   "gpu-node",
			Status: models.StatusComplete,
			Tools:  []models.ToolInfo{{Name: "llama-server", Path: "/usr/local/bin/llama-server"}},
			Resources: &models.Resources{
				RAMFreeMB:  64000,
				RAMTotalMB: 128000,
				Volumes:    []models.Volume{{Mount: "/mnt/models", Kind: "local"}},
				GPUs: []models.GPUInfo{{
					Vendor: "nvidia", Model: "RTX 4090", VRAMMB: 24576,
					VRAMFreeMB: 20000, VRAMFreeMeasured: true, Capabilities: []string{"cuda"},
				}},
			},
		}},
	}
	plan, err := PlanSingleNode(snap, spec, 8080)
	if err != nil {
		t.Fatal(err)
	}
	if plan.BestCandidate != "gpu-node" || plan.Selected == nil {
		t.Fatalf("best=%q selected=%v", plan.BestCandidate, plan.Selected)
	}
	got := plan.Selected
	if got.Schema != models.ModelRunSchema || got.Node != "gpu-node" || got.Engine != "llama.cpp" {
		t.Fatalf("selected=%+v", got)
	}
	if got.EngineBinary != "/usr/local/bin/llama-server" || got.Volume != "/mnt/models" || got.WeightsPath != "/mnt/models/qwen.gguf" {
		t.Fatalf("selected=%+v", got)
	}
	if got.Port != 8080 || got.PortSource != models.PortSourcePlanDefault || got.BindHost != "127.0.0.1" {
		t.Fatalf("selected=%+v", got)
	}
	if got.DeviceKind != models.DeviceKindDiscrete || !got.VRAMFreeMeasured || got.VRAMFreeMB != 20000 {
		t.Fatalf("device=%+v", got)
	}
	if len(got.Refusals) != 0 || got.NGPULayers != nil || got.NGPULayersMode != "" || got.DeviceIndex != nil {
		t.Fatalf("selected must not pin or offload: %+v", got)
	}
	if got.SnapshotPublicationID != "pub-selected" || got.Quantization != "Q4_K_M" || got.SpecSource != "disk-weight" {
		t.Fatalf("provenance=%+v", got)
	}
}

func TestPlanSingleNodeSelectedSetWhenBestCandidateCannotLaunch(t *testing.T) {
	spec := models.ModelSpec{
		Schema:       "axis.model-spec/v1",
		ID:           "ms-bare",
		Name:         "qwen",
		Format:       models.ModelFormatGGUF,
		WeightsPath:  "/data/models/qwen.gguf",
		Memory:       models.ModelMemoryRequirements{WeightSizeMB: 4096, ContextOverheadMB: 512, RuntimeOverheadMB: 256},
		Accelerators: []models.AcceleratorType{models.AcceleratorCUDA, models.AcceleratorCPU},
	}
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{{
			Name:   "gpu-worker",
			Status: models.StatusComplete,
			Resources: &models.Resources{
				RAMFreeMB: 32000, RAMTotalMB: 64000,
				GPUs: []models.GPUInfo{{Vendor: "nvidia", Model: "RTX 4090", VRAMMB: 24576, Capabilities: []string{"cuda"}}},
			},
		}},
	}
	plan, err := PlanSingleNode(snap, spec, 8080)
	if err != nil {
		t.Fatal(err)
	}
	if plan.BestCandidate != "gpu-worker" || plan.Selected == nil {
		t.Fatalf("best=%q selected nil=%v", plan.BestCandidate, plan.Selected == nil)
	}
	joined := strings.Join(plan.Selected.Refusals, "\n")
	if !strings.Contains(joined, "llama-server") || !strings.Contains(joined, "named local volume") {
		t.Fatalf("refusals=%v", plan.Selected.Refusals)
	}
}
