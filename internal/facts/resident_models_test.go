package facts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

// minimalRemoteExec builds the base fake executor map that satisfies the
// RemoteCollector's mandatory probes for a Linux worker node.
func minimalRemoteExec() map[string]fakeRunResult {
	return map[string]fakeRunResult{
		"uname -s": {out: "Linux\n"},
		"hostname": {out: "worker-1\n"},
		"cat /etc/machine-id 2>/dev/null || hostnamectl --static 2>/dev/null || hostname": {out: "worker-1-id\n"},
		"uname -m": {out: "x86_64\n"},
		"uname -r": {out: "6.8.0\n"},
		"nproc":    {out: "8\n"},
		"cat /proc/cpuinfo | awk -F: '/model name/ {gsub(/^ /, \"\", $2); print $2; exit}'": {out: "AMD Ryzen\n"},
		"grep MemTotal /proc/meminfo | awk '{print $2}'":                                    {out: "16777216\n"},
		"grep MemAvailable /proc/meminfo | awk '{print $2}'":                                {out: "8388608\n"},
		"cut -d' ' -f1-3 /proc/loadavg":                                                     {out: "0.10 0.20 0.30\n"},
		"df -kP / | tail -1":                                                                {out: "/dev/nvme0n1 1048576 524288 524288 50% /\n"},
		"df -kP /": {out: `Filesystem 1024-blocks Used Available Capacity Mounted on
/dev/nvme0n1 1048576 524288 524288 50% /
`},
		"df -kPl": {out: `Filesystem 1024-blocks Used Available Capacity Mounted on
/dev/nvme0n1 1048576 524288 524288 50% /
tmpfs 8388608 0 8388608 0% /tmp
/dev/sda1 2097152 1048576 1048576 50% /mnt/models
`},
		`if [ -r /proc/mounts ]; then cat /proc/mounts; else mount; fi`: {out: ""},

		"cat /proc/pressure/memory 2>/dev/null": {out: "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n"},
		`if command -v ip >/dev/null 2>&1; then ip -o addr show scope global 2>/dev/null || ip addr show scope global | awk '/inet/ {print $2}'; else ifconfig 2>/dev/null | awk '/^[a-z]/ {iface=$1} /inet / && !/127.0.0.1/ {print iface, $2}; /inet6 / && !/::1/ && !/fe80/ {print iface, $2}' | sed 's/://'; fi`: {out: "2: eth0    inet 10.0.0.5/24 brd 10.0.0.255 scope global eth0\n"},
		// Inference backends not installed on this baseline node.
		OllamaDiscoveryScript:      {out: `{"installed":false}`},
		LlamaServerDiscoveryScript: {out: `{"installed":false}`},
		MLXDiscoveryScript:         {out: `{"installed":false}`},
		DiskWeightsDiscoveryScript: {out: `{"weights":[],"truncated":false}`},
	}
}

func TestRemoteCollectorCollectsResidentModelsFromOllamaProbe(t *testing.T) {
	m := minimalRemoteExec()
	m[OllamaDiscoveryScript] = fakeRunResult{out: `{"installed":true,"path":"/usr/bin/ollama","version":"0.6.0","running":true,"listening":true,"port":11434,"models":["llama3:8b"],"resident_models":[{"name":"llama3:8b","runtime":"ollama","processor":"100% GPU","source":"ollama-ps"}],"gpu_offload":"gpu:cuda"}`}
	exec := &fakeRemoteExecutor{exact: m}

	collector := NewRemoteCollector("worker-1", "worker", "worker-1.internal", exec)
	facts, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(facts.ResidentModels) != 1 {
		t.Fatalf("resident models = %#v, want 1 model", facts.ResidentModels)
	}
	if got := facts.ResidentModels[0]; got.Name != "llama3:8b" || got.Runtime != "ollama" || got.Source != "ollama-ps" {
		t.Fatalf("unexpected resident model: %+v", got)
	}
	if facts.Ollama == nil || !facts.Ollama.Installed {
		t.Fatalf("expected ollama info to remain populated, got %+v", facts.Ollama)
	}
	if facts.Status != models.StatusComplete && facts.Status != models.StatusPartial {
		t.Fatalf("status = %s, want complete or partial", facts.Status)
	}
}

func TestRemoteCollectorCollectsResidentModelsFromLlamaServerProbe(t *testing.T) {
	m := minimalRemoteExec()
	m[LlamaServerDiscoveryScript] = fakeRunResult{out: `{"installed":true,"path":"/usr/local/bin/llama-server","version":"b3447","running":true,"listening":true,"port":8080,"resident_models":[{"name":"qwen2.5-coder-7b-q4","runtime":"llama.cpp","processor":"gpu","weight_size_mb":4384,"source":"llama-server-ps"}]}`}
	exec := &fakeRemoteExecutor{exact: m}

	collector := NewRemoteCollector("worker-1", "worker", "worker-1.internal", exec)
	facts, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(facts.ResidentModels) != 1 {
		t.Fatalf("resident models = %#v, want 1 llama-server model", facts.ResidentModels)
	}
	got := facts.ResidentModels[0]
	if got.Name != "qwen2.5-coder-7b-q4" {
		t.Errorf("Name = %q, want qwen2.5-coder-7b-q4", got.Name)
	}
	if got.Runtime != "llama.cpp" {
		t.Errorf("Runtime = %q, want llama.cpp", got.Runtime)
	}
	if got.Source != "llama-server-ps" {
		t.Errorf("Source = %q, want llama-server-ps", got.Source)
	}
	if got.WeightSizeMB != 4384 || got.SizeVRAMMB != 0 {
		t.Errorf("memory facts = %+v, want weight size 4384 and unknown VRAM", got)
	}
}

func TestRemoteCollectorCollectsResidentModelsFromMLXProbe(t *testing.T) {
	m := minimalRemoteExec()
	m[MLXDiscoveryScript] = fakeRunResult{out: `{"installed":true,"running":true,"port":8080,"resident_models":[{"name":"Qwen2.5-Coder-7B-Instruct-4bit","runtime":"mlx","processor":"gpu","size_ram_mb":4096,"source":"mlx-lm-api"}]}`}
	exec := &fakeRemoteExecutor{exact: m}

	collector := NewRemoteCollector("cortex", "primary", "cortex.local", exec)
	facts, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(facts.ResidentModels) != 1 {
		t.Fatalf("resident models = %#v, want 1 mlx model", facts.ResidentModels)
	}
	got := facts.ResidentModels[0]
	if got.Name != "Qwen2.5-Coder-7B-Instruct-4bit" {
		t.Errorf("Name = %q, want Qwen2.5-Coder-7B-Instruct-4bit", got.Name)
	}
	if got.Runtime != "mlx" {
		t.Errorf("Runtime = %q, want mlx", got.Runtime)
	}
	if got.Processor != "gpu" {
		t.Errorf("Processor = %q, want gpu", got.Processor)
	}
	if got.Source != "mlx-lm-api" {
		t.Errorf("Source = %q, want mlx-lm-api", got.Source)
	}
	if got.SizeRAMMB != 4096 || got.SizeVRAMMB != 0 {
		t.Errorf("memory facts = %+v, want resident RAM 4096 and unknown VRAM", got)
	}
}

