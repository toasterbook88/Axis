package facts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteBundleKeepsEveryNvidiaSMIRow(t *testing.T) {
	if !strings.Contains(remoteFactBundleScript, "{ nvidia-smi "+nvidiaSMIMemoryQuery) ||
		!strings.Contains(remoteFactBundleScript, "} | base64 | tr -d '\\n'") {
		t.Fatal("nvidia-smi success path is not grouped into base64")
	}
	dir := t.TempDir()
	stub := "#!/bin/sh\nprintf '%s\\n' '0, RTX 4090, 24576, 20000' '1, RTX 4090, 24576, 100'\n"
	if err := os.WriteFile(filepath.Join(dir, "nvidia-smi"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", remoteFactBundleScript)
	cmd.Env = append(os.Environ(), "PATH="+dir+":/usr/bin:/bin")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bundle: %v\n%s", err, out)
	}
	kv, err := parseRemoteFactBundle(string(out))
	if err != nil {
		t.Fatal(err)
	}
	gpus := parseNvidiaSMIOutput(strings.TrimSpace(b64field(kv, "gpu_b64")))
	if len(gpus) != 2 || gpus[0].Index == nil || *gpus[0].Index != 0 || gpus[1].Index == nil || *gpus[1].Index != 1 {
		t.Fatalf("gpus=%+v bundle=%s", gpus, out)
	}
}
