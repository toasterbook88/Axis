package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/llmrouter"
	"github.com/toasterbook88/axis/internal/models"
)

// stubLLMClassify replaces llmClassifyFn for the duration of a test.
func stubLLMClassify(t *testing.T, class models.WorkloadClass, sig llmrouter.IntentSignal) func() {
	t.Helper()
	prev := llmClassifyFn
	llmClassifyFn = func(_ context.Context, _ *llmrouter.Engine, _, _ string) (models.WorkloadClass, llmrouter.IntentSignal, error) {
		return class, sig, nil
	}
	return func() { llmClassifyFn = prev }
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
	restore := stubLLMClassify(t, models.ClassGoBuild, llmrouter.IntentSignal{
		Class:      models.ClassGoBuild,
		Confidence: 0.95,
		Source:     llmrouter.SourceSemantic,
		Signals:    []string{"go", "build"},
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
	restore := stubLLMClassify(t, models.ClassDockerBuild, llmrouter.IntentSignal{
		Class:      models.ClassDockerBuild,
		Confidence: 1.0,
		Source:     llmrouter.SourceReflex,
		Notes:      []string{"semantic fallback: ollama unreachable"},
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
	restore := stubLLMClassify(t, models.ClassBatchScript, llmrouter.IntentSignal{
		Class:      models.ClassBatchScript,
		Confidence: 0.80,
		Source:     llmrouter.SourceSemantic,
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
	restore := stubLLMClassify(t, models.ClassRepoAnalysis, llmrouter.IntentSignal{
		Class:      models.ClassRepoAnalysis,
		Confidence: 0.91,
		Source:     llmrouter.SourceSemantic,
		Signals:    []string{"review", "codebase"},
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
	restore := stubLLMClassify(t, models.ClassLlamaServer, llmrouter.IntentSignal{
		Class:      models.ClassLlamaServer,
		Confidence: 0.88,
		Source:     llmrouter.SourceSemantic,
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
	restore := stubLLMClassify(t, models.ClassUnknown, llmrouter.IntentSignal{
		Class:      models.ClassUnknown,
		Confidence: 0.55,
		Source:     llmrouter.SourceSemantic,
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