func TestRemoteCollectorMLXNotInstalledReturnsNoResidentModels(t *testing.T) {
	m := minimalRemoteExec()
	m[MLXDiscoveryScript] = fakeRunResult{out: `{"installed":false}`}
	exec := &fakeRemoteExecutor{exact: m}

	collector := NewRemoteCollector("scout", "worker", "scout.local", exec)
	facts, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	for _, rm := range facts.ResidentModels {
		if rm.Runtime == "mlx" {
			t.Errorf("unexpected mlx resident model on node without mlx: %+v", rm)
		}
	}
}

func TestRemoteCollectorMergesOllamaAndLlamaServerResidentModels(t *testing.T) {
	m := minimalRemoteExec()
	m[OllamaDiscoveryScript] = fakeRunResult{out: `{"installed":true,"path":"/usr/bin/ollama","version":"0.6.0","running":true,"listening":true,"port":11434,"models":["llama3:8b"],"resident_models":[{"name":"llama3:8b","runtime":"ollama","processor":"100% GPU","size_vram_mb":4915,"source":"ollama-ps"}],"gpu_offload":"gpu:cuda"}`}
	m[LlamaServerDiscoveryScript] = fakeRunResult{out: `{"installed":true,"path":"/usr/local/bin/llama-server","version":"b3447","running":true,"listening":true,"port":8080,"resident_models":[{"name":"qwen2.5-coder-7b-q4","runtime":"llama.cpp","processor":"gpu","weight_size_mb":4384,"source":"llama-server-ps"}]}`}
	exec := &fakeRemoteExecutor{exact: m}

	collector := NewRemoteCollector("worker-1", "worker", "worker-1.internal", exec)
	facts, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(facts.ResidentModels) != 2 {
		t.Fatalf("resident models = %#v, want 2 (one ollama, one llama.cpp)", facts.ResidentModels)
	}
	runtimes := map[string]bool{}
	for _, m := range facts.ResidentModels {
		runtimes[m.Runtime] = true
	}
	if !runtimes["ollama"] {
		t.Error("expected an ollama resident model")
	}
	if !runtimes["llama.cpp"] {
		t.Error("expected a llama.cpp resident model")
	}
}

func TestRemoteCollectorMergesAllThreeResidentModelBackends(t *testing.T) {
	m := minimalRemoteExec()
	m[OllamaDiscoveryScript] = fakeRunResult{out: `{"installed":true,"path":"/usr/bin/ollama","version":"0.6.0","running":true,"listening":true,"port":11434,"models":["llama3:8b"],"resident_models":[{"name":"llama3:8b","runtime":"ollama","processor":"100% GPU","size_vram_mb":4915,"source":"ollama-ps"}],"gpu_offload":"gpu:cuda"}`}
	m[LlamaServerDiscoveryScript] = fakeRunResult{out: `{"installed":true,"path":"/usr/local/bin/llama-server","version":"b3447","running":true,"listening":true,"port":8080,"resident_models":[{"name":"qwen2.5-coder-7b-q4","runtime":"llama.cpp","processor":"gpu","weight_size_mb":4384,"source":"llama-server-ps"}]}`}
	m[MLXDiscoveryScript] = fakeRunResult{out: `{"installed":true,"running":true,"port":8080,"resident_models":[{"name":"Qwen2.5-Coder-7B-Instruct-4bit","runtime":"mlx","processor":"gpu","size_ram_mb":4096,"source":"mlx-lm-api"}]}`}
	exec := &fakeRemoteExecutor{exact: m}

	collector := NewRemoteCollector("cortex", "primary", "cortex.local", exec)
	facts, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(facts.ResidentModels) != 3 {
		t.Fatalf("resident models = %#v, want 3 (ollama + llama.cpp + mlx)", facts.ResidentModels)
	}
	runtimes := map[string]bool{}
	for _, rm := range facts.ResidentModels {
		runtimes[rm.Runtime] = true
	}
	for _, want := range []string{"ollama", "llama.cpp", "mlx"} {
		if !runtimes[want] {
			t.Errorf("expected a %q resident model in merged list", want)
		}
	}
}

// --- SizeVRAMMB field tests ---

// TestOllamaResidentModelCarriesSizeVRAMMB_GB verifies that the Ollama probe
// JSON (as emitted by the updated awk script for a GB-sized model) is parsed
// into the SizeVRAMMB field on the ResidentModel struct.
func TestOllamaResidentModelCarriesSizeVRAMMB_GB(t *testing.T) {
	m := minimalRemoteExec()
	// size_vram_mb: 4915 ≈ 4.8 GB (awk: int(4.8 * 1024 + 0.5) = 4915)
	m[OllamaDiscoveryScript] = fakeRunResult{out: `{"installed":true,"path":"/usr/bin/ollama","version":"0.6.0","running":true,"listening":true,"port":11434,"models":["llama3:8b"],"resident_models":[{"name":"llama3:8b","runtime":"ollama","processor":"100% GPU","size_vram_mb":4915,"source":"ollama-ps"}],"gpu_offload":"gpu:cuda"}`}
	exec := &fakeRemoteExecutor{exact: m}

	collector := NewRemoteCollector("cortex", "primary", "cortex.local", exec)
	facts, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(facts.ResidentModels) != 1 {
		t.Fatalf("resident models = %#v, want 1", facts.ResidentModels)
	}
	got := facts.ResidentModels[0]
	if got.SizeVRAMMB != 4915 {
		t.Errorf("SizeVRAMMB = %d, want 4915 (4.8 GB → awk int(4.8*1024+0.5))", got.SizeVRAMMB)
	}
}

// TestOllamaResidentModelCarriesSizeVRAMMB_MB verifies parsing when the probe
// emits a sub-GB value (MB unit path in the awk script).
func TestOllamaResidentModelCarriesSizeVRAMMB_MB(t *testing.T) {
	m := minimalRemoteExec()
	m[OllamaDiscoveryScript] = fakeRunResult{out: `{"installed":true,"path":"/usr/bin/ollama","version":"0.6.0","running":true,"listening":true,"port":11434,"models":["smol:latest"],"resident_models":[{"name":"smol:latest","runtime":"ollama","processor":"100% CPU","size_vram_mb":512,"source":"ollama-ps"}],"gpu_offload":"none"}`}
	exec := &fakeRemoteExecutor{exact: m}

	collector := NewRemoteCollector("cortex", "primary", "cortex.local", exec)
	facts, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(facts.ResidentModels) != 1 {
		t.Fatalf("resident models = %#v, want 1", facts.ResidentModels)
	}
	got := facts.ResidentModels[0]
	if got.SizeVRAMMB != 512 {
		t.Errorf("SizeVRAMMB = %d, want 512 (MB unit path)", got.SizeVRAMMB)
	}
}

