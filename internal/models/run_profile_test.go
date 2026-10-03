package models

import (
	"strings"
	"testing"
)

func TestNamedLocalVolumeSkipsNetworkAndKeepsLongestMount(t *testing.T) {
	node := NodeFacts{Resources: &Resources{Volumes: []Volume{
		{Mount: "/", Kind: "local"},
		{Mount: "/mnt/models", Kind: "local"},
		{Mount: "/mnt/models/hot", Kind: "local"},
		{Mount: "/mnt/nas", Kind: "network"},
		{Mount: "", Kind: "local"},
	}}}

	mount, ok := NamedLocalVolume(node, "/mnt/models/hot/a.gguf")
	if !ok || mount != "/mnt/models/hot" {
		t.Fatalf("mount=%q ok=%v, want /mnt/models/hot", mount, ok)
	}
	if _, ok := NamedLocalVolume(node, "/mnt/nas/a.gguf"); ok {
		t.Fatal("network mount must not win")
	}
	if mount, ok = NamedLocalVolume(node, "/mnt/models"); !ok || mount != "/mnt/models" {
		t.Fatalf("exact mount=%q ok=%v", mount, ok)
	}
	// A mount of "/" uses prefix "//", so it does not match every absolute path.
	if _, ok := NamedLocalVolume(node, "/etc/passwd"); ok {
		t.Fatal("root mount must not match an unrelated absolute path")
	}
	if _, ok := NamedLocalVolume(NodeFacts{}, "/mnt/models/a.gguf"); ok {
		t.Fatal("missing resources must not match")
	}
}

func TestMeasuredFreeVRAMMatchesPlannerRules(t *testing.T) {
	cases := []struct {
		name     string
		gpu      GPUInfo
		wantFree int64
		wantMeas bool
	}{
		{name: "negative is unmeasured total", gpu: GPUInfo{VRAMMB: 8000, VRAMFreeMB: -1, VRAMFreeMeasured: true}, wantFree: 8000},
		{name: "measured zero stays zero", gpu: GPUInfo{VRAMMB: 8000, VRAMFreeMB: 0, VRAMFreeMeasured: true}, wantFree: 0, wantMeas: true},
		{name: "unflagged zero uses total", gpu: GPUInfo{VRAMMB: 8000}, wantFree: 8000},
		{name: "positive free is measured without the flag", gpu: GPUInfo{VRAMMB: 8000, VRAMFreeMB: 1000}, wantFree: 1000, wantMeas: true},
		{name: "flagged positive free", gpu: GPUInfo{VRAMMB: 8000, VRAMFreeMB: 1000, VRAMFreeMeasured: true}, wantFree: 1000, wantMeas: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			free, measured := MeasuredFreeVRAM(tc.gpu)
			if free != tc.wantFree || measured != tc.wantMeas {
				t.Fatalf("free=%d measured=%v, want %d %v", free, measured, tc.wantFree, tc.wantMeas)
			}
		})
	}
}

func TestObserveLaunchDevice(t *testing.T) {
	measured := GPUInfo{Vendor: "nvidia", Model: "RTX 4090", VRAMMB: 24576, VRAMFreeMB: 20000, VRAMFreeMeasured: true, Capabilities: []string{"cuda"}}
	smaller := GPUInfo{Vendor: "nvidia", Model: "RTX 3060", VRAMMB: 12288, VRAMFreeMB: 4000, VRAMFreeMeasured: true, Capabilities: []string{"cuda"}}
	node := NodeFacts{Resources: &Resources{GPUs: []GPUInfo{smaller, measured}}}
	got := ObserveLaunchDevice(node)
	if got.Kind != DeviceKindDiscrete || got.Accelerator != "cuda" || got.Model != "RTX 4090" || !got.VRAMFreeMeasured || got.VRAMFreeMB != 20000 {
		t.Fatalf("discrete device = %+v", got)
	}

	unified := NodeFacts{Resources: &Resources{
		MemoryTopology: MemoryTopologyUnified,
		GPUs:           []GPUInfo{measured},
	}}
	got = ObserveLaunchDevice(unified)
	if got.Kind != DeviceKindUnified || got.VRAMFreeMeasured {
		t.Fatalf("unified topology must not expose a discrete measured device: %+v", got)
	}

	apple := NodeFacts{Resources: &Resources{GPUs: []GPUInfo{{
		Vendor: "apple", Model: "M3", Capabilities: []string{"metal"},
	}}}}
	got = ObserveLaunchDevice(apple)
	if got.Kind != DeviceKindUnified || got.Accelerator != "metal" {
		t.Fatalf("apple gpu = %+v", got)
	}

	got = ObserveLaunchDevice(NodeFacts{})
	if got.Kind != DeviceKindCPU || got.Accelerator != "cpu" || got.VRAMFreeMeasured {
		t.Fatalf("cpu = %+v", got)
	}
}

func TestParseNGPULayers(t *testing.T) {
	n, mode, err := ParseNGPULayers("12")
	if err != nil || mode != "" || n == nil || *n != 12 {
		t.Fatalf("n=%v mode=%q err=%v", n, mode, err)
	}
	n, mode, err = ParseNGPULayers("auto")
	if err != nil || n != nil || mode != "auto" {
		t.Fatalf("n=%v mode=%q err=%v", n, mode, err)
	}
	if _, _, err := ParseNGPULayers("all"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"0", "-1", "nope", ""} {
		if _, _, err := ParseNGPULayers(bad); err == nil || !strings.Contains(err.Error(), "n-gpu-layers") {
			t.Fatalf("%q err=%v", bad, err)
		}
	}
}

