package workload

import (
	"context"
	"errors"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

// --- Classifier seam ---

// mockClassifier is a test double for the Classifier interface.
type mockClassifier struct {
	result models.WorkloadProfileMatch
	err    error
	calls  int
}

func (m *mockClassifier) ClassifyWorkload(_ context.Context, _, _ string) (models.WorkloadProfileMatch, error) {
	m.calls++
	return m.result, m.err
}

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

// --- Signal matching edge cases ---

func TestSignalMatching(t *testing.T) {
	tests := []struct {
		name      string
		desc      string
		wantClass models.WorkloadClass
	}{
		// Repo analysis
		{"review codebase", "review codebase for security issues", models.ClassRepoAnalysis},
		{"repo + analyze", "analyze the repo for issues", models.ClassRepoAnalysis},
		{"commit + history", "check commit history in the repository", models.ClassRepoAnalysis},

		// Go build
		{"go test", "run go tests for the project", models.ClassGoBuild},
		{"go + compile", "compile the go module", models.ClassGoBuild},

		// Docker
		{"docker build", "docker build and push the image", models.ClassDockerBuild},
		{"docker + container", "spin up a docker container", models.ClassDockerBuild},

		// Indexing
		{"vectorize", "vectorize the document corpus", models.ClassIndexingIO},
		{"index + files", "index all files in the filesystem", models.ClassIndexingIO},

		// Batch script
		{"batch job", "run a batch job for processing", models.ClassBatchScript},
		{"batch + script", "execute the batch script", models.ClassBatchScript},
		{"run python", "run python data pipeline", models.ClassBatchScript},

		// Apple Intelligence
		{"apple foundation models", "summarize with apple foundation models", models.ClassAppleIntelligence},
		{"language model session", "create a language model session", models.ClassAppleIntelligence},

		// Llama
		{"llama server", "start a llama server instance", models.ClassLlamaServer},

		// Local LLM
		{"local llm", "run local llm for chat", models.ClassLocalLLMInference},
		{"ollama chat", "ollama chat with llama3", models.ClassLocalLLMInference},
		{"action + local model", "serve a local model for testing", models.ClassLocalLLMInference},
		{"inference + model", "run inference on the model", models.ClassLocalLLMInference},
		{"run + 32b model", "run 32b coding model locally", models.ClassLocalLLMInference},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			match := Match(tt.desc)
			if match.Class != tt.wantClass {
				t.Errorf("Match(%q).Class = %q, want %q (notes=%v)",
					tt.desc, match.Class, tt.wantClass, match.Notes)
			}
		})
	}
}

// --- PeakRAMHint coverage ---

func TestPeakRAMHintValues(t *testing.T) {
	tests := []struct {
		class models.WorkloadClass
		want  int64
	}{
		{models.ClassAppleIntelligence, 0},
		{models.ClassLlamaServer, 8192},
		{models.ClassLongContextInference, 10240},
		{models.ClassLocalLLMInference, 6144},
		{models.ClassRepoAnalysis, 1024},
		{models.ClassGoBuild, 2048},
		{models.ClassDockerBuild, 4096},
		{models.ClassIndexingIO, 2048},
		{models.ClassBatchScript, 512},
		{models.ClassUnknown, 0},
		{"nonexistent-class", 0},
	}
	for _, tt := range tests {
		got := PeakRAMHint(tt.class)
		if got != tt.want {
			t.Errorf("PeakRAMHint(%q) = %d, want %d", tt.class, got, tt.want)
		}
	}
}

// --- Context window and RAM floor tiers ---

func TestContextWindowTiers(t *testing.T) {
	tests := []struct {
		desc       string
		wantTokens int
	}{
		{"process 1m tokens", 1000000},
		{"million token context analysis", 1000000},
		{"use 512k context window", 512000},
		{"256k document summarization", 256000},
		{"200k context size", 256000},
		{"128k inference run", 128000},
		{"long context analysis", 128000},
		{"book length summarization", 128000},
		{"needle in a haystack test", 128000},
		{"just a normal task", 0},
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			got := InferContextWindowTokens(tt.desc)
			if got != tt.wantTokens {
				t.Errorf("InferContextWindowTokens(%q) = %d, want %d", tt.desc, got, tt.wantTokens)
			}
		})
	}
}

