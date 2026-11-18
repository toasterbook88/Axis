package facts

import "testing"

// nvidia-smi prints its failures on stdout and exits non-zero, so the
// "nvidia-smi ... || lspci ..." collect command passes the failure on, followed
// by lspci's lines. The failure must never become a GPU.
const nvidiaSMIDriverFailure = "NVIDIA-SMI has failed because it couldn't communicate with the NVIDIA driver. " +
	"Make sure that the latest NVIDIA driver is installed and running."

func TestGPUCollectOutputDropsNvidiaSMIFailures(t *testing.T) {
	const igpu = "Intel Corporation Example iGPU (rev 07)"
	for name, tc := range map[string]struct {
		out  string
		want []string // models that must come back, in order; nothing else may
	}{
		"driver unreachable, lspci fallback": {nvidiaSMIDriverFailure + "\n" + igpu + "\n", []string{igpu}},
		"driver unreachable, nothing else":   {nvidiaSMIDriverFailure + "\n", nil},
		"no devices":                         {"No devices were found\n" + igpu + "\n", []string{igpu}},
		"NVML init failure":                  {"Failed to initialize NVML: Driver/library version mismatch\n" + igpu + "\n", []string{igpu}},
		"library missing": {"NVIDIA-SMI couldn't find libnvidia-ml.so library in your system. Please make sure that the NVIDIA " +
			"Display Driver is properly installed and present in your system.\n", nil},
	} {
		gpus := parseGPUCollectOutput(tc.out)
		var got []string
		for _, g := range gpus {
			if g.Vendor == "nvidia" {
				t.Errorf("%s: an nvidia-smi failure became a GPU: %+v", name, g)
			}
			got = append(got, g.Model)
		}
		if len(got) != len(tc.want) {
			t.Errorf("%s: got GPUs %q, want %q: the fallback GPU must survive the dropped failure", name, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: got GPUs %q, want %q", name, got, tc.want)
			}
		}
	}
}

func TestGPUCollectOutputStillReadsRealGPUs(t *testing.T) {
	gpus := parseGPUCollectOutput("0, NVIDIA Example GPU A, 11264, 900\n1, NVIDIA Example GPU B, 8151, 3500\n")
	if len(gpus) != 2 || gpus[0].Vendor != "nvidia" || gpus[1].Model != "NVIDIA Example GPU B" {
		t.Fatalf("nvidia-smi rows = %+v", gpus)
	}
	if gpus := parseGPUCollectOutput("NVIDIA Corporation Example 3D controller\n"); len(gpus) != 1 || gpus[0].Vendor != "nvidia" {
		t.Fatalf("an lspci NVIDIA line must still be read: %+v", gpus)
	}
	if gpus := parseGPUCollectOutput("  \n"); gpus != nil {
		t.Fatalf("empty output = %+v, want nil", gpus)
	}
}
