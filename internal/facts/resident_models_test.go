package facts

import (
	"context"
	"testing"
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
