package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/capability"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
)

const linuxProbeOut = "os=Linux\narch=x86_64\ncores=8\nram_total_kb=16384000\ndisk_total_kb=943718400\nnvidia_driver=present\nnvidia_gpu=NVIDIA Example GPU A\nnvidia_gpu=NVIDIA Example GPU B\n"

func TestParseNodeProbe(t *testing.T) {
	r, err := parseNodeProbe(linuxProbeOut)
	if err != nil {
		t.Fatal(err)
	}
	want := hardwareReading{OS: "linux", Arch: "amd64", Cores: 8, RAMTotalMB: 16000, DiskTotalGB: 900}
	if r.Hardware != want || r.NVIDIADriver != "present" || len(r.NVIDIAGPUs) != 2 {
		t.Fatalf("linux probe = %+v", r)
	}
	r, err = parseNodeProbe("os=Darwin\narch=arm64\ncores=10\nram_total_bytes=17179869184\ndisk_total_kb=239362496\nnvidia_driver=absent\n")
	if err != nil || r.Hardware != (hardwareReading{OS: "darwin", Arch: "arm64", Cores: 10, RAMTotalMB: 16384, DiskTotalGB: 228}) || r.NVIDIADriver != "absent" {
		t.Fatalf("darwin probe = %+v, %v", r, err)
	}
	if r, err := parseNodeProbe("os=Linux\nnvidia_driver=present\nnvidia_unreadable=1\n"); err != nil || !r.NVIDIAUnreadable {
		t.Fatalf("an unreadable driver list must be reported: %+v, %v", r, err)
	}
	if !strings.Contains(nodeProbeScript, "nproc") {
		t.Fatal("the node probe must count CPUs as the collector does, with nproc")
	}
	if _, err := parseNodeProbe("ssh: connection refused\n"); err == nil {
		t.Fatal("output with no readings must be refused")
	}
}

func gpu(vendor, model string) models.GPUInfo { return models.GPUInfo{Vendor: vendor, Model: model} }

func TestJudgeGPU(t *testing.T) {
	present := nodeReading{NVIDIADriver: "present", NVIDIAGPUs: []string{"NVIDIA Example GPU A"}}
	absent := nodeReading{NVIDIADriver: "absent"}
	smiError := "NVIDIA-SMI has failed because it couldn't communicate with the NVIDIA driver. Make sure that the latest NVIDIA driver is installed and running."
	for _, tc := range []struct {
		name     string
		gpus     []models.GPUInfo
		probe    nodeReading
		want     capability.Outcome
		claimHas string
	}{
		{"agree", []models.GPUInfo{gpu("nvidia", "NVIDIA Example GPU A")}, present, capability.OutcomePass, "the 1 NVIDIA GPU(s) the driver shows"},
		{"an error listed as a GPU", []models.GPUInfo{gpu("nvidia", smiError), gpu("intel", "Example iGPU")}, absent, capability.OutcomeFail, "error message as a GPU"},
		{"a GPU with no driver", []models.GPUInfo{gpu("nvidia", "NVIDIA Example GPU C")}, absent, capability.OutcomeFail, "no NVIDIA driver loaded"},
		{"one GPU missing", nil, present, capability.OutcomeFail, "AXIS lists 0 NVIDIA GPU(s), the driver shows 1"},
		{"no NVIDIA anywhere", []models.GPUInfo{gpu("intel", "Example iGPU")}, absent, capability.OutcomePass, "lists no NVIDIA GPU"},
		{"apple silicon", []models.GPUInfo{gpu("apple", "Apple GPU")}, absent, capability.OutcomePass, "lists no NVIDIA GPU"},
		{"same count, another model", []models.GPUInfo{gpu("nvidia", "NVIDIA Example GPU B")}, present, capability.OutcomeFail, "the driver shows NVIDIA Example GPU A"},
		{"model spelled with other case and spacing", []models.GPUInfo{gpu("nvidia", "nvidia  example GPU a")}, present, capability.OutcomePass, "the driver shows"},
		{"driver list unreadable", []models.GPUInfo{gpu("nvidia", "NVIDIA Example GPU A")}, nodeReading{NVIDIADriver: "present", NVIDIAUnreadable: true}, capability.OutcomeInconclusive, "cannot read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obs := judgeGPU(capability.Observation{Capability: "facts.gpu"}, tc.gpus, tc.probe)
			if obs.Outcome != tc.want || !strings.Contains(obs.Claim, tc.claimHas) || len(obs.Evidence.SHA256) != 64 {
				t.Fatalf("outcome %s claim %q; want %s containing %q", obs.Outcome, obs.Claim, tc.want, tc.claimHas)
			}
		})
	}
}

