package llmrouter_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/llmrouter"
	"github.com/toasterbook88/axis/internal/models"
)

func TestOpenRouterProviderSendAndHealth(t *testing.T) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			if got := r.Header.Get("Authorization"); got != "Bearer key-openrouter" {
				t.Fatalf("Authorization = %q, want Bearer key-openrouter", got)
			}
			fmt.Fprint(w, `{"data":[{"id":"openai/gpt-4o-mini"}]}`)
		case "/chat/completions":
			if got := r.Header.Get("Authorization"); got != "Bearer key-openrouter" {
				t.Fatalf("Authorization = %q, want Bearer key-openrouter", got)
			}
			fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"class\":\"go-build\",\"confidence\":0.84,\"signals\":[\"go\"]}"}}],"usage":{"prompt_tokens":11,"completion_tokens":7}}`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	provider := llmrouter.NewOpenRouterProvider(llmrouter.CloudProviderConfig{
		Name:     "openrouter",
		Endpoint: server.URL,
		APIKey:   "key-openrouter",
		Models: []llmrouter.CloudModel{
			{Name: "openai/gpt-4o-mini", CostPer1K: 0.001},
		},
	})

	status, err := provider.Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error = %v", err)
	}
	if !status.OK {
		t.Fatalf("Health() OK = false, want true (%+v)", status)
	}

	result, err := provider.Send(context.Background(), "hello", "openai/gpt-4o-mini")
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if !strings.Contains(result.Response, `"class":"go-build"`) {
		t.Fatalf("Response = %q, want classify JSON", result.Response)
	}
	if result.TokensIn != 11 || result.TokensOut != 7 {
		t.Fatalf("token counts = (%d,%d), want (11,7)", result.TokensIn, result.TokensOut)
	}
	if result.Cost <= 0 {
		t.Fatalf("Cost = %v, want > 0", result.Cost)
	}
}

func TestAnthropicProviderSend(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/messages":
			if got := r.Header.Get("x-api-key"); got != "key-anthropic" {
				t.Fatalf("x-api-key = %q, want key-anthropic", got)
			}
			if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
				t.Fatalf("anthropic-version = %q, want 2023-06-01", got)
			}
			fmt.Fprint(w, "{\"content\":[{\"type\":\"text\",\"text\":\"```json\\n{\\\"class\\\":\\\"repo-analysis\\\",\\\"confidence\\\":0.77,\\\"signals\\\":[\\\"review\\\"]}\\n```\"}],\"usage\":{\"input_tokens\":19,\"output_tokens\":9}}")
		case "/models":
			fmt.Fprint(w, `{"data":[{"id":"claude-3-5-haiku-latest"}]}`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	provider := llmrouter.NewAnthropicProvider(llmrouter.CloudProviderConfig{
		Name:     "anthropic",
		Endpoint: server.URL,
		APIKey:   "key-anthropic",
		Models: []llmrouter.CloudModel{
			{Name: "claude-3-5-haiku-latest", CostPer1K: 0.002},
		},
	})

	result, err := provider.Send(context.Background(), "review the repo", "claude-3-5-haiku-latest")
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if !strings.Contains(result.Response, "repo-analysis") {
		t.Fatalf("Response = %q, want repo-analysis", result.Response)
	}
}

func TestNewRegistryFromConfigRegistersConfiguredCloudProviders(t *testing.T) {
	t.Setenv("OPENROUTER_KEY", "env-openrouter")

	keyFile := writeTempFile(t, "anthropic-key", " file-anthropic \n")
	cfg := &config.Config{
		Nodes: []config.NodeConfig{
			{Name: "local", Hostname: "localhost", SSHUser: "me"},
		},
		AIProviders: map[string]config.AIProviderConfig{
			"openrouter-prod": {
				Type:      "cloud",
				Enabled:   true,
				APIKeyEnv: "OPENROUTER_KEY",
				Models: []config.AIModelConfig{
					{Name: "openai/gpt-4o-mini", CostPer1K: 0.001},
				},
			},
			"anthropic-prod": {
				Type:       "cloud",
				Enabled:    true,
				APIKeyFile: keyFile,
				Models: []config.AIModelConfig{
					{Name: "claude-3-5-haiku-latest", CostPer1K: 0.002},
				},
			},
			"groq-disabled": {
				Type:      "cloud",
				Enabled:   false,
				APIKeyEnv: "GROQ_API_KEY",
				Models: []config.AIModelConfig{
					{Name: "llama-3.1-8b-instant", CostPer1K: 0.0005},
				},
			},
		},
	}

	registry, err := llmrouter.NewRegistryFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewRegistryFromConfig() error = %v", err)
	}
	if got := registry.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2", got)
	}

	names := []string{}
	for _, provider := range registry.ListByType(llmrouter.ProviderCloud) {
		names = append(names, provider.Name())
	}
	want := []string{"anthropic-prod", "openrouter-prod"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("cloud providers = %v, want %v", names, want)
	}
}

