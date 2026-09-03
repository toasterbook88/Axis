package models

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// ModelFormat identifies the artifact format of a model.
type ModelFormat string

const (
	ModelFormatGGUF        ModelFormat = "gguf"
	ModelFormatSafeTensors ModelFormat = "safetensors"
	ModelFormatMLX         ModelFormat = "mlx"
	ModelFormatOllama      ModelFormat = "ollama"
)

// AcceleratorType identifies hardware accelerator classes.
type AcceleratorType string

const (
	AcceleratorCUDA  AcceleratorType = "cuda"
	AcceleratorMetal AcceleratorType = "metal"
	AcceleratorROCm  AcceleratorType = "rocm"
	AcceleratorCPU   AcceleratorType = "cpu"
)

// ModelMemoryRequirements defines separate, honest memory quantities
// for model deployment, keeping weight, context, runtime, and VRAM distinct.
type ModelMemoryRequirements struct {
	// WeightSizeMB is the in-memory footprint of the model weights.
	WeightSizeMB int64 `json:"weight_size_mb" yaml:"weight_size_mb"`
	// ContextOverheadMB is the estimated KV-cache and activation overhead
	// for the intended context window.
	ContextOverheadMB int64 `json:"context_overhead_mb" yaml:"context_overhead_mb"`
	// RuntimeOverheadMB is the baseline execution engine overhead.
	RuntimeOverheadMB int64 `json:"runtime_overhead_mb" yaml:"runtime_overhead_mb"`
	// MinVRAMMB is the minimum VRAM required if offloading to an accelerator.
	// A value of 0 means the model can run entirely in system RAM / CPU.
	MinVRAMMB int64 `json:"min_vram_mb,omitempty" yaml:"min_vram_mb,omitempty"`
	// RecommendedVRAMMB is the recommended VRAM to fit the full model weights + context.
	RecommendedVRAMMB int64 `json:"recommended_vram_mb,omitempty" yaml:"recommended_vram_mb,omitempty"`
}

// TotalMemoryMB returns the sum of weights, context, and runtime overhead.
func (m ModelMemoryRequirements) TotalMemoryMB() int64 {
	return m.WeightSizeMB + m.ContextOverheadMB + m.RuntimeOverheadMB
}

// ModelSpec represents a canonical, portable description of a runnable model.
// ModelSpec is not residency; a spec does not imply that the model is loaded.
type ModelSpec struct {
	Schema           string                  `json:"schema" yaml:"schema"`
	ID               string                  `json:"id" yaml:"id"`
	Name             string                  `json:"name" yaml:"name"`
	Aliases          []string                `json:"aliases,omitempty" yaml:"aliases,omitempty"`
	Format           ModelFormat             `json:"format" yaml:"format"`
	ParameterCount   string                  `json:"parameter_count,omitempty" yaml:"parameter_count,omitempty"`
	Quantization     string                  `json:"quantization,omitempty" yaml:"quantization,omitempty"`
	WeightsPath      string                  `json:"weights_path,omitempty" yaml:"weights_path,omitempty"`
	Checksum         string                  `json:"checksum,omitempty" yaml:"checksum,omitempty"`
	Memory           ModelMemoryRequirements `json:"memory" yaml:"memory"`
	Accelerators     []AcceleratorType       `json:"accelerators,omitempty" yaml:"accelerators,omitempty"`
	SupportedEngines []string                `json:"supported_engines,omitempty" yaml:"supported_engines,omitempty"`
	ParallelismModes []string                `json:"parallelism_modes,omitempty" yaml:"parallelism_modes,omitempty"`
	Source           string                  `json:"source,omitempty" yaml:"source,omitempty"`
	ObservedAt       time.Time               `json:"observed_at" yaml:"observed_at"`
}

