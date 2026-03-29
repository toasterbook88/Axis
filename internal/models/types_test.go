package models_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
	"gopkg.in/yaml.v3"
)

func sampleNodeFacts() models.NodeFacts {
	return models.NodeFacts{
		Name:      "test-node",
		Role:      "worker",
		Hostname:  "test.local",
		OS:        "linux",
		OSVersion: "6.1.0",
		Arch:      "amd64",
		Resources: &models.Resources{
			CPUCores:         4,
			CPUModel:         "Intel i7-1065G7",
			RAMTotalMB:       16384,
			RAMFreeMB:        8192,
			MemoryTopology:   models.MemoryTopologyStandard,
			Load1M:           1.25,
			Load5M:           0.80,
			Load15M:          0.50,
			RAMReservedMB:    1024,
			RAMAllocatableMB: 7168,
			DiskTotalGB:      500,
			DiskFreeGB:       250,
			GPUs:             []string{"NVIDIA MX250"},
			Pressure:         "none",
			PressureSource:   "free-ram",
		},
		Addresses: []models.NetworkAddress{
			{Kind: "ipv4", Address: "192.168.1.100"},
			{Kind: "hostname", Address: "test.local"},
		},
		Tools: []models.ToolInfo{
			{Name: "git", Path: "/usr/bin/git", Version: "2.39.0", Class: models.ToolClassVCS},
			{Name: "python3", Path: "/usr/bin/python3", Version: "3.11.0", Class: models.ToolClassRuntime},
		},
		TurboQuant: &models.TurboQuantInfo{
			Supported:    true,
			Verified:     true,
			Backends:     []string{"mlx"},
			Capabilities: []string{"apple-silicon", "long-context"},
		},
		Status:      models.StatusComplete,
		CollectedAt: time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC),
	}
}

func sampleSnapshot() models.ClusterSnapshot {
	return models.ClusterSnapshot{
		Timestamp: time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC),
		Status:    models.SnapshotHealthy,
		Nodes:     []models.NodeFacts{sampleNodeFacts()},
		Summary: models.ClusterSummary{
			TotalNodes:         1,
			ReachableNodes:     1,
			TotalRAMMB:         16384,
			TotalFreeRAMMB:     8192,
			TotalAllocatableMB: 7168,
			TotalReservedMB:    1024,
		},
	}
}

func TestNodeFacts_JSONRoundTrip(t *testing.T) {
	original := sampleNodeFacts()
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded models.NodeFacts
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Name != original.Name {
		t.Errorf("name: got %q, want %q", decoded.Name, original.Name)
	}
	if decoded.Status != original.Status {
		t.Errorf("status: got %q, want %q", decoded.Status, original.Status)
	}
	if decoded.OS != "linux" || decoded.OSVersion != "6.1.0" {
		t.Errorf("os fields: got %q/%q", decoded.OS, decoded.OSVersion)
	}
	if decoded.Resources == nil {
		t.Fatal("resources nil after round-trip")
	}
	if decoded.Resources.CPUCores != 4 {
		t.Errorf("cpu_cores: got %d, want 4", decoded.Resources.CPUCores)
	}
	if decoded.Resources.Pressure != "none" {
		t.Errorf("pressure: got %q, want %q", decoded.Resources.Pressure, "none")
	}
	if decoded.Resources.MemoryTopology != models.MemoryTopologyStandard {
		t.Errorf("memory_topology: got %q, want %q", decoded.Resources.MemoryTopology, models.MemoryTopologyStandard)
	}
	if decoded.Resources.PressureSource != "free-ram" {
		t.Errorf("pressure_source: got %q, want %q", decoded.Resources.PressureSource, "free-ram")
	}
	if decoded.Resources.Load1M != 1.25 || decoded.Resources.Load5M != 0.80 || decoded.Resources.Load15M != 0.50 {
		t.Errorf("load averages: got %.2f/%.2f/%.2f", decoded.Resources.Load1M, decoded.Resources.Load5M, decoded.Resources.Load15M)
	}
	if decoded.Resources.RAMAllocatableMB != 7168 {
		t.Errorf("ram_allocatable_mb: got %d, want 7168", decoded.Resources.RAMAllocatableMB)
	}
	if decoded.TurboQuant == nil || !decoded.TurboQuant.Supported {
		t.Fatal("turboquant missing after round-trip")
	}
	if !decoded.TurboQuant.Verified {
		t.Fatal("expected turboquant verified flag after round-trip")
	}
	if len(decoded.TurboQuant.Backends) != 1 || decoded.TurboQuant.Backends[0] != "mlx" {
		t.Fatalf("turboquant backends = %v, want [mlx]", decoded.TurboQuant.Backends)
	}
	if len(decoded.TurboQuant.Capabilities) != 2 {
		t.Fatalf("turboquant capabilities = %v, want 2 entries", decoded.TurboQuant.Capabilities)
	}
	if len(decoded.Addresses) != 2 {
		t.Errorf("addresses: got %d, want 2", len(decoded.Addresses))
	}
	if decoded.Addresses[0].Kind != "ipv4" {
		t.Errorf("address kind: got %q, want ipv4", decoded.Addresses[0].Kind)
	}
}

