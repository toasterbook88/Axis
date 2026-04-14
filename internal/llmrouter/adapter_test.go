package llmrouter_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/llmrouter"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/workload"
)

// TestEngine_ImplementsWorkloadClassifier is a runtime confirmation that
// *Engine satisfies workload.Classifier. The compile-time check in adapter.go
// (var _ workload.Classifier = (*Engine)(nil)) catches drift at build time;
// this test catches it in the test binary as well.
func TestEngine_ImplementsWorkloadClassifier(t *testing.T) {
	var _ workload.Classifier = llmrouter.NewEngine()
}

// TestClassifyWorkload_SemanticPath verifies that a successful Ollama response
// is converted correctly into a models.WorkloadProfileMatch.
func TestClassifyWorkload_SemanticPath(t *testing.T) {
	body := ollamaGenResp(t, map[string]any{
		"class":      "repo-analysis",
		"confidence": 0.93,
		"signals":    []string{"review", "codebase"},
	})
	srv, client := mockOllama(t, http.StatusOK, body)

	engine := llmrouter.NewEngine(
		llmrouter.WithEndpoint(srv.URL),
		llmrouter.WithHTTPClient(client),
		llmrouter.WithTimeout(2*time.Second),
	)

	match, err := engine.ClassifyWorkload(context.Background(), "review the codebase", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if match.Class != models.ClassRepoAnalysis {
		t.Errorf("class = %q, want %q", match.Class, models.ClassRepoAnalysis)
	}
}

// TestClassifyWorkload_ReflexFallback verifies that when Ollama is down,
// ClassifyWorkload still returns a valid match (via the internal reflex path)
// and a nil error — so workload.resolveWorkloadMatch always gets a usable result.
func TestClassifyWorkload_ReflexFallback(t *testing.T) {
	engine := llmrouter.NewEngine(
		llmrouter.WithEndpoint("http://127.0.0.1:1"), // always closed
		llmrouter.WithTimeout(30*time.Millisecond),
	)

	match, err := engine.ClassifyWorkload(context.Background(), "go build ./...", "")
	if err != nil {
		t.Fatalf("ClassifyWorkload must not surface errors to caller: %v", err)
	}
	if match.Class != models.ClassGoBuild {
		t.Errorf("reflex fallback class = %q, want %q", match.Class, models.ClassGoBuild)
	}
}

// TestClassifyWorkload_CancelledContext verifies that a pre-cancelled context
// causes ClassifyWorkload to return an error (so the caller can fall back to
// the legacy path in workload.resolveWorkloadMatch).
func TestClassifyWorkload_CancelledContext(t *testing.T) {
	engine := llmrouter.NewEngine()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := engine.ClassifyWorkload(ctx, "go test ./...", "")
	if err == nil {
		t.Error("expected error for pre-cancelled context, got nil")
	}
}

// TestClassifyWorkload_NotesPreserved verifies that Notes from IntentSignal
// (e.g. fallback reasons) are propagated into the WorkloadProfileMatch.Notes.
func TestClassifyWorkload_NotesPreserved(t *testing.T) {
	// Ollama returns unrecognised class → reflex fallback fires, Notes carries reason.
	body := ollamaGenResp(t, map[string]any{
		"class":      "web-scraping", // invalid
		"confidence": 0.5,
		"signals":    []string{},
	})
	srv, client := mockOllama(t, http.StatusOK, body)

	engine := llmrouter.NewEngine(
		llmrouter.WithEndpoint(srv.URL),
		llmrouter.WithHTTPClient(client),
		llmrouter.WithTimeout(2*time.Second),
	)

	match, err := engine.ClassifyWorkload(context.Background(), "go build ./...", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Reflex fallback was used (unrecognised class), Notes should carry the reason.
	if len(match.Notes) == 0 {
		t.Error("expected fallback reason in Notes, got none")
	}
}

// TestInferRequirements_IntegrationWithEngine is an end-to-end test that wires
// a real Engine (backed by a mock Ollama server) into workload.InferRequirements
// and verifies the full pipeline produces correct TaskRequirements.
func TestInferRequirements_IntegrationWithEngine(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"response": mustMarshal(t, map[string]any{
			"class":      "docker-build",
			"confidence": 0.97,
			"signals":    []string{"docker", "image"},
		}),
		"done": true,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	srv, client := mockOllama(t, http.StatusOK, body)
	engine := llmrouter.NewEngine(
		llmrouter.WithEndpoint(srv.URL),
		llmrouter.WithHTTPClient(client),
		llmrouter.WithTimeout(2*time.Second),
	)

	// Description alone would not normally trigger docker-build (ambiguous).
	// The semantic classifier tips it over.
	reqs := workload.InferRequirements("build the container image", workload.InferRequirementsOptions{
		Classifier: engine,
	})

	if reqs.Workload.Class != models.ClassDockerBuild {
		t.Errorf("class = %q, want docker-build", reqs.Workload.Class)
	}
	foundDocker := false
	for _, tool := range reqs.RequiredTools {
		if tool == "docker" {
			foundDocker = true
		}
	}
	if !foundDocker {
		t.Errorf("docker tool not applied from profile, tools=%v", reqs.RequiredTools)
	}
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("mustMarshal: %v", err)
	}
	return string(b)
}
