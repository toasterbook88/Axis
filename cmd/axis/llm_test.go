package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/llmrouter"
	"github.com/toasterbook88/axis/internal/models"
)

type llmTestProvider struct {
	name string
}

func (p *llmTestProvider) Name() string { return p.name }

func (p *llmTestProvider) Type() llmrouter.ProviderType { return llmrouter.ProviderCloud }

func (p *llmTestProvider) Health(context.Context) (llmrouter.HealthStatus, error) {
	return llmrouter.HealthStatus{OK: true}, nil
}

func (p *llmTestProvider) SupportsModel(string) bool { return true }

func (p *llmTestProvider) EstimateCost(string, string) float64 { return 0.001 }

func (p *llmTestProvider) Send(context.Context, string, string) (llmrouter.GenerateResult, error) {
	return llmrouter.GenerateResult{}, nil
}

// stubLLMInferRequirements replaces llmInferRequirementsFn for the duration of a test.
func stubLLMInferRequirements(t *testing.T, result llmInferenceResult) func() {
	t.Helper()
	prev := llmInferRequirementsFn
	llmInferRequirementsFn = func(_ string, _ *llmrouter.Engine) llmInferenceResult {
		return result
	}
	return func() { llmInferRequirementsFn = prev }
}

func stubLLMCloudFallbacks(t *testing.T) func() {
	t.Helper()

	prevLoad := loadLLMConfig
	prevPath := llmConfigPath
	prevBuild := buildLLMRegistry
	prevSelect := selectLLMCloudFallback
	prevClassify := llmClassifyWithProvider
	prevConfirm := confirmLLMCloudFallback

	loadLLMConfig = config.Load
	llmConfigPath = config.DefaultConfigPath
	buildLLMRegistry = llmrouter.NewRegistryFromConfig
	selectLLMCloudFallback = llmrouter.SelectCloudFallback
	llmClassifyWithProvider = llmrouter.ClassifyWithProvider
	confirmLLMCloudFallback = defaultConfirmLLMCloudFallback

	return func() {
		loadLLMConfig = prevLoad
		llmConfigPath = prevPath
		buildLLMRegistry = prevBuild
		selectLLMCloudFallback = prevSelect
		llmClassifyWithProvider = prevClassify
		confirmLLMCloudFallback = prevConfirm
	}
}

// --- Surface tests ---

func TestLLMCmdSurface(t *testing.T) {
	cmd := llmCmd()
	if got := cmd.Name(); got != "llm" {
		t.Fatalf("llmCmd name = %q, want llm", got)
	}

	// Verify required flags exist.
	required := []string{"model", "endpoint", "timeout", "format", "dry-run"}
	for _, flag := range required {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("llmCmd missing flag --%s", flag)
		}
	}
}

func TestLLMCmdRegisteredOnRoot(t *testing.T) {
	root := newRootCmd()
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "llm" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("llm command not registered on root")
	}
}

func TestLLMCmdRequiresPrompt(t *testing.T) {
	cmd := llmCmd()
	cmd.SetArgs(nil)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when no prompt provided, got nil")
	}
}

// --- Text output ---

