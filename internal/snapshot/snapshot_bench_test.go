package snapshot

import (
	"fmt"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func benchNode(name string, totalRAM, freeRAM int64, pressure string) models.NodeFacts {
	return models.NodeFacts{
		Name:   name,
		Status: models.StatusComplete,
		Resources: &models.Resources{
			CPUCores:     8,
			RAMTotalMB:   totalRAM,
			RAMFreeMB:    freeRAM,
			Pressure:     pressure,
			StorageClass: "ssd",
			GPUs: []models.GPUInfo{
				{Vendor: "nvidia", Model: "RTX 4090", VRAMMB: 24576, Capabilities: []string{"cuda"}},
			},
		},
		CollectedAt: time.Now().UTC(),
	}
}

func makeBenchNodes(count int) []models.NodeFacts {
	pressures := []string{"none", "low", "medium", "high"}
	statuses := []models.NodeStatus{models.StatusComplete, models.StatusComplete, models.StatusComplete, models.StatusPartial, models.StatusUnreachable}
	nodes := make([]models.NodeFacts, count)
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("node-%03d", i)
		totalRAM := int64(8192 + (i%8)*2048)
		freeRAM := totalRAM / 4
		pressure := pressures[i%len(pressures)]
		status := statuses[i%len(statuses)]

		if status == models.StatusComplete || status == models.StatusPartial {
			nodes[i] = benchNode(name, totalRAM, freeRAM, pressure)
			nodes[i].Status = status
			if status == models.StatusPartial {
				nodes[i].Error = "some facts failed"
			}
		} else {
			nodes[i] = models.NodeFacts{
				Name:        name,
				Status:      status,
				Error:       "connection refused",
				CollectedAt: time.Now().UTC(),
			}
		}
	}
	return nodes
}

func BenchmarkBuild(b *testing.B) {
	for _, n := range []int{1, 10, 50, 100} {
		b.Run(fmt.Sprintf("nodes=%d", n), func(b *testing.B) {
			nodes := makeBenchNodes(n)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = Build(nodes)
			}
		})
	}
}
