package facts

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

type fakeRemoteExecutor struct {
	connectErr error
	closed     bool
	exact      map[string]fakeRunResult
	contains   map[string]fakeRunResult
	runs       []string
}

type fakeRunResult struct {
	out string
	err error
}

func (f *fakeRemoteExecutor) Connect(context.Context) error {
	return f.connectErr
}

func (f *fakeRemoteExecutor) Run(_ context.Context, cmd string) (string, error) {
	f.runs = append(f.runs, cmd)
	if res, ok := f.exact[cmd]; ok {
		return res.out, res.err
	}
	for needle, res := range f.contains {
		if strings.Contains(cmd, needle) {
			return res.out, res.err
		}
	}
	return "", fmt.Errorf("unexpected command: %s", cmd)
}

func (f *fakeRemoteExecutor) Close() error {
	f.closed = true
	return nil
}

func TestLocalCollectorCollectsFacts(t *testing.T) {
	collector := NewLocalCollector("local-node", "worker")

	facts, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect local facts: %v", err)
	}

	if facts.Name != "local-node" || facts.Role != "worker" {
		t.Fatalf("unexpected collector identity: %+v", facts)
	}
	if facts.OS == "" || facts.Arch == "" {
		t.Fatalf("expected local OS and arch to be set, got OS=%q arch=%q", facts.OS, facts.Arch)
	}
	if facts.Resources == nil {
		t.Fatal("expected resources")
	}
	if facts.Resources.CPUCores <= 0 {
		t.Fatalf("expected cpu cores > 0, got %d", facts.Resources.CPUCores)
	}
	if facts.Resources.RAMTotalMB <= 0 {
		t.Fatalf("expected RAM total > 0, got %d", facts.Resources.RAMTotalMB)
	}
	if facts.Hostname == "" {
		t.Fatal("expected hostname")
	}
	if facts.CollectedAt.IsZero() || time.Since(facts.CollectedAt) > time.Minute {
		t.Fatalf("unexpected collected_at: %s", facts.CollectedAt)
	}
	if facts.Status != models.StatusComplete && facts.Status != models.StatusPartial {
		t.Fatalf("expected complete or partial status, got %s", facts.Status)
	}
}

func TestRunLocalTurboQuantProbe(t *testing.T) {
	out, err := runLocalTurboQuantProbe(context.Background(), `printf 'mlx_lm --help'`)
	if err != nil {
		t.Fatalf("run local turboquant probe: %v", err)
	}
	if strings.TrimSpace(out) != "mlx_lm --help" {
		t.Fatalf("unexpected probe output: %q", out)
	}
}

func TestDetectAppleFoundationModelsRequiresEligibleHost(t *testing.T) {
	if got := detectAppleFoundationModels(context.Background(), "linux", "amd64", "6.8.0", nil); got != nil {
		t.Fatalf("expected nil on non-darwin host, got %+v", got)
	}

	info := detectAppleFoundationModels(context.Background(), "darwin", "arm64", "25.4", []models.ToolInfo{{Name: "swift", Path: "/usr/bin/swift"}})
	if info == nil {
		t.Fatal("expected ineligible darwin host to report capability state")
	}
	if info.Available || info.Verified {
		t.Fatalf("expected unavailable info on macOS < 26, got %+v", info)
	}
}

func TestDetectAppleFoundationModelsUsesRuntimeProbe(t *testing.T) {
	prev := runAppleFoundationModelsProbeFn
	t.Cleanup(func() { runAppleFoundationModelsProbeFn = prev })

	runAppleFoundationModelsProbeFn = func(context.Context) (string, error) {
		return "OK\n", nil
	}

	info := detectAppleFoundationModels(context.Background(), "darwin", "arm64", "26.1", []models.ToolInfo{{Name: "swift", Path: "/usr/bin/swift"}})
	if info == nil {
		t.Fatal("expected apple foundation models info")
	}
	if !info.Available || !info.Verified {
		t.Fatalf("expected verified availability, got %+v", info)
	}
}

func TestDetectAppleFoundationModelsAcceptsTrailingProbeNoise(t *testing.T) {
	prev := runAppleFoundationModelsProbeFn
	t.Cleanup(func() { runAppleFoundationModelsProbeFn = prev })

	runAppleFoundationModelsProbeFn = func(context.Context) (string, error) {
		return "swift-driver warning\nOK\n", nil
	}

	info := detectAppleFoundationModels(context.Background(), "darwin", "arm64", "26.1", []models.ToolInfo{{Name: "swift", Path: "/usr/bin/swift"}})
	if info == nil || !info.Verified {
		t.Fatalf("expected OK marker to remain verified, got %+v", info)
	}
}