func TestLongContextMinRAMTiers(t *testing.T) {
	tests := []struct {
		tokens  int
		wantRAM int64
	}{
		{1000000, 12288},
		{512000, 8192},
		{256000, 6144},
		{128000, 6144},
		{64000, 0},
		{0, 0},
	}
	for _, tt := range tests {
		got := LongContextMinRAM(tt.tokens)
		if got != tt.wantRAM {
			t.Errorf("LongContextMinRAM(%d) = %d, want %d", tt.tokens, got, tt.wantRAM)
		}
	}
}

// --- Multi-signal overlap ---

func TestMultiSignalOverlap(t *testing.T) {
	t.Run("docker + repo notes both", func(t *testing.T) {
		reqs := InferRequirements("docker build this repo and analyze it")
		// Repo signal fires first (repo + analyze), docker is secondary
		if reqs.Workload.Class != models.ClassRepoAnalysis {
			t.Fatalf("class = %q, want repo-analysis (repo signal has priority)", reqs.Workload.Class)
		}
		foundDocker := false
		for _, tool := range reqs.RequiredTools {
			if tool == "docker" {
				foundDocker = true
			}
		}
		if !foundDocker {
			t.Errorf("expected docker in RequiredTools for docker+repo combo, got %v", reqs.RequiredTools)
		}
	})

	t.Run("go build + repo adds git", func(t *testing.T) {
		reqs := InferRequirements("go build the repo")
		if reqs.Workload.Class != models.ClassGoBuild {
			t.Fatalf("class = %q, want go-build", reqs.Workload.Class)
		}
		foundGit := false
		for _, tool := range reqs.RequiredTools {
			if tool == "git" {
				foundGit = true
			}
		}
		if !foundGit {
			t.Errorf("expected git from go-build profile, got %v", reqs.RequiredTools)
		}
	})

	t.Run("ollama explicit adds tool", func(t *testing.T) {
		reqs := InferRequirements("scan the filesystem with ollama embeddings")
		foundOllama := false
		for _, tool := range reqs.RequiredTools {
			if tool == "ollama" {
				foundOllama = true
			}
		}
		if !foundOllama {
			t.Errorf("explicit ollama mention should add tool, got %v", reqs.RequiredTools)
		}
	})
}

// --- shouldRequireOllama branches ---

func TestOllamaRequirement(t *testing.T) {
	t.Run("explicit ollama keyword", func(t *testing.T) {
		reqs := InferRequirements("ollama run llama3")
		foundOllama := false
		for _, tool := range reqs.RequiredTools {
			if tool == "ollama" {
				foundOllama = true
			}
		}
		if !foundOllama {
			t.Error("expected ollama in RequiredTools")
		}
	})

	t.Run("mlx backend skips ollama", func(t *testing.T) {
		reqs := InferRequirements("run 14b model with mlx inference")
		for _, tool := range reqs.RequiredTools {
			if tool == "ollama" {
				t.Error("mlx backend should not add ollama requirement")
			}
		}
	})

	t.Run("llm keyword requires ollama", func(t *testing.T) {
		reqs := InferRequirements("run llm inference on 13b model")
		foundOllama := false
		for _, tool := range reqs.RequiredTools {
			if tool == "ollama" {
				foundOllama = true
			}
		}
		if !foundOllama {
			t.Error("llm keyword should trigger ollama requirement")
		}
	})

	t.Run("large model size requires ollama", func(t *testing.T) {
		reqs := InferRequirements("run 70b inference")
		foundOllama := false
		for _, tool := range reqs.RequiredTools {
			if tool == "ollama" {
				foundOllama = true
			}
		}
		if !foundOllama {
			t.Error("70b description should trigger ollama requirement")
		}
	})
}

// --- Classifier seam tests ---

