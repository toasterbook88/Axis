package llmrouter_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/llmrouter"
	"github.com/toasterbook88/axis/internal/models"
)

// --- helpers ---

// ollamaGenResp wraps inner JSON (the classify payload) in the Ollama
// /api/generate envelope, matching the real wire format.
func ollamaGenResp(t *testing.T, inner any) []byte {
	t.Helper()
	innerBytes, err := json.Marshal(inner)
	if err != nil {
		t.Fatalf("marshal inner: %v", err)
	}
	envelope := map[string]any{
		"response": string(innerBytes),
		"done":     true,
	}
	b, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return b
}

// mockOllama starts a test HTTP server that returns a fixed response body.
func mockOllama(t *testing.T, status int, body []byte) (*httptest.Server, *http.Client) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, srv.Client()
}

// slowOllama starts a server that responds after delay, letting us test timeout
// enforcement without real network calls.
func slowOllama(t *testing.T, delay time.Duration) (*httptest.Server, *http.Client) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, srv.Client()
}

// --- semantic path tests ---

func TestClassify_SemanticPath(t *testing.T) {
	cases := []struct {
		name       string
		prompt     string
		modelClass string
		modelConf  float64
		wantClass  models.WorkloadClass
	}{
		{
			name:       "go build",
			prompt:     "compile and test the Go binary",
			modelClass: "go-build",
			modelConf:  0.95,
			wantClass:  models.ClassGoBuild,
		},
		{
			name:       "local llm inference",
			prompt:     "run ollama with llama3.1:8b",
			modelClass: "local-llm-inference",
			modelConf:  0.88,
			wantClass:  models.ClassLocalLLMInference,
		},
		{
			name:       "repo analysis",
			prompt:     "analyze the entire repository for dead code",
			modelClass: "repo-analysis",
			modelConf:  0.91,
			wantClass:  models.ClassRepoAnalysis,
		},
		{
			name:       "docker build",
			prompt:     "build the docker image and push",
			modelClass: "docker-build",
			modelConf:  0.97,
			wantClass:  models.ClassDockerBuild,
		},
		{
			name:       "long context inference",
			prompt:     "summarize this 200k token document",
			modelClass: "long-context-inference",
			modelConf:  0.82,
			wantClass:  models.ClassLongContextInference,
		},
		{
			name:       "unknown class",
			prompt:     "play some music",
			modelClass: "unknown",
			modelConf:  0.60,
			wantClass:  models.ClassUnknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := ollamaGenResp(t, map[string]any{
				"class":      tc.modelClass,
				"confidence": tc.modelConf,
				"signals":    []string{"test-signal"},
			})
			srv, client := mockOllama(t, http.StatusOK, body)

			engine := llmrouter.NewEngine(
				llmrouter.WithEndpoint(srv.URL),
				llmrouter.WithHTTPClient(client),
				llmrouter.WithTimeout(2*time.Second),
			)

			class, sig, err := engine.Classify(context.Background(), tc.prompt, "")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if class != tc.wantClass {
				t.Errorf("class = %q, want %q", class, tc.wantClass)
			}
			if sig.Source != llmrouter.SourceSemantic {
				t.Errorf("source = %q, want %q", sig.Source, llmrouter.SourceSemantic)
			}
			if sig.Confidence < 0 || sig.Confidence > 1 {
				t.Errorf("confidence %f out of [0,1]", sig.Confidence)
			}
		})
	}
}

// --- reflex fallback tests ---

func TestClassify_ReflexFallback_OllamaDown(t *testing.T) {
	// No server running — connection refused forces fallback.
	engine := llmrouter.NewEngine(
		llmrouter.WithEndpoint("http://127.0.0.1:1"), // port 1 is always closed
		llmrouter.WithTimeout(50*time.Millisecond),
	)

	class, sig, err := engine.Classify(context.Background(), "go build ./...", "")
	if err != nil {
		t.Fatalf("Classify must not surface errors to caller: %v", err)
	}
	if sig.Source != llmrouter.SourceReflex {
		t.Errorf("source = %q, want %q", sig.Source, llmrouter.SourceReflex)
	}
	if class != models.ClassGoBuild {
		t.Errorf("reflex fallback class = %q, want %q", class, models.ClassGoBuild)
	}
	if sig.Confidence != 1.0 {
		t.Errorf("reflex confidence should be 1.0, got %f", sig.Confidence)
	}
	// Fallback must note the reason.
	if len(sig.Notes) == 0 {
		t.Error("expected fallback reason in Notes, got none")
	}
}

func TestClassify_ReflexFallback_Timeout(t *testing.T) {
	// Server exists but sleeps longer than the engine timeout.
	srv, client := slowOllama(t, 300*time.Millisecond)

	engine := llmrouter.NewEngine(
		llmrouter.WithEndpoint(srv.URL),
		llmrouter.WithHTTPClient(client),
		llmrouter.WithTimeout(50*time.Millisecond),
	)

	_, sig, err := engine.Classify(context.Background(), "docker build -t myimage .", "")
	if err != nil {
		t.Fatalf("Classify must absorb timeout: %v", err)
	}
	if sig.Source != llmrouter.SourceReflex {
		t.Errorf("source = %q, want %q", sig.Source, llmrouter.SourceReflex)
	}
}

