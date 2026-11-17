package facts

import "testing"

// nvidia-smi prints its failures on stdout and exits non-zero, so the
// "nvidia-smi ... || lspci ..." collect command passes the failure on, followed
// by lspci's lines. The failure must never become a GPU.
const nvidiaSMIDriverFailure = "NVIDIA-SMI has failed because it couldn't communicate with the NVIDIA driver. " +
	"Make sure that the latest NVIDIA driver is installed and running."

func TestGPUCollectOutputDropsNvidiaSMIFailures(t *testing.T) {
	for name, out := range map[string]string{
		"driver unreachable, lspci fallback": nvidiaSMIDriverFailure + "\nIntel Corporation Example iGPU (rev 07)\n",
		"driver unreachable, nothing else":   nvidiaSMIDriverFailure + "\n",
		"no devices":                         "No devices were found\nIntel Corporation Example iGPU (rev 07)\n",
		"NVML init failure":                  "Failed to initialize NVML: Driver/library version mismatch\nIntel Corporation Example iGPU (rev 07)\n",
		"library missing":                    "NVIDIA-SMI couldn't find libnvidia-ml.so library in your system. Please make sure that the NVIDIA Display Driver is properly installed and present in your system.\n",
	} {
		for _, g := range parseGPUCollectOutput(out) {
			if g.Vendor == "nvidia" || g.Model != "Intel Corporation Example iGPU (rev 07)" {
				t.Errorf("%s: an nvidia-smi failure became a GPU: %+v", name, g)
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
