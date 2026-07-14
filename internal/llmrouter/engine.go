// Package llmrouter is EXPERIMENTAL — semantic workload classification and LLM routing.
// It is subordinate to observed state and emits warnings automatically.
//
// # Design Constraints
//
//   - Latency budget: < 200 ms per Classify call (enforced via context timeout)
//   - Zero cloud tokens: only local Ollama endpoints are contacted
//   - Graceful degradation: if the local model is unavailable or exceeds the
//     latency budget, Classify falls back to the legacy reflex classifier
//     (workload.Match) and marks the result with SourceReflex
//
// # Placement in the pipeline
//
//	axis task place "<desc>"
//	    └─ workload.InferRequirements
//	           └─ llmrouter.Engine.Classify   ← replaces analyzeDescription
//	                  ├─ semantic path (Ollama, <200 ms)
//	                  └─ reflex fallback (workload.Match)
package llmrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/workload"
)

const maxGenerateResponseBytes = 1 << 20

// ClassifySource records how a classification was produced.
type ClassifySource string

const (
	// SourceSemantic means the local LLM produced the classification.
	SourceSemantic ClassifySource = "semantic"

	// SourceReflex means the legacy string-matcher fallback was used.
	// This happens when Ollama is unreachable, too slow, or returns
	// an unrecognisable response.
	SourceReflex ClassifySource = "reflex"
)

// IntentSignal is the output of Engine.Classify. It pairs a WorkloadClass with
// metadata about how confident the classifier was and which signals it detected.
type IntentSignal struct {
	// Class is the primary workload classification.
	Class models.WorkloadClass `json:"class"`

	// Confidence is a [0.0, 1.0] estimate produced by the semantic model.
	// For SourceReflex results it is always 1.0 (the reflex matcher is
	// deterministic and has no probability estimate).
	Confidence float64 `json:"confidence"`

	// Signals lists the keywords or semantic features that drove the
	// classification (model-reported for semantic, derived for reflex).
	Signals []string `json:"signals,omitempty"`

	// Source records whether Ollama or the legacy fallback was used.
	Source ClassifySource `json:"source"`

	// Notes carries any additional context (e.g. secondary class matches
	// from the reflex path, or a reason for fallback).
	Notes []string `json:"notes,omitempty"`
}

// Engine is a semantic reflex classifier. It is safe for concurrent use.
type Engine struct {
	ollamaEndpoint string        // e.g. "http://localhost:11434"
	model          string        // e.g. "granite3.1-moe:1b"
	timeout        time.Duration // hard ceiling for the Ollama round-trip
	httpClient     *http.Client
}

// Option configures an Engine.
type Option func(*Engine)

// WithEndpoint overrides the default Ollama endpoint.
func WithEndpoint(endpoint string) Option {
	return func(e *Engine) {
		e.ollamaEndpoint = strings.TrimRight(endpoint, "/")
	}
}

// WithModel overrides the default local classification model.
func WithModel(model string) Option {
	return func(e *Engine) {
		e.model = model
	}
}

// WithTimeout overrides the default per-call latency budget.
// Values > 200 ms are accepted but violate the routing SLO.
func WithTimeout(d time.Duration) Option {
	return func(e *Engine) {
		e.timeout = d
	}
}

// WithHTTPClient injects a custom HTTP client (useful in tests).
func WithHTTPClient(c *http.Client) Option {
	return func(e *Engine) {
		e.httpClient = c
	}
}

// NewEngine constructs an Engine with sensible defaults.
// Granite 3.1 MoE 1B is the recommended model: it fits in < 1 GB RAM,
// loads in milliseconds from hot cache, and classifies in ~30–80 ms on
// CPU-only hardware.
func NewEngine(opts ...Option) *Engine {
	e := &Engine{
		ollamaEndpoint: "http://localhost:11434",
		model:          "granite3.1-moe:1b",
		timeout:        150 * time.Millisecond,
		// No client-level Timeout: the per-call budget is enforced via
		// context.WithTimeout in Classify, so WithTimeout() is authoritative.
		httpClient: &http.Client{},
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Classify maps a raw user prompt (and optional surrounding context) to a
// WorkloadClass and IntentSignal.
//
// It first attempts a semantic classification via the local Ollama model
// within the configured timeout. If that fails for any reason (network,
// parse error, unrecognised class, or timeout exceeded) it transparently
// falls back to the legacy reflex classifier and notes the reason.
func (e *Engine) Classify(ctx context.Context, prompt, extraContext string) (models.WorkloadClass, IntentSignal, error) {
	// Attempt semantic path with a hard latency budget.
	semCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	class, sig, err := e.classifyViaSemantic(semCtx, prompt, extraContext)
	if err == nil {
		return class, sig, nil
	}

	// Semantic path failed — fall back silently.
	fallbackClass, fallbackSig := reflexFallback(prompt, err)
	return fallbackClass, fallbackSig, nil
}

// --- Semantic path ---

// generateRequest is the JSON body for Ollama POST /api/generate.
type generateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
	Format string `json:"format"` // "json" enables grammar-constrained output
}

// generateResponse is the non-streaming response from /api/generate.
type generateResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
}

