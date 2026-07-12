package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/llmrouter"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"gopkg.in/yaml.v3"
)

func init() {
	llmWarmupDelay = 0
}

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
	prevResolveNode := llmResolveLocalNodeName

	loadLLMConfig = config.Load
	llmConfigPath = config.DefaultConfigPath
	buildLLMRegistry = llmrouter.NewRegistryFromConfig
	selectLLMCloudFallback = llmrouter.SelectCloudFallback
	llmClassifyWithProvider = llmrouter.ClassifyWithProvider
	confirmLLMCloudFallback = defaultConfirmLLMCloudFallback
	llmResolveLocalNodeName = func(context.Context) string { return "local" }

	return func() {
		loadLLMConfig = prevLoad
		llmConfigPath = prevPath
		buildLLMRegistry = prevBuild
		selectLLMCloudFallback = prevSelect
		llmClassifyWithProvider = prevClassify
		confirmLLMCloudFallback = prevConfirm
		llmResolveLocalNodeName = prevResolveNode
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

	result := maybeLLMCloudFallback(context.Background(), "go build ./...", current, strings.NewReader("YES\n"), &bytes.Buffer{}, "granite3.1-moe:1b")
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

	result := maybeLLMCloudFallback(context.Background(), "review repo", current, strings.NewReader("YES\n"), &bytes.Buffer{}, "granite3.1-moe:1b")
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

func TestLLMSelectCmdNonInteractive(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	t.Setenv("USERPROFILE", tempDir)
	tempConfigPath := filepath.Join(tempDir, "nodes.yaml")

	initialConfig := `
nodes:
  - name: local
    hostname: localhost
    ssh_user: me
`
	if err := os.WriteFile(tempConfigPath, []byte(initialConfig), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	prevPath := llmConfigPath
	llmConfigPath = func() string { return tempConfigPath }
	defer func() { llmConfigPath = prevPath }()

	prevTerm := llmIsTerminal
	llmIsTerminal = func(fd int) bool { return false }
	defer func() { llmIsTerminal = prevTerm }()

	prevStatusRuntime := loadStatusRuntime
	loadStatusRuntime = func(ctx context.Context) (*runtimectx.Context, error) {
		return &runtimectx.Context{
			Snapshot: &models.ClusterSnapshot{},
		}, nil
	}
	defer func() { loadStatusRuntime = prevStatusRuntime }()

	cmd := llmSelectCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("execute select command: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Available models for task routing:") {
		t.Fatalf("unexpected non-interactive output:\n%s", out)
	}
	if !strings.Contains(out, "google:gemini-2.5-pro") {
		t.Fatalf("expected gemini fallback model in output, got:\n%s", out)
	}
}

func TestLLMSelectCmdInteractive(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	t.Setenv("USERPROFILE", tempDir)
	tempConfigPath := filepath.Join(tempDir, "nodes.yaml")

	initialConfig := `
nodes:
  - name: local
    hostname: localhost
    ssh_user: me
`
	if err := os.WriteFile(tempConfigPath, []byte(initialConfig), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	prevPath := llmConfigPath
	llmConfigPath = func() string { return tempConfigPath }
	defer func() { llmConfigPath = prevPath }()

	prevTerm := llmIsTerminal
	llmIsTerminal = func(fd int) bool { return true }
	defer func() { llmIsTerminal = prevTerm }()

	prevSelect := llmSelectModelInteractive
	llmSelectModelInteractive = func(w io.Writer, in io.Reader, options []string) (int, error) {
		// Mock picking the first option
		return 0, nil
	}
	defer func() { llmSelectModelInteractive = prevSelect }()

	prevStatusRuntime := loadStatusRuntime
	loadStatusRuntime = func(ctx context.Context) (*runtimectx.Context, error) {
		return &runtimectx.Context{
			Snapshot: &models.ClusterSnapshot{},
		}, nil
	}
	defer func() { loadStatusRuntime = prevStatusRuntime }()

	cmd := llmSelectCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("execute select command: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Selected active model: google:gemini-2.5-pro") {
		t.Fatalf("unexpected output:\n%s", out)
	}

	// Verify it wrote the default model back to nodes.yaml
	cfg, err := loadLLMConfig(tempConfigPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Chat == nil || cfg.Chat.DefaultModel != "google:gemini-2.5-pro" {
		t.Fatalf("expected Chat.DefaultModel to be 'google:gemini-2.5-pro', got %v", cfg.Chat)
	}
}

func TestLLMFallbackTransitionAndAlerts(t *testing.T) {
	var buf bytes.Buffer

	// Stub writer terminal state
	isWriterTerm := false
	prevWriterTerm := llmWriterIsTerminal
	llmWriterIsTerminal = func(w io.Writer) bool { return isWriterTerm }
	defer func() { llmWriterIsTerminal = prevWriterTerm }()

	// Test non-terminal path: should instantly output OOM Alert.
	isWriterTerm = false

	showWarmupAndOOMAlert(&buf, "llama3", "NixOS")
	got := buf.String()
	if !strings.Contains(got, "⚠️  OOM Alert: NixOS RAM limit exceeded.") {
		t.Errorf("expected OOM Alert in output, got %q", got)
	}

	// Test terminal path animation.
	buf.Reset()
	isWriterTerm = true

	prevDelay := llmWarmupDelay
	llmWarmupDelay = 1 * time.Millisecond
	defer func() { llmWarmupDelay = prevDelay }()

	showWarmupAndOOMAlert(&buf, "llama3", "NixOS")
	gotInteractive := buf.String()
	if !strings.Contains(gotInteractive, "🔄 Warm-up:") {
		t.Errorf("expected Warm-up spinner in output, got %q", gotInteractive)
	}
	if !strings.Contains(gotInteractive, "⚠️  OOM Alert: NixOS RAM limit exceeded.") {
		t.Errorf("expected OOM Alert in output, got %q", gotInteractive)
	}
}

type lineByLineReader struct {
	lines []string
	idx   int
}

func (r *lineByLineReader) Read(p []byte) (n int, err error) {
	if r.idx >= len(r.lines) {
		return 0, io.EOF
	}
	line := r.lines[r.idx] + "\n"
	r.idx++
	n = copy(p, line)
	return n, nil
}

func TestLLMConfigureCmd_CloudProviderSecretFile(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	t.Setenv("USERPROFILE", tempDir)

	tempConfigPath := filepath.Join(tempDir, ".axis", "nodes.yaml")
	if err := os.MkdirAll(filepath.Dir(tempConfigPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	initialConfig := `# This is a comment at the top of nodes.yaml
nodes:
  - name: local
    hostname: localhost
    ssh_user: me # user comment
`
	if err := os.WriteFile(tempConfigPath, []byte(initialConfig), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Stub buildLLMRegistry
	prevBuildRegistry := buildLLMRegistry
	buildLLMRegistry = func(cfg *config.Config) (*llmrouter.Registry, error) {
		reg := llmrouter.NewRegistry()
		reg.Register(&llmTestProvider{name: "openrouter"})
		return reg, nil
	}
	defer func() { buildLLMRegistry = prevBuildRegistry }()

	cmd := llmConfigureCmd()
	cmd.SetArgs([]string{"openrouter"})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	// Simulated user input:
	// 1. Enter provider type: cloud
	// 2. Enable this provider?: y
	// 3. Enter provider priority: 60
	// 4. Enter cloud provider endpoint: (blank)
	// 5. Enter API key value: my-super-secret-key
	// 6. Enter option: 1
	reader := &lineByLineReader{
		lines: []string{"cloud", "y", "60", "", "my-super-secret-key", "1"},
	}
	cmd.SetIn(reader)

	// Run command
	err := cmd.ExecuteContext(context.Background())
	if err != nil {
		t.Fatalf("execute configure command: %v\nstderr: %s", err, stderr.String())
	}

	// Verify backup was created
	backupPath := tempConfigPath + ".bak"
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		t.Errorf("expected backup file %s to be created", backupPath)
	}

	// Verify secrets file was created
	secretsFile := filepath.Join(tempDir, ".axis", "secrets", "openrouter.key")
	if _, err := os.Stat(secretsFile); os.IsNotExist(err) {
		t.Fatalf("expected secrets file %s to be created", secretsFile)
	}
	secBytes, err := os.ReadFile(secretsFile)
	if err != nil {
		t.Fatalf("read secrets file: %v", err)
	}
	if string(secBytes) != "my-super-secret-key" {
		t.Errorf("expected secret content 'my-super-secret-key', got %q", string(secBytes))
	}

	// Check file permissions of secrets file
	info, err := os.Stat(secretsFile)
	if err != nil {
		t.Fatalf("stat secrets file: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("expected secret file permission 0600, got %o", perm)
	}

	// Verify updated config
	updatedBytes, err := os.ReadFile(tempConfigPath)
	if err != nil {
		t.Fatalf("read updated config: %v", err)
	}
	updatedContent := string(updatedBytes)

	if !strings.Contains(updatedContent, "# This is a comment at the top of nodes.yaml") {
		t.Error("expected top comment to be preserved")
	}
	if !strings.Contains(updatedContent, "# user comment") {
		t.Error("expected inline user comment to be preserved")
	}
	if strings.Contains(updatedContent, "my-super-secret-key") {
		t.Error("expected raw API key value to NOT be stored in nodes.yaml")
	}
	if !strings.Contains(updatedContent, "api_key_file:") {
		t.Error("expected api_key_file reference in updated nodes.yaml")
	}
}

func TestLLMConfigureCmd_CloudProviderEnvVar(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	t.Setenv("USERPROFILE", tempDir)

	tempConfigPath := filepath.Join(tempDir, ".axis", "nodes.yaml")
	if err := os.MkdirAll(filepath.Dir(tempConfigPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	initialConfig := `nodes:
  - name: local
    hostname: localhost
    ssh_user: me
`
	if err := os.WriteFile(tempConfigPath, []byte(initialConfig), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Stub buildLLMRegistry
	prevBuildRegistry := buildLLMRegistry
	buildLLMRegistry = func(cfg *config.Config) (*llmrouter.Registry, error) {
		reg := llmrouter.NewRegistry()
		reg.Register(&llmTestProvider{name: "openrouter"})
		return reg, nil
	}
	defer func() { buildLLMRegistry = prevBuildRegistry }()

	cmd := llmConfigureCmd()
	cmd.SetArgs([]string{"openrouter"})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	// Simulated user input:
	// 1. Enter provider type: cloud
	// 2. Enable this provider?: y
	// 3. Enter provider priority: 60
	// 4. Enter cloud provider endpoint: (blank)
	// 5. Enter API key value: my-secret
	// 6. Enter option: 2
	// 7. Enter environment variable name: MY_OPENAI_API_KEY
	reader := &lineByLineReader{
		lines: []string{"cloud", "y", "60", "", "my-secret", "2", "MY_OPENAI_API_KEY"},
	}
	cmd.SetIn(reader)

	// Run command
	err := cmd.ExecuteContext(context.Background())
	if err != nil {
		t.Fatalf("execute configure command: %v\nstderr: %s", err, stderr.String())
	}

	// Verify updated config
	updatedBytes, err := os.ReadFile(tempConfigPath)
	if err != nil {
		t.Fatalf("read updated config: %v", err)
	}
	updatedContent := string(updatedBytes)

	if strings.Contains(updatedContent, "my-secret") {
		t.Error("expected raw API key value to NOT be stored in nodes.yaml")
	}
	if !strings.Contains(updatedContent, "api_key_env: MY_OPENAI_API_KEY") {
		t.Error("expected api_key_env reference in updated nodes.yaml")
	}
}

func TestWriteKeyFileSecurelyRejectsSymlink(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "real.key")
	link := filepath.Join(tmp, "link.key")
	if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
		t.Fatalf("write target key: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	_, err := writeKeyFileSecurely(link, []byte("secret"), strings.NewReader("yes\n"), io.Discard)
	if err == nil {
		t.Fatal("expected error for symlink key path")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink rejection, got: %v", err)
	}
}

func TestWriteConfigSafelyDetectsConcurrentModification(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "nodes.yaml")
	initial := "nodes:\n  - name: local\n    hostname: localhost\n    ssh_user: me\n"
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	hash, err := computeFileHash(path)
	if err != nil {
		t.Fatalf("compute hash: %v", err)
	}

	// Simulate a concurrent edit.
	modified := "nodes:\n  - name: other\n    hostname: localhost\n    ssh_user: me\n"
	if err := os.WriteFile(path, []byte(modified), 0600); err != nil {
		t.Fatalf("modify config: %v", err)
	}

	rootNode := yaml.Node{
		Kind: yaml.DocumentNode,
		Content: []*yaml.Node{
			{Kind: yaml.MappingNode, Tag: "!!map"},
		},
	}
	if err := writeConfigSafely(path, &rootNode, hash); err == nil {
		t.Fatal("expected concurrent modification error")
	} else if !strings.Contains(err.Error(), "concurrent modification") {
		t.Fatalf("expected concurrent modification message, got: %v", err)
	}
}

func TestLLMConfigureCmd_RollbackNewKeyOnConfigFailure(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	t.Setenv("USERPROFILE", tempDir)

	tempConfigPath := filepath.Join(tempDir, ".axis", "nodes.yaml")
	if err := os.MkdirAll(filepath.Dir(tempConfigPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	initialConfig := "nodes:\n  - name: local\n    hostname: localhost\n    ssh_user: me\n"
	if err := os.WriteFile(tempConfigPath, []byte(initialConfig), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	prevBuildRegistry := buildLLMRegistry
	buildLLMRegistry = func(cfg *config.Config) (*llmrouter.Registry, error) {
		reg := llmrouter.NewRegistry()
		reg.Register(&llmTestProvider{name: "openrouter"})
		return reg, nil
	}
	defer func() { buildLLMRegistry = prevBuildRegistry }()

	prevLoad := loadLLMConfig
	loadLLMConfig = func(path string) (*config.Config, error) {
		return nil, errors.New("forced validation failure")
	}
	defer func() { loadLLMConfig = prevLoad }()

	cmd := llmConfigureCmd()
	cmd.SetArgs([]string{"openrouter"})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	reader := &lineByLineReader{lines: []string{"cloud", "y", "60", "", "my-secret-key", "1"}}
	cmd.SetIn(reader)

	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected configure command to fail")
	}
	if !strings.Contains(stderr.String(), "forced validation failure") {
		t.Fatalf("expected validation failure in stderr, got: %s", stderr.String())
	}

	secretsFile := filepath.Join(tempDir, ".axis", "secrets", "openrouter.key")
	if _, statErr := os.Stat(secretsFile); !os.IsNotExist(statErr) {
		t.Fatalf("expected newly created key file to be rolled back, but it exists")
	}
}

func TestLLMConfigureCmd_PreexistingKeySurvivesConfigFailure(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	t.Setenv("USERPROFILE", tempDir)

	tempConfigPath := filepath.Join(tempDir, ".axis", "nodes.yaml")
	secretsDir := filepath.Join(tempDir, ".axis", "secrets")
	if err := os.MkdirAll(filepath.Dir(tempConfigPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	initialConfig := "nodes:\n  - name: local\n    hostname: localhost\n    ssh_user: me\n"
	if err := os.WriteFile(tempConfigPath, []byte(initialConfig), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	preexistingKey := filepath.Join(secretsDir, "openrouter.key")
	if err := os.WriteFile(preexistingKey, []byte("old-key"), 0600); err != nil {
		t.Fatalf("write preexisting key: %v", err)
	}

	prevBuildRegistry := buildLLMRegistry
	buildLLMRegistry = func(cfg *config.Config) (*llmrouter.Registry, error) {
		reg := llmrouter.NewRegistry()
		reg.Register(&llmTestProvider{name: "openrouter"})
		return reg, nil
	}
	defer func() { buildLLMRegistry = prevBuildRegistry }()

	prevLoad := loadLLMConfig
	loadLLMConfig = func(path string) (*config.Config, error) {
		return nil, errors.New("forced validation failure")
	}
	defer func() { loadLLMConfig = prevLoad }()

	cmd := llmConfigureCmd()
	cmd.SetArgs([]string{"openrouter"})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	reader := &lineByLineReader{lines: []string{"cloud", "y", "60", "", "my-secret-key", "1", "yes"}}
	cmd.SetIn(reader)

	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected configure command to fail")
	}

	if _, statErr := os.Stat(preexistingKey); os.IsNotExist(statErr) {
		t.Fatal("expected pre-existing key file to survive failure")
	}
	content, readErr := os.ReadFile(preexistingKey)
	if readErr != nil {
		t.Fatalf("read preexisting key: %v", readErr)
	}
	if string(content) != "my-secret-key" {
		t.Fatalf("expected key to contain new content after overwrite, got %q", string(content))
	}
}

func TestMigrateASTProvidersInfersKind(t *testing.T) {
	root := yaml.Node{
		Kind: yaml.DocumentNode,
		Content: []*yaml.Node{
			{
				Kind: yaml.MappingNode,
				Tag:  "!!map",
				Content: []*yaml.Node{
					{Kind: yaml.ScalarNode, Value: "ai_providers"},
					{
						Kind: yaml.MappingNode,
						Tag:  "!!map",
						Content: []*yaml.Node{
							{Kind: yaml.ScalarNode, Value: "openrouter"},
							{
								Kind: yaml.MappingNode,
								Tag:  "!!map",
								Content: []*yaml.Node{
									{Kind: yaml.ScalarNode, Value: "type"},
									{Kind: yaml.ScalarNode, Value: "cloud"},
								},
							},
						},
					},
				},
			},
		},
	}

	migrateASTProviders(&root)

	providers := root.Content[0].Content[1]
	openrouter := providers.Content[1]
	for i := 0; i < len(openrouter.Content); i += 2 {
		if openrouter.Content[i].Value == "kind" {
			if openrouter.Content[i+1].Value != "openrouter" {
				t.Fatalf("expected kind openrouter, got %q", openrouter.Content[i+1].Value)
			}
			return
		}
	}
	t.Fatal("expected kind key to be added to provider")
}

func TestMigrateASTProvidersLeavesExistingKind(t *testing.T) {
	root := yaml.Node{
		Kind: yaml.DocumentNode,
		Content: []*yaml.Node{
			{
				Kind: yaml.MappingNode,
				Tag:  "!!map",
				Content: []*yaml.Node{
					{Kind: yaml.ScalarNode, Value: "ai_providers"},
					{
						Kind: yaml.MappingNode,
						Tag:  "!!map",
						Content: []*yaml.Node{
							{Kind: yaml.ScalarNode, Value: "openrouter"},
							{
								Kind: yaml.MappingNode,
								Tag:  "!!map",
								Content: []*yaml.Node{
									{Kind: yaml.ScalarNode, Value: "type"},
									{Kind: yaml.ScalarNode, Value: "cloud"},
									{Kind: yaml.ScalarNode, Value: "kind"},
									{Kind: yaml.ScalarNode, Value: "anthropic"},
								},
							},
						},
					},
				},
			},
		},
	}

	migrateASTProviders(&root)

	providers := root.Content[0].Content[1]
	openrouter := providers.Content[1]
	for i := 0; i < len(openrouter.Content); i += 2 {
		if openrouter.Content[i].Value == "kind" {
			if openrouter.Content[i+1].Value != "anthropic" {
				t.Fatalf("expected existing kind to be preserved, got %q", openrouter.Content[i+1].Value)
			}
			return
		}
	}
	t.Fatal("expected kind key to remain")
}
