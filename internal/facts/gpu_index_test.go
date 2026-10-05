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
	input := "nope, NVIDIA GeForce RTX 4090, 24564, 20000\n0, NVIDIA GeForce RTX 3080, not-a-number, 10\n"
	gpus := parseNvidiaSMIOutput(input)
	if len(gpus) != 0 {
		t.Fatalf("bad cells must drop the row, got %+v", gpus)
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
