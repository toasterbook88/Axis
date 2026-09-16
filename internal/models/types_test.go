package models_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

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