// TestOllamaResidentModelZeroVRAMWhenSizeAbsent verifies that a probe JSON
// without size_vram_mb (e.g. an older script version) results in SizeVRAMMB == 0
// rather than an error.
func TestOllamaResidentModelZeroVRAMWhenSizeAbsent(t *testing.T) {
	m := minimalRemoteExec()
	m[OllamaDiscoveryScript] = fakeRunResult{out: `{"installed":true,"path":"/usr/bin/ollama","version":"0.5.0","running":true,"listening":true,"port":11434,"models":["llama3:8b"],"resident_models":[{"name":"llama3:8b","runtime":"ollama","processor":"100% GPU","source":"ollama-ps"}],"gpu_offload":"gpu:cuda"}`}
	exec := &fakeRemoteExecutor{exact: m}

	collector := NewRemoteCollector("cortex", "primary", "cortex.local", exec)
	facts, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(facts.ResidentModels) != 1 {
		t.Fatalf("resident models = %#v, want 1", facts.ResidentModels)
	}
	if got := facts.ResidentModels[0].SizeVRAMMB; got != 0 {
		t.Errorf("SizeVRAMMB = %d, want 0 when field absent in probe JSON", got)
	}
}

func TestLlamaServerResidentModelZeroVRAMWhenSizeUnavailable(t *testing.T) {
	m := minimalRemoteExec()
	m[LlamaServerDiscoveryScript] = fakeRunResult{out: `{"installed":true,"path":"/usr/local/bin/llama-server","version":"b3447","running":true,"listening":true,"port":8080,"resident_models":[{"name":"qwen2.5-coder-7b-q4","runtime":"llama.cpp","processor":"gpu","source":"llama-server-ps"}]}`}
	exec := &fakeRemoteExecutor{exact: m}

	collector := NewRemoteCollector("worker-1", "worker", "worker-1.internal", exec)
	facts, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(facts.ResidentModels) != 1 {
		t.Fatalf("resident models = %#v, want 1", facts.ResidentModels)
	}
	if got := facts.ResidentModels[0].SizeVRAMMB; got != 0 {
		t.Errorf("SizeVRAMMB = %d, want 0 when llama-server does not report live VRAM", got)
	}
}

func TestMLXResidentModelZeroVRAMWhenSizeUnavailable(t *testing.T) {
	m := minimalRemoteExec()
	m[MLXDiscoveryScript] = fakeRunResult{out: `{"installed":true,"running":true,"port":8080,"resident_models":[{"name":"Qwen2.5-Coder-7B-Instruct-4bit","runtime":"mlx","processor":"gpu","source":"mlx-lm-api"}]}`}
	exec := &fakeRemoteExecutor{exact: m}

	collector := NewRemoteCollector("cortex", "primary", "cortex.local", exec)
	facts, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(facts.ResidentModels) != 1 {
		t.Fatalf("resident models = %#v, want 1", facts.ResidentModels)
	}
	if got := facts.ResidentModels[0].SizeVRAMMB; got != 0 {
		t.Errorf("SizeVRAMMB = %d, want 0 when mlx-lm does not report live VRAM", got)
	}
}

// TestLlamaServerResidentModelCarriesWeightSize verifies that model-file bytes
// remain distinct from runtime-reported accelerator memory.
func TestLlamaServerResidentModelCarriesWeightSize(t *testing.T) {
	m := minimalRemoteExec()
	m[LlamaServerDiscoveryScript] = fakeRunResult{out: `{"installed":true,"path":"/usr/local/bin/llama-server","version":"b3447","running":true,"listening":true,"port":8080,"resident_models":[{"name":"qwen2.5-coder-7b-q4","runtime":"llama.cpp","processor":"gpu","weight_size_mb":4384,"source":"llama-server-ps"}]}`}
	exec := &fakeRemoteExecutor{exact: m}

	collector := NewRemoteCollector("worker-1", "worker", "worker-1.internal", exec)
	facts, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(facts.ResidentModels) != 1 {
		t.Fatalf("resident models = %#v, want 1", facts.ResidentModels)
	}
	got := facts.ResidentModels[0]
	if got.WeightSizeMB != 4384 || got.SizeVRAMMB != 0 {
		t.Errorf("memory facts = %+v, want weight size 4384 and unknown VRAM", got)
	}
}

// TestMLXResidentModelCarriesRAMSize verifies that process RSS remains
// distinct from runtime-reported accelerator memory.
func TestMLXResidentModelCarriesRAMSize(t *testing.T) {
	m := minimalRemoteExec()
	m[MLXDiscoveryScript] = fakeRunResult{out: `{"installed":true,"running":true,"port":8080,"resident_models":[{"name":"Qwen2.5-Coder-7B-Instruct-4bit","runtime":"mlx","processor":"gpu","size_ram_mb":4096,"source":"mlx-lm-api"}]}`}
	exec := &fakeRemoteExecutor{exact: m}

	collector := NewRemoteCollector("cortex", "primary", "cortex.local", exec)
	facts, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(facts.ResidentModels) != 1 {
		t.Fatalf("resident models = %#v, want 1", facts.ResidentModels)
	}
	got := facts.ResidentModels[0]
	if got.SizeRAMMB != 4096 || got.SizeVRAMMB != 0 {
		t.Errorf("memory facts = %+v, want resident RAM 4096 and unknown VRAM", got)
	}
}

// withSandboxedPATH puts stub binaries first. It keeps the runner PATH and
// appends NixOS's system profile so awk/sed/head/stat resolve even if the
// job PATH is still Ubuntu-shaped (/usr/bin) with no /nix/store links.
func withSandboxedPATH(bin string) []string {
	sep := string(os.PathListSeparator)
	path := bin + sep + os.Getenv("PATH") + sep + "/run/current-system/sw/bin" + sep + "/usr/bin" + sep + "/bin"
	return append(os.Environ(), "PATH="+path)
}

// withExactToolPATH makes a hermetic PATH from the test stubs plus symlinks to
// only the named host tools. It lets a test prove an executable is absent from
// PATH without assuming FHS paths, which NixOS intentionally does not provide.
func withExactToolPATH(t *testing.T, bin string, tools ...string) []string {
	t.Helper()
	for _, name := range tools {
		target := filepath.Join(bin, name)
		if _, err := os.Lstat(target); err == nil {
			continue
		}
		source, err := exec.LookPath(name)
		if err != nil {
			t.Skipf("required test tool %s not available: %v", name, err)
		}
		if err := os.Symlink(source, target); err != nil {
			t.Fatalf("link test tool %s: %v", name, err)
		}
	}
	return append(os.Environ(), "PATH="+bin)
}