func TestNodeFacts_YAMLRoundTrip(t *testing.T) {
	original := sampleNodeFacts()
	data, err := yaml.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded models.NodeFacts
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Name != original.Name {
		t.Errorf("name: got %q, want %q", decoded.Name, original.Name)
	}
	if decoded.OS != "linux" {
		t.Errorf("os: got %q, want linux", decoded.OS)
	}
	if len(decoded.Tools) != 2 {
		t.Errorf("tools: got %d, want 2", len(decoded.Tools))
	}
	if decoded.Tools[0].Class != models.ToolClassVCS {
		t.Errorf("tool class: got %q, want %q", decoded.Tools[0].Class, models.ToolClassVCS)
	}
}

func TestClusterSnapshot_JSONRoundTrip(t *testing.T) {
	original := sampleSnapshot()
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded models.ClusterSnapshot
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Status != models.SnapshotHealthy {
		t.Errorf("status: got %q, want healthy", decoded.Status)
	}
	if decoded.Summary.TotalNodes != 1 {
		t.Errorf("total_nodes: got %d, want 1", decoded.Summary.TotalNodes)
	}
	if decoded.Summary.TotalRAMMB != 16384 {
		t.Errorf("total_ram: got %d, want 16384", decoded.Summary.TotalRAMMB)
	}
	if decoded.Summary.TotalAllocatableMB != 7168 {
		t.Errorf("total_allocatable_mb: got %d, want 7168", decoded.Summary.TotalAllocatableMB)
	}
}

func TestNodeFacts_ZeroValue(t *testing.T) {
	var empty models.NodeFacts
	data, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal zero: %v", err)
	}
	var decoded models.NodeFacts
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal zero: %v", err)
	}
	if decoded.Resources != nil {
		t.Error("expected nil resources for zero value")
	}
	if len(decoded.Tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(decoded.Tools))
	}
	if len(decoded.Addresses) != 0 {
		t.Errorf("expected 0 addresses, got %d", len(decoded.Addresses))
	}
}

func TestSnapshotStatus_DegradedWhenUnreachable(t *testing.T) {
	snap := models.ClusterSnapshot{
		Status: models.SnapshotDegraded,
		Nodes: []models.NodeFacts{
			{Name: "ok", Status: models.StatusComplete},
			{Name: "down", Status: models.StatusUnreachable, Error: "timeout"},
		},
		Warnings: []models.Warning{
			{Node: "down", Kind: "unreachable", Message: "timeout"},
		},
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded models.ClusterSnapshot
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Status != models.SnapshotDegraded {
		t.Errorf("expected degraded, got %q", decoded.Status)
	}
	if len(decoded.Warnings) != 1 {
		t.Errorf("expected 1 warning, got %d", len(decoded.Warnings))
	}
}
