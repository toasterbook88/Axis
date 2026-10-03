package models

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
)

const (
	// ModelRunSchema is the JSON schema of a launch profile.
	ModelRunSchema = "axis.model-run/v1"
	// PortSourceExplicit is an operator-chosen port.
	PortSourceExplicit = "explicit"
	// PortSourcePlanDefault is the plan command's default occupancy port.
	PortSourcePlanDefault = "plan-default"
	// DeviceKindDiscrete is a CUDA or ROCm device with its own VRAM.
	DeviceKindDiscrete = "discrete"
	// DeviceKindUnified is Apple unified memory, or a node marked unified.
	DeviceKindUnified = "unified"
	// DeviceKindCPU is a node with no discrete or unified accelerator.
	DeviceKindCPU = "cpu"
	// EngineLlamaCpp is the only engine PR1 can launch.
	EngineLlamaCpp = "llama.cpp"
	// ToolLlamaServer is the observed tool name for llama.cpp.
	ToolLlamaServer = "llama-server"
	// ArtifactWeightsPath is a local weight file, not an Ollama model name.
	ArtifactWeightsPath = "weights-path"
)

// ModelRunProfile is the launch description shared by model plan and model start.
// It does not exec. Argv is derived from it.
type ModelRunProfile struct {
	Schema                string         `json:"schema"`
	Node                  string         `json:"node"`
	Engine                string         `json:"engine"`
	EngineBinary          string         `json:"engine_binary,omitempty"`
	ToolName              string         `json:"tool_name,omitempty"`
	SpecID                string         `json:"spec_id,omitempty"`
	ArtifactKind          string         `json:"artifact_kind,omitempty"`
	WeightsPath           string         `json:"weights_path,omitempty"`
	OllamaModel           string         `json:"ollama_model,omitempty"`
	Format                ModelFormat    `json:"format,omitempty"`
	Quantization          string         `json:"quantization,omitempty"`
	Volume                string         `json:"volume,omitempty"`
	SpecSource            string         `json:"spec_source,omitempty"`
	DeviceKind            string         `json:"device_kind,omitempty"`
	DeviceIndex           *int           `json:"device_index,omitempty"`
	IndexSource           string         `json:"index_source,omitempty"`
	DeviceModel           string         `json:"device_model,omitempty"`
	MemoryTopology        MemoryTopology `json:"memory_topology,omitempty"`
	VRAMFreeMB            int64          `json:"vram_free_mb,omitempty"`
	VRAMFreeMeasured      bool           `json:"vram_free_measured,omitempty"`
	Accelerator           string         `json:"accelerator,omitempty"`
	BindHost              string         `json:"bind_host"`
	Port                  int            `json:"port"`
	ContextTokens         *int           `json:"context_tokens,omitempty"`
	NGPULayers            *int           `json:"n_gpu_layers,omitempty"`
	NGPULayersMode        string         `json:"n_gpu_layers_mode,omitempty"`
	BatchSize             *int           `json:"batch_size,omitempty"`
	UBatchSize            *int           `json:"ubatch_size,omitempty"`
	Threads               *int           `json:"threads,omitempty"`
	OllamaNumCtx          *int           `json:"ollama_num_ctx,omitempty"`
	OllamaKeepAlive       string         `json:"ollama_keep_alive,omitempty"`
	OllamaNumGPU          *int           `json:"ollama_num_gpu,omitempty"`
	MLXModel              string         `json:"mlx_model,omitempty"`
	PrefillStepSize       *int           `json:"prefill_step_size,omitempty"`
	PromptCacheBytes      *int64         `json:"prompt_cache_bytes,omitempty"`
	KVBits                *int           `json:"kv_bits,omitempty"`
	PortSource            string         `json:"port_source,omitempty"`
	SnapshotPublicationID string         `json:"snapshot_publication_id,omitempty"`
	Refusals              []string       `json:"refusals,omitempty"`
}

// LaunchDevice is the one device a llama-server launch is allowed to name.
type LaunchDevice struct {
	Kind             string
	Model            string
	Accelerator      string
	MemoryTopology   MemoryTopology
	VRAMFreeMB       int64
	VRAMFreeMeasured bool
}

// NamedLocalVolume returns the longest non-network mount that contains weights.
// The match is the historical namedLocalVolume rule: a mount of "/" does not
// prefix-match other absolute paths, because the prefix used is "//".
func NamedLocalVolume(node NodeFacts, weights string) (string, bool) {
	if node.Resources == nil {
		return "", false
	}
	weights = path.Clean(strings.TrimSpace(weights))
	best := ""
	for _, vol := range node.Resources.Volumes {
		if vol.Kind == "network" || vol.Mount == "" {
			continue
		}
		mount := path.Clean(vol.Mount)
		if weights == mount || strings.HasPrefix(weights, mount+"/") {
			if len(mount) > len(best) {
				best = mount
			}
		}
	}
	if best == "" {
		return "", false
	}
	return best, true
}