func TestLoadModelRunProfileRejectsPlanDocument(t *testing.T) {
	_, err := LoadModelRunProfile([]byte(`{"schema":"axis.model-plan/v1","best_candidate":"gpu"}`))
	if err == nil || !strings.Contains(err.Error(), "expected axis.model-run/v1") {
		t.Fatalf("plan document err=%v", err)
	}
	_, err = LoadModelRunProfile([]byte(`{"selected":{"schema":"axis.model-run/v1"}}`))
	if err == nil || !strings.Contains(err.Error(), "expected axis.model-run/v1") {
		t.Fatalf("wrapper err=%v", err)
	}
	_, err = LoadModelRunProfile([]byte(`{"schema":"axis.model-run/v1","extra":true}`))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field err=%v", err)
	}
}

func TestNewPlanProfileRecordsToolAndVolumeRefusals(t *testing.T) {
	node := NodeFacts{
		Name: "gpu-node",
		Resources: &Resources{
			GPUs: []GPUInfo{{Vendor: "nvidia", Model: "RTX 4090", VRAMMB: 24576, Capabilities: []string{"cuda"}}},
		},
	}
	spec := ModelSpec{
		ID:           "ms-test",
		Name:         "qwen",
		Format:       ModelFormatGGUF,
		Source:       "disk-weight",
		WeightsPath:  "/mnt/models/qwen.gguf",
		Quantization: "Q4_K_M",
	}
	profile := NewPlanProfile(node, spec, 8080, "pub-1")
	if profile.Schema != ModelRunSchema || profile.PortSource != PortSourcePlanDefault {
		t.Fatalf("profile identity = %+v", profile)
	}
	if profile.Engine != "llama.cpp" || profile.ToolName != "llama-server" || profile.BindHost != "127.0.0.1" {
		t.Fatalf("profile launch = %+v", profile)
	}
	if profile.SpecID != "ms-test" || profile.Quantization != "Q4_K_M" || profile.SpecSource != "disk-weight" {
		t.Fatalf("spec copy = %+v", profile)
	}
	if profile.SnapshotPublicationID != "pub-1" || profile.NGPULayers != nil || profile.NGPULayersMode != "" {
		t.Fatalf("plan must not invent an offload flag: %+v", profile)
	}
	joined := strings.Join(profile.Refusals, "\n")
	if !strings.Contains(joined, "llama-server") || !strings.Contains(joined, "named local volume") {
		t.Fatalf("refusals=%v", profile.Refusals)
	}
	if profile.DeviceKind != DeviceKindDiscrete || profile.VRAMFreeMeasured {
		t.Fatalf("unmeasured discrete device = %+v", profile)
	}
}

func TestValidateOllamaAllowsModelNameAndRefusesForeignFields(t *testing.T) {
	profile := ModelRunProfile{
		Schema:       ModelRunSchema,
		Node:         "storage",
		Engine:       EngineOllama,
		ArtifactKind: ArtifactOllamaModelName,
		OllamaModel:  "mistral",
		BindHost:     "127.0.0.1",
	}
	if err := profile.Validate(); err != nil {
		t.Fatal(err)
	}
	ctx := 1024
	profile.OllamaNumCtx = &ctx
	profile.OllamaKeepAlive = "10m"
	if err := profile.Validate(); err != nil {
		t.Fatal(err)
	}
	gpus := 1
	profile.OllamaNumGPU = &gpus
	if err := profile.Validate(); err == nil || !strings.Contains(err.Error(), "num_gpu") {
		t.Fatalf("num_gpu err=%v", err)
	}
	profile.OllamaNumGPU = nil
	profile.MLXModel = "mlx"
	if err := profile.Validate(); err == nil || !strings.Contains(err.Error(), "mlx") {
		t.Fatalf("mlx err=%v", err)
	}
	profile.MLXModel = ""
	pin := 0
	profile.DeviceIndex = &pin
	profile.IndexSource = IndexSourceNvidiaSMI
	if err := profile.Validate(); err == nil || !strings.Contains(err.Error(), "main_gpu") {
		t.Fatalf("pin err=%v", err)
	}

	llama := ModelRunProfile{
		Schema:      ModelRunSchema,
		Engine:      EngineLlamaCpp,
		BindHost:    "127.0.0.1",
		Port:        8081,
		WeightsPath: "/mnt/models/a.gguf",
		OllamaModel: "mistral",
	}
	if err := llama.Validate(); err == nil || !strings.Contains(err.Error(), "llama-server") {
		t.Fatalf("llama with ollama field err=%v", err)
	}
}

func TestValidateMLXRequiresDirectoryAndRefusesForeignFields(t *testing.T) {
	profile := ModelRunProfile{
		Schema:       ModelRunSchema,
		Engine:       EngineMLX,
		ArtifactKind: ArtifactMLXModelDir,
		MLXModel:     "/mnt/models/qwen",
		BindHost:     "127.0.0.1",
		Port:         8080,
	}
	if err := profile.Validate(); err != nil {
		t.Fatal(err)
	}
	profile.WeightsPath = "/mnt/models/a.gguf"
	if err := profile.Validate(); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("weights err=%v", err)
	}
	profile.WeightsPath = ""
	profile.OllamaModel = "mistral"
	if err := profile.Validate(); err == nil || !strings.Contains(err.Error(), "only mlx") {
		t.Fatalf("ollama err=%v", err)
	}
	profile.OllamaModel = ""
	bits := 0
	profile.KVBits = &bits
	if err := profile.Validate(); err == nil || !strings.Contains(err.Error(), "kv-bits") {
		t.Fatalf("kv err=%v", err)
	}
}