func TestClassify_ReflexFallback_BadJSON(t *testing.T) {
	// Server returns HTTP 200 but the payload is garbage.
	srv, client := mockOllama(t, http.StatusOK, []byte(`not json at all`))

	engine := llmrouter.NewEngine(
		llmrouter.WithEndpoint(srv.URL),
		llmrouter.WithHTTPClient(client),
		llmrouter.WithTimeout(2*time.Second),
	)

	_, sig, err := engine.Classify(context.Background(), "run ollama llama3", "")
	if err != nil {
		t.Fatalf("Classify must absorb parse errors: %v", err)
	}
	if sig.Source != llmrouter.SourceReflex {
		t.Errorf("source = %q, want %q", sig.Source, llmrouter.SourceReflex)
	}
}

func TestClassify_ReflexFallback_UnknownModelClass(t *testing.T) {
	// Model returns a class string AXIS does not recognise.
	body := ollamaGenResp(t, map[string]any{
		"class":      "web-scraping", // not a valid WorkloadClass
		"confidence": 0.7,
		"signals":    []string{"scrape"},
	})
	srv, client := mockOllama(t, http.StatusOK, body)

	engine := llmrouter.NewEngine(
		llmrouter.WithEndpoint(srv.URL),
		llmrouter.WithHTTPClient(client),
		llmrouter.WithTimeout(2*time.Second),
	)

	_, sig, err := engine.Classify(context.Background(), "scrape the website", "")
	if err != nil {
		t.Fatalf("Classify must absorb unrecognised class: %v", err)
	}
	if sig.Source != llmrouter.SourceReflex {
		t.Errorf("source = %q, want %q", sig.Source, llmrouter.SourceReflex)
	}
}

func TestClassify_ReflexFallback_OllamaError(t *testing.T) {
	// Server returns 500.
	srv, client := mockOllama(t, http.StatusInternalServerError, []byte(`{"error":"model not found"}`))

	engine := llmrouter.NewEngine(
		llmrouter.WithEndpoint(srv.URL),
		llmrouter.WithHTTPClient(client),
		llmrouter.WithTimeout(2*time.Second),
	)

	_, sig, err := engine.Classify(context.Background(), "embed documents", "")
	if err != nil {
		t.Fatalf("Classify must absorb HTTP errors: %v", err)
	}
	if sig.Source != llmrouter.SourceReflex {
		t.Errorf("source = %q, want %q", sig.Source, llmrouter.SourceReflex)
	}
}

// --- confidence clamping ---

func TestClassify_SemanticPath_ConfidenceClamped(t *testing.T) {
	// Model returns out-of-range confidence values; engine must clamp to [0,1].
	cases := []struct {
		rawConf     float64
		wantClamped float64
	}{
		{rawConf: 1.5, wantClamped: 1.0},
		{rawConf: -0.3, wantClamped: 0.0},
		{rawConf: 0.75, wantClamped: 0.75},
	}

	for _, tc := range cases {
		body := ollamaGenResp(t, map[string]any{
			"class":      "go-build",
			"confidence": tc.rawConf,
			"signals":    []string{},
		})
		srv, client := mockOllama(t, http.StatusOK, body)

		engine := llmrouter.NewEngine(
			llmrouter.WithEndpoint(srv.URL),
			llmrouter.WithHTTPClient(client),
			llmrouter.WithTimeout(2*time.Second),
		)

		_, sig, err := engine.Classify(context.Background(), "go test ./...", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if sig.Confidence != tc.wantClamped {
			t.Errorf("rawConf=%v: got %v, want %v", tc.rawConf, sig.Confidence, tc.wantClamped)
		}
	}
}

// --- extra context forwarded to prompt ---

func TestClassify_ExtraContextIncluded(t *testing.T) {
	// Verify that the engine doesn't panic when extraContext is provided.
	body := ollamaGenResp(t, map[string]any{
		"class":      "batch-script",
		"confidence": 0.80,
		"signals":    []string{"batch"},
	})
	srv, client := mockOllama(t, http.StatusOK, body)

	engine := llmrouter.NewEngine(
		llmrouter.WithEndpoint(srv.URL),
		llmrouter.WithHTTPClient(client),
		llmrouter.WithTimeout(2*time.Second),
	)

	class, sig, err := engine.Classify(context.Background(),
		"process the dataset", "node=medulla, available_ram=12gb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if class != models.ClassBatchScript {
		t.Errorf("class = %q, want %q", class, models.ClassBatchScript)
	}
	if sig.Source != llmrouter.SourceSemantic {
		t.Errorf("source = %q, want semantic", sig.Source)
	}
}