// MeasuredFreeVRAM is the free-VRAM reading shared with the placement planner.
// A negative free value is unmeasured and is not used. A positive free value is
// measured even when the collector omitted the flag. A flagged zero stays zero.
func MeasuredFreeVRAM(gpu GPUInfo) (freeMB int64, measured bool) {
	total := int64(gpu.VRAMMB)
	if gpu.VRAMFreeMB < 0 {
		return total, false
	}
	if gpu.VRAMFreeMeasured || gpu.VRAMFreeMB > 0 {
		return int64(gpu.VRAMFreeMB), true
	}
	return total, false
}

// ObserveLaunchDevice classifies the node for a llama-server launch.
// Unified topology wins even when a discrete GPU is also present, and that
// result does not report measured discrete VRAM. Otherwise the best discrete
// device wins by measured-or-total free VRAM, then by total VRAM.
func ObserveLaunchDevice(node NodeFacts) LaunchDevice {
	if node.Resources == nil {
		return LaunchDevice{Kind: DeviceKindCPU, Accelerator: "cpu"}
	}
	if node.Resources.MemoryTopology == MemoryTopologyUnified {
		return unifiedLaunchDevice(node.Resources)
	}
	if gpu, acc, ok := bestDiscreteGPU(node.Resources.GPUs); ok {
		free, measured := MeasuredFreeVRAM(gpu)
		return LaunchDevice{
			Kind:             DeviceKindDiscrete,
			Model:            gpu.Model,
			Accelerator:      acc,
			MemoryTopology:   node.Resources.MemoryTopology,
			VRAMFreeMB:       free,
			VRAMFreeMeasured: measured,
		}
	}
	if gpu, ok := firstMetalGPU(node.Resources.GPUs); ok {
		return LaunchDevice{
			Kind:           DeviceKindUnified,
			Model:          gpu.Model,
			Accelerator:    "metal",
			MemoryTopology: node.Resources.MemoryTopology,
		}
	}
	return LaunchDevice{
		Kind:           DeviceKindCPU,
		Accelerator:    "cpu",
		MemoryTopology: node.Resources.MemoryTopology,
	}
}

func unifiedLaunchDevice(res *Resources) LaunchDevice {
	dev := LaunchDevice{
		Kind:           DeviceKindUnified,
		Accelerator:    "cpu",
		MemoryTopology: MemoryTopologyUnified,
	}
	if gpu, ok := firstMetalGPU(res.GPUs); ok {
		dev.Model = gpu.Model
		dev.Accelerator = "metal"
		return dev
	}
	if len(res.GPUs) > 0 {
		dev.Model = res.GPUs[0].Model
	}
	return dev
}

func bestDiscreteGPU(gpus []GPUInfo) (GPUInfo, string, bool) {
	var best GPUInfo
	var bestAcc string
	var bestFree, bestTotal int64
	found := false
	for _, gpu := range gpus {
		acc, ok := discreteAccelerator(gpu)
		if !ok {
			continue
		}
		free, _ := MeasuredFreeVRAM(gpu)
		total := int64(gpu.VRAMMB)
		if !found || free > bestFree || (free == bestFree && total > bestTotal) {
			best = gpu
			bestAcc = acc
			bestFree = free
			bestTotal = total
			found = true
		}
	}
	return best, bestAcc, found
}

func discreteAccelerator(gpu GPUInfo) (string, bool) {
	if gpu.HasCapability("cuda") || strings.EqualFold(gpu.Vendor, "nvidia") {
		return "cuda", true
	}
	if gpu.HasCapability("rocm") || strings.EqualFold(gpu.Vendor, "amd") {
		return "rocm", true
	}
	return "", false
}

func firstMetalGPU(gpus []GPUInfo) (GPUInfo, bool) {
	for _, gpu := range gpus {
		if gpu.HasCapability("metal") || strings.EqualFold(gpu.Vendor, "apple") {
			return gpu, true
		}
	}
	return GPUInfo{}, false
}

// ParseNGPULayers parses the start flag. An integer of at least 1 and the
// tokens auto and all are the only accepted values.
func ParseNGPULayers(raw string) (*int, string, error) {
	raw = strings.TrimSpace(raw)
	switch raw {
	case "auto", "all":
		return nil, raw, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return nil, "", fmt.Errorf("n-gpu-layers must be an integer >= 1, auto, or all")
	}
	return &n, "", nil
}

// LoadModelRunProfile decodes one axis.model-run/v1 object.
// A placement plan, or a document that wraps the profile, is rejected.
func LoadModelRunProfile(data []byte) (ModelRunProfile, error) {
	var header struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return ModelRunProfile{}, err
	}
	if header.Schema != ModelRunSchema {
		return ModelRunProfile{}, fmt.Errorf("expected axis.model-run/v1")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var profile ModelRunProfile
	if err := dec.Decode(&profile); err != nil {
		return ModelRunProfile{}, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return ModelRunProfile{}, fmt.Errorf("expected axis.model-run/v1")
	}
	return profile, nil
}

