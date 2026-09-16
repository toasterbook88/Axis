package models_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func sampleNodeFacts() models.NodeFacts {
	idleGPUUtil := 0.0

	return models.NodeFacts{
		Name:      "test-node",
		Role:      "worker",
		Hostname:  "test.local",
		Identity:  models.NewNodeIdentity("f47ac10b-58cc-4372-a567-0e02b2c3d479", "linux-machine-id"),
		OS:        "linux",
		OSVersion: "6.1.0",
		Arch:      "amd64",
		Resources: &models.Resources{
			CPUCores:       4,
			CPUModel:       "Intel i7-1065G7",
			RAMTotalMB:     16384,
			RAMFreeMB:      8192,
			MemoryTopology: models.MemoryTopologyStandard,
			Load1M:         1.25,
			Load5M:         0.80,
			Load15M:        0.50,
			DiskTotalGB:    500,
			DiskFreeGB:     250,
			GPUs:           []models.GPUInfo{{Model: "NVIDIA MX250", Vendor: "nvidia", Capabilities: []string{"cuda"}}},
			GPUUtilPercent: &idleGPUUtil,
			Pressure:       "none",
			PressureSource: "free-ram",
		},
		RAMReservedMB:    1024,
		RAMAllocatableMB: 7168,
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
			TotalReservableMB:  8192,
			TotalAllocatableMB: 7168,
			TotalReservedMB:    1024,
		},
	}
}

func TestResources_JSONIncludesZeroGPUUtilWhenMeasured(t *testing.T) {
	zero := 0.0
	data, err := json.Marshal(models.Resources{
		Pressure:       "none",
		GPUUtilPercent: &zero,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(data) == "{}" || !containsJSONField(string(data), `"gpu_util_percent":0`) {
		t.Fatalf("expected zero gpu util field in json, got %s", data)
	}
}

func containsJSONField(data, want string) bool {
	return strings.Contains(data, want)
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

func TestGPUNames_Helper(t *testing.T) {
	gpus := []models.GPUInfo{
		{Model: "RTX 4090", Vendor: "nvidia"},
		{Model: "Apple M3", Vendor: "apple"},
	}
	names := models.GPUNames(gpus)
	if len(names) != 2 || names[0] != "RTX 4090" || names[1] != "Apple M3" {
		t.Errorf("GPUNames = %v, unexpected", names)
	}
}

func TestFormatPartialReasons(t *testing.T) {
	if got := models.FormatPartialReasons(nil); got != "some facts failed to collect" {
		t.Fatalf("nil = %q", got)
	}
	got := models.FormatPartialReasons([]models.PartialReason{{Probe: "df", Message: "deadline"}})
	if got != "some facts failed (df: deadline)" {
		t.Fatalf("got %q", got)
	}
}
