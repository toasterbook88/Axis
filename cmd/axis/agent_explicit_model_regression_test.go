package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

func TestExplicitOllamaV1EndpointCanChat(t *testing.T) {
	explicitModelConfig(t, []config.AIBackendConfig{{Name: "ollama", Kind: config.AIBackendOllama, BaseURL: "https://ollama.example/v1"}}, map[string]string{
		"https://ollama.example/api/tags": `{"models":[{"name":"review-model:latest"}]}`,
	})
	cfg, _ := inferenceAILoadFn("")
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	choices := appendExplicitAIModelChoice(context.Background(), "review-model", readyRuntime(), nil)
	if len(choices) != 1 {
		t.Fatalf("no selected target: %+v", choices)
	}
	old := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = old })
	var seen string
	http.DefaultClient = &http.Client{Transport: explicitModelTransport(func(r *http.Request) (*http.Response, error) {
		seen = r.URL.Path
		code := http.StatusNotFound
		if r.Method == http.MethodPost && r.URL.Path == "/api/chat" {
			code = http.StatusOK
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	})}
	verdict, reason := checkModelReadiness(context.Background(), choices[0], agent.CloudBackendOptions{})
	if verdict != modelReady {
		t.Fatalf("positively resolved Ollama target cannot chat: endpoint=%s path=%s verdict=%v reason=%s", choices[0].Endpoint, seen, verdict, reason)
	}
}

func TestRoleDoesNotResolveAsBareExplicitModel(t *testing.T) {
	for _, console := range []bool{false, true} {
		t.Run(fmt.Sprint("console=", console), func(t *testing.T) {
			seen, cfg := explicitModelConfig(t, []config.AIBackendConfig{
				{Name: "preferred", Kind: config.AIBackendOpenAICompatible, BaseURL: "https://preferred.example/v1"},
				{Name: "outside-role", Kind: config.AIBackendOpenAICompatible, BaseURL: "https://other.example/v1"},
			}, map[string]string{"https://other.example/v1/models": `{"data":[{"id":"review-model"}]}`})
			cfg.Roles = map[string]config.AIRoleConfig{"review": {Model: "review-model", Prefer: []string{"preferred"}}}
			prevProbe, prevLoader := inferenceProbeFn, loadAgentShellRuntime
			t.Cleanup(func() { inferenceProbeFn = prevProbe; loadAgentShellRuntime = prevLoader })
			inferenceProbeFn = func(string) bool { return false }
			loadAgentShellRuntime = func(context.Context) (*runtimectx.Context, error) { return readyRuntime(), nil }
			stubStartupReady(t, map[string]modelReadiness{"review-model": modelDown})
			if console {
				_, _ = consoleExplicitPrelude(context.Background(), &agentStartupInputs{role: "review", provider: "local"}, loadAgentShellRuntime, time.Second, setupAgentStartupBackend)
			} else {
				// Run the real setup, then stop before generation so the test
				// never reaches a real local Ollama.
				var origins []bool
				prevSetup := agentStartupBackendFn
				t.Cleanup(func() { agentStartupBackendFn = prevSetup })
				agentStartupBackendFn = func(p agentStartupBackendParams) (agentStartupBackendResult, error) {
					origins = append(origins, p.ExplicitModel)
					res, err := setupAgentStartupBackend(p)
					if err == nil {
						err = errStopBeforeGeneration
					}
					return res, err
				}
				cmd := agentCmd()
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				cmd.SetArgs([]string{"--role", "review", "--provider", "local", "--plain", "-p", "inspect"})
				if err := cmd.Execute(); err == nil {
					t.Fatal("startup did not stop before generation")
				}
				if len(origins) != 1 || origins[0] {
					t.Fatalf("--role reached setup as an explicit model: %v", origins)
				}
			}
			for _, url := range *seen {
				if strings.Contains(url, "other.example") {
					t.Fatalf("role escaped preference: %v", *seen)
				}
			}
		})
	}
}

var errStopBeforeGeneration = errors.New("test: stop before generation")

func TestConsolePreservesExplicitModelOrigin(t *testing.T) {
	_, cfg := explicitModelConfig(t, nil, nil)
	cfg.Roles = map[string]config.AIRoleConfig{"review": {Model: "review-model"}}
	for _, tc := range []struct {
		model, role string
		want        bool
	}{{"review-model", "", true}, {"", "review", false}} {
		in := &agentStartupInputs{model: tc.model, role: tc.role}
		_, err := consoleExplicitPrelude(context.Background(), in, func(context.Context) (*runtimectx.Context, error) { return readyRuntime(), nil }, time.Second, func(p agentStartupBackendParams) (agentStartupBackendResult, error) {
			if p.ExplicitModel != tc.want {
				t.Fatalf("origin lost for %+v: %v", tc, p.ExplicitModel)
			}
			return agentStartupBackendResult{}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
