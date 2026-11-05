package main

import (
	"context"
	"io"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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

type discardProgram struct{}

func (discardProgram) Send(tea.Msg) {}

// The console path with a model already resolved builds the agent once,
// before any /model switch, so the window must reach it there too.
func TestConsoleStartupAgentUsesTargetWindow(t *testing.T) {
	j := &consoleStartupJob{
		in:       agentStartupInputs{maxTurns: 1, maxTokens: 32768},
		out:      io.Discard,
		prog:     discardProgram{},
		launcher: newConsoleLauncher(nil, time.Second, time.Now),
		prelude: &consolePrelude{result: agentStartupBackendResult{
			ActiveTarget: ModelChoice{Model: "m", ContextWindow: 16384},
			Backend:      resumeTestBackend{},
		}},
	}
	a := j.buildAgent(context.Background(), nil, nil)
	if u := a.MaxTokens(); u != 14336 {
		t.Fatalf("usable budget = %d, want 14336 (16,384 window minus the reply reserve)", u)
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