// fakeCluster replaces Axis's published snapshot and the probe for one test.
func fakeCluster(t *testing.T, nodes []models.NodeFacts, probes map[string]string) {
	t.Helper()
	prevNodes, prevProbe := capabilitySnapshotNodes, capabilityRunProbe
	capabilitySnapshotNodes = func(context.Context) ([]models.NodeFacts, error) { return nodes, nil }
	capabilityRunProbe = func(_ context.Context, name string) (string, string, error) {
		out, ok := probes[name]
		if !ok {
			return "", "ssh to " + name, errors.New("ssh: handshake failed")
		}
		return out, "ssh to " + name, nil
	}
	t.Cleanup(func() { capabilitySnapshotNodes, capabilityRunProbe = prevNodes, prevProbe })
}

func snapNode(name string, status models.NodeStatus, r hardwareReading, gpus ...models.GPUInfo) models.NodeFacts {
	return models.NodeFacts{Name: name, Status: status, OS: r.OS, Arch: r.Arch,
		Resources: &models.Resources{CPUCores: r.Cores, RAMTotalMB: r.RAMTotalMB, DiskTotalGB: r.DiskTotalGB, GPUs: gpus}}
}

func TestNodeChecksAgainstThePublishedSnapshot(t *testing.T) {
	ok := sampleHardware
	wrongCores := sampleHardware
	wrongCores.Cores = 4
	fakeCluster(t, []models.NodeFacts{
		snapNode("alpha", models.StatusComplete, ok, gpu("nvidia", "NVIDIA Example GPU A"), gpu("nvidia", "NVIDIA Example GPU B")),
		snapNode("beta", models.StatusComplete, wrongCores),
		snapNode("gamma", models.StatusUnreachable, hardwareReading{}),
		snapNode("delta", models.StatusComplete, ok),
	}, map[string]string{"alpha": linuxProbeOut, "beta": linuxProbeOut, "gamma": linuxProbeOut})
	ctx := context.Background()
	for name, want := range map[string]capability.Outcome{
		"alpha": capability.OutcomePass,         // agrees
		"beta":  capability.OutcomeFail,         // the snapshot says 4 cores
		"gamma": capability.OutcomeInconclusive, // Axis has no facts
		"delta": capability.OutcomeInconclusive, // the probe cannot reach it
		"omega": capability.OutcomeInconclusive, // not in the snapshot
	} {
		if obs := checkFactsHardwareNode(ctx, name); obs.Outcome != want || obs.Subject != "node:"+name {
			t.Errorf("facts.hardware %s = %s (%s), want %s", name, obs.Outcome, obs.Claim, want)
		}
	}
	if obs := checkFactsGPU(ctx, "alpha"); obs.Outcome != capability.OutcomePass {
		t.Errorf("facts.gpu alpha = %s (%s)", obs.Outcome, obs.Claim)
	}
	if obs := checkFactsGPU(ctx, "beta"); obs.Outcome != capability.OutcomeFail {
		t.Errorf("facts.gpu beta (driver shows 2, Axis lists 0) = %s (%s)", obs.Outcome, obs.Claim)
	}
}

