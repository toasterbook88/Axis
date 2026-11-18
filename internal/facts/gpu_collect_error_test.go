package facts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// nvidia-smi prints its failures on stdout and exits non-zero, so the
// "nvidia-smi ... || lspci ..." collect command passes the failure on, followed
// by lspci's lines. The failure must never become a GPU.
const nvidiaSMIDriverFailure = "NVIDIA-SMI has failed because it couldn't communicate with the NVIDIA driver. " +
	"Make sure that the latest NVIDIA driver is installed and running."

const exampleIGPU = "Intel Corporation Example iGPU (rev 07)"

func TestGPUCollectOutputDropsNvidiaSMIFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		out  string
		want []string // models that must come back, in order; nothing else may
	}{
		"driver unreachable, lspci fallback": {nvidiaSMIDriverFailure + "\n" + exampleIGPU + "\n", []string{exampleIGPU}},
		"driver unreachable, nothing else":   {nvidiaSMIDriverFailure + "\n", nil},
		"no devices":                         {"No devices were found\n" + exampleIGPU + "\n", []string{exampleIGPU}},
		"NVML init failure":                  {"Failed to initialize NVML: Driver/library version mismatch\n" + exampleIGPU + "\n", []string{exampleIGPU}},
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

// An lspci GPU name can hold ", " ("Advanced Micro Devices, Inc."), so the
// format is chosen per line, not for the whole output.
func TestGPUCollectOutputClassifiesEachLine(t *testing.T) {
	const amd = "Advanced Micro Devices, Inc. [AMD/ATI] Example dGPU (rev c7)"
	const partial = "0, NVIDIA Example GPU A, 11264, 900\n" +
		"Unable to determine the device handle for GPU 0000:02:00.0: Unknown Error\n" +
		exampleIGPU + "\nNVIDIA Corporation Example GPU A\n"
	for name, tc := range map[string]struct {
		out     string
		models  []string
		vendors []string
	}{
		"failure then AMD lspci":    {nvidiaSMIDriverFailure + "\n" + amd + "\n", []string{amd}, []string{"amd"}},
		"AMD lspci, no nvidia-smi":  {amd + "\n", []string{amd}, []string{"amd"}},
		"AMD and Intel lspci":       {amd + "\n" + exampleIGPU + "\n", []string{amd, exampleIGPU}, []string{"amd", "intel"}},
		"partial failure and lspci": {partial, []string{"NVIDIA Example GPU A", exampleIGPU}, []string{"nvidia", "intel"}},
	} {
		gpus := parseGPUCollectOutput(tc.out)
		if len(gpus) != len(tc.models) {
			t.Errorf("%s: got %+v, want models %q", name, gpus, tc.models)
			continue
		}
		for i, g := range gpus {
			if g.Model != tc.models[i] || g.Vendor != tc.vendors[i] {
				t.Errorf("%s: GPU %d = %s %q, want %s %q", name, i, g.Vendor, g.Model, tc.vendors[i], tc.models[i])
			}
		}
	}
}

// The collect commands pass on only nvidia-smi data rows: failure text never
// leaves the machine, whatever its wording, and the lspci fallback runs only
// when nvidia-smi printed no row.
func TestGPUCollectCommandsPassOnlyNvidiaSMIRows(t *testing.T) {
	const row = "0, NVIDIA Example GPU A, 11264, 900"
	for name, tc := range map[string]struct {
		smi  string // what the stub nvidia-smi prints
		code int    // and its exit code
		want string // the GPU lines the command must print
	}{
		"driver unreachable": {nvidiaSMIDriverFailure, 9, "Example iGPU"},
		"partial failure":    {row + "\nUnable to determine the device handle for GPU 0000:02:00.0: Unknown Error", 15, row},
		"healthy":            {row, 0, row},
	} {
		dir := t.TempDir()
		files := map[string]string{
			"smi.out":    tc.smi + "\n",
			"nvidia-smi": "#!/bin/sh\ncat " + filepath.Join(dir, "smi.out") + "\nexit " + strconv.Itoa(tc.code) + "\n",
			"lspci":      "#!/bin/sh\necho '00:02.0 VGA compatible controller: Example iGPU'\n",
		}
		for file, body := range files {
			if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for _, script := range []string{linuxGPUCollectCommand(), remoteFactBundleScript} {
			cmd := exec.Command("bash", "-c", script)
			cmd.Env = withSandboxedPATH(dir)
			raw, _ := cmd.CombinedOutput()
			out := string(raw)
			if script == remoteFactBundleScript {
				kv, err := parseRemoteFactBundle(out)
				if err != nil {
					t.Fatalf("%s: bundle: %v", name, err)
				}
				out = b64field(kv, "gpu_b64")
			}
			if strings.Contains(out, "NVIDIA-SMI") || strings.Contains(out, "Unable to determine") {
				t.Errorf("%s: failure text left the machine: %q", name, out)
			}
			if got := strings.TrimSpace(out); got != tc.want {
				t.Errorf("%s: printed %q, want %q", name, got, tc.want)
			}
		}
	}
}
