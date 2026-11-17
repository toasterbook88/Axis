package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/capability"
	"github.com/toasterbook88/axis/internal/models"
)

const linuxProbeOut = "os=Linux\narch=x86_64\ncores=8\nram_total_kb=16384000\ndisk_total_kb=943718400\nnvidia_driver=present\nnvidia_gpu=NVIDIA GeForce RTX 2080 Ti\nnvidia_gpu=NVIDIA GeForce RTX 5060\n"

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
	if _, err := parseNodeProbe("ssh: connection refused\n"); err == nil {
		t.Fatal("output with no readings must be refused")
	}
}

func gpu(vendor, model string) models.GPUInfo { return models.GPUInfo{Vendor: vendor, Model: model} }

func TestJudgeGPU(t *testing.T) {
	present := nodeReading{NVIDIADriver: "present", NVIDIAGPUs: []string{"NVIDIA GeForce RTX 2080 Ti"}}
	absent := nodeReading{NVIDIADriver: "absent"}
	smiError := "NVIDIA-SMI has failed because it couldn't communicate with the NVIDIA driver. Make sure that the latest NVIDIA driver is installed and running."
	for _, tc := range []struct {
		name     string
		gpus     []models.GPUInfo
		probe    nodeReading
		want     capability.Outcome
		claimHas string
	}{
		{"agree", []models.GPUInfo{gpu("nvidia", "NVIDIA GeForce RTX 2080 Ti")}, present, capability.OutcomePass, "the 1 NVIDIA GPU(s) the driver shows"},
		{"an error listed as a GPU", []models.GPUInfo{gpu("nvidia", smiError), gpu("intel", "Iris Plus")}, absent, capability.OutcomeFail, "error message as a GPU"},
		{"a GPU with no driver", []models.GPUInfo{gpu("nvidia", "NVIDIA GeForce MX250")}, absent, capability.OutcomeFail, "no NVIDIA driver loaded"},
		{"one GPU missing", nil, present, capability.OutcomeFail, "AXIS lists 0 NVIDIA GPU(s), the driver shows 1"},
		{"no NVIDIA anywhere", []models.GPUInfo{gpu("intel", "HD Graphics 5500")}, absent, capability.OutcomeInconclusive, "no NVIDIA driver"},
		{"apple silicon", []models.GPUInfo{gpu("apple", "Apple GPU")}, absent, capability.OutcomeInconclusive, "no NVIDIA driver"},
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
		snapNode("alpha", models.StatusComplete, ok, gpu("nvidia", "NVIDIA GeForce RTX 2080 Ti"), gpu("nvidia", "NVIDIA GeForce RTX 5060")),
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