func TestCapabilityCheckAcrossNodes(t *testing.T) {
	useTempAxisHome(t)
	fakeCluster(t, []models.NodeFacts{
		snapNode("alpha", models.StatusComplete, sampleHardware),
		snapNode("beta", models.StatusComplete, sampleHardware),
		snapNode("gamma", models.StatusComplete, sampleHardware),
	}, map[string]string{"alpha": linuxProbeOut, "beta": linuxProbeOut, "gamma": linuxProbeOut})
	out, err := runCapability(t, "check", "facts.hardware", "--all-nodes", "--except", "gamma", "--format", "json")
	if err != nil {
		t.Fatalf("check --all-nodes: %v\n%s", err, out)
	}
	var results []struct {
		Node        string                 `json:"node"`
		Observation capability.Observation `json:"observation"`
	}
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(results) != 2 || results[0].Node != "alpha" || results[1].Node != "beta" {
		t.Fatalf("results = %+v; want alpha and beta, gamma skipped", results)
	}
	out, err = runCapability(t, "show", "facts.hardware", "--format", "json")
	var e capability.Entry
	if err != nil || json.Unmarshal([]byte(out), &e) != nil || e.Totals.Pass != 2 {
		t.Fatalf("show after two node passes = %q, %v", out, err)
	}
	out, err = runCapability(t, "check", "facts.hardware", "--node", "ALPHA", "--format", "json")
	var spelled []struct {
		Observation capability.Observation `json:"observation"`
	}
	if err != nil || json.Unmarshal([]byte(out), &spelled) != nil || len(spelled) != 1 || spelled[0].Observation.Subject != "node:alpha" {
		t.Fatalf("a node named ALPHA must be recorded in the snapshot's spelling: %q, %v", out, err)
	}
	if _, err := runCapability(t, "check", "facts.hardware", "--all-nodes", "--except", "alpha,beta,gamma"); err == nil {
		t.Fatal("excluding every node must be an error, not a silent no-op")
	}
}

func TestTheRealProbeScriptRunsHere(t *testing.T) {
	out, _, err := runNodeProbe(context.Background(), "")
	if err != nil {
		t.Skipf("no local shell probe here: %v", err)
	}
	r, err := parseNodeProbe(out)
	if err != nil || r.Hardware.Cores < 1 || r.Hardware.RAMTotalMB < 1 || r.Hardware.DiskTotalGB < 1 {
		t.Fatalf("probe = %+v, %v\n%s", r, err, out)
	}
}

func TestProbeRunnerUsesALocalShellForThisMachine(t *testing.T) {
	host, err := os.Hostname()
	if err != nil || host == "" {
		t.Skip("no hostname here")
	}
	prev := capabilitySnapshotNodes
	t.Cleanup(func() { capabilitySnapshotNodes = prev })
	node := config.NodeConfig{Name: "alpha", Hostname: "192.0.2.10"} // neither field is this machine's hostname
	capabilitySnapshotNodes = func(context.Context) ([]models.NodeFacts, error) {
		return []models.NodeFacts{{Name: "alpha", Hostname: host}}, nil
	}
	if ex, _ := probeRunnerFor(context.Background(), node); fmt.Sprintf("%T", ex) != "*transport.LocalExecutor" {
		t.Fatalf("the snapshot says alpha is this machine, but the probe would dial %T", ex)
	}
	capabilitySnapshotNodes = func(context.Context) ([]models.NodeFacts, error) {
		return []models.NodeFacts{{Name: "alpha", Hostname: "elsewhere"}}, nil
	}
	if ex, _ := probeRunnerFor(context.Background(), node); fmt.Sprintf("%T", ex) != "*transport.SSHExecutor" {
		t.Fatalf("another machine must be dialed over SSH, got %T", ex)
	}
}

// A node whose name or hostname only shares this machine's first label is
// another machine: it is dialed over SSH, not probed here.
func TestCapabilityNodeIsLocalNeedsTheFullHostnameForAQualifiedName(t *testing.T) {
	host, err := os.Hostname()
	if err != nil || host == "" {
		t.Skip("no hostname here")
	}
	short := shortHostname(host)
	for _, tc := range []struct {
		node config.NodeConfig
		want bool
	}{
		{config.NodeConfig{Name: short + ".remote.example", Hostname: "unreachable.example"}, false},
		{config.NodeConfig{Name: "alpha", Hostname: short + ".remote.example"}, false},
		{config.NodeConfig{Name: short, Hostname: "192.0.2.10"}, true},
		{config.NodeConfig{Name: "alpha", Hostname: host}, true},
	} {
		if got := capabilityNodeIsLocal(tc.node); got != tc.want {
			t.Errorf("capabilityNodeIsLocal(%+v) = %v, want %v (this machine is %q)", tc.node, got, tc.want, host)
		}
	}
	prev := capabilitySnapshotNodes
	t.Cleanup(func() { capabilitySnapshotNodes = prev })
	capabilitySnapshotNodes = func(context.Context) ([]models.NodeFacts, error) {
		return []models.NodeFacts{{Name: "alpha", Hostname: short + ".remote.example"}}, nil
	}
	if ex, _ := probeRunnerFor(context.Background(), config.NodeConfig{Name: "alpha", Hostname: "192.0.2.10"}); fmt.Sprintf("%T", ex) != "*transport.SSHExecutor" {
		t.Fatalf("a snapshot hostname sharing only the first label made the probe run here (%T)", ex)
	}
}

