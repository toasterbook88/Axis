package facts

import (
	"strings"
	"testing"
)

func TestParseNvidiaSMIOutput_FourColumnsRecordIndex(t *testing.T) {
	input := "0, NVIDIA GeForce RTX 4090, 24564, 20000\n1, NVIDIA GeForce RTX 3080, 10240, 0\n"
	gpus := parseNvidiaSMIOutput(input)
	if len(gpus) != 2 {
		t.Fatalf("got %d gpus, want 2", len(gpus))
	}
	if gpus[0].Index == nil || *gpus[0].Index != 0 {
		t.Fatalf("gpu[0].Index = %v, want 0", gpus[0].Index)
	}
	if gpus[0].IndexSource != "nvidia-smi" {
		t.Fatalf("gpu[0].IndexSource = %q", gpus[0].IndexSource)
	}
	if gpus[0].Model != "NVIDIA GeForce RTX 4090" || gpus[0].VRAMMB != 24564 || gpus[0].VRAMFreeMB != 20000 || !gpus[0].VRAMFreeMeasured {
		t.Fatalf("gpu[0] = %+v", gpus[0])
	}
	if gpus[1].Index == nil || *gpus[1].Index != 1 || gpus[1].VRAMFreeMB != 0 || !gpus[1].VRAMFreeMeasured {
		t.Fatalf("gpu[1] = %+v", gpus[1])
	}
}

func TestParseNvidiaSMIOutput_NonIntegerIndexDropsRow(t *testing.T) {
	// Row 1: non-integer index → dropped. Row 2: valid index but unparseable
	// memory → kept with VRAMFreeMeasured=false (new behavior).
	input := "nope, NVIDIA GeForce RTX 4090, 24564, 20000\n0, NVIDIA GeForce RTX 3080, not-a-number, 10\n"
	gpus := parseNvidiaSMIOutput(input)
	if len(gpus) != 1 {
		t.Fatalf("got %d gpus, want 1 (only the valid-index row)", len(gpus))
	}
	if gpus[0].Model != "NVIDIA GeForce RTX 3080" {
		t.Errorf("Model = %q, want NVIDIA GeForce RTX 3080", gpus[0].Model)
	}
	if gpus[0].VRAMMB != 0 {
		t.Errorf("VRAMMB = %d, want 0 for unparseable memory", gpus[0].VRAMMB)
	}
	if gpus[0].VRAMFreeMB != 10 {
		t.Errorf("VRAMFreeMB = %d, want 10", gpus[0].VRAMFreeMB)
	}
	if !gpus[0].VRAMFreeMeasured {
		t.Error("VRAMFreeMeasured = false, want true for numeric free")
	}
}

func TestParseNvidiaSMIOutput_LegacyWidthsLeaveIndexNil(t *testing.T) {
	three := parseNvidiaSMIOutput("NVIDIA GeForce RTX 4090, 24564, 20480")
	if len(three) != 1 || three[0].Index != nil || three[0].IndexSource != "" || three[0].VRAMFreeMB != 20480 || !three[0].VRAMFreeMeasured {
		t.Fatalf("three-column = %+v", three)
	}
	two := parseNvidiaSMIOutput("NVIDIA GeForce RTX 4090, 24564")
	if len(two) != 1 || two[0].Index != nil || two[0].IndexSource != "" || two[0].VRAMFreeMeasured || two[0].VRAMMB != 24564 {
		t.Fatalf("two-column = %+v", two)
	}
}

func TestParseNvidiaSMIOutput_NATotalMemoryKeepsRow(t *testing.T) {
	input := "0, NVIDIA GeForce RTX 4090, [N/A], 20000\n"
	gpus := parseNvidiaSMIOutput(input)
	if len(gpus) != 1 {
		t.Fatalf("got %d gpus, want 1", len(gpus))
	}
	g := gpus[0]
	if g.Model != "NVIDIA GeForce RTX 4090" {
		t.Errorf("Model = %q", g.Model)
	}
	if g.VRAMMB != 0 {
		t.Errorf("VRAMMB = %d, want 0 for [N/A] total", g.VRAMMB)
	}
	if g.VRAMFreeMB != 20000 {
		t.Errorf("VRAMFreeMB = %d, want 20000", g.VRAMFreeMB)
	}
	if !g.VRAMFreeMeasured {
		t.Error("VRAMFreeMeasured = false, want true for numeric free")
	}
	if g.Index == nil || *g.Index != 0 {
		t.Errorf("Index = %v, want 0", g.Index)
	}
}

