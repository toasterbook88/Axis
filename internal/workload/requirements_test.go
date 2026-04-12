package workload

import (
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestMatchIgnoresHardwareInventoryPrompts(t *testing.T) {
	t.Run("generic hardware description stays unknown", func(t *testing.T) {
		match := Match("Dad computer specs: GPU NVIDIA RTX 5090 32GB, 192GB RAM, organize files from multiple machines and prep for cluster connection")
		if match.Class != models.ClassUnknown {
			t.Fatalf("match class = %q, want %q (notes=%v)", match.Class, models.ClassUnknown, match.Notes)
		}
	})

	t.Run("generic gpu wording does not imply inference", func(t *testing.T) {
		match := Match("deploy using gpu")
		if match.Class != models.ClassUnknown {
			t.Fatalf("match class = %q, want %q (notes=%v)", match.Class, models.ClassUnknown, match.Notes)
		}
	})
}

func TestMatchPromotesLongContextOverLocalInference(t *testing.T) {
	match := Match("run 128k book-length ollama inference")
	if match.Class != models.ClassLongContextInference {
		t.Fatalf("match class = %q, want %q", match.Class, models.ClassLongContextInference)
	}
	foundNote := false
	for _, note := range match.Notes {
		if note == "also matched class: local-llm-inference" {
			foundNote = true
			break
		}
	}
	if !foundNote {
		t.Fatalf("expected local-llm note, got %v", match.Notes)
	}
}

func TestInferRequirementsBoundaryAware(t *testing.T) {
	tests := []struct {
		desc        string
		wantClass   models.WorkloadClass
		wantTool    string
		wantRAM     int64
		wantTokens  int
		wantTQ      bool
		wantBackend string
	}{
		{
			desc:      "organize files from multiple machines and prepare this machine as a cluster node",
			wantClass: models.ClassUnknown,
		},
		{
			desc:      "run a local 7b coding model",
			wantClass: models.ClassLocalLLMInference,
			wantRAM:   4096,
		},
		{
			desc:      "run ollama inference",
			wantClass: models.ClassLocalLLMInference,
			wantTool:  "ollama",
			wantRAM:   6144,
		},
		{
			desc:        "run mlx long-context inference on apple silicon",
			wantClass:   models.ClassLongContextInference,
			wantRAM:     6144,
			wantTokens:  128000,
			wantTQ:      true,
			wantBackend: "mlx",
		},
		{
			desc:        "run 14b inference with mlx",
			wantClass:   models.ClassLocalLLMInference,
			wantRAM:     8192,
			wantBackend: "mlx",
		},
		{
			desc:        "llama-server -m /models/qwen.gguf",
			wantClass:   models.ClassLlamaServer,
			wantTool:    "llama-server",
			wantRAM:     6144,
			wantBackend: "llama.cpp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			reqs := InferRequirements(tt.desc)
			if reqs.Workload.Class != tt.wantClass {
				t.Fatalf("workload class = %q, want %q (notes=%v)", reqs.Workload.Class, tt.wantClass, reqs.Workload.Notes)
			}
			gotTool := ""
			if len(reqs.RequiredTools) > 0 {
				gotTool = reqs.RequiredTools[0]
			}
			if gotTool != tt.wantTool {
				t.Fatalf("required tool = %q, want %q (all=%v)", gotTool, tt.wantTool, reqs.RequiredTools)
			}
			if reqs.MinFreeRAMMB != tt.wantRAM {
				t.Fatalf("min free ram = %d, want %d", reqs.MinFreeRAMMB, tt.wantRAM)
			}
			if reqs.ContextWindowTokens != tt.wantTokens {
				t.Fatalf("context window = %d, want %d", reqs.ContextWindowTokens, tt.wantTokens)
			}
			if reqs.PrefersTurboQuant != tt.wantTQ {
				t.Fatalf("prefers turboquant = %v, want %v", reqs.PrefersTurboQuant, tt.wantTQ)
			}
			if tt.wantBackend != "" {
				if len(reqs.PreferredBackends) == 0 || reqs.PreferredBackends[0] != tt.wantBackend {
					t.Fatalf("preferred backends = %v, want leading backend %q", reqs.PreferredBackends, tt.wantBackend)
				}
			}
		})
	}
}