// NewPlanProfile builds the advisory profile for one node. Refusals are
// recorded on the profile. The caller does not exec it.
func NewPlanProfile(node NodeFacts, spec ModelSpec, port int, publicationID string) ModelRunProfile {
	weights := path.Clean(strings.TrimSpace(spec.WeightsPath))
	profile := ModelRunProfile{
		Schema:                ModelRunSchema,
		Node:                  node.Name,
		Engine:                EngineLlamaCpp,
		ToolName:              ToolLlamaServer,
		SpecID:                spec.ID,
		ArtifactKind:          ArtifactWeightsPath,
		WeightsPath:           weights,
		Format:                spec.Format,
		Quantization:          spec.Quantization,
		SpecSource:            spec.Source,
		BindHost:              "127.0.0.1",
		Port:                  port,
		PortSource:            PortSourcePlanDefault,
		SnapshotPublicationID: publicationID,
	}
	if tool, ok := toolByName(node, ToolLlamaServer); ok {
		profile.EngineBinary = tool.Path
	} else {
		profile.Refusals = append(profile.Refusals, fmt.Sprintf("node %s has no observed llama-server tool", node.Name))
	}
	if weights == "" || weights == "." {
		profile.Refusals = append(profile.Refusals, "weights path is required")
	} else if mount, ok := NamedLocalVolume(node, weights); ok {
		profile.Volume = mount
	} else {
		profile.Refusals = append(profile.Refusals, fmt.Sprintf("weights %s are not on a named local volume", weights))
	}
	applyLaunchDevice(&profile, ObserveLaunchDevice(node))
	return profile
}

func toolByName(node NodeFacts, name string) (ToolInfo, bool) {
	for _, tool := range node.Tools {
		if strings.EqualFold(tool.Name, name) {
			return tool, true
		}
	}
	return ToolInfo{}, false
}

func applyLaunchDevice(profile *ModelRunProfile, dev LaunchDevice) {
	profile.DeviceKind = dev.Kind
	profile.DeviceModel = dev.Model
	profile.Accelerator = dev.Accelerator
	profile.MemoryTopology = dev.MemoryTopology
	profile.VRAMFreeMB = dev.VRAMFreeMB
	profile.VRAMFreeMeasured = dev.VRAMFreeMeasured
}

// Validate checks the closed llama-server field set. Fact-plane refusals
// (tool, volume, measured VRAM, CPU count) are not decided here.
func (p ModelRunProfile) Validate() error {
	if p.Schema != ModelRunSchema {
		return fmt.Errorf("expected axis.model-run/v1")
	}
	if p.Engine != EngineLlamaCpp {
		return fmt.Errorf("engine %q is not supported", p.Engine)
	}
	if p.BindHost != "127.0.0.1" {
		return fmt.Errorf("bind host must be 127.0.0.1")
	}
	if p.Port < 1 || p.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	weights := path.Clean(strings.TrimSpace(p.WeightsPath))
	if weights == "" || weights == "." {
		return fmt.Errorf("weights path is required")
	}
	if p.DeviceIndex != nil || p.IndexSource != "" {
		return fmt.Errorf("device index is not supported")
	}
	if err := p.validateLaunchFields(); err != nil {
		return err
	}
	if p.hasForeignEngineFields() {
		return fmt.Errorf("only llama-server launch fields are supported")
	}
	return nil
}

func (p ModelRunProfile) validateLaunchFields() error {
	if p.NGPULayers != nil && p.NGPULayersMode != "" {
		return fmt.Errorf("n-gpu-layers accepts an integer or a mode, not both")
	}
	if p.NGPULayers != nil && *p.NGPULayers < 1 {
		return fmt.Errorf("n-gpu-layers must be an integer >= 1, auto, or all")
	}
	if p.NGPULayersMode != "" && p.NGPULayersMode != "auto" && p.NGPULayersMode != "all" {
		return fmt.Errorf("n-gpu-layers must be an integer >= 1, auto, or all")
	}
	if p.ContextTokens != nil && *p.ContextTokens < 1 {
		return fmt.Errorf("ctx-size must be >= 1")
	}
	if p.BatchSize != nil && *p.BatchSize < 1 {
		return fmt.Errorf("batch-size must be >= 1")
	}
	if p.UBatchSize != nil && *p.UBatchSize < 1 {
		return fmt.Errorf("ubatch-size must be >= 1")
	}
	if p.Threads != nil && *p.Threads < 1 {
		return fmt.Errorf("threads must be >= 1")
	}
	return nil
}

func (p ModelRunProfile) hasForeignEngineFields() bool {
	return p.OllamaModel != "" || p.OllamaNumCtx != nil || p.OllamaKeepAlive != "" || p.OllamaNumGPU != nil ||
		p.MLXModel != "" || p.PrefillStepSize != nil || p.PromptCacheBytes != nil || p.KVBits != nil
}
