package execution

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

func TestPrepareRequirementsExecKeepsOllamaRequirement(t *testing.T) {
	reqs := prepareRequirements("ollama run llama3", ModeExec, Intent{})
	if len(reqs.RequiredTools) == 0 {
		t.Fatal("expected required tools to be preserved")
	}
	found := false
	for _, tool := range reqs.RequiredTools {
		if strings.EqualFold(tool, "ollama") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected ollama requirement to remain, got %v", reqs.RequiredTools)
	}
}

func TestPrepareRequirementsExplicitLlamaServerRequiresObservedTool(t *testing.T) {
	reqs := prepareRequirements("llama-server -m qwen.gguf --port 8080", ModeExec, Intent{})
	if len(reqs.RequiredTools) != 1 || reqs.RequiredTools[0] != "llama-server" {
		t.Fatalf("expected llama-server requirement, got %v", reqs.RequiredTools)
	}
	if reqs.MinFreeRAMMB != 6144 {
		t.Fatalf("expected 6144MB floor for llama-server task, got %d", reqs.MinFreeRAMMB)
	}
	if len(reqs.PreferredBackends) == 0 || reqs.PreferredBackends[0] != "llama.cpp" {
		t.Fatalf("expected llama.cpp preferred backend, got %v", reqs.PreferredBackends)
	}
}

func TestRunGuardedBlocksLocalInferenceOnConstrainedMac(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	rt := testGuardedRuntime([]models.NodeFacts{
		{
			Name:     "macbook",
			Hostname: "localhost",
			OS:       "darwin",
			Status:   models.StatusComplete,
			Resources: &models.Resources{
				RAMTotalMB: 8192,
				RAMFreeMB:  7600,
				Pressure:   "low",
				CPUCores:   8,
			},
			Tools: []models.ToolInfo{{Name: "ollama", Version: "test"}},
			Ollama: &models.OllamaInfo{
				Installed: true,
				Listening: true,
				Models:    []string{"llama3"},
			},
		},
	})

	prevProbe := ProbeLocalAvailableRAMMB
	ProbeLocalAvailableRAMMB = func(context.Context) (int64, error) { return 7600, nil }
	defer func() { ProbeLocalAvailableRAMMB = prevProbe }()

	var called bool
	prevShell := RunLocalShell
	RunLocalShell = func(context.Context, string, []string) ([]byte, error) {
		called = true
		return nil, errors.New("should not run")
	}
	defer func() { RunLocalShell = prevShell }()

	resp, err := RunGuarded(context.Background(), rt, GuardedExecutionRequest{
		Description: "ollama run llama3",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
	})
	if err == nil {
		t.Fatal("expected local safety block")
	}
	if called {
		t.Fatal("expected local shell to remain blocked")
	}
	if !strings.Contains(resp.Error, "disabled on constrained") {
		t.Fatalf("expected constrained-host block, got %#v", resp)
	}
}

func TestRunGuardedLocalInferenceUsesLiveMemoryPreflight(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	rt := testGuardedRuntime([]models.NodeFacts{
		{
			Name:     "studio",
			Hostname: "localhost",
			OS:       "darwin",
			Status:   models.StatusComplete,
			Resources: &models.Resources{
				RAMTotalMB: 16384,
				RAMFreeMB:  12000,
				Pressure:   "low",
				CPUCores:   10,
			},
			Tools: []models.ToolInfo{{Name: "ollama", Version: "test"}},
			Ollama: &models.OllamaInfo{
				Installed: true,
				Listening: true,
				Models:    []string{"llama3"},
			},
		},
	})

	prevProbe := ProbeLocalAvailableRAMMB
	ProbeLocalAvailableRAMMB = func(context.Context) (int64, error) { return 2048, nil }
	defer func() { ProbeLocalAvailableRAMMB = prevProbe }()

	var called bool
	prevShell := RunLocalShell
	RunLocalShell = func(context.Context, string, []string) ([]byte, error) {
		called = true
		return nil, errors.New("should not run")
	}
	defer func() { RunLocalShell = prevShell }()

	resp, err := RunGuarded(context.Background(), rt, GuardedExecutionRequest{
		Description: "ollama run llama3",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
	})
	if err == nil {
		t.Fatal("expected live-memory preflight failure")
	}
	if called {
		t.Fatal("expected local shell to remain blocked")
	}
	if !strings.Contains(resp.Error, "live local memory preflight failed") {
		t.Fatalf("expected live preflight failure, got %#v", resp)
	}
}

func testGuardedRuntime(nodes []models.NodeFacts) *runtimectx.Context {
	cfgNodes := make([]config.NodeConfig, 0, len(nodes))
	for _, node := range nodes {
		cfgNodes = append(cfgNodes, config.NodeConfig{
			Name:     node.Name,
			Hostname: node.Hostname,
			SSHUser:  "me",
		})
	}

	return &runtimectx.Context{
		Config: &config.Config{Nodes: cfgNodes},
		Snapshot: &models.ClusterSnapshot{
			Status:  models.SnapshotHealthy,
			Nodes:   nodes,
			Summary: models.ClusterSummary{TotalNodes: len(nodes)},
		},
		State:  &state.ClusterState{Nodes: map[string]state.NodeState{}},
		Skills: &skills.Store{},
	}
}