func TestParseNvidiaSMIOutput_NAFreeMemoryKeepsRow(t *testing.T) {
	input := "0, NVIDIA GeForce RTX 4090, 24564, [N/A]\n"
	gpus := parseNvidiaSMIOutput(input)
	if len(gpus) != 1 {
		t.Fatalf("got %d gpus, want 1", len(gpus))
	}
	g := gpus[0]
	if g.VRAMMB != 24564 {
		t.Errorf("VRAMMB = %d, want 24564", g.VRAMMB)
	}
	if g.VRAMFreeMB != 0 {
		t.Errorf("VRAMFreeMB = %d, want 0 for [N/A] free", g.VRAMFreeMB)
	}
	if g.VRAMFreeMeasured {
		t.Error("VRAMFreeMeasured = true, want false for [N/A] free")
	}
}

func TestParseNvidiaSMIOutput_BothNAMemoryKeepsRow(t *testing.T) {
	input := "0, NVIDIA GeForce RTX 4090, [N/A], [N/A]\n"
	gpus := parseNvidiaSMIOutput(input)
	if len(gpus) != 1 {
		t.Fatalf("got %d gpus, want 1", len(gpus))
	}
	g := gpus[0]
	if g.VRAMMB != 0 {
		t.Errorf("VRAMMB = %d, want 0", g.VRAMMB)
	}
	if g.VRAMFreeMB != 0 {
		t.Errorf("VRAMFreeMB = %d, want 0", g.VRAMFreeMB)
	}
	if g.VRAMFreeMeasured {
		t.Error("VRAMFreeMeasured = true, want false when both are [N/A]")
	}
	if g.Index == nil || *g.Index != 0 {
		t.Errorf("Index = %v, want 0", g.Index)
	}
}

func TestParseNvidiaSMIOutput_MixedNumericAndNARows(t *testing.T) {
	input := "0, NVIDIA GeForce RTX 4090, 24564, 20000\n1, NVIDIA GeForce RTX 3080, [N/A], [N/A]\n"
	gpus := parseNvidiaSMIOutput(input)
	if len(gpus) != 2 {
		t.Fatalf("got %d gpus, want 2", len(gpus))
	}
	if gpus[0].VRAMMB != 24564 || gpus[0].VRAMFreeMB != 20000 || !gpus[0].VRAMFreeMeasured {
		t.Errorf("gpu[0] = %+v", gpus[0])
	}
	if gpus[1].VRAMMB != 0 || gpus[1].VRAMFreeMB != 0 || gpus[1].VRAMFreeMeasured {
		t.Errorf("gpu[1] = %+v", gpus[1])
	}
	if gpus[1].Index == nil || *gpus[1].Index != 1 {
		t.Errorf("gpu[1].Index = %v, want 1", gpus[1].Index)
	}
}

func TestParseNvidiaSMIOutput_NumericRowsStillWork(t *testing.T) {
	input := "0, NVIDIA GeForce RTX 4090, 24564, 20000\n1, NVIDIA GeForce RTX 3080, 10240, 0\n"
	gpus := parseNvidiaSMIOutput(input)
	if len(gpus) != 2 {
		t.Fatalf("got %d gpus, want 2", len(gpus))
	}
	if gpus[0].VRAMMB != 24564 || gpus[0].VRAMFreeMB != 20000 || !gpus[0].VRAMFreeMeasured {
		t.Errorf("gpu[0] = %+v", gpus[0])
	}
	if gpus[1].VRAMMB != 10240 || gpus[1].VRAMFreeMB != 0 || !gpus[1].VRAMFreeMeasured {
		t.Errorf("gpu[1] = %+v", gpus[1])
	}
}

func TestMetalAndLspciLeaveGPUIndexNil(t *testing.T) {
	metal := parseSystemProfilerGPUs("Chipset Model: Apple M3\nVRAM (Dynamic, Max): 16384 MB\nMetal Family: Supported")
	for _, gpu := range metal {
		if gpu.Index != nil || gpu.IndexSource != "" {
			t.Fatalf("metal gpu index = %+v", gpu)
		}
	}
	lspci := "NVIDIA Corporation GA102 [GeForce RTX 3090]"
	gpu := parseNvidiaSMIOutput(lspci)
	if len(gpu) != 1 || gpu[0].Index != nil || gpu[0].IndexSource != "" {
		t.Fatalf("lspci-shaped line = %+v", gpu)
	}
	if strings.Contains(lspci, ", ") {
		t.Fatal("fixture accidentally looks like nvidia-smi csv")
	}
}
