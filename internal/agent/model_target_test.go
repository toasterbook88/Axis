package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/chat"
)

func TestBuildBackendOllamaUsesEndpoint(t *testing.T) {
	target := ModelTarget{
		Model:        "qwen3:1.7b",
		Protocol:     ProtocolOllama,
		ProviderName: "ollama",
		ProviderKind: "local",
		Endpoint:     "http://10.0.0.5:11434",
	}
	b, err := BuildBackend(target, CloudBackendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := b.(*chat.Client)
	if !ok {
		t.Fatalf("expected *chat.Client, got %T", b)
	}
	if c.Endpoint != "http://10.0.0.5:11434" {
		t.Fatalf("endpoint = %q", c.Endpoint)
	}
	if c.Model != "qwen3:1.7b" {
		t.Fatalf("model = %q", c.Model)
	}
}

func TestBuildBackendOpenAIHitsChatCompletions(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Header.Get("Authorization") != "" {
			t.Errorf("expected no Authorization header for empty key, got %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	target := ModelTarget{
		Model:        "local-model",
		Protocol:     ProtocolOpenAI,
		ProviderName: "mlx",
		ProviderKind: "local",
		Endpoint:     srv.URL,
	}
	b, err := BuildBackend(target, CloudBackendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := b.ChatStream(t.Context(), []chat.Message{{Role: chat.RoleUser, Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg.Content, "hi") {
		t.Fatalf("content = %q", msg.Content)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q, want /v1/chat/completions", gotPath)
	}
}

func TestBuildBackendDisabledRejected(t *testing.T) {
	_, err := BuildBackend(ModelTarget{
		Model:          "x",
		Protocol:       ProtocolOllama,
		Disabled:       true,
		DisabledReason: "unreachable",
	}, CloudBackendOptions{})
	if err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildBackendCloudRequiresKey(t *testing.T) {
	_, err := BuildBackend(ModelTarget{
		Model:        "gpt",
		Protocol:     ProtocolCloud,
		ProviderName: "groq",
		ProviderKind: "cloud",
	}, CloudBackendOptions{ProviderKind: "groq"})
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewOpenAICompatibleBackendNormalizesV1(t *testing.T) {
	b, err := NewOpenAICompatibleBackend("http://127.0.0.1:8080", "m", "")
	if err != nil {
		t.Fatal(err)
	}
	if b.endpoint != "http://127.0.0.1:8080/v1" {
		t.Fatalf("endpoint = %q", b.endpoint)
	}
	// Already has /v1
	b2, err := NewOpenAICompatibleBackend("http://127.0.0.1:8080/v1/", "m", "k")
	if err != nil {
		t.Fatal(err)
	}
	if b2.endpoint != "http://127.0.0.1:8080/v1" {
		t.Fatalf("endpoint = %q", b2.endpoint)
	}
	_ = json.RawMessage(nil)
}
