package models

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestModelSpecTotalMemoryMB(t *testing.T) {
	spec := ModelSpec{
		ID:     "ms-test",
		Name:   "test-model",
		Format: ModelFormatGGUF,
		Memory: ModelMemoryRequirements{
			WeightSizeMB:      2048,
			ContextOverheadMB: 512,
			RuntimeOverheadMB: 256,
		},
	}
	want := int64(2816)
	if got := spec.Memory.TotalMemoryMB(); got != want {
		t.Fatalf("TotalMemoryMB() = %d, want %d", got, want)
	}
}

func TestModelSpecValidate(t *testing.T) {
	cases := []struct {
		name    string
		spec    ModelSpec
		wantErr string
	}{
		{
			name: "valid",
			spec: ModelSpec{
				ID:     "ms-valid",
				Name:   "valid-model",
				Format: ModelFormatGGUF,
				Memory: ModelMemoryRequirements{WeightSizeMB: 1000},
			},
		},
		{
			name: "missing id",
			spec: ModelSpec{
				Name:   "no-id",
				Format: ModelFormatGGUF,
				Memory: ModelMemoryRequirements{WeightSizeMB: 1000},
			},
			wantErr: "ID is required",
		},
		{
			name: "missing name",
			spec: ModelSpec{
				ID:     "ms-1",
				Format: ModelFormatGGUF,
				Memory: ModelMemoryRequirements{WeightSizeMB: 1000},
			},
			wantErr: "Name is required",
		},
		{
			name: "missing format",
			spec: ModelSpec{
				ID:     "ms-1",
				Name:   "no-format",
				Memory: ModelMemoryRequirements{WeightSizeMB: 1000},
			},
			wantErr: "Format is required",
		},
		{
			name: "invalid weight size",
			spec: ModelSpec{
				ID:     "ms-1",
				Name:   "zero-weight",
				Format: ModelFormatGGUF,
				Memory: ModelMemoryRequirements{WeightSizeMB: 0},
			},
			wantErr: "WeightSizeMB must be positive",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
			}
		})
	}
}

func TestModelSpecFromDiskWeight(t *testing.T) {
	dw := DiskWeight{
		Name:   "qwen2.5-7b",
		Path:   "/models/qwen2.5-7b.gguf",
		Bytes:  4 * 1024 * 1024 * 1024, // 4 GiB
		Format: "gguf",
		Source: "well-known",
	}

	spec := ModelSpecFromDiskWeight(dw)
	if !strings.HasPrefix(spec.ID, "ms-") {
		t.Errorf("expected ms- prefix in ID, got %q", spec.ID)
	}
	if spec.Name != "qwen2.5-7b" {
		t.Errorf("spec.Name = %q, want qwen2.5-7b", spec.Name)
	}
	if spec.Format != ModelFormatGGUF {
		t.Errorf("spec.Format = %q, want %q", spec.Format, ModelFormatGGUF)
	}
	if spec.Memory.WeightSizeMB != 4096 {
		t.Errorf("spec.Memory.WeightSizeMB = %d, want 4096", spec.Memory.WeightSizeMB)
	}
	if spec.Memory.RecommendedVRAMMB != 4096+512 {
		t.Errorf("spec.Memory.RecommendedVRAMMB = %d, want %d", spec.Memory.RecommendedVRAMMB, 4096+512)
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("spec should be valid: %v", err)
	}
}

func TestModelSpecFromPath(t *testing.T) {
	spec := ModelSpecFromPath("/data/weights/bitnet-2B.gguf", 1132*1024*1024)
	if spec.Name != "bitnet-2B" {
		t.Errorf("spec.Name = %q, want bitnet-2B", spec.Name)
	}
	if spec.Format != ModelFormatGGUF {
		t.Errorf("spec.Format = %q, want %q", spec.Format, ModelFormatGGUF)
	}
	if spec.Memory.WeightSizeMB != 1132 {
		t.Errorf("spec.Memory.WeightSizeMB = %d, want 1132", spec.Memory.WeightSizeMB)
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("spec should be valid: %v", err)
	}
}

func TestModelSpecJSONAndYAMLSerialization(t *testing.T) {
	spec := ModelSpec{
		Schema:       "axis.model-spec/v1",
		ID:           "ms-test1234",
		Name:         "test-model",
		Format:       ModelFormatGGUF,
		WeightsPath:  "/models/test.gguf",
		Accelerators: []AcceleratorType{AcceleratorCUDA, AcceleratorCPU},
		Memory: ModelMemoryRequirements{
			WeightSizeMB:      2048,
			ContextOverheadMB: 512,
			RuntimeOverheadMB: 256,
			MinVRAMMB:         1024,
			RecommendedVRAMMB: 2560,
		},
		ObservedAt: time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC),
	}

	// Test JSON
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("json marshal failed: %v", err)
	}
	var unmarshaledJSON ModelSpec
	if err := json.Unmarshal(data, &unmarshaledJSON); err != nil {
		t.Fatalf("json unmarshal failed: %v", err)
	}
	if unmarshaledJSON.ID != spec.ID || unmarshaledJSON.Memory.TotalMemoryMB() != spec.Memory.TotalMemoryMB() {
		t.Fatalf("unmarshaled JSON mismatch: %+v", unmarshaledJSON)
	}

	// Test YAML
	yamlData, err := yaml.Marshal(spec)
	if err != nil {
		t.Fatalf("yaml marshal failed: %v", err)
	}
	var unmarshaledYAML ModelSpec
	if err := yaml.Unmarshal(yamlData, &unmarshaledYAML); err != nil {
		t.Fatalf("yaml unmarshal failed: %v", err)
	}
	if unmarshaledYAML.ID != spec.ID || unmarshaledYAML.Memory.TotalMemoryMB() != spec.Memory.TotalMemoryMB() {
		t.Fatalf("unmarshaled YAML mismatch: %+v", unmarshaledYAML)
	}
}
