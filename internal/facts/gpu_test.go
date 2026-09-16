package facts

import (
	"testing"
)

func TestParseSystemProfilerGPUs_AppleSilicon(t *testing.T) {
	input := `Graphics/Displays:

    Apple M3 Pro:

      Chipset Model: Apple M3 Pro
      Type: GPU
      Bus: Built-In
      Total Number of Cores: 18
      Vendor: Apple (0x106b)
      Metal Family: Supported, Metal GPUFamily Apple 9

    Displays:
      Color LCD:
        Resolution: 3456 x 2234 Retina`

	gpus := parseSystemProfilerGPUs(input)
	if len(gpus) != 1 {
		t.Fatalf("expected 1 GPU, got %d", len(gpus))
	}
	g := gpus[0]
	if g.Model != "Apple M3 Pro" {
		t.Errorf("model = %q, want Apple M3 Pro", g.Model)
	}
	if g.Vendor != "apple" {
		t.Errorf("vendor = %q, want apple", g.Vendor)
	}
	if !g.HasCapability("metal") {
		t.Error("expected metal capability")
	}
}

func TestParseSystemProfilerGPUs_DiscreteWithVRAM(t *testing.T) {
	input := `Graphics/Displays:

    AMD Radeon Pro 5500M:

      Chipset Model: AMD Radeon Pro 5500M
      Type: GPU
      Bus: PCIe
      VRAM (Dynamic, Max): 4096 MB
      Vendor: AMD (0x1002)
      Metal Family: Supported, Metal GPUFamily macOS 2`

	gpus := parseSystemProfilerGPUs(input)
	if len(gpus) != 1 {
		t.Fatalf("expected 1 GPU, got %d", len(gpus))
	}
	g := gpus[0]
	if g.Model != "AMD Radeon Pro 5500M" {
		t.Errorf("model = %q", g.Model)
	}
	if g.Vendor != "amd" {
		t.Errorf("vendor = %q, want amd", g.Vendor)
	}
	if g.VRAMMB != 4096 {
		t.Errorf("VRAM = %d, want 4096", g.VRAMMB)
	}
	if !g.HasCapability("metal") {
		t.Error("expected metal capability")
	}
}

func TestParseSystemProfilerGPUs_MultipleGPUs(t *testing.T) {
	input := `Graphics/Displays:

    Intel UHD Graphics 630:
      Chipset Model: Intel UHD Graphics 630
      Type: GPU
      Bus: Built-In
      VRAM (Dynamic, Max): 1536 MB
      Vendor: Intel (0x8086)
      Metal Family: Supported, Metal GPUFamily macOS 2

    AMD Radeon Pro 5500M:
      Chipset Model: AMD Radeon Pro 5500M
      Type: GPU
      Bus: PCIe
      VRAM (Dynamic, Max): 4096 MB
      Vendor: AMD (0x1002)
      Metal Family: Supported, Metal GPUFamily macOS 2`

	gpus := parseSystemProfilerGPUs(input)
	if len(gpus) != 2 {
		t.Fatalf("expected 2 GPUs, got %d", len(gpus))
	}
	if gpus[0].Vendor != "intel" {
		t.Errorf("first GPU vendor = %q, want intel", gpus[0].Vendor)
	}
	if gpus[1].Vendor != "amd" {
		t.Errorf("second GPU vendor = %q, want amd", gpus[1].Vendor)
	}
}

func TestParseNvidiaSMIOutput(t *testing.T) {
	input := `NVIDIA GeForce RTX 4090, 24564
NVIDIA GeForce MX250, 2048`

	gpus := parseNvidiaSMIOutput(input)
	if len(gpus) != 2 {
		t.Fatalf("expected 2 GPUs, got %d", len(gpus))
	}

	if gpus[0].Model != "NVIDIA GeForce RTX 4090" {
		t.Errorf("gpu[0].Model = %q", gpus[0].Model)
	}
	if gpus[0].VRAMMB != 24564 {
		t.Errorf("gpu[0].VRAM = %d, want 24564", gpus[0].VRAMMB)
	}
	if gpus[0].Vendor != "nvidia" {
		t.Errorf("gpu[0].Vendor = %q", gpus[0].Vendor)
	}
	if !gpus[0].HasCapability("cuda") {
		t.Error("expected cuda capability on RTX 4090")
	}

	if gpus[1].VRAMMB != 2048 {
		t.Errorf("gpu[1].VRAM = %d, want 2048", gpus[1].VRAMMB)
	}
}

func TestParseNvidiaSMIOutput_Empty(t *testing.T) {
	gpus := parseNvidiaSMIOutput("")
	if len(gpus) != 0 {
		t.Errorf("expected 0 GPUs from empty, got %d", len(gpus))
	}
}

func TestParseVRAMMB(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"16 GB", 16384},
		{"4096 MB", 4096},
		{"2048 MB", 2048},
		{"8 GB", 8192},
		{"", 0},
		{"unknown", 0},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseVRAMMB(tt.input)
			if got != tt.want {
				t.Errorf("parseVRAMMB(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}