func TestDetectAppleFoundationModelsRejectsMissingProbeMarker(t *testing.T) {
	prev := runAppleFoundationModelsProbeFn
	t.Cleanup(func() { runAppleFoundationModelsProbeFn = prev })

	runAppleFoundationModelsProbeFn = func(context.Context) (string, error) {
		return "swift-driver warning\nready\n", nil
	}

	info := detectAppleFoundationModels(context.Background(), "darwin", "arm64", "26.1", []models.ToolInfo{{Name: "swift", Path: "/usr/bin/swift"}})
	if info == nil {
		t.Fatal("expected apple foundation models info")
	}
	if info.Verified {
		t.Fatalf("expected missing OK marker to stay unverified, got %+v", info)
	}
	if !strings.Contains(info.Error, "expected OK marker") {
		t.Fatalf("expected missing marker error, got %+v", info)
	}
}

func TestDiscoverToolsUsesConfiguredLookups(t *testing.T) {
	prevLookPath := lookPathTool
	prevRun := runToolVersionCommand
	t.Cleanup(func() {
		lookPathTool = prevLookPath
		runToolVersionCommand = prevRun
	})

	lookPathTool = func(name string) (string, error) {
		switch name {
		case "go":
			return "/usr/local/bin/go", nil
		case "git":
			return "/usr/bin/git", nil
		default:
			return "", exec.ErrNotFound
		}
	}
	runToolVersionCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		switch name {
		case "go":
			return exec.CommandContext(ctx, "bash", "-lc", `printf 'go version go1.24.1 darwin/arm64\n'`)
		case "git":
			return exec.CommandContext(ctx, "bash", "-lc", `printf 'git version 2.39.3\n'`)
		default:
			return exec.CommandContext(ctx, "bash", "-lc", "exit 1")
		}
	}

	tools := DiscoverTools(context.Background())
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %v", tools)
	}
	if tools[0].Name != "go" || tools[0].Version != "1.24.1" {
		t.Fatalf("unexpected go tool info: %+v", tools[0])
	}
	if tools[1].Name != "git" || tools[1].Version != "2.39.3" {
		t.Fatalf("unexpected git tool info: %+v", tools[1])
	}
}

func TestRemoteCollectorCollectsDarwinFacts(t *testing.T) {
	exec := &fakeRemoteExecutor{
		exact: map[string]fakeRunResult{
			"uname -s":                {out: "Darwin\n"},
			"uname -m":                {out: "arm64\n"},
			"sw_vers -productVersion": {out: "14.4\n"},
			"sysctl -n hw.ncpu":       {out: "16\n"},
			"sysctl -n machdep.cpu.brand_string 2>/dev/null || sysctl -n hw.model": {out: "Apple M3 Max\n"},
			"sysctl -n hw.memsize": {out: "34359738368\n"},
			"vm_stat": {out: `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                              100000.
Pages inactive:                          100000.
`},
			"sysctl -n kern.memorystatus_vm_pressure_level 2>/dev/null": {out: "2\n"},
			"sysctl -n vm.loadavg": {out: "{ 3.14 2.72 1.62 }\n"},
			"df -kP /": {out: `Filesystem 1024-blocks Used Available Capacity Mounted on
/dev/disk3s1 3145728 1048576 2097152 34% /
`},
			"system_profiler SPDisplaysDataType 2>/dev/null | grep 'Chipset Model:' | sed 's/.*Chipset Model: //'":                                                                                                                                         {out: "Apple M3 Max GPU\n"},
			`if command -v ip >/dev/null 2>&1; then ip addr show scope global | awk '/inet/ {print $2}' | cut -d/ -f1; else ifconfig | awk '/inet / && !/127.0.0.1/ {print $2}; /inet6 / && !/::1/ && !/fe80/ {print $2}' | cut -d% -f1 | cut -d/ -f1; fi`: {out: "192.168.1.10\n2001:db8::10\n"},
			"command -v git 2>/dev/null":          {out: "/usr/bin/git\n"},
			"git --version 2>/dev/null":           {out: "git version 2.39.3\n"},
			"command -v llama-server 2>/dev/null": {out: "/opt/homebrew/bin/llama-server\n"},
			"llama-server --version 2>/dev/null":  {out: "llama.cpp server version 0.0.1\n"},
			OllamaDiscoveryScript:                 {out: `{"installed":true,"path":"/usr/local/bin/ollama","version":"0.6.0","running":true,"listening":true,"port":11434,"models":["llama3:8b"],"gpu_offload":"gpu:metal"}`},
		},
		contains: map[string]fakeRunResult{
			"llama-server --help": {out: "llama.cpp server --ctx-size --n-gpu-layers --flash-attn\n"},
		},
	}

	collector := NewRemoteCollector("mac-studio", "worker", "mac-studio.local", exec)
	facts, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect remote facts: %v", err)
	}

	if !exec.closed {
		t.Fatal("expected executor to be closed")
	}
	if facts.Status != models.StatusComplete {
		t.Fatalf("expected complete status, got %s", facts.Status)
	}
	if facts.Resources == nil {
		t.Fatal("expected resources")
	}
	if facts.Resources.MemoryTopology != models.MemoryTopologyUnified {
		t.Fatalf("expected unified memory topology, got %q", facts.Resources.MemoryTopology)
	}
	if facts.Resources.MemoryClass != 4 {
		t.Fatalf("expected memory class 4, got %d", facts.Resources.MemoryClass)
	}
	if facts.Resources.PressureSource != "darwin-vm-pressure" {
		t.Fatalf("expected darwin pressure source, got %q", facts.Resources.PressureSource)
	}
	if facts.Resources.Pressure != "medium" {
		t.Fatalf("expected medium pressure, got %q", facts.Resources.Pressure)
	}
	if facts.Ollama == nil || !facts.Ollama.Installed {
		t.Fatalf("expected ollama info, got %+v", facts.Ollama)
	}
	if facts.TurboQuant == nil || !facts.TurboQuant.Verified {
		t.Fatalf("expected verified turboquant info, got %+v", facts.TurboQuant)
	}
	if len(facts.Addresses) != 2 {
		t.Fatalf("expected 2 addresses, got %v", facts.Addresses)
	}
	if len(facts.Tools) < 3 {
		t.Fatalf("expected appended tools, got %v", facts.Tools)
	}
}

