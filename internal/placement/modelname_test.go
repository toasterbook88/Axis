package placement

import (
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

// TestInferenceModelNameGating verifies that model name extraction only fires
// for inference workload classes, not general ones where -m is a common flag
// (e.g. git commit -m "message").
func TestInferenceModelNameGating(t *testing.T) {
	desc := "git commit -m llama3.2:latest"

	inferenceClasses := []models.WorkloadClass{
		models.ClassLocalLLMInference,
		models.ClassLongContextInference,
		models.ClassAppleIntelligence,
		models.ClassLlamaServer,
	}
	nonInferenceClasses := []models.WorkloadClass{
		models.ClassRepoAnalysis,
		models.ClassGoBuild,
		models.ClassDockerBuild,
		models.ClassBatchScript,
		models.ClassIndexingIO,
		models.ClassUnknown,
	}

	for _, cls := range inferenceClasses {
		reqs := models.TaskRequirements{
			Description: desc,
			Workload:    models.WorkloadProfileMatch{Class: cls},
		}
		got := inferenceModelName(reqs)
		if got == "" {
			t.Errorf("inferenceModelName with class %q: expected non-empty, got %q", cls, got)
		}
	}

	for _, cls := range nonInferenceClasses {
		reqs := models.TaskRequirements{
			Description: desc,
			Workload:    models.WorkloadProfileMatch{Class: cls},
		}
		got := inferenceModelName(reqs)
		if got != "" {
			t.Errorf("inferenceModelName with class %q: expected empty, got %q", cls, got)
		}
	}
}

func TestExtractModelName(t *testing.T) {
	tests := []struct {
		name        string
		description string
		want        string
	}{
		// --- Explicit flag forms ---
		{
			name:        "double dash equals",
			description: "ollama --model=llama3.2:latest run",
			want:        "llama3.2:latest",
		},
		{
			name:        "short flag equals",
			description: "llama-server -m=qwen2.5-coder-7b-q4.gguf",
			want:        "qwen2.5-coder-7b-q4.gguf",
		},
		{
			name:        "double dash space",
			description: "run inference --model llama3.2:latest on cortex",
			want:        "llama3.2:latest",
		},
		{
			name:        "short flag space",
			description: "llama-server -m qwen2.5:14b --port 8080",
			want:        "qwen2.5:14b",
		},

		// --- Ollama subcommand forms ---
		{
			name:        "ollama run",
			description: "ollama run llama3.2:latest",
			want:        "llama3.2:latest",
		},
		{
			name:        "ollama pull",
			description: "ollama pull qwen2.5-coder:7b",
			want:        "qwen2.5-coder:7b",
		},
		{
			name:        "ollama run in longer sentence",
			description: "please run ollama run phi4:latest on cortex",
			want:        "phi4:latest",
		},

		// --- Bare model-tag heuristic ---
		{
			name:        "bare model with colon tag",
			description: "run llama3.2:latest",
			want:        "llama3.2:latest",
		},
		{
			name:        "namespaced model with slash",
			description: "hf.co/org/model:q4_k_m inference",
			want:        "hf.co/org/model:q4_k_m",
		},

		// --- Negative cases: should return "" ---
		{
			name:        "empty string",
			description: "",
			want:        "",
		},
		{
			name:        "plain prose without model",
			description: "run a swift build on the fastest node",
			want:        "",
		},
		{
			name:        "absolute file path is not a model name",
			description: "--model /home/user/models/qwen.gguf",
			want:        "",
		},
		{
			name:        "known non-model word cpu",
			description: "use cpu backend",
			want:        "",
		},
		{
			name:        "bare word without colon or slash is not extracted",
			description: "run llama3",
			want:        "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractModelName(tc.description)
			if got != tc.want {
				t.Errorf("ExtractModelName(%q) = %q, want %q", tc.description, got, tc.want)
			}
		})
	}
}
