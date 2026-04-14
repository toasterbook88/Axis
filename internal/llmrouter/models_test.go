package llmrouter_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/llmrouter"
)

type mockProvider struct {
	name         string
	providerType llmrouter.ProviderType
	models       []string
	health       llmrouter.HealthStatus
	healthErr    error
	result       llmrouter.GenerateResult
	generateErr  error
}

func (m *mockProvider) Name() string {
	return m.name
}

func (m *mockProvider) Type() llmrouter.ProviderType {
	return m.providerType
}

func (m *mockProvider) Health(context.Context) (llmrouter.HealthStatus, error) {
	return m.health, m.healthErr
}

func (m *mockProvider) SupportsModel(model string) bool {
	for _, candidate := range m.models {
		if candidate == model {
			return true
		}
	}
	return false
}

func (m *mockProvider) Generate(context.Context, string, string) (llmrouter.GenerateResult, error) {
	return m.result, m.generateErr
}

var _ llmrouter.Provider = (*mockProvider)(nil)

func TestProviderTypeConstants(t *testing.T) {
	if llmrouter.ProviderLocal != "local" {
		t.Fatalf("ProviderLocal = %q, want %q", llmrouter.ProviderLocal, "local")
	}
	if llmrouter.ProviderCloud != "cloud" {
		t.Fatalf("ProviderCloud = %q, want %q", llmrouter.ProviderCloud, "cloud")
	}
}

func TestMockProviderImplementsProviderContract(t *testing.T) {
	expected := llmrouter.GenerateResult{
		Response:  "pong",
		TokensIn:  12,
		TokensOut: 3,
		LatencyMs: 17,
		Cost:      0.002,
	}
	p := &mockProvider{
		name:         "openai",
		providerType: llmrouter.ProviderCloud,
		models:       []string{"gpt-4o", "gpt-4.1-mini"},
		health: llmrouter.HealthStatus{
			OK:      true,
			Latency: 42 * time.Millisecond,
			Message: "ok",
		},
		result: expected,
	}

	if p.Name() != "openai" {
		t.Fatalf("Name() = %q, want %q", p.Name(), "openai")
	}
	if p.Type() != llmrouter.ProviderCloud {
		t.Fatalf("Type() = %q, want %q", p.Type(), llmrouter.ProviderCloud)
	}
	if !p.SupportsModel("gpt-4o") {
		t.Fatal("SupportsModel(gpt-4o) = false, want true")
	}
	if p.SupportsModel("unknown") {
		t.Fatal("SupportsModel(unknown) = true, want false")
	}

	status, err := p.Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error = %v", err)
	}
	if status != p.health {
		t.Fatalf("Health() = %+v, want %+v", status, p.health)
	}

	got, err := p.Generate(context.Background(), "ping", "gpt-4o")
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if got != expected {
		t.Fatalf("Generate() = %+v, want %+v", got, expected)
	}
}

func TestMockProviderSurfacesErrors(t *testing.T) {
	p := &mockProvider{
		healthErr:   errors.New("dial tcp: refused"),
		generateErr: errors.New("quota exceeded"),
	}

	if _, err := p.Health(context.Background()); err == nil {
		t.Fatal("Health() error = nil, want non-nil")
	}
	if _, err := p.Generate(context.Background(), "prompt", "model"); err == nil {
		t.Fatal("Generate() error = nil, want non-nil")
	}
}

func TestRoutingDecisionJSONShape(t *testing.T) {
	decision := llmrouter.RoutingDecision{
		Provider:   "ollama",
		Model:      "llama3.1:8b",
		Reasoning:  []string{"provider is healthy", "model already installed"},
		EstLatency: "45ms",
		IsLocal:    true,
		Confidence: 0.92,
	}

	data, err := json.Marshal(decision)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if got["provider"] != "ollama" {
		t.Fatalf("provider = %#v, want %q", got["provider"], "ollama")
	}
	if got["estimated_latency"] != "45ms" {
		t.Fatalf("estimated_latency = %#v, want %q", got["estimated_latency"], "45ms")
	}
	if got["is_local"] != true {
		t.Fatalf("is_local = %#v, want true", got["is_local"])
	}
	if _, exists := got["endpoint"]; exists {
		t.Fatal("endpoint should be omitted when empty")
	}
}