// TestLlamaServerDiscoveryScriptReportsPortFromCmdline is the regression for
// the hardcoded-8080 bug: a live llama-server started with --port 8081 must
// be published on 8081, not the llama-server default. The script is the
// production parser (local + SSH), so this runs it under a PATH sandbox.
func TestLlamaServerDiscoveryScriptReportsPortFromCmdline(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	bin := t.TempDir()
	model := filepath.Join(t.TempDir(), "NVIDIA-Nemotron-3.5-Lightning-30B-A3B-IQ4_XS.gguf")
	if err := os.WriteFile(model, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeStub := func(name, body string) {
		t.Helper()
		p := filepath.Join(bin, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeStub("llama-server", `echo b9999`)
	writeStub("pgrep", `echo 4242`)
	writeStub("ps", `echo "llama-server -m `+model+` --port 8081 --host 127.0.0.1"`)
	writeStub("lsof", `exit 1`)
	writeStub("ss", `exit 1`)
	writeStub("netstat", `exit 1`)

	cmd := exec.Command("bash", "-c", LlamaServerDiscoveryScript)
	cmd.Env = withSandboxedPATH(bin)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script: %v\n%s", err, out)
	}
	var payload llamaServerDiscoveryPayload
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json %q: %v", bytes.TrimSpace(out), err)
	}
	if payload.Port != 8081 {
		t.Fatalf("port = %d, want 8081 (parsed from --port on llama-server argv)", payload.Port)
	}
	if len(payload.ResidentModels) != 1 || payload.ResidentModels[0].Name == "" {
		t.Fatalf("resident_models = %#v", payload.ResidentModels)
	}
}

func TestLlamaServerDiscoveryScriptReportsPortFromShortFlag(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	bin := t.TempDir()
	model := filepath.Join(t.TempDir(), "qwen.gguf")
	if err := os.WriteFile(model, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeStub := func(name, body string) {
		t.Helper()
		p := filepath.Join(bin, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeStub("llama-server", `echo b9999`)
	writeStub("pgrep", `echo 4242`)
	writeStub("ps", `echo "llama-server -m `+model+` -p 9090"`)
	writeStub("lsof", `exit 1`)
	writeStub("ss", `exit 1`)
	writeStub("netstat", `exit 1`)

	cmd := exec.Command("bash", "-c", LlamaServerDiscoveryScript)
	cmd.Env = withSandboxedPATH(bin)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script: %v\n%s", err, out)
	}
	var payload llamaServerDiscoveryPayload
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json %q: %v", bytes.TrimSpace(out), err)
	}
	if payload.Port != 9090 {
		t.Fatalf("port = %d, want 9090 (parsed from -p on llama-server argv)", payload.Port)
	}
}

// TestLlamaServerDiscoveryScriptFindsRunningBinaryOutsidePATH is the
// regression for supervised runtimes whose executable is referenced by an
// absolute service path but is not available in the collector's login PATH.
// A running process is direct evidence that llama-server is installed.
func TestLlamaServerDiscoveryScriptFindsRunningBinaryOutsidePATH(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	bin := t.TempDir()
	model := filepath.Join(t.TempDir(), "qwen-outside-path.gguf")
	if err := os.WriteFile(model, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeStub := func(name, body string) {
		t.Helper()
		p := filepath.Join(bin, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeStub("pgrep", `echo 4242`)
	writeStub("ps", `echo "/opt/llama.cpp/bin/llama-server --model `+model+` --port 8181"`)
	writeStub("readlink", `echo /opt/llama.cpp/bin/llama-server`)
	writeStub("lsof", `exit 1`)
	writeStub("ss", `exit 1`)
	writeStub("netstat", `exit 1`)

	cmd := exec.Command("bash", "-c", LlamaServerDiscoveryScript)
	cmd.Env = withExactToolPATH(t, bin, "head", "awk", "grep", "basename", "sed", "stat")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script: %v\n%s", err, out)
	}
	var payload llamaServerDiscoveryPayload
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json %q: %v", bytes.TrimSpace(out), err)
	}
	if !payload.Installed || !payload.Running {
		t.Fatalf("payload = %+v, want installed and running", payload)
	}
	if payload.Path != "/opt/llama.cpp/bin/llama-server" {
		t.Fatalf("path = %q, want supervised executable path", payload.Path)
	}
	if payload.Port != 8181 || len(payload.ResidentModels) != 1 {
		t.Fatalf("payload = %+v, want port 8181 and one resident model", payload)
	}
}

// The pgrep -f fallback also matches the shell that is running this script,
// because the script text contains "llama-server". Observed on a fleet node:
// the payload claimed llama-server at /usr/bin/bash and published a resident
// model named "null)". A process only counts when its argv[0] is llama-server.
func TestLlamaServerDiscoveryScriptIgnoresWrapperShellMatch(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	bin := t.TempDir()
	writeStub := func(name, body string) {
		t.Helper()
		p := filepath.Join(bin, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeStub("pgrep", `case "$1" in -x) exit 1;; *) echo 5151;; esac`)
	writeStub("ps", `echo "bash -c RAW_PIDS=\$(pgrep -x llama-server) --model (null)"`)
	writeStub("readlink", `echo /usr/bin/bash`)
	writeStub("lsof", `exit 1`)
	writeStub("ss", `exit 1`)
	writeStub("netstat", `exit 1`)

	cmd := exec.Command("bash", "-c", LlamaServerDiscoveryScript)
	cmd.Env = withExactToolPATH(t, bin, "head", "awk", "grep", "basename", "sed", "stat")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script: %v\n%s", err, out)
	}
	var payload llamaServerDiscoveryPayload
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json %q: %v", bytes.TrimSpace(out), err)
	}
	if payload.Installed || payload.Running || len(payload.ResidentModels) != 0 {
		t.Fatalf("payload = %+v, want not installed and no resident models", payload)
	}
}

func TestLlamaServerDiscoveryScriptExtractsSupervisorUnitFromCgroup(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	bin := t.TempDir()
	model := filepath.Join(t.TempDir(), "supervised-model.gguf")
	if err := os.WriteFile(model, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeStub := func(name, body string) {
		t.Helper()
		p := filepath.Join(bin, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeStub("llama-server", `echo b9999`)
	writeStub("pgrep", `echo 4242`)
	writeStub("ps", `echo "llama-server --model `+model+` --port 8082"`)
	writeStub("lsof", `exit 1`)
	writeStub("ss", `exit 1`)
	writeStub("netstat", `exit 1`)
	writeStub("cat", `echo "0::/user.slice/user-1000.slice/user@1000.service/app.slice/bonsai2-27b.service"`)

	cmd := exec.Command("bash", "-c", LlamaServerDiscoveryScript)
	cmd.Env = withExactToolPATH(t, bin, "head", "awk", "grep", "basename", "sed", "stat")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script: %v\n%s", err, out)
	}
	var payload llamaServerDiscoveryPayload
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json %q: %v", bytes.TrimSpace(out), err)
	}
	if len(payload.ResidentModels) != 1 {
		t.Fatalf("resident_models = %#v, want 1", payload.ResidentModels)
	}
	rm := payload.ResidentModels[0]
	if rm.SupervisorType != "systemd-user" {
		t.Errorf("supervisor_type = %q, want systemd-user", rm.SupervisorType)
	}
	if rm.SupervisorUnit != "bonsai2-27b.service" {
		t.Errorf("supervisor_unit = %q, want bonsai2-27b.service", rm.SupervisorUnit)
	}
}

func TestLlamaServerDiscoveryScriptReportsWeightSizeNotVRAM(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	bin := t.TempDir()
	model := filepath.Join(t.TempDir(), "two-mib.gguf")
	if err := os.WriteFile(model, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(model, 2*1024*1024); err != nil {
		t.Fatal(err)
	}
	writeStub := func(name, body string) {
		t.Helper()
		p := filepath.Join(bin, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeStub("llama-server", `echo b9999`)
	writeStub("pgrep", `echo 4242`)
	writeStub("ps", `echo "llama-server --model `+model+` --port 8182"`)
	writeStub("lsof", `exit 1`)
	writeStub("ss", `exit 1`)
	writeStub("netstat", `exit 1`)

	cmd := exec.Command("bash", "-c", LlamaServerDiscoveryScript)
	cmd.Env = withSandboxedPATH(bin)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script: %v\n%s", err, out)
	}
	var payload struct {
		ResidentModels []map[string]any `json:"resident_models"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json %q: %v", bytes.TrimSpace(out), err)
	}
	if len(payload.ResidentModels) != 1 {
		t.Fatalf("resident_models = %#v, want one", payload.ResidentModels)
	}
	resident := payload.ResidentModels[0]
	if got := resident["weight_size_mb"]; got != float64(2) {
		t.Fatalf("weight_size_mb = %#v, want 2", got)
	}
	if _, exists := resident["size_vram_mb"]; exists {
		t.Fatalf("resident model falsely reports model-file bytes as VRAM: %#v", resident)
	}
}

func TestMLXDiscoveryScriptReportsResidentRAMNotVRAM(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	bin := t.TempDir()
	writeStub := func(name, body string) {
		t.Helper()
		p := filepath.Join(bin, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeStub("mlx_lm", `exit 0`)
	writeStub("pgrep", `echo 4242`)
	writeStub("ps", `
case "$*" in
  "-p 4242 -o args=") echo "python -m mlx_lm.server --model org/mlx-model --port 8183" ;;
  "-o rss= -p 4242") echo 3145728 ;;
esac`)
	writeStub("curl", `printf 200`)

	cmd := exec.Command("bash", "-c", MLXDiscoveryScript)
	cmd.Env = withExactToolPATH(t, bin, "head", "awk", "grep", "python3")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script: %v\n%s", err, out)
	}
	var payload struct {
		ResidentModels []map[string]any `json:"resident_models"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json %q: %v", bytes.TrimSpace(out), err)
	}
	if len(payload.ResidentModels) != 1 {
		t.Fatalf("resident_models = %#v, want one", payload.ResidentModels)
	}
	resident := payload.ResidentModels[0]
	if got := resident["size_ram_mb"]; got != float64(3072) {
		t.Fatalf("size_ram_mb = %#v, want 3072", got)
	}
	if _, exists := resident["size_vram_mb"]; exists {
		t.Fatalf("resident model falsely reports process RSS as VRAM: %#v", resident)
	}
}

// Same self-match as the llama-server script: pgrep -f finds the shell that is
// running this script. Observed on a fleet Mac (running=true with no MLX
// server). With a llama-server on the default port, the old script would also
// have published that server's models as MLX residents.
func TestMLXDiscoveryScriptIgnoresWrapperShellMatch(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	bin := t.TempDir()
	writeStub := func(name, body string) {
		t.Helper()
		p := filepath.Join(bin, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeStub("mlx_lm", `exit 0`)
	writeStub("pgrep", `echo 6161`)
	writeStub("ps", `
case "$*" in
  "-p 6161 -o args=") echo "bash -c set -o pipefail; PGREP=\$(pgrep -f [m]lx_lm.server) mlx_lm.server" ;;
  "-o rss= -p 6161") echo 2048 ;;
esac`)
	writeStub("curl", `echo '{"data":[{"id":"bitnet-2B"}]}'`)

	cmd := exec.Command("bash", "-c", MLXDiscoveryScript)
	cmd.Env = withExactToolPATH(t, bin, "head", "awk", "grep", "python3")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script: %v\n%s", err, out)
	}
	var payload mlxDiscoveryPayload
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json %q: %v", bytes.TrimSpace(out), err)
	}
	if payload.Running || len(payload.ResidentModels) != 0 {
		t.Fatalf("payload = %+v, want not running and no resident models", payload)
	}
}

func TestMLXDiscoveryScriptReportsProcessGenerationEvidence(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	// Each case runs the production MLXDiscoveryScript against the same stub
	// fleet. "default port" omits --port, so the script must fall back to
	// mlx_lm.server's 8080 both in the probe URL and the published JSON;
	// "stopped" has no server process and must still emit valid JSON.
	cases := []struct {
		name     string
		pgrep    string
		args     string
		wantPort float64
		wantRun  bool
	}{
		{name: "explicit port", pgrep: "echo 4242", args: "/usr/local/bin/mlx_lm.server --model /mnt/models/qwen --port 8183", wantPort: 8183, wantRun: true},
		{name: "default port", pgrep: "echo 4242", args: "/usr/local/bin/mlx_lm.server --model /mnt/models/qwen", wantPort: 8080, wantRun: true},
		{name: "stopped", pgrep: "exit 1", wantPort: 8080},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			writeStub := func(name, body string) {
				t.Helper()
				p := filepath.Join(bin, name)
				if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			writeStub("mlx_lm", `exit 0`)
			writeStub("pgrep", tc.pgrep)
			writeStub("ps", `
case "$*" in
  "-p 4242 -o args=") echo "`+tc.args+`" ;;
  "-p 4242 -o lstart=") echo "Thu Sep  3 09:00:00 2026" ;;
  "-o rss= -p 4242") echo 3145728 ;;
  *) exit 1 ;;
esac`)
			// The stub answers only on the port the script should probe, so a
			// wrong or empty port makes the resident down instead of listed.
			wantURL := "http://127.0.0.1:" + strconv.Itoa(int(tc.wantPort)) + "/v1/models"
			writeStub("curl", `case "$*" in *" `+wantURL+`"*) printf 200 ;; *) printf 000; exit 7 ;; esac`)

			cmd := exec.Command("bash", "-c", MLXDiscoveryScript)
			cmd.Env = withExactToolPATH(t, bin, "head", "awk", "grep", "python3")
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("script: %v\n%s", err, out)
			}
			var payload struct {
				Running        bool             `json:"running"`
				Port           float64          `json:"port"`
				ResidentModels []map[string]any `json:"resident_models"`
			}
			if err := json.Unmarshal(out, &payload); err != nil {
				t.Fatalf("json %q: %v", bytes.TrimSpace(out), err)
			}
			if payload.Port != tc.wantPort || payload.Running != tc.wantRun {
				t.Fatalf("port=%v running=%v, want port=%v running=%v (out=%s)", payload.Port, payload.Running, tc.wantPort, tc.wantRun, bytes.TrimSpace(out))
			}
			if !tc.wantRun {
				if len(payload.ResidentModels) != 0 {
					t.Fatalf("stopped server published residents: %#v", payload.ResidentModels)
				}
				return
			}
			if len(payload.ResidentModels) != 1 {
				t.Fatalf("resident_models = %#v, want one", payload.ResidentModels)
			}
			resident := payload.ResidentModels[0]
			if got := resident["pid"]; got != float64(4242) || resident["state"] != "listed" {
				t.Fatalf("pid = %#v state = %#v, want 4242 listed", got, resident["state"])
			}
			if got := resident["executable"]; got != "/usr/local/bin/mlx_lm.server" {
				t.Fatalf("executable = %#v, want /usr/local/bin/mlx_lm.server", got)
			}
			if got := resident["process_start_token"]; got != "Thu Sep 3 09:00:00 2026" {
				t.Fatalf("process_start_token = %#v, want 'Thu Sep  3 09:00:00 2026'", got)
			}
		})
	}
}

func TestLlamaServerDiscoveryScriptReportsProcessGenerationEvidence(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	bin := t.TempDir()
	model := filepath.Join(t.TempDir(), "generation.gguf")
	if err := os.WriteFile(model, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeStub := func(name, body string) {
		t.Helper()
		p := filepath.Join(bin, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeStub("llama-server", `echo b9999`)
	writeStub("pgrep", `echo 4242`)
	writeStub("ps", `
case "$*" in
  "-p 4242 -o args=") echo "llama-server --model `+model+` --port 8184" ;;
  "-p 4242 -o user=") echo "axis-user" ;;
  "-p 4242 -o lstart=") echo "Thu Sep  3 09:00:00 2026" ;;
esac`)
	writeStub("lsof", `exit 1`)
	writeStub("ss", `exit 1`)
	writeStub("netstat", `exit 1`)

	cmd := exec.Command("bash", "-c", LlamaServerDiscoveryScript)
	cmd.Env = withSandboxedPATH(bin)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script: %v\n%s", err, out)
	}
	var payload struct {
		ResidentModels []map[string]any `json:"resident_models"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json %q: %v", bytes.TrimSpace(out), err)
	}
	if len(payload.ResidentModels) != 1 {
		t.Fatalf("resident_models = %#v, want one", payload.ResidentModels)
	}
	resident := payload.ResidentModels[0]
	if resident["pid"] != float64(4242) {
		t.Fatalf("pid = %#v, want 4242", resident["pid"])
	}
	if resident["executable"] != filepath.Join(bin, "llama-server") {
		t.Fatalf("executable = %#v, want test binary path", resident["executable"])
	}
	if resident["process_owner"] != "axis-user" {
		t.Fatalf("process_owner = %#v, want axis-user", resident["process_owner"])
	}
	if resident["process_start_token"] != "Thu Sep 3 09:00:00 2026" {
		t.Fatalf("process_start_token = %#v", resident["process_start_token"])
	}
}

// TestLlamaServerDiscoveryScriptPublishesEveryPIDAndGPUIndex is the regression
// for pgrep | head -1 and a resident object that never carried gpu_indices.
// Each llama-server PID is one resident. A GPU index comes from nvidia-smi,
// or from --main-gpu when that PID has no compute-apps row. A PID with neither
// observation does not gain a default index of 0.
func TestLlamaServerDiscoveryScriptPublishesEveryPIDAndGPUIndex(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	bin := t.TempDir()
	modelDir := t.TempDir()
	writeModel := func(name string) string {
		t.Helper()
		p := filepath.Join(modelDir, name)
		if err := os.WriteFile(p, []byte("gguf"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	alpha := writeModel("alpha.gguf")
	beta := writeModel("beta.gguf")
	gamma := writeModel("gamma.gguf")
	delta := writeModel("delta.gguf")
	writeStub := func(name, body string) {
		t.Helper()
		p := filepath.Join(bin, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeStub("llama-server", `echo b9999`)
	writeStub("pgrep", "printf '%s\\n' 101 202 303 404")
	writeStub("ps", `
case "$*" in
  *"-p 101 "*) echo "llama-server --model `+alpha+` --port 8082 --n-gpu-layers 32" ;;
  *"-p 202 "*) echo "llama-server --model `+beta+` --port 8084 --n-gpu-layers 32" ;;
  *"-p 303 "*) echo "llama-server --model `+gamma+` --port 8090 --n-gpu-layers 32" ;;
  *"-p 404 "*) echo "llama-server --model `+delta+` --port 8091 --n-gpu-layers 32 --main-gpu 1" ;;
esac`)
	writeStub("nvidia-smi", `
case "$*" in
  *--query-gpu=index,uuid*) printf '%s\n' '0, GPU-aaa' '1, GPU-bbb' ;;
  *--query-compute-apps=gpu_uuid,pid*) printf '%s\n' 'GPU-aaa, 101' 'GPU-bbb, 202' ;;
  *) exit 1 ;;