func TestSelectCloudFallbackPrefersCheapestThenLatency(t *testing.T) {
	registry := llmrouter.NewRegistry()
	registry.MustRegister(&mockProvider{
		name:         "groq",
		providerType: llmrouter.ProviderCloud,
		models:       []string{"llama-3.1-8b-instant"},
		health:       llmrouter.HealthStatus{OK: true, Latency: 40 * time.Millisecond},
		estimated:    0.004,
	})
	registry.MustRegister(&mockProvider{
		name:         "openrouter",
		providerType: llmrouter.ProviderCloud,
		models:       []string{"openai/gpt-4o-mini"},
		health:       llmrouter.HealthStatus{OK: true, Latency: 55 * time.Millisecond},
		estimated:    0.002,
	})
	registry.MustRegister(&mockProvider{
		name:         "anthropic",
		providerType: llmrouter.ProviderCloud,
		models:       []string{"claude-3-5-haiku-latest"},
		health:       llmrouter.HealthStatus{OK: true, Latency: 20 * time.Millisecond},
		estimated:    0.006,
	})

	provider, decision, err := llmrouter.SelectCloudFallback(context.Background(), registry, "review code", "")
	if err != nil {
		t.Fatalf("SelectCloudFallback() error = %v", err)
	}
	if provider.Name() != "openrouter" {
		t.Fatalf("provider = %q, want openrouter", provider.Name())
	}
	if decision.Provider != "openrouter" {
		t.Fatalf("decision.Provider = %q, want openrouter", decision.Provider)
	}
}

func TestSelectCloudFallbackPrefersLatencyWhenRequested(t *testing.T) {
	registry := llmrouter.NewRegistry()
	registry.MustRegister(&mockProvider{
		name:         "groq",
		providerType: llmrouter.ProviderCloud,
		models:       []string{"llama-3.1-8b-instant"},
		health:       llmrouter.HealthStatus{OK: true, Latency: 18 * time.Millisecond},
		estimated:    0.003,
	})
	registry.MustRegister(&mockProvider{
		name:         "openrouter",
		providerType: llmrouter.ProviderCloud,
		models:       []string{"openai/gpt-4o-mini"},
		health:       llmrouter.HealthStatus{OK: true, Latency: 30 * time.Millisecond},
		estimated:    0.001,
	})

	provider, _, err := llmrouter.SelectCloudFallback(context.Background(), registry, "review code", "latency")
	if err != nil {
		t.Fatalf("SelectCloudFallback() error = %v", err)
	}
	if provider.Name() != "groq" {
		t.Fatalf("provider = %q, want groq", provider.Name())
	}
}

func TestClassifyWithProviderParsesWrappedJSON(t *testing.T) {
	provider := &mockProvider{
		name:         "anthropic",
		providerType: llmrouter.ProviderCloud,
		models:       []string{"claude-3-5-haiku-latest"},
		result: llmrouter.GenerateResult{
			Response: "```json\n{\"class\":\"repo-analysis\",\"confidence\":0.83,\"signals\":[\"review\"]}\n```",
		},
	}

	class, sig, err := llmrouter.ClassifyWithProvider(context.Background(), provider, "review repo", "")
	if err != nil {
		t.Fatalf("ClassifyWithProvider() error = %v", err)
	}
	if class != models.ClassRepoAnalysis {
		t.Fatalf("class = %q, want %q", class, models.ClassRepoAnalysis)
	}
	if sig.Source != llmrouter.SourceSemantic {
		t.Fatalf("source = %q, want semantic", sig.Source)
	}
}

func writeTempFile(t *testing.T, name, contents string) string {
	t.Helper()

	file, err := os.CreateTemp(t.TempDir(), name)
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	if _, err := file.WriteString(contents); err != nil {
		t.Fatalf("WriteString() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	return file.Name()
}