// TestTheNodeProbeNeverPassesAGPUListItCannotRead runs the real probe script
// against a stand-in for /proc/driver/nvidia/gpus.
func TestTheNodeProbeNeverPassesAGPUListItCannotRead(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the probe reads the NVIDIA driver list only on Linux")
	}
	run := func(t *testing.T, gpuDir string, axis []models.GPUInfo) (nodeReading, capability.Observation) {
		t.Helper()
		script := strings.ReplaceAll(nodeProbeScript, "/proc/driver/nvidia/gpus", gpuDir)
		out, err := exec.Command("sh", "-c", script).Output()
		if err != nil {
			t.Fatal(err)
		}
		r, err := parseNodeProbe(string(out))
		if err != nil {
			t.Fatal(err)
		}
		obs := judgeGPU(capability.Observation{Capability: "facts.gpu", Checker: factsGPUChecker,
			Evidence: capability.Evidence{Kind: capability.EvidenceLiveProbe, Ref: "test script"}}, axis, r)
		return r, obs
	}
	gpuDir := func(t *testing.T, info string, write bool) string {
		dir := filepath.Join(t.TempDir(), "gpus")
		if err := os.MkdirAll(filepath.Join(dir, "0000:01:00.0"), 0o700); err != nil {
			t.Fatal(err)
		}
		if write {
			if err := os.WriteFile(filepath.Join(dir, "0000:01:00.0", "information"), []byte(info), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	one := []models.GPUInfo{gpu("nvidia", "NVIDIA Example GPU A")}

	t.Run("readable list passes", func(t *testing.T) {
		if r, obs := run(t, gpuDir(t, "Model: NVIDIA Example GPU A\n", true), one); obs.Outcome != capability.OutcomePass || r.NVIDIAUnreadable {
			t.Fatalf("got %s (%s), unreadable=%t", obs.Outcome, obs.Claim, r.NVIDIAUnreadable)
		}
	})
	t.Run("a GPU without its information file", func(t *testing.T) {
		if _, obs := run(t, gpuDir(t, "", false), nil); obs.Outcome != capability.OutcomeInconclusive {
			t.Fatalf("got %s (%s); want inconclusive", obs.Outcome, obs.Claim)
		}
	})
	t.Run("information without a model", func(t *testing.T) {
		if _, obs := run(t, gpuDir(t, "IRQ: 42\n", true), nil); obs.Outcome != capability.OutcomeInconclusive {
			t.Fatalf("got %s (%s); want inconclusive", obs.Outcome, obs.Claim)
		}
	})
	t.Run("a list that cannot be enumerated", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a directory whatever its mode")
		}
		dir := gpuDir(t, "Model: NVIDIA Example GPU A\n", true)
		if err := os.Chmod(dir, 0o100); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		// From the fresh-context diff review: before the fix this was a counted pass.
		r, obs := run(t, dir, nil)
		rec, _, _, err := capability.Open(t.TempDir()).RecordCheck(obs, nil)
		if err != nil {
			t.Fatal(err)
		}
		if obs.Outcome != capability.OutcomeInconclusive || rec.Totals.Pass != 0 {
			t.Fatalf("got %s driver=%s gpus=%d unreadable=%t passes=%d; want inconclusive", obs.Outcome, r.NVIDIADriver, len(r.NVIDIAGPUs), r.NVIDIAUnreadable, rec.Totals.Pass)
		}
	})
}