esac`)
	writeStub("lsof", `exit 1`)
	writeStub("ss", `exit 1`)
	writeStub("netstat", `exit 1`)

	cmd := exec.Command("bash", "-c", LlamaServerDiscoveryScript)
	cmd.Env = withExactToolPATH(t, bin, "head", "awk", "grep", "basename", "sed", "stat")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script: %v\n%s", err, out)
	}
	var payload struct {
		Port           int              `json:"port"`
		ResidentModels []map[string]any `json:"resident_models"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json %q: %v", bytes.TrimSpace(out), err)
	}
	if payload.Port != 8082 {
		t.Fatalf("top-level port = %d, want 8082 from the first resident", payload.Port)
	}
	byPID := map[int]map[string]any{}
	for _, resident := range payload.ResidentModels {
		pid, _ := resident["pid"].(float64)
		byPID[int(pid)] = resident
	}
	if len(byPID) != 4 {
		t.Fatalf("resident pids = %v, want 101 202 303 404", pidKeys(byPID))
	}
	assertResidentPort(t, byPID[101], 8082)
	assertResidentPort(t, byPID[202], 8084)
	assertResidentPort(t, byPID[303], 8090)
	assertResidentPort(t, byPID[404], 8091)
	assertGPUIndices(t, byPID[101], []int{0})
	assertGPUIndices(t, byPID[202], []int{1})
	if _, ok := byPID[303]["gpu_indices"]; ok {
		t.Fatalf("pid 303 gpu_indices = %#v, want the field absent", byPID[303]["gpu_indices"])
	}
	assertGPUIndices(t, byPID[404], []int{1})
}

