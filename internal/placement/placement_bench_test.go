package placement

import (
	"fmt"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

func benchNode(name string, freeRAM int64, pressure string, tools ...string) models.NodeFacts {
	var toolInfos []models.ToolInfo
	for _, t := range tools {
		toolInfos = append(toolInfos, models.ToolInfo{Name: t, Path: "/usr/bin/" + t, Class: models.ToolClassBuild})
	}
	return models.NodeFacts{
		Name:   name,
		Status: models.StatusComplete,
		Resources: &models.Resources{
			CPUCores:     8,
			RAMTotalMB:   16384,
			RAMFreeMB:    freeRAM,
			Pressure:     pressure,
			StorageClass: "ssd",
			GPUs: []models.GPUInfo{
				{Vendor: "nvidia", Model: "RTX 4090", VRAMMB: 24576, Capabilities: []string{"cuda"}},
			},
		},
		Tools:       toolInfos,
		CollectedAt: time.Now().UTC(),
	}
}

func benchNodeUnified(name string, freeRAM int64, pressure string, class int, tools ...string) models.NodeFacts {
	n := benchNode(name, freeRAM, pressure, tools...)
	n.Resources.MemoryTopology = models.MemoryTopologyUnified
	n.Resources.MemoryClass = class
	n.Resources.GPUs = []models.GPUInfo{
		{Vendor: "apple", Model: "Apple M3 Max", Capabilities: []string{"metal"}},
	}
	return n
}

func benchNodeTurboQuant(name string, freeRAM int64, pressure string, backends ...string) models.NodeFacts {
	n := benchNode(name, freeRAM, pressure, "ollama")
	n.Ollama = &models.OllamaInfo{
		Installed: true,
		Listening: true,
		Models:    []string{"llama3:8b"},
	}
	n.TurboQuant = &models.TurboQuantInfo{
		Supported:    true,
		Verified:     true,
		Backends:     backends,
		Capabilities: []string{"long-context"},
	}
	return n
}

func makeBenchNodes(count int) []models.NodeFacts {
	pressures := []string{"none", "low", "medium", "high"}
	tools := [][]string{
		{"git", "go"},
		{"python3", "ollama"},
		{"docker"},
		{"git", "go", "python3", "ollama"},
	}
	nodes := make([]models.NodeFacts, count)
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("node-%03d", i)
		freeRAM := int64(2048 + (i%12)*1024)
		pressure := pressures[i%len(pressures)]
		toolList := tools[i%len(tools)]

		switch i % 5 {
		case 0:
			nodes[i] = benchNodeTurboQuant(name, freeRAM, pressure, "mlx", "llama.cpp")
		case 1:
			nodes[i] = benchNodeUnified(name, freeRAM, pressure, 2+(i%3), toolList...)
		default:
			nodes[i] = benchNode(name, freeRAM, pressure, toolList...)
		}
	}
	return nodes
}

func BenchmarkRank(b *testing.B) {
	for _, n := range []int{1, 10, 50} {
		b.Run(fmt.Sprintf("nodes=%d", n), func(b *testing.B) {
			nodes := makeBenchNodes(n)
			reqs := models.TaskRequirements{
				MinFreeRAMMB:        2048,
				RequiredTools:       []string{"git"},
				PreferredBackends:   []string{"mlx", "llama.cpp"},
				PrefersTurboQuant:   true,
				ContextWindowTokens: 256000,
				Workload: models.WorkloadProfileMatch{
					Class: models.ClassLongContextInference,
				},
			}
			st := emptyClusterState()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = RankCandidates(nodes, reqs, st)
			}
		})
	}
}

func BenchmarkFilter(b *testing.B) {
	nodes := makeBenchNodes(50)

	cases := []struct {
		name string
		reqs models.TaskRequirements
	}{
		{
			name: "no-requirements",
			reqs: models.TaskRequirements{},
		},
		{
			name: "min-ram",
			reqs: models.TaskRequirements{MinFreeRAMMB: 4096},
		},
		{
			name: "tools",
			reqs: models.TaskRequirements{RequiredTools: []string{"git", "go"}},
		},
		{
			name: "ram+tools",
			reqs: models.TaskRequirements{
				MinFreeRAMMB:  4096,
				RequiredTools: []string{"git", "go"},
			},
		},
		{
			name: "turboquant",
			reqs: models.TaskRequirements{
				MinFreeRAMMB:      4096,
				PrefersTurboQuant: true,
				PreferredBackends: []string{"mlx"},
			},
		},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			st := emptyClusterState()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = FilterCandidates(tc.reqs, nodes, st)
			}
		})
	}
}

func emptyClusterState() *state.ClusterState {
	return &state.ClusterState{
		Nodes:        make(map[string]state.NodeState),
		Failures:     nil,
		Observations: make(map[string]models.ExecutionObservation),
	}
}
