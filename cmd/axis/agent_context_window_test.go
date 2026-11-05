package main

import (
	"io"
	"testing"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/models"
)

// The served window llama.cpp states reaches the model choice, and an
// entry without one stays unknown.
func TestCatalogChoiceCarriesContextWindow(t *testing.T) {
	snap := &models.ClusterSnapshot{Nodes: []models.NodeFacts{{
		Name: "n", Status: models.StatusComplete,
		ResidentModels: []models.ResidentModel{
			{Name: "windowed", Runtime: "llama.cpp", Port: 8082, State: models.ModelCatalogLoaded, LoadSignal: "health-ok", ContextWindow: 16384},
			{Name: "plain", Runtime: "llama.cpp", Port: 8083, State: models.ModelCatalogLoaded, LoadSignal: "health-ok"},
		},
	}}}
	got := map[string]int{}
	for _, c := range catalogModelChoices(snap) {
		got[c.Model] = c.ContextWindow
	}
	if got["windowed"] != 16384 || got["plain"] != 0 {
		t.Fatalf("windows = %v, want windowed 16384, plain 0", got)
	}
}

// The plain axis agent path builds the agent from Config, never through
// SetBackend, so the window must arrive through the session config.
func TestPlainAgentPathUsesTargetWindow(t *testing.T) {
	cfg := buildAgentSessionConfig(agentSessionParams{
		Model:         "m",
		Backend:       resumeTestBackend{},
		MaxTurns:      1,
		MaxTokens:     32768,
		ContextWindow: 16384,
		Output:        io.Discard,
		ToolContext:   agent.NewToolContext(&agent.RuntimeView{}, nil),
	})
	a := agent.New(cfg)
	if u := a.MaxTokens(); u != 14336 {
		t.Fatalf("usable budget = %d, want 14336 (16,384 window minus the reply reserve)", u)
	}
}
