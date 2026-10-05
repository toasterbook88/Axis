package facts

import (
	"strings"
	"testing"
)

func TestNvidiaSMIMemoryQueryPrependsIndex(t *testing.T) {
	const want = "--query-gpu=index,name,memory.total,memory.free"
	if nvidiaSMIMemoryQuery != want {
		t.Fatalf("query = %q, want %q", nvidiaSMIMemoryQuery, want)
	}
	if strings.Contains(nvidiaSMIMemoryQuery, "utilization") || strings.Contains(nvidiaSMIMemoryQuery, "uuid") {
		t.Fatalf("memory query must not be the util or resident query: %s", nvidiaSMIMemoryQuery)
	}
	local := strings.Join(localNvidiaSMIMemoryArgs(), " ")
	if !strings.Contains(local, "nvidia-smi "+nvidiaSMIMemoryQuery) {
		t.Fatalf("local args = %q", local)
	}
	if !strings.Contains(linuxGPUCollectCommand(), "nvidia-smi "+nvidiaSMIMemoryQuery) {
		t.Fatalf("remote cmd = %q", linuxGPUCollectCommand())
	}
	if !strings.Contains(remoteFactBundleScript, "nvidia-smi "+nvidiaSMIMemoryQuery) {
		t.Fatal("remote bundle gpu_b64 lost the indexed memory query")
	}
	if !strings.Contains(darwinGPUCollectCommand(), "system_profiler SPDisplaysDataType") || strings.Contains(darwinGPUCollectCommand(), "nvidia-smi") {
		t.Fatalf("darwin cmd = %q", darwinGPUCollectCommand())
	}
	if !strings.Contains(remoteFactBundleScript, "system_profiler SPDisplaysDataType") {
		t.Fatal("darwin bundle probe changed")
	}
	if !strings.Contains(LlamaServerDiscoveryScript, "--query-gpu=index,uuid") || strings.Contains(LlamaServerDiscoveryScript, "memory.total") {
		t.Fatal("resident compute-apps query must stay index,uuid")
	}
}
