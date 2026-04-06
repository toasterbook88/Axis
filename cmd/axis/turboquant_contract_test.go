package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

func TestTaskPlaceTurboQuantJSONGolden(t *testing.T) {
	restore := stubPlacementState(t, &state.ClusterState{Nodes: map[string]state.NodeState{}}, nil)
	defer restore()

	decision, source, err := planTaskPlacement(
		context.Background(),
		"run 128k ollama inference",
		true,
		false,
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			return &models.ClusterSnapshot{
				Nodes: []models.NodeFacts{goldenTurboQuantNode()},
			}, "daemon-cache", nil
		},
		nil,
	)
	if err != nil {
		t.Fatalf("planTaskPlacement: %v", err)
	}

	stdout, stderr, err := captureProcessOutput(t, func() error {
		return printOutput(taskPlaceOutput{
			Source:   source,
			Decision: decision,
		}, "json")
	})
	if err != nil {
		t.Fatalf("printOutput: %v", err)
	}

	assertNormalizedGoldenText(t,
		filepath.Join("testdata", "task_place_turboquant_json.golden"),
		normalizeGoldenOutput(renderGoldenSections(stderr, normalizeGoldenOutput(stdout))),
	)
}

func TestTaskContextTurboQuantGolden(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{goldenTurboQuantNode()},
		Summary: models.ClusterSummary{
			TotalNodes:         1,
			TotalAllocatableMB: 8192,
			TotalReservedMB:    0,
		},
	}

	actual := buildContextBlock(
		snap,
		models.TaskRequirements{
			Description:         "run 128k ollama inference",
			RequiredTools:       []string{"ollama"},
			MinFreeRAMMB:        4096,
			ContextWindowTokens: 128000,
			PrefersTurboQuant:   true,
		},
		"run 128k ollama inference",
		"daemon-cache",
		nil,
		nil,
	)

	assertNormalizedGoldenText(t,
		filepath.Join("testdata", "task_context_turboquant.golden"),
		normalizeGoldenOutput(actual),
	)
}

func TestStatusTurboQuantJSONGolden(t *testing.T) {
	restoreLive := stubStatusLiveLoader(t, func(context.Context) (*models.ClusterSnapshot, string, error) {
		return &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Nodes:  []models.NodeFacts{goldenTurboQuantNode()},
			Summary: models.ClusterSummary{
				TotalNodes:         1,
				ReachableNodes:     1,
				TotalRAMMB:         16384,
				TotalFreeRAMMB:     8192,
				TotalAllocatableMB: 8192,
			},
		}, "live", nil
	})
	defer restoreLive()

	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := statusCmd()
		cmd.SetArgs([]string{"--format", "json"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("status Execute: %v", err)
	}

	assertNormalizedGoldenText(t,
		filepath.Join("testdata", "status_turboquant_json.golden"),
		normalizeGoldenOutput(renderGoldenSections(stderr, normalizeGoldenOutput(stdout))),
	)
}

func goldenTurboQuantNode() models.NodeFacts {
	return models.NodeFacts{
		Name:   "mlx-node",
		Status: models.StatusComplete,
		Resources: &models.Resources{
			CPUCores:         10,
			RAMTotalMB:       16384,
			RAMFreeMB:        8192,
			MemoryTopology:   models.MemoryTopologyUnified,
			MemoryClass:      4,
			RAMReservedMB:    0,
			RAMAllocatableMB: 8192,
			Pressure:         "none",
			PressureSource:   "darwin-vm-pressure",
		},
		Tools: []models.ToolInfo{
			{Name: "ollama", Version: "0.7.0"},
		},
		Ollama: &models.OllamaInfo{
			Installed: true,
			Listening: true,
			Models:    []string{"llama3:8b"},
		},
		TurboQuant: &models.TurboQuantInfo{
			Supported:    true,
			Verified:     true,
			Backends:     []string{"mlx"},
			Capabilities: []string{"apple-silicon", "backend-probed", "cpu-fallback", "long-context", "ollama-present"},
		},
	}
}

func normalizeGoldenOutput(s string) string {
	return strings.TrimSpace(s)
}

func assertNormalizedGoldenText(t *testing.T, path string, actual string) {
	t.Helper()
	expectedBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	expected := normalizeGoldenOutput(string(expectedBytes))
	if actual != expected {
		t.Fatalf("golden mismatch for %s\nEXPECTED:\n%s\nACTUAL:\n%s", path, expected, actual)
	}
}
