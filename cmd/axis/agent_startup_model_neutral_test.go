package main

import (
	"testing"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

func TestStartupUnnamedLocalDoesNotPreferOllama(t *testing.T) {
	choices := []ModelChoice{
		{
			Model:        "example-llama-model",
			Protocol:     agent.ProtocolOpenAI,
			ProviderName: "llama.cpp",
			ProviderKind: "local",
			Node:         "",
			Disabled:     false,
		},
		{
			Model:        "example-ollama-model",
			Protocol:     agent.ProtocolOllama,
			ProviderName: "ollama",
			ProviderKind: "local",
			Node:         "",
			Disabled:     false,
		},
	}

	for _, mode := range []string{"auto", "local"} {
		t.Run(mode, func(t *testing.T) {
			t.Run("unnamed", func(t *testing.T) {
				got, _, err := resolveStartupModelTarget("", mode, "", nil, &runtimectx.Context{}, choices)
				if err != nil {
					t.Fatalf("resolveStartupModelTarget: %v", err)
				}
				t.Logf("selected model %q via %s", got.Model, got.ProviderName)
				if got.Model != choices[0].Model || got.Protocol != choices[0].Protocol || got.ProviderName != choices[0].ProviderName {
					t.Fatalf("got %q via %s; want first catalog choice %q via %s", got.Model, got.ProviderName, choices[0].Model, choices[0].ProviderName)
				}
			})
			t.Run("duplicate_name", func(t *testing.T) {
				matches := append([]ModelChoice(nil), choices...)
				matches[1].Model = matches[0].Model
				got, _, err := resolveStartupModelTarget(matches[0].Model, mode, "", nil, &runtimectx.Context{}, matches)
				if err != nil {
					t.Fatalf("resolveStartupModelTarget: %v", err)
				}
				t.Logf("selected model %q via %s", got.Model, got.ProviderName)
				if got.Model != matches[0].Model || got.Protocol != matches[0].Protocol || got.ProviderName != matches[0].ProviderName {
					t.Fatalf("got %q via %s; want first match %q via %s", got.Model, got.ProviderName, matches[0].Model, matches[0].ProviderName)
				}
			})
			t.Run("ollama_first", func(t *testing.T) {
				reversed := []ModelChoice{choices[1], choices[0]}
				got, _, err := resolveStartupModelTarget("", mode, "", nil, &runtimectx.Context{}, reversed)
				if err != nil {
					t.Fatalf("resolveStartupModelTarget: %v", err)
				}
				t.Logf("selected model %q via %s", got.Model, got.ProviderName)
				if got.Model != reversed[0].Model || got.Protocol != reversed[0].Protocol || got.ProviderName != reversed[0].ProviderName {
					t.Fatalf("got %q via %s; want first catalog choice %q via %s", got.Model, got.ProviderName, reversed[0].Model, reversed[0].ProviderName)
				}
			})
			t.Run("duplicate_name_remote_first", func(t *testing.T) {
				matches := append([]ModelChoice(nil), choices...)
				matches[0].Node = "other"
				matches[1].Model = matches[0].Model
				got, _, err := resolveStartupModelTarget(matches[0].Model, mode, "", nil, &runtimectx.Context{}, matches)
				if err != nil {
					t.Fatalf("resolveStartupModelTarget: %v", err)
				}
				t.Logf("selected model %q via %s node %q", got.Model, got.ProviderName, got.Node)
				if got.Model != matches[0].Model || got.Protocol != matches[0].Protocol || got.ProviderName != matches[0].ProviderName || got.Node != matches[0].Node {
					t.Fatalf("got %q via %s node %q; want first match %q via %s node %q", got.Model, got.ProviderName, got.Node, matches[0].Model, matches[0].ProviderName, matches[0].Node)
				}
			})
		})
	}
}
