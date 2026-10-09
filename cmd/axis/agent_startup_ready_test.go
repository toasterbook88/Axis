package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

func readyTestChoices() []ModelChoice {
	return []ModelChoice{
		{ID: "hub-dead", Model: "dead-default", Protocol: agent.ProtocolOpenAI, ProviderName: "ai-backend:hub", ProviderKind: "local", Endpoint: "http://hub.example.com/v1"},
		{ID: "here:ollama:good:1b", Model: "good:1b", Protocol: agent.ProtocolOllama, ProviderName: "ollama", ProviderKind: "local", Endpoint: "http://localhost:11434"},
	}
}

func stubStartupReady(t *testing.T, verdicts map[string]modelReadiness) *[]string {
	t.Helper()
	prev := startupReadyFn
	t.Cleanup(func() { startupReadyFn = prev })
	var checked []string
	startupReadyFn = func(_ context.Context, c ModelChoice, _ agent.CloudBackendOptions) (modelReadiness, string) {
		checked = append(checked, c.Model)
		v, ok := verdicts[c.Model]
		if !ok {
			return modelReady, ""
		}
		return v, "upstream connection error"
	}
	return &checked
}

func readyRuntime() *runtimectx.Context {
	return &runtimectx.Context{Config: &config.Config{}}
}

// A configured default that fails fast must not become the session model.
func TestStartupFallsBackWhenDefaultIsDown(t *testing.T) {
	stubStartupReady(t, map[string]modelReadiness{"dead-default": modelDown})
	var notes bytes.Buffer
	got, _, err := resolveReadyStartupModelTarget("dead-default", "auto", "", nil, readyRuntime(), readyTestChoices(), false, &notes)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "good:1b" {
		t.Fatalf("model = %q, want fallback good:1b", got.Model)
	}
	for _, want := range []string{"dead-default", "upstream connection error", "good:1b"} {
		if !strings.Contains(notes.String(), want) {
			t.Fatalf("notice %q missing %q", notes.String(), want)
		}
	}
}

// A slow answer is a cold model, not a dead one: keep it and say so.
func TestStartupKeepsColdDefaultWithNote(t *testing.T) {
	stubStartupReady(t, map[string]modelReadiness{"dead-default": modelCold})
	var notes bytes.Buffer
	got, _, err := resolveReadyStartupModelTarget("dead-default", "auto", "", nil, readyRuntime(), readyTestChoices(), false, &notes)
	if err != nil || got.Model != "dead-default" {
		t.Fatalf("model = %q err %v, want the cold default kept", got.Model, err)
	}
	if !strings.Contains(notes.String(), "loading") {
		t.Fatalf("notice %q, want a may-be-loading note", notes.String())
	}
}

// --model is operator intent: warn, never swap.
func TestStartupNeverSwapsOperatorPinnedModel(t *testing.T) {
	stubStartupReady(t, map[string]modelReadiness{"dead-default": modelDown})
	var notes bytes.Buffer
	got, _, err := resolveReadyStartupModelTarget("dead-default", "auto", "", nil, readyRuntime(), readyTestChoices(), true, &notes)
	if err != nil || got.Model != "dead-default" {
		t.Fatalf("model = %q err %v, want pinned model kept", got.Model, err)
	}
	if !strings.Contains(notes.String(), "not answering") {
		t.Fatalf("notice %q, want a warning", notes.String())
	}
}

func TestStartupDoesNotCheckCloudTargets(t *testing.T) {
	checked := stubStartupReady(t, nil)
	cloud := ModelChoice{ID: "cloud:groq:m", Model: "m", Protocol: agent.ProtocolCloud, ProviderKind: "cloud"}
	// Credentials are not configured here, so resolution errors; what matters
	// is that no request was sent to the provider either way.
	_, _, _ = resolveReadyStartupModelTarget("", "auto", "", &cloud, readyRuntime(), nil, false, &bytes.Buffer{})
	if len(*checked) != 0 {
		t.Fatalf("checked %v, want no readiness request to a paid cloud provider", *checked)
	}
}

func TestCheckModelReadinessClassifiesResponses(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ok.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"Connection error."}`, http.StatusInternalServerError)
	}))
	defer broken.Close()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer slow.Close()

	prev := startupReadyTimeout
	startupReadyTimeout = 300 * time.Millisecond
	t.Cleanup(func() { startupReadyTimeout = prev })

	for _, tc := range []struct {
		name   string
		choice ModelChoice
		want   modelReadiness
	}{
		{"openai ok", ModelChoice{Model: "m", Protocol: agent.ProtocolOpenAI, Endpoint: ok.URL + "/v1"}, modelReady},
		{"ollama ok", ModelChoice{Model: "m", Protocol: agent.ProtocolOllama, Endpoint: ok.URL}, modelReady},
		{"server error", ModelChoice{Model: "m", Protocol: agent.ProtocolOpenAI, Endpoint: broken.URL + "/v1"}, modelDown},
		{"slow", ModelChoice{Model: "m", Protocol: agent.ProtocolOllama, Endpoint: slow.URL}, modelCold},
		{"refused", ModelChoice{Model: "m", Protocol: agent.ProtocolOllama, Endpoint: "http://" + closedLoopbackAddr(t)}, modelDown},
	} {
		got, reason := checkModelReadiness(context.Background(), tc.choice, agent.CloudBackendOptions{})
		if got != tc.want {
			t.Errorf("%s: readiness = %v (%s), want %v", tc.name, got, reason, tc.want)
		}
	}
}

func closedLoopbackAddr(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := strings.TrimPrefix(srv.URL, "http://")
	srv.Close()
	return addr
}