func TestLLMCmd_TextOutput_SemanticResult(t *testing.T) {
	restore := stubLLMInferRequirements(t, llmInferenceResult{
		reqs: models.TaskRequirements{
			Workload: models.WorkloadProfileMatch{
				Class: models.ClassGoBuild,
			},
		},
		sig: llmrouter.IntentSignal{
			Class:      models.ClassGoBuild,
			Confidence: 0.95,
			Source:     llmrouter.SourceSemantic,
			Signals:    []string{"go", "build"},
		},
	})
	defer restore()

	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := llmCmd()
		cmd.SetArgs([]string{"go build the binary"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("llm Execute: %v", err)
	}

	// Class and confidence must appear.
	if !strings.Contains(stdout, "go-build") {
		t.Errorf("expected class go-build in output, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "0.95") {
		t.Errorf("expected confidence 0.95 in output, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "semantic") {
		t.Errorf("expected source 'semantic' in output, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "go, build") {
		t.Errorf("expected signals 'go, build' in output, got:\n%s", stdout)
	}
	// Advisory note on stderr.
	if !strings.Contains(stderr, "advisory") {
		t.Errorf("expected advisory note on stderr, got:\n%s", stderr)
	}
}

func TestLLMCmd_TextOutput_ReflexFallback(t *testing.T) {
	restore := stubLLMInferRequirements(t, llmInferenceResult{
		reqs: models.TaskRequirements{
			Workload: models.WorkloadProfileMatch{
				Class: models.ClassDockerBuild,
			},
		},
		sig: llmrouter.IntentSignal{
			Class:      models.ClassDockerBuild,
			Confidence: 1.0,
			Source:     llmrouter.SourceReflex,
			Notes:      []string{"semantic fallback: ollama unreachable"},
		},
	})
	defer restore()

	stdout, _, err := captureProcessOutput(t, func() error {
		cmd := llmCmd()
		cmd.SetArgs([]string{"docker build -t myapp ."})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("llm Execute: %v", err)
	}

	if !strings.Contains(stdout, "docker-build") {
		t.Errorf("expected class docker-build in output, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "reflex") {
		t.Errorf("expected 'reflex' source in output, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "ollama unreachable") {
		t.Errorf("expected fallback note in output, got:\n%s", stdout)
	}
}

func TestLLMCmd_TextOutput_DryRunLabel(t *testing.T) {
	restore := stubLLMInferRequirements(t, llmInferenceResult{
		reqs: models.TaskRequirements{
			Workload: models.WorkloadProfileMatch{
				Class: models.ClassBatchScript,
			},
		},
		sig: llmrouter.IntentSignal{
			Class:      models.ClassBatchScript,
			Confidence: 0.80,
			Source:     llmrouter.SourceSemantic,
		},
	})
	defer restore()

	_, stderr, err := captureProcessOutput(t, func() error {
		cmd := llmCmd()
		cmd.SetArgs([]string{"--dry-run", "process the dataset"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("llm Execute: %v", err)
	}
	if !strings.Contains(stderr, "dry-run") {
		t.Errorf("expected dry-run label on stderr, got:\n%s", stderr)
	}
}

// --- JSON output ---

func TestLLMCmd_JSONOutput(t *testing.T) {
	restore := stubLLMInferRequirements(t, llmInferenceResult{
		reqs: models.TaskRequirements{
			Workload: models.WorkloadProfileMatch{
				Class: models.ClassRepoAnalysis,
			},
		},
		sig: llmrouter.IntentSignal{
			Class:      models.ClassRepoAnalysis,
			Confidence: 0.91,
			Source:     llmrouter.SourceSemantic,
			Signals:    []string{"review", "codebase"},
		},
	})
	defer restore()

	stdout, _, err := captureProcessOutput(t, func() error {
		cmd := llmCmd()
		cmd.SetArgs([]string{"--format", "json", "review the codebase"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("llm Execute: %v", err)
	}

	var result llmResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &result); err != nil {
		t.Fatalf("JSON unmarshal failed: %v\noutput:\n%s", err, stdout)
	}
	if result.Class != "repo-analysis" {
		t.Errorf("class = %q, want repo-analysis", result.Class)
	}
	if result.Confidence != 0.91 {
		t.Errorf("confidence = %v, want 0.91", result.Confidence)
	}
	if result.Source != "semantic" {
		t.Errorf("source = %q, want semantic", result.Source)
	}
	if result.Prompt != "review the codebase" {
		t.Errorf("prompt = %q, want 'review the codebase'", result.Prompt)
	}
}

// --- Requirements propagation ---

func TestLLMCmd_RequirementsInOutput(t *testing.T) {
	// When classifier returns llama-server, the profile (6144 MB, llama-server tool)
	// should appear in the JSON output — verifying the placement seam fires.
	restore := stubLLMInferRequirements(t, llmInferenceResult{
		reqs: models.TaskRequirements{
			Workload: models.WorkloadProfileMatch{
				Class: models.ClassLlamaServer,
			},
			MinFreeRAMMB:  6144,
			RequiredTools: []string{"llama-server"},
		},
		sig: llmrouter.IntentSignal{
			Class:      models.ClassLlamaServer,
			Confidence: 0.88,
			Source:     llmrouter.SourceSemantic,
		},
	})
	defer restore()

	stdout, _, err := captureProcessOutput(t, func() error {
		cmd := llmCmd()
		cmd.SetArgs([]string{"--format", "json", "start the llama server"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("llm Execute: %v", err)
	}

	var result llmResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &result); err != nil {
		t.Fatalf("JSON unmarshal: %v\noutput:\n%s", err, stdout)
	}
	if result.Requirements.MinFreeRAMMB != 6144 {
		t.Errorf("MinFreeRAMMB = %d, want 6144 (llama-server profile)", result.Requirements.MinFreeRAMMB)
	}
	foundTool := false
	for _, tool := range result.Requirements.RequiredTools {
		if tool == "llama-server" {
			foundTool = true
		}
	}
	if !foundTool {
		t.Errorf("llama-server tool not in requirements, got %v", result.Requirements.RequiredTools)
	}
}

func TestLLMCmd_UnknownClass_NoRequirements(t *testing.T) {
	// unknown class → zero RAM, no tools in the profile.
	restore := stubLLMInferRequirements(t, llmInferenceResult{
		reqs: models.TaskRequirements{
			Workload: models.WorkloadProfileMatch{
				Class: models.ClassUnknown,
			},
		},
		sig: llmrouter.IntentSignal{
			Class:      models.ClassUnknown,
			Confidence: 0.55,
			Source:     llmrouter.SourceSemantic,
		},
	})
	defer restore()

	stdout, _, err := captureProcessOutput(t, func() error {
		cmd := llmCmd()
		cmd.SetArgs([]string{"--format", "json", "play some music"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("llm Execute: %v", err)
	}

	var result llmResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &result); err != nil {
		t.Fatalf("JSON unmarshal: %v\noutput:\n%s", err, stdout)
	}
	if result.Class != "unknown" {
		t.Errorf("class = %q, want unknown", result.Class)
	}
	if result.Requirements.MinFreeRAMMB != 0 {
		t.Errorf("MinFreeRAMMB = %d, want 0 for unknown class", result.Requirements.MinFreeRAMMB)
	}
}

func TestTruncate_UTF8SafeAndSmallLimits(t *testing.T) {
	if got := truncate("hello", 0); got != "…" {
		t.Fatalf("truncate max=0 = %q, want %q", got, "…")
	}
	if got := truncate("🙂🙂🙂", 2); got != "🙂…" {
		t.Fatalf("truncate utf8 = %q, want %q", got, "🙂…")
	}
}

func TestMaybeLLMCloudFallbackUpgradesReflexResult(t *testing.T) {
	restore := stubLLMCloudFallbacks(t)
	defer restore()

	loadLLMConfig = func(string) (*config.Config, error) {
		return &config.Config{
			Nodes: []config.NodeConfig{{Name: "local", Hostname: "localhost", SSHUser: "me"}},
			Inference: &config.InferenceConfig{
				Prefer: "cost",
			},
		}, nil
	}
	provider := &llmTestProvider{name: "openrouter"}
	buildLLMRegistry = func(*config.Config) (*llmrouter.Registry, error) {
		registry := llmrouter.NewRegistry()
		registry.MustRegister(provider)
		return registry, nil
	}
	selectLLMCloudFallback = func(_ context.Context, _ *llmrouter.Registry, _ string, prefer string) (llmrouter.Provider, llmrouter.RoutingDecision, error) {
		if prefer != "cost" {
			t.Fatalf("prefer = %q, want cost", prefer)
		}
		return provider, llmrouter.RoutingDecision{
			Provider:   "openrouter",
			Model:      "openai/gpt-4o-mini",
			EstCost:    0.002,
			EstLatency: "35ms",
		}, nil
	}
	confirmLLMCloudFallback = func(_ io.Reader, _ io.Writer, decision llmrouter.RoutingDecision) bool {
		return decision.Provider == "openrouter"
	}
	llmClassifyWithProvider = func(_ context.Context, _ llmrouter.Provider, _ string, model string) (models.WorkloadClass, llmrouter.IntentSignal, error) {
		if model != "openai/gpt-4o-mini" {
			t.Fatalf("model = %q, want openai/gpt-4o-mini", model)
		}
		return models.ClassGoBuild, llmrouter.IntentSignal{
			Class:      models.ClassGoBuild,
			Confidence: 0.88,
			Source:     llmrouter.SourceSemantic,
			Signals:    []string{"go"},
		}, nil
	}

	current := llmInferenceResult{
		reqs: models.TaskRequirements{
			Description: "go build ./...",
			Workload: models.WorkloadProfileMatch{
				Class: models.ClassUnknown,
				Notes: []string{"semantic fallback: ollama unavailable"},
			},
		},
		sig: llmrouter.IntentSignal{
			Class:      models.ClassUnknown,
			Confidence: 1.0,
			Source:     llmrouter.SourceReflex,
			Notes:      []string{"semantic fallback: ollama unavailable"},
		},
	}

	result := maybeLLMCloudFallback(context.Background(), "go build ./...", current, strings.NewReader("YES\n"), &bytes.Buffer{})
	if result.sig.Source != llmrouter.SourceSemantic {
		t.Fatalf("source = %q, want semantic", result.sig.Source)
	}
	if result.reqs.Workload.Class != models.ClassGoBuild {
		t.Fatalf("class = %q, want go-build", result.reqs.Workload.Class)
	}
	if !strings.Contains(strings.Join(result.sig.Notes, " | "), "cloud fallback via openrouter/openai/gpt-4o-mini") {
		t.Fatalf("notes = %v, want cloud fallback note", result.sig.Notes)
	}
}

func TestMaybeLLMCloudFallbackHonorsCostCap(t *testing.T) {
	restore := stubLLMCloudFallbacks(t)
	defer restore()

	loadLLMConfig = func(string) (*config.Config, error) {
		return &config.Config{
			Nodes: []config.NodeConfig{{Name: "local", Hostname: "localhost", SSHUser: "me"}},
			Inference: &config.InferenceConfig{
				MaxCostPerRequest: 0.001,
			},
		}, nil
	}
	buildLLMRegistry = func(*config.Config) (*llmrouter.Registry, error) {
		registry := llmrouter.NewRegistry()
		registry.MustRegister(&llmTestProvider{name: "openrouter"})
		return registry, nil
	}
	selectLLMCloudFallback = func(context.Context, *llmrouter.Registry, string, string) (llmrouter.Provider, llmrouter.RoutingDecision, error) {
		return &llmTestProvider{name: "openrouter"}, llmrouter.RoutingDecision{
			Provider:   "openrouter",
			Model:      "openai/gpt-4o-mini",
			EstCost:    0.01,
			EstLatency: "35ms",
		}, nil
	}
	confirmCalled := false
	confirmLLMCloudFallback = func(io.Reader, io.Writer, llmrouter.RoutingDecision) bool {
		confirmCalled = true
		return true
	}

	current := llmInferenceResult{
		reqs: models.TaskRequirements{
			Workload: models.WorkloadProfileMatch{Class: models.ClassUnknown},
		},
		sig: llmrouter.IntentSignal{
			Class:      models.ClassUnknown,
			Source:     llmrouter.SourceReflex,
			Confidence: 1.0,
		},
	}

	result := maybeLLMCloudFallback(context.Background(), "review repo", current, strings.NewReader("YES\n"), &bytes.Buffer{})
	if confirmCalled {
		t.Fatal("confirmation should not run when cost cap blocks the call")
	}
	if result.sig.Source != llmrouter.SourceReflex {
		t.Fatalf("source = %q, want reflex", result.sig.Source)
	}
	if !strings.Contains(strings.Join(result.sig.Notes, " | "), "exceeds max") {
		t.Fatalf("notes = %v, want cost cap note", result.sig.Notes)
	}
}

func TestDefaultConfirmLLMCloudFallback(t *testing.T) {
	var stderr bytes.Buffer
	ok := defaultConfirmLLMCloudFallback(strings.NewReader("YES\n"), &stderr, llmrouter.RoutingDecision{
		Provider:   "groq",
		Model:      "llama-3.1-8b-instant",
		EstCost:    0.0015,
		EstLatency: "18ms",
	})
	if !ok {
		t.Fatal("confirmation = false, want true")
	}
	if !strings.Contains(stderr.String(), "cloud fallback required") {
		t.Fatalf("stderr = %q, want confirmation prompt", stderr.String())
	}
}