func pidKeys(m map[int]map[string]any) []int {
	keys := make([]int, 0, len(m))
	for pid := range m {
		keys = append(keys, pid)
	}
	return keys
}

func assertResidentPort(t *testing.T, resident map[string]any, want int) {
	t.Helper()
	got, _ := resident["port"].(float64)
	if int(got) != want {
		t.Fatalf("pid %v port = %v, want %d", resident["pid"], resident["port"], want)
	}
}

func assertGPUIndices(t *testing.T, resident map[string]any, want []int) {
	t.Helper()
	raw, ok := resident["gpu_indices"].([]any)
	if !ok {
		t.Fatalf("pid %v gpu_indices = %#v, want %v", resident["pid"], resident["gpu_indices"], want)
	}
	if len(raw) != len(want) {
		t.Fatalf("pid %v gpu_indices = %#v, want %v", resident["pid"], raw, want)
	}
	for i, item := range raw {
		got, _ := item.(float64)
		if int(got) != want[i] {
			t.Fatalf("pid %v gpu_indices = %#v, want %v", resident["pid"], raw, want)
		}
	}
}

func TestRemoteCollectorDiscoversAppleFoundationModelsOnDarwinArm64(t *testing.T) {
	ctx := context.Background()

	t.Run("darwin arm64 with macOS 27 and OK probe", func(t *testing.T) {
		exec := &fakeRemoteExecutor{
			exact: map[string]fakeRunResult{
				AppleFoundationModelsDiscoveryScript: {out: "OK\n"},
			},
		}
		c := NewRemoteCollector("samson", "worker", "samson.local", exec)
		facts := &models.NodeFacts{
			OS:        "darwin",
			Arch:      "arm64",
			OSVersion: "27.2",
			Tools: []models.ToolInfo{
				{Name: "swift", Path: "/usr/bin/swift", Version: "6.1"},
			},
		}
		c.discoverAppleFoundationModels(ctx, facts)
		if facts.AppleFM == nil {
			t.Fatal("expected AppleFM to be populated")
		}
		if !facts.AppleFM.Available || !facts.AppleFM.Verified {
			t.Fatalf("expected available and verified AppleFM, got %+v", facts.AppleFM)
		}
		if facts.AppleFM.Version != "27.2" {
			t.Errorf("version = %q, want 27.2", facts.AppleFM.Version)
		}
		foundTool := false
		for _, tool := range facts.Tools {
			if tool.Name == "apple-foundation-models" {
				foundTool = true
				if tool.Path != "/usr/bin/swift" {
					t.Errorf("tool path = %q, want /usr/bin/swift", tool.Path)
				}
				if tool.Version != "27.2" {
					t.Errorf("tool version = %q, want 27.2", tool.Version)
				}
			}
		}
		if !foundTool {
			t.Error("expected apple-foundation-models tool to be appended")
		}
	})

	t.Run("darwin arm64 with AVAILABLE probe", func(t *testing.T) {
		exec := &fakeRemoteExecutor{
			exact: map[string]fakeRunResult{
				AppleFoundationModelsDiscoveryScript: {out: "AVAILABLE\n"},
			},
		}
		c := NewRemoteCollector("m3", "worker", "m3.local", exec)
		facts := &models.NodeFacts{
			OS:        "darwin",
			Arch:      "arm64",
			OSVersion: "27.0",
			Tools: []models.ToolInfo{
				{Name: "swift", Path: "/usr/bin/swift"},
			},
		}
		c.discoverAppleFoundationModels(ctx, facts)
		if facts.AppleFM == nil || !facts.AppleFM.Available || !facts.AppleFM.Verified {
			t.Fatalf("expected available and verified AppleFM, got %+v", facts.AppleFM)
		}
		foundTool := false
		for _, tool := range facts.Tools {
			if tool.Name == "apple-foundation-models" {
				foundTool = true
			}
		}
		if !foundTool {
			t.Error("expected apple-foundation-models tool to be appended")
		}
	})

	t.Run("darwin arm64 with multiline OK probe", func(t *testing.T) {
		exec := &fakeRemoteExecutor{
			exact: map[string]fakeRunResult{
				AppleFoundationModelsDiscoveryScript: {out: "OK\nOK\n"},
			},
		}
		c := NewRemoteCollector("samson", "worker", "samson.local", exec)
		facts := &models.NodeFacts{
			OS:        "darwin",
			Arch:      "arm64",
			OSVersion: "27.2",
			Tools: []models.ToolInfo{
				{Name: "swift", Path: "/usr/bin/swift"},
			},
		}
		c.discoverAppleFoundationModels(ctx, facts)
		if facts.AppleFM == nil || !facts.AppleFM.Available || !facts.AppleFM.Verified {
			t.Fatalf("expected available and verified AppleFM for multiline OK, got %+v", facts.AppleFM)
		}
		foundTool := false
		for _, tool := range facts.Tools {
			if tool.Name == "apple-foundation-models" {
				foundTool = true
			}
		}
		if !foundTool {
			t.Error("expected apple-foundation-models tool to be appended")
		}
	})

	t.Run("darwin arm64 rejects multiline OK with embedded errors", func(t *testing.T) {
		exec := &fakeRemoteExecutor{
			exact: map[string]fakeRunResult{
				AppleFoundationModelsDiscoveryScript: {out: "OK\nUNAVAILABLE:modelNotReady\nOK\n"},
			},
		}
		c := NewRemoteCollector("samson", "worker", "samson.local", exec)
		facts := &models.NodeFacts{
			OS:        "darwin",
			Arch:      "arm64",
			OSVersion: "27.2",
			Tools: []models.ToolInfo{
				{Name: "swift", Path: "/usr/bin/swift"},
			},
		}
		c.discoverAppleFoundationModels(ctx, facts)
		if facts.AppleFM == nil {
			t.Fatal("expected AppleFM to be populated")
		}
		if facts.AppleFM.Available || facts.AppleFM.Verified {
			t.Fatalf("expected Available=false, Verified=false for embedded errors, got %+v", facts.AppleFM)
		}
		for _, tool := range facts.Tools {
			if tool.Name == "apple-foundation-models" {
				t.Error("rejected probe must not append apple-foundation-models tool")
			}
		}
	})

	t.Run("darwin arm64 with UNVERIFIED probe (import-only)", func(t *testing.T) {
		exec := &fakeRemoteExecutor{
			exact: map[string]fakeRunResult{
				AppleFoundationModelsDiscoveryScript: {out: "UNVERIFIED\n"},
			},
		}
		c := NewRemoteCollector("m3", "worker", "m3.local", exec)
		facts := &models.NodeFacts{
			OS:        "darwin",
			Arch:      "arm64",
			OSVersion: "27.0",
			Tools: []models.ToolInfo{
				{Name: "swift", Path: "/usr/bin/swift"},
			},
		}
		c.discoverAppleFoundationModels(ctx, facts)
		if facts.AppleFM == nil {
			t.Fatal("expected AppleFM to be populated")
		}
		if facts.AppleFM.Available || facts.AppleFM.Verified {
			t.Fatalf("expected Available=false, Verified=false, got %+v", facts.AppleFM)
		}
		if !strings.Contains(facts.AppleFM.Error, "unverified") {
			t.Errorf("error = %q, want unverified message", facts.AppleFM.Error)
		}
		for _, tool := range facts.Tools {
			if tool.Name == "apple-foundation-models" {
				t.Error("unverified AppleFM must not append apple-foundation-models tool")
			}
		}
	})

	t.Run("darwin arm64 with UNAVAILABLE:modelNotReady probe", func(t *testing.T) {
		exec := &fakeRemoteExecutor{
			exact: map[string]fakeRunResult{
				AppleFoundationModelsDiscoveryScript: {out: "UNAVAILABLE:modelNotReady\n"},
			},
		}
		c := NewRemoteCollector("m3", "worker", "m3.local", exec)
		facts := &models.NodeFacts{
			OS:        "darwin",
			Arch:      "arm64",
			OSVersion: "27.0",
			Tools: []models.ToolInfo{
				{Name: "swift", Path: "/usr/bin/swift"},
			},
		}
		c.discoverAppleFoundationModels(ctx, facts)
		if facts.AppleFM == nil {
			t.Fatal("expected AppleFM to be populated")
		}
		if facts.AppleFM.Available || facts.AppleFM.Verified {
			t.Fatalf("expected Available=false, Verified=false, got %+v", facts.AppleFM)
		}
		if facts.AppleFM.Error != "UNAVAILABLE:modelNotReady" {
			t.Errorf("error = %q, want UNAVAILABLE:modelNotReady", facts.AppleFM.Error)
		}
		for _, tool := range facts.Tools {
			if tool.Name == "apple-foundation-models" {
				t.Error("unavailable AppleFM must not append apple-foundation-models tool")
			}
		}
	})

	t.Run("darwin arm64 with failed probe", func(t *testing.T) {
		exec := &fakeRemoteExecutor{
			exact: map[string]fakeRunResult{
				AppleFoundationModelsDiscoveryScript: {err: errors.New("exit 1"), out: "probe execution error\n"},
			},
		}
		c := NewRemoteCollector("m3", "worker", "m3.local", exec)
		facts := &models.NodeFacts{
			OS:        "darwin",
			Arch:      "arm64",
			OSVersion: "27.0",
			Tools: []models.ToolInfo{
				{Name: "swift", Path: "/usr/bin/swift"},
			},
		}
		c.discoverAppleFoundationModels(ctx, facts)
		if facts.AppleFM == nil {
			t.Fatal("expected AppleFM to be populated")
		}
		if facts.AppleFM.Available || facts.AppleFM.Verified {
			t.Fatalf("expected Available=false, Verified=false, got %+v", facts.AppleFM)
		}
		if facts.AppleFM.Error != "probe execution error" {
			t.Errorf("error = %q, want 'probe execution error'", facts.AppleFM.Error)
		}
		for _, tool := range facts.Tools {
			if tool.Name == "apple-foundation-models" {
				t.Error("failed probe must not append apple-foundation-models tool")
			}
		}
	})

	t.Run("darwin arm64 older macOS 15 returns version error", func(t *testing.T) {
		exec := &fakeRemoteExecutor{exact: map[string]fakeRunResult{}}
		c := NewRemoteCollector("legacy-mac", "worker", "legacy.local", exec)
		facts := &models.NodeFacts{
			OS:        "darwin",
			Arch:      "arm64",
			OSVersion: "15.7.1",
			Tools:     []models.ToolInfo{{Name: "swift"}},
		}
		c.discoverAppleFoundationModels(ctx, facts)
		if facts.AppleFM == nil {
			t.Fatal("expected AppleFM to be populated with error")
		}
		if facts.AppleFM.Available {
			t.Error("expected Available = false for older macOS")
		}
		if !strings.Contains(facts.AppleFM.Error, "requires macOS 26 or later") {
			t.Errorf("unexpected error message: %q", facts.AppleFM.Error)
		}
	})

	t.Run("darwin arm64 missing swift returns toolchain error", func(t *testing.T) {
		exec := &fakeRemoteExecutor{exact: map[string]fakeRunResult{}}
		c := NewRemoteCollector("samson", "worker", "samson.local", exec)
		facts := &models.NodeFacts{
			OS:        "darwin",
			Arch:      "arm64",
			OSVersion: "27.2",
			Tools:     []models.ToolInfo{},
		}
		c.discoverAppleFoundationModels(ctx, facts)
		if facts.AppleFM == nil {
			t.Fatal("expected AppleFM to be populated with error")
		}
		if facts.AppleFM.Available {
			t.Error("expected Available = false without swift")
		}
		if !strings.Contains(facts.AppleFM.Error, "swift toolchain not detected") {
			t.Errorf("unexpected error message: %q", facts.AppleFM.Error)
		}
	})

	t.Run("darwin x86_64 returns nil", func(t *testing.T) {
		exec := &fakeRemoteExecutor{exact: map[string]fakeRunResult{}}
		c := NewRemoteCollector("imac", "worker", "imac.local", exec)
		facts := &models.NodeFacts{
			OS:        "darwin",
			Arch:      "x86_64",
			OSVersion: "15.7.9",
			Tools:     []models.ToolInfo{{Name: "swift"}},
		}
		c.discoverAppleFoundationModels(ctx, facts)
		if facts.AppleFM != nil {
			t.Errorf("expected AppleFM to be nil for x86_64, got %+v", facts.AppleFM)
		}
	})

	t.Run("linux returns nil", func(t *testing.T) {
		exec := &fakeRemoteExecutor{exact: map[string]fakeRunResult{}}
		c := NewRemoteCollector("cranium", "primary", "cranium.local", exec)
		facts := &models.NodeFacts{
			OS:        "linux",
			Arch:      "amd64",
			OSVersion: "6.8.0",
		}
		c.discoverAppleFoundationModels(ctx, facts)
		if facts.AppleFM != nil {
			t.Errorf("expected AppleFM to be nil for linux, got %+v", facts.AppleFM)
		}
	})
}