// classifyResponse is the JSON structure we ask the model to emit.
type classifyResponse struct {
	Class      string   `json:"class"`
	Confidence float64  `json:"confidence"`
	Signals    []string `json:"signals"`
}

// classifyViaSemantic sends a classification prompt to Ollama and parses the
// structured JSON response. It returns an error on any failure so the caller
// can fall back.
func (e *Engine) classifyViaSemantic(ctx context.Context, prompt, extraContext string) (models.WorkloadClass, IntentSignal, error) {
	body, err := json.Marshal(generateRequest{
		Model:  e.model,
		Prompt: buildClassifyPrompt(prompt, extraContext),
		Stream: false,
		Format: "json",
	})
	if err != nil {
		return models.ClassUnknown, IntentSignal{}, fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		e.ollamaEndpoint+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return models.ClassUnknown, IntentSignal{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return models.ClassUnknown, IntentSignal{}, fmt.Errorf("ollama unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return models.ClassUnknown, IntentSignal{}, fmt.Errorf("ollama status %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxGenerateResponseBytes))
	if err != nil {
		return models.ClassUnknown, IntentSignal{}, fmt.Errorf("read body: %w", err)
	}

	var genResp generateResponse
	if err := json.Unmarshal(raw, &genResp); err != nil {
		return models.ClassUnknown, IntentSignal{}, fmt.Errorf("parse generate response: %w", err)
	}

	var cr classifyResponse
	if err := json.Unmarshal([]byte(genResp.Response), &cr); err != nil {
		return models.ClassUnknown, IntentSignal{}, fmt.Errorf("parse classify json: %w", err)
	}

	class, ok := parseWorkloadClass(cr.Class)
	if !ok {
		return models.ClassUnknown, IntentSignal{}, fmt.Errorf("unrecognised class %q", cr.Class)
	}

	sig := IntentSignal{
		Class:      class,
		Confidence: clamp(cr.Confidence, 0, 1),
		Signals:    cr.Signals,
		Source:     SourceSemantic,
	}
	return class, sig, nil
}

// buildClassifyPrompt constructs the few-shot classification prompt. Keeping
// it short matters: fewer tokens → lower latency on a 1B model.
func buildClassifyPrompt(prompt, extraContext string) string {
	var sb strings.Builder
	sb.WriteString(`Classify the task into exactly one workload class.

Valid classes:
  repo-analysis          - code review, clone, inspect a repository
  go-build               - compile, test, or build a Go project
  docker-build           - build or run a Docker container/image
  local-llm-inference    - run/serve/chat with a local LLM (Ollama, llama.cpp, MLX)
  long-context-inference - inference requiring 128k+ token context window
  apple-intelligence     - Apple Foundation Models / language model session
  llama-server           - llama.cpp server specifically
  indexing-io            - embed, vectorize, or scan a filesystem/corpus
  batch-script           - batch job, data processing, run a script
  unknown                - does not match any above class

Respond with JSON only. No explanation. Schema:
{"class":"<class>","confidence":<0.0-1.0>,"signals":["<keyword>",...]}

`)

	if extraContext != "" {
		sb.WriteString("Context: ")
		sb.WriteString(extraContext)
		sb.WriteString("\n")
	}
	sb.WriteString("Task: ")
	sb.WriteString(prompt)
	return sb.String()
}

// parseWorkloadClass converts a model-returned string into a WorkloadClass,
// returning false if the string is not a recognised class value.
func parseWorkloadClass(s string) (models.WorkloadClass, bool) {
	switch models.WorkloadClass(s) {
	case models.ClassRepoAnalysis,
		models.ClassGoBuild,
		models.ClassDockerBuild,
		models.ClassLocalLLMInference,
		models.ClassLongContextInference,
		models.ClassAppleIntelligence,
		models.ClassLlamaServer,
		models.ClassIndexingIO,
		models.ClassBatchScript,
		models.ClassUnknown:
		return models.WorkloadClass(s), true
	default:
		return models.ClassUnknown, false
	}
}

// --- Reflex fallback ---

// reflexFallback wraps workload.Match so the llmrouter package does not need
// to duplicate classification logic. It always succeeds.
func reflexFallback(prompt string, reason error) (models.WorkloadClass, IntentSignal) {
	match := workload.Match(prompt)

	var notes []string
	if reason != nil {
		notes = append(notes, "semantic fallback: "+reason.Error())
	}
	notes = append(notes, match.Notes...)

	return match.Class, IntentSignal{
		Class:      match.Class,
		Confidence: 1.0, // deterministic
		Source:     SourceReflex,
		Notes:      notes,
	}
}

// --- Helpers ---

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