func TestRemoteCollectorMarksUnreachableNode(t *testing.T) {
	exec := &fakeRemoteExecutor{connectErr: fmt.Errorf("ssh timeout")}
	collector := NewRemoteCollector("down", "worker", "down.local", exec)

	facts, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect unreachable facts: %v", err)
	}
	if facts.Status != models.StatusUnreachable {
		t.Fatalf("expected unreachable, got %s", facts.Status)
	}
	if !strings.Contains(facts.Error, "ssh timeout") {
		t.Fatalf("expected ssh timeout error, got %q", facts.Error)
	}
}

func TestRemoteCollectorMarksPartialOnCollectorFailures(t *testing.T) {
	exec := &fakeRemoteExecutor{
		exact: map[string]fakeRunResult{
			"uname -s": {out: "Linux\n"},
			"uname -m": {out: "amd64\n"},
			"uname -r": {out: "6.8.0\n"},
			"nproc":    {err: fmt.Errorf("boom")},
			"grep -m1 'model name' /proc/cpuinfo 2>/dev/null | cut -d: -f2": {out: "AMD Ryzen\n"},
			`grep -E 'MemTotal|MemAvailable|MemFree' /proc/meminfo`: {out: `MemTotal:       16301328 kB
MemAvailable:   12456780 kB
`},
			"cat /proc/loadavg": {err: fmt.Errorf("missing loadavg")},
			"df -kP /": {out: `Filesystem 1024-blocks Used Available Capacity Mounted on
/dev/root 3145728 1048576 2097152 34% /
`},
			`if command -v ip >/dev/null 2>&1; then ip addr show scope global | awk '/inet/ {print $2}' | cut -d/ -f1; else ifconfig | awk '/inet / && !/127.0.0.1/ {print $2}; /inet6 / && !/::1/ && !/fe80/ {print $2}' | cut -d% -f1 | cut -d/ -f1; fi`: {err: fmt.Errorf("no network tool")},
			OllamaDiscoveryScript: {err: fmt.Errorf("no ollama")},
		},
	}

	collector := NewRemoteCollector("linux-node", "worker", "linux-node.local", exec)
	facts, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect partial facts: %v", err)
	}
	if facts.Status != models.StatusPartial {
		t.Fatalf("expected partial status, got %s", facts.Status)
	}
	if facts.Resources == nil {
		t.Fatal("expected resources even on partial failure")
	}
	if facts.Resources.CPUModel != "AMD Ryzen" {
		t.Fatalf("unexpected cpu model: %q", facts.Resources.CPUModel)
	}
	if facts.Ollama != nil {
		t.Fatalf("expected ollama info to stay nil on discovery error, got %+v", facts.Ollama)
	}
}
