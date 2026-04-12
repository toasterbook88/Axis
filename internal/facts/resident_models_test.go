package facts

import (
	"context"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestRemoteCollectorCollectsResidentModelsFromOllamaProbe(t *testing.T) {
	exec := &fakeRemoteExecutor{
		exact: map[string]fakeRunResult{
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
			"cat /proc/pressure/memory 2>/dev/null":                                             {out: "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n"},
			`if command -v ip >/dev/null 2>&1; then ip -o addr show scope global 2>/dev/null || ip addr show scope global | awk '/inet/ {print $2}'; else ifconfig 2>/dev/null | awk '/^[a-z]/ {iface=$1} /inet / && !/127.0.0.1/ {print iface, $2}; /inet6 / && !/::1/ && !/fe80/ {print iface, $2}' | sed 's/://'; fi`: {out: "2: eth0    inet 10.0.0.5/24 brd 10.0.0.255 scope global eth0\n"},
			OllamaDiscoveryScript: {out: `{"installed":true,"path":"/usr/bin/ollama","version":"0.6.0","running":true,"listening":true,"port":11434,"models":["llama3:8b"],"resident_models":[{"name":"llama3:8b","runtime":"ollama","processor":"100% GPU","source":"ollama-ps"}],"gpu_offload":"gpu:cuda"}`},
		},
	}

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