func TestInferRequirements_WithClassifier_UsesSemanticResult(t *testing.T) {
	// When a Classifier is provided and succeeds, InferRequirements must use
	// its WorkloadClass as the primary class — not the legacy string-matcher.
	mc := &mockClassifier{
		result: models.WorkloadProfileMatch{
			Class: models.ClassDockerBuild,
			Notes: []string{"semantic: detected docker intent"},
		},
	}

	// The description alone would NOT trigger ClassDockerBuild via legacy path.
	reqs := InferRequirements("containerize the application", InferRequirementsOptions{
		Classifier: mc,
	})

	if reqs.Workload.Class != models.ClassDockerBuild {
		t.Errorf("class = %q, want %q", reqs.Workload.Class, models.ClassDockerBuild)
	}
	if mc.calls != 1 {
		t.Errorf("classifier called %d times, want 1", mc.calls)
	}
	// Profile for docker-build must have been applied (requires docker tool).
	foundDocker := false
	for _, tool := range reqs.RequiredTools {
		if tool == "docker" {
			foundDocker = true
		}
	}
	if !foundDocker {
		t.Errorf("docker tool not applied from docker-build profile, tools=%v", reqs.RequiredTools)
	}
}

func TestInferRequirements_WithClassifier_FallsBackOnError(t *testing.T) {
	// When the Classifier returns an error, InferRequirements must silently
	// fall back to the legacy string-matcher and still produce a valid result.
	mc := &mockClassifier{
		err: errors.New("ollama unreachable"),
	}

	// "go build" is unambiguously matched by the legacy path.
	reqs := InferRequirements("go build ./...", InferRequirementsOptions{
		Classifier: mc,
	})

	if reqs.Workload.Class != models.ClassGoBuild {
		t.Errorf("class = %q, want %q (legacy fallback should have fired)", reqs.Workload.Class, models.ClassGoBuild)
	}
	if mc.calls != 1 {
		t.Errorf("classifier called %d times, want 1", mc.calls)
	}
}

func TestInferRequirements_WithNilClassifier_UsesLegacy(t *testing.T) {
	// An explicit nil Classifier is treated identically to the no-opts path.
	reqs := InferRequirements("go test ./...", InferRequirementsOptions{
		Classifier: nil,
	})

	if reqs.Workload.Class != models.ClassGoBuild {
		t.Errorf("class = %q, want go-build (legacy path)", reqs.Workload.Class)
	}
}

func TestInferRequirements_NoOpts_UsesLegacy(t *testing.T) {
	// Original call-site: no opts at all — must behave exactly as before.
	reqs := InferRequirements("docker build -t myapp .")
	if reqs.Workload.Class != models.ClassDockerBuild {
		t.Errorf("class = %q, want docker-build", reqs.Workload.Class)
	}
}

func TestInferRequirements_WithClassifier_ExtraContextForwarded(t *testing.T) {
	// ExtraContext should be forwarded to the classifier. We verify this by
	// capturing it in the mock.
	var capturedExtra string
	mc := classifierFunc(func(_ context.Context, _, extra string) (models.WorkloadProfileMatch, error) {
		capturedExtra = extra
		return models.WorkloadProfileMatch{Class: models.ClassBatchScript}, nil
	})

	InferRequirements("process data", InferRequirementsOptions{
		Classifier:   mc,
		ExtraContext: "node=medulla",
	})

	if capturedExtra != "node=medulla" {
		t.Errorf("extraContext = %q, want %q", capturedExtra, "node=medulla")
	}
}

func TestInferRequirements_WithPrecomputedMatch_SkipsClassifier(t *testing.T) {
	mc := &mockClassifier{
		result: models.WorkloadProfileMatch{Class: models.ClassGoBuild},
	}
	match := models.WorkloadProfileMatch{
		Class: models.ClassDockerBuild,
		Notes: []string{"precomputed"},
	}

	reqs := InferRequirements("containerize the application", InferRequirementsOptions{
		Match:      &match,
		Classifier: mc,
	})

	if mc.calls != 0 {
		t.Fatalf("classifier called %d times, want 0", mc.calls)
	}
	if reqs.Workload.Class != models.ClassDockerBuild {
		t.Fatalf("class = %q, want %q", reqs.Workload.Class, models.ClassDockerBuild)
	}
	if len(reqs.Workload.Notes) != 1 || reqs.Workload.Notes[0] != "precomputed" {
		t.Fatalf("notes = %v, want precomputed marker", reqs.Workload.Notes)
	}
}