// Validate ensures required fields are set and memory values are coherent.
func (s ModelSpec) Validate() error {
	if strings.TrimSpace(s.ID) == "" {
		return fmt.Errorf("model spec ID is required")
	}
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("model spec Name is required")
	}
	if strings.TrimSpace(string(s.Format)) == "" {
		return fmt.Errorf("model spec Format is required")
	}
	if s.Memory.WeightSizeMB <= 0 {
		return fmt.Errorf("model spec WeightSizeMB must be positive")
	}
	return nil
}

// ModelSpecID derives a deterministic spec ID from a model name, format, and weight size.
func ModelSpecID(name string, format ModelFormat, weightSizeMB int64) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "axis-model-spec-v1:%s:%s:%d", strings.ToLower(strings.TrimSpace(name)), format, weightSizeMB)
	digest := hex.EncodeToString(h.Sum(nil))
	if len(digest) > 16 {
		digest = digest[:16]
	}
	return "ms-" + digest
}

// ModelSpecFromDiskWeight creates a canonical ModelSpec from an observed DiskWeight artifact.
func ModelSpecFromDiskWeight(dw DiskWeight) ModelSpec {
	weightMB := dw.Bytes / (1024 * 1024)
	if weightMB == 0 && dw.Bytes > 0 {
		weightMB = 1
	}
	format := ModelFormat(strings.ToLower(dw.Format))
	if format == "" {
		format = ModelFormatGGUF
	}

	// Default heuristic overheads: 512 MiB context, 256 MiB runtime overhead.
	ctxOverhead := int64(512)
	runOverhead := int64(256)

	accelerators := []AcceleratorType{
		AcceleratorCUDA,
		AcceleratorMetal,
		AcceleratorROCm,
		AcceleratorCPU,
	}

	engines := []string{"llama.cpp"}
	if format == ModelFormatOllama {
		engines = []string{"ollama"}
	}

	return ModelSpec{
		Schema:           "axis.model-spec/v1",
		ID:               ModelSpecID(dw.Name, format, weightMB),
		Name:             dw.Name,
		Format:           format,
		WeightsPath:      dw.Path,
		Source:           dw.Source,
		ObservedAt:       time.Now().UTC(),
		Accelerators:     accelerators,
		SupportedEngines: engines,
		ParallelismModes: []string{"single-node"},
		Memory: ModelMemoryRequirements{
			WeightSizeMB:      weightMB,
			ContextOverheadMB: ctxOverhead,
			RuntimeOverheadMB: runOverhead,
			MinVRAMMB:         0,
			RecommendedVRAMMB: weightMB + ctxOverhead,
		},
	}
}

// ModelSpecFromPath creates a ModelSpec from a local file path and size in bytes.
func ModelSpecFromPath(path string, sizeBytes int64) ModelSpec {
	base := filepath.Base(path)
	ext := strings.ToLower(filepath.Ext(base))
	name := strings.TrimSuffix(base, filepath.Ext(base))

	var format ModelFormat
	switch ext {
	case ".gguf":
		format = ModelFormatGGUF
	case ".safetensors":
		format = ModelFormatSafeTensors
	default:
		format = ModelFormatGGUF
	}

	weightMB := sizeBytes / (1024 * 1024)
	if weightMB == 0 && sizeBytes > 0 {
		weightMB = 1
	}

	ctxOverhead := int64(512)
	runOverhead := int64(256)

	return ModelSpec{
		Schema:           "axis.model-spec/v1",
		ID:               ModelSpecID(name, format, weightMB),
		Name:             name,
		Format:           format,
		WeightsPath:      path,
		Source:           "path",
		ObservedAt:       time.Now().UTC(),
		Accelerators:     []AcceleratorType{AcceleratorCUDA, AcceleratorMetal, AcceleratorROCm, AcceleratorCPU},
		SupportedEngines: []string{"llama.cpp"},
		ParallelismModes: []string{"single-node"},
		Memory: ModelMemoryRequirements{
			WeightSizeMB:      weightMB,
			ContextOverheadMB: ctxOverhead,
			RuntimeOverheadMB: runOverhead,
			MinVRAMMB:         0,
			RecommendedVRAMMB: weightMB + ctxOverhead,
		},
	}
}
