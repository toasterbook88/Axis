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