func TestInferRequirements_WithClassifier_ProfileStillApplied(t *testing.T) {
	// Even when the semantic classifier picks the class, the workload profile
	// (RAM floors, required tools, etc.) must still be applied via Apply().
	mc := &mockClassifier{
		result: models.WorkloadProfileMatch{Class: models.ClassLlamaServer},
	}

	reqs := InferRequirements("start the inference backend", InferRequirementsOptions{
		Classifier: mc,
	})

	if reqs.Workload.Class != models.ClassLlamaServer {
		t.Errorf("class = %q, want llama-server", reqs.Workload.Class)
	}
	// llama-server profile: MinFreeRAMMB=6144, RequiredTools=["llama-server"]
	if reqs.MinFreeRAMMB != 6144 {
		t.Errorf("MinFreeRAMMB = %d, want 6144 (from llama-server profile)", reqs.MinFreeRAMMB)
	}
	foundTool := false
	for _, tool := range reqs.RequiredTools {
		if tool == "llama-server" {
			foundTool = true
		}
	}
	if !foundTool {
		t.Errorf("llama-server tool not in RequiredTools, got %v", reqs.RequiredTools)
	}
}

func TestInferRequirements_WithClassifier_NoPanic(t *testing.T) {
	// resolveWorkloadMatch always passes context.Background() to the classifier,
	// so InferRequirements is context-free at its own call boundary.
	// This test documents that invariant and verifies no panic occurs.
	mc := &mockClassifier{
		result: models.WorkloadProfileMatch{Class: models.ClassGoBuild},
	}
	reqs := InferRequirements("go build ./cmd/...", InferRequirementsOptions{
		Classifier: mc,
	})
	if reqs.Workload.Class != models.ClassGoBuild {
		t.Errorf("class = %q, want go-build", reqs.Workload.Class)
	}
	if mc.calls != 1 {
		t.Errorf("classifier called %d times, want 1", mc.calls)
	}
}

// classifierFunc is an adapter that lets a plain function implement Classifier.
type classifierFunc func(ctx context.Context, prompt, extraContext string) (models.WorkloadProfileMatch, error)

func (f classifierFunc) ClassifyWorkload(ctx context.Context, prompt, extra string) (models.WorkloadProfileMatch, error) {
	return f(ctx, prompt, extra)
}

// --- Backend inference ---

func TestInferBackends(t *testing.T) {
	t.Run("mlx keyword", func(t *testing.T) {
		reqs := models.TaskRequirements{Description: "run on mlx"}
		InferBackends("run on mlx", &reqs)
		if len(reqs.PreferredBackends) == 0 || reqs.PreferredBackends[0] != "mlx" {
			t.Errorf("expected mlx backend, got %v", reqs.PreferredBackends)
		}
	})

	t.Run("apple silicon keyword", func(t *testing.T) {
		reqs := models.TaskRequirements{}
		InferBackends("deploy to apple silicon", &reqs)
		if len(reqs.PreferredBackends) == 0 || reqs.PreferredBackends[0] != "mlx" {
			t.Errorf("expected mlx backend for apple silicon, got %v", reqs.PreferredBackends)
		}
	})

	t.Run("no duplicate backends", func(t *testing.T) {
		reqs := models.TaskRequirements{PreferredBackends: []string{"mlx"}}
		InferBackends("run on mlx apple silicon", &reqs)
		count := 0
		for _, b := range reqs.PreferredBackends {
			if b == "mlx" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("expected 1 mlx backend, got %d in %v", count, reqs.PreferredBackends)
		}
	})
}
