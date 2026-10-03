package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/events"
	"github.com/toasterbook88/axis/internal/modellife"
	"github.com/toasterbook88/axis/internal/models"
)

// drainModelEventsBeforeTempCleanup runs before t.TempDir removal. Evict and
// resume enqueue an event that creates files in the temp AXIS home after the
// command returns. RemoveAll returns ENOTEMPTY if that create lands between
// readdir and rmdir.
func drainModelEventsBeforeTempCleanup(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if err := events.FlushEvents(5 * time.Second); err != nil {
			t.Errorf("FlushEvents: %v", err)
		}
	})
}

func makeDualGPUSnapshot() *models.ClusterSnapshot {
	now := time.Now().UTC()
	return &models.ClusterSnapshot{
		Timestamp: now,
		Nodes: []models.NodeFacts{
			{
				Name:   "cranium",
				Status: models.StatusComplete,
				ResidentModels: []models.ResidentModel{
					{
						Name:           "bonsai2-27b",
						Runtime:        "llama.cpp",
						Port:           8082,
						PID:            10101,
						WeightSizeMB:   9842,
						SizeVRAMMB:     9842,
						SupervisorType: "systemd-user",
						SupervisorUnit: "bonsai2-27b.service",
						GPUIndices:     []int{0},
					},
					{
						Name:           "coder7b",
						Runtime:        "llama.cpp",
						Port:           8081,
						PID:            10102,
						WeightSizeMB:   7574,
						SizeVRAMMB:     7574,
						SupervisorType: "systemd-user",
						SupervisorUnit: "coder7b.service",
						GPUIndices:     []int{1},
					},
				},
			},
		},
	}
}

func TestModelEvictDefaultsToLiveSnapshot(t *testing.T) {
	snap := makeDualGPUSnapshot()
	cacheCalls := 0
	prevLive := loadModelSnapshot
	loadModelSnapshot = func(context.Context) (*models.ClusterSnapshot, error) { return snap, nil }
	prevFetch := fetchModelInventorySnapshot
	fetchModelInventorySnapshot = func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		cacheCalls++
		return snap, "daemon-cache", nil
	}
	t.Cleanup(func() {
		loadModelSnapshot = prevLive
		fetchModelInventorySnapshot = prevFetch
	})
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "cranium"}}})

	tmpDir := t.TempDir()
	drainModelEventsBeforeTempCleanup(t)
	origHome := os.Getenv("HOME")
	origAxis := os.Getenv("AXIS_HOME")
	os.Setenv("HOME", tmpDir)
	os.Setenv("AXIS_HOME", filepath.Join(tmpDir, ".axis"))
	defer os.Setenv("HOME", origHome)
	defer os.Setenv("AXIS_HOME", origAxis)

	runner := &fakeModelRunner{}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	defer func() { defaultModelRunner = prevRunner }()

	cmd := modelEvictCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"bonsai2-27b", "--node", "cranium"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("model evict failed: %v\n%s", err, buf.String())
	}
	if cacheCalls != 0 {
		t.Fatalf("default evict read the daemon cache %d times", cacheCalls)
	}
	loaded := loadOnlyEvictionReceipt(t)
	if loaded.SnapshotSource != "live" {
		t.Fatalf("snapshot source = %q, want live", loaded.SnapshotSource)
	}
}

func TestModelEvictLiveFalseUsesDaemonCache(t *testing.T) {
	snap := makeDualGPUSnapshot()
	liveCalls := 0
	prevLive := loadModelSnapshot
	loadModelSnapshot = func(context.Context) (*models.ClusterSnapshot, error) {
		liveCalls++
		return snap, nil
	}
	prevFetch := fetchModelInventorySnapshot
	fetchModelInventorySnapshot = func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return snap, "daemon-cache", nil
	}
	t.Cleanup(func() {
		loadModelSnapshot = prevLive
		fetchModelInventorySnapshot = prevFetch
	})
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "cranium"}}})

	tmpDir := t.TempDir()
	drainModelEventsBeforeTempCleanup(t)
	origHome := os.Getenv("HOME")
	origAxis := os.Getenv("AXIS_HOME")
	os.Setenv("HOME", tmpDir)
	os.Setenv("AXIS_HOME", filepath.Join(tmpDir, ".axis"))
	defer os.Setenv("HOME", origHome)
	defer os.Setenv("AXIS_HOME", origAxis)

	runner := &fakeModelRunner{}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	defer func() { defaultModelRunner = prevRunner }()

	cmd := modelEvictCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"bonsai2-27b", "--node", "cranium", "--live=false"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("model evict failed: %v\n%s", err, buf.String())
	}
	if liveCalls != 0 {
		t.Fatalf("--live=false collected a live snapshot %d times", liveCalls)
	}
	loaded := loadOnlyEvictionReceipt(t)
	if loaded.SnapshotSource != "daemon-cache" {
		t.Fatalf("snapshot source = %q, want daemon-cache", loaded.SnapshotSource)
	}
}

func TestModelEvictByModelName(t *testing.T) {
	snap := makeDualGPUSnapshot()
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "cranium"}}})

	tmpDir := t.TempDir()
	drainModelEventsBeforeTempCleanup(t)
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	runner := &fakeModelRunner{}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	defer func() { defaultModelRunner = prevRunner }()

	cmd := modelEvictCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"bonsai2-27b", "--node", "cranium"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("model evict failed: %v\n%s", err, buf.String())
	}

	if len(runner.evictedTargets) != 1 {
		t.Fatalf("evicted targets count = %d, want 1", len(runner.evictedTargets))
	}
	target := runner.evictedTargets[0]
	if target.Model != "bonsai2-27b" || target.SupervisorUnit != "bonsai2-27b.service" {
		t.Errorf("unexpected target: %+v", target)
	}
	if !strings.Contains(buf.String(), "evicted bonsai2-27b on cranium") {
		t.Errorf("output does not report eviction: %q", buf.String())
	}
}

func TestModelEvictByGPUIndex(t *testing.T) {
	snap := makeDualGPUSnapshot()
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "cranium"}}})

	tmpDir := t.TempDir()
	drainModelEventsBeforeTempCleanup(t)
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	runner := &fakeModelRunner{}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	defer func() { defaultModelRunner = prevRunner }()

	cmd := modelEvictCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--node", "cranium", "--gpu", "1"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("model evict failed: %v\n%s", err, buf.String())
	}

	if len(runner.evictedTargets) != 1 {
		t.Fatalf("evicted targets count = %d, want 1", len(runner.evictedTargets))
	}
	if runner.evictedTargets[0].Model != "coder7b" {
		t.Errorf("expected coder7b for GPU 1, got %q", runner.evictedTargets[0].Model)
	}
}

func TestModelEvictAll(t *testing.T) {
	snap := makeDualGPUSnapshot()
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "cranium"}}})

	tmpDir := t.TempDir()
	drainModelEventsBeforeTempCleanup(t)
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	runner := &fakeModelRunner{}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	defer func() { defaultModelRunner = prevRunner }()

	cmd := modelEvictCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--node", "cranium", "--all"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("model evict failed: %v\n%s", err, buf.String())
	}

	if len(runner.evictedTargets) != 2 {
		t.Fatalf("evicted targets count = %d, want 2", len(runner.evictedTargets))
	}
	if !strings.Contains(buf.String(), "2 instance(s)") {
		t.Errorf("output does not report 2 instances: %q", buf.String())
	}
}

func TestModelEvictGPUIndexFiltersAll(t *testing.T) {
	snap := makeDualGPUSnapshot()
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "cranium"}}})

	tmpDir := t.TempDir()
	drainModelEventsBeforeTempCleanup(t)
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	runner := &fakeModelRunner{}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	defer func() { defaultModelRunner = prevRunner }()

	cmd := modelEvictCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--node", "cranium", "--gpu", "1", "--all"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("model evict failed: %v\n%s", err, buf.String())
	}
	if len(runner.evictedTargets) != 1 {
		t.Fatalf("evicted targets count = %d, want 1 (GPU 1 only)", len(runner.evictedTargets))
	}
	if runner.evictedTargets[0].Model != "coder7b" {
		t.Errorf("expected coder7b for --gpu 1 --all, got %q", runner.evictedTargets[0].Model)
	}
}

func TestModelEvictGPUIndexMissesEmptyIndices(t *testing.T) {
	snap := makeDualGPUSnapshot()
	for ni := range snap.Nodes {
		for ri := range snap.Nodes[ni].ResidentModels {
			snap.Nodes[ni].ResidentModels[ri].GPUIndices = nil
		}
	}
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "cranium"}}})

	runner := &fakeModelRunner{}
	cmd := modelEvictCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--node", "cranium", "--gpu", "0"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected no match when GPU indices were not observed")
	}
	if !strings.Contains(err.Error(), "no resident model instances matched") {
		t.Fatalf("error = %v, want no resident model instances matched", err)
	}
	if len(runner.evictedTargets) != 0 {
		t.Fatalf("unexpected eviction occurred: %+v", runner.evictedTargets)
	}
}

func TestModelEvictForcePersistsMode(t *testing.T) {
	snap := makeDualGPUSnapshot()
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "cranium"}}})

	tmpDir := t.TempDir()
	drainModelEventsBeforeTempCleanup(t)
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	runner := &fakeModelRunner{}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	defer func() { defaultModelRunner = prevRunner }()

	cmd := modelEvictCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"bonsai2-27b", "--node", "cranium", "--mode", "force"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("model evict failed: %v\n%s", err, buf.String())
	}
	loaded := loadOnlyEvictionReceipt(t)
	if loaded.Mode != string(modellife.EvictModeForce) {
		t.Fatalf("receipt mode = %q, want force", loaded.Mode)
	}
	script := modellife.BuildResumeShellScript(*loaded)
	if !strings.Contains(script, "systemctl --user unmask --runtime 'bonsai2-27b.service'") {
		t.Fatalf("forced receipt resume script = %s", script)
	}
}

func loadOnlyEvictionReceipt(t *testing.T) *modellife.EvictionReceipt {
	t.Helper()
	entries, err := os.ReadDir(modellife.ReceiptDirectory())
	if err != nil {
		t.Fatalf("receipt dir: %v", err)
	}
	var names []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "evict-") && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	if len(names) != 1 {
		t.Fatalf("receipt files = %v, want one", names)
	}
	id := strings.TrimSuffix(strings.TrimPrefix(names[0], "evict-"), ".json")
	loaded, err := modellife.LoadEvictionReceipt(id)
	if err != nil {
		t.Fatalf("load receipt: %v", err)
	}
	return loaded
}

func TestModelEvictFreezeReportsNoReclaimedVRAM(t *testing.T) {
	snap := makeDualGPUSnapshot()
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "cranium"}}})

	tmpDir := t.TempDir()
	drainModelEventsBeforeTempCleanup(t)
	origHome := os.Getenv("HOME")
	origAxis := os.Getenv("AXIS_HOME")
	os.Setenv("HOME", tmpDir)
	os.Setenv("AXIS_HOME", filepath.Join(tmpDir, ".axis"))
	defer os.Setenv("HOME", origHome)
	defer os.Setenv("AXIS_HOME", origAxis)

	runner := &fakeModelRunner{}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	defer func() { defaultModelRunner = prevRunner }()

	cmd := modelEvictCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"bonsai2-27b", "--node", "cranium", "--mode", "freeze"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("model evict failed: %v\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "reclaimed 0 MiB") {
		t.Fatalf("output = %q, want reclaimed 0 MiB", buf.String())
	}
	if strings.Contains(buf.String(), "9842") {
		t.Fatalf("freeze reported weight size as reclaimed VRAM: %q", buf.String())
	}
	loaded := loadOnlyEvictionReceipt(t)
	if loaded.ReclaimedVRAMMB != 0 {
		t.Fatalf("receipt reclaimed = %d, want 0", loaded.ReclaimedVRAMMB)
	}
	if len(loaded.EvictedInstances) != 1 || loaded.EvictedInstances[0].VRAMFreedMB != 0 {
		t.Fatalf("instance freed = %+v, want 0", loaded.EvictedInstances)
	}
}

func TestModelEvictStopUsesObservedVRAMDelta(t *testing.T) {
	snap := makeDualGPUSnapshot()
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "cranium"}}})

	tmpDir := t.TempDir()
	drainModelEventsBeforeTempCleanup(t)
	origHome := os.Getenv("HOME")
	origAxis := os.Getenv("AXIS_HOME")
	os.Setenv("HOME", tmpDir)
	os.Setenv("AXIS_HOME", filepath.Join(tmpDir, ".axis"))
	defer os.Setenv("HOME", origHome)
	defer os.Setenv("AXIS_HOME", origAxis)

	runner := &fakeModelRunner{
		evictResult: modellife.EvictResult{VRAMMeasured: true, ReclaimedVRAMMB: 800},
	}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	defer func() { defaultModelRunner = prevRunner }()

	cmd := modelEvictCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"bonsai2-27b", "--node", "cranium", "--mode", "stop"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("model evict failed: %v\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "reclaimed 800 MiB observed") {
		t.Fatalf("output = %q, want observed delta", buf.String())
	}
	if strings.Contains(buf.String(), "9842") || strings.Contains(buf.String(), "estimated") {
		t.Fatalf("output used the snapshot estimate: %q", buf.String())
	}
	loaded := loadOnlyEvictionReceipt(t)
	if !loaded.VRAMObserved || loaded.ReclaimedVRAMMB != 800 {
		t.Fatalf("receipt observed=%v reclaimed=%d, want observed 800", loaded.VRAMObserved, loaded.ReclaimedVRAMMB)
	}
	if len(loaded.EvictedInstances) != 1 || loaded.EvictedInstances[0].VRAMFreedMB != 800 {
		t.Fatalf("instance freed = %+v, want 800", loaded.EvictedInstances)
	}
}

func TestModelEvictAllKeepsObservedDeltaOnTheReceipt(t *testing.T) {
	snap := makeDualGPUSnapshot()
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "cranium"}}})

	tmpDir := t.TempDir()
	drainModelEventsBeforeTempCleanup(t)
	origHome := os.Getenv("HOME")
	origAxis := os.Getenv("AXIS_HOME")
	os.Setenv("HOME", tmpDir)
	os.Setenv("AXIS_HOME", filepath.Join(tmpDir, ".axis"))
	defer os.Setenv("HOME", origHome)
	defer os.Setenv("AXIS_HOME", origAxis)

	runner := &fakeModelRunner{
		evictResult: modellife.EvictResult{VRAMMeasured: true, ReclaimedVRAMMB: 800},
	}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	defer func() { defaultModelRunner = prevRunner }()

	cmd := modelEvictCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--all", "--node", "cranium", "--mode", "stop"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("model evict failed: %v\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "reclaimed 800 MiB observed") {
		t.Fatalf("output = %q, want the node-wide observed delta", buf.String())
	}
	loaded := loadOnlyEvictionReceipt(t)
	if !loaded.VRAMObserved || loaded.ReclaimedVRAMMB != 800 {
		t.Fatalf("receipt observed=%v reclaimed=%d, want observed 800", loaded.VRAMObserved, loaded.ReclaimedVRAMMB)
	}
	if len(loaded.EvictedInstances) != 2 {
		t.Fatalf("instances = %d, want 2", len(loaded.EvictedInstances))
	}
	for _, inst := range loaded.EvictedInstances {
		if inst.VRAMFreedMB != 0 {
			t.Fatalf("per-instance freed = %d, want 0 when the delta is node-wide", inst.VRAMFreedMB)
		}
	}
}

func TestModelEvictFreezeIgnoresMeasuredDelta(t *testing.T) {
	snap := makeDualGPUSnapshot()
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "cranium"}}})

	tmpDir := t.TempDir()
	drainModelEventsBeforeTempCleanup(t)
	origHome := os.Getenv("HOME")
	origAxis := os.Getenv("AXIS_HOME")
	os.Setenv("HOME", tmpDir)
	os.Setenv("AXIS_HOME", filepath.Join(tmpDir, ".axis"))
	defer os.Setenv("HOME", origHome)
	defer os.Setenv("AXIS_HOME", origAxis)

	runner := &fakeModelRunner{
		evictResult: modellife.EvictResult{VRAMMeasured: true, ReclaimedVRAMMB: 800, Freeze: true},
	}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	defer func() { defaultModelRunner = prevRunner }()

	cmd := modelEvictCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"bonsai2-27b", "--node", "cranium", "--mode", "freeze"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("model evict failed: %v\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "reclaimed 0 MiB") || strings.Contains(buf.String(), "800") {
		t.Fatalf("output = %q, want reclaimed 0 MiB", buf.String())
	}
	loaded := loadOnlyEvictionReceipt(t)
	if loaded.VRAMObserved || loaded.ReclaimedVRAMMB != 0 {
		t.Fatalf("receipt observed=%v reclaimed=%d, want unobserved 0", loaded.VRAMObserved, loaded.ReclaimedVRAMMB)
	}
	if len(loaded.EvictedInstances) != 1 || loaded.EvictedInstances[0].VRAMFreedMB != 0 {
		t.Fatalf("instance freed = %+v, want 0", loaded.EvictedInstances)
	}
}

func TestModelEvictStopUnmeasuredDoesNotUseSnapshot(t *testing.T) {
	snap := makeDualGPUSnapshot()
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "cranium"}}})

	tmpDir := t.TempDir()
	drainModelEventsBeforeTempCleanup(t)
	origHome := os.Getenv("HOME")
	origAxis := os.Getenv("AXIS_HOME")
	os.Setenv("HOME", tmpDir)
	os.Setenv("AXIS_HOME", filepath.Join(tmpDir, ".axis"))
	defer os.Setenv("HOME", origHome)
	defer os.Setenv("AXIS_HOME", origAxis)

	runner := &fakeModelRunner{}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	defer func() { defaultModelRunner = prevRunner }()

	cmd := modelEvictCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"bonsai2-27b", "--node", "cranium", "--mode", "stop"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("model evict failed: %v\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "VRAM unmeasured") {
		t.Fatalf("output = %q, want VRAM unmeasured", buf.String())
	}
	if strings.Contains(buf.String(), "9842") || strings.Contains(buf.String(), "estimated") {
		t.Fatalf("unmeasured evict reused the snapshot size: %q", buf.String())
	}
	loaded := loadOnlyEvictionReceipt(t)
	if loaded.VRAMObserved || loaded.ReclaimedVRAMMB != 0 {
		t.Fatalf("receipt observed=%v reclaimed=%d, want unmeasured 0", loaded.VRAMObserved, loaded.ReclaimedVRAMMB)
	}
	if len(loaded.EvictedInstances) != 1 || loaded.EvictedInstances[0].VRAMFreedMB != 0 {
		t.Fatalf("instance freed = %+v, want 0", loaded.EvictedInstances)
	}
}

func TestModelEvictReceiptSaveFailureIsNotSuccess(t *testing.T) {
	snap := makeDualGPUSnapshot()
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "cranium"}}})

	tmpDir := t.TempDir()
	drainModelEventsBeforeTempCleanup(t)
	blocked := filepath.Join(tmpDir, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	origHome := os.Getenv("HOME")
	origAxis := os.Getenv("AXIS_HOME")
	os.Setenv("HOME", tmpDir)
	os.Setenv("AXIS_HOME", blocked)
	defer os.Setenv("HOME", origHome)
	defer os.Setenv("AXIS_HOME", origAxis)

	runner := &fakeModelRunner{}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	defer func() { defaultModelRunner = prevRunner }()

	cmd := modelEvictCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"bonsai2-27b", "--node", "cranium"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected receipt save failure")
	}
	if strings.Contains(buf.String(), "evicted ") {
		t.Fatalf("save failure printed success: %q", buf.String())
	}
	if len(runner.evictedTargets) != 1 {
		t.Fatalf("evict calls = %d, want the stop to have been attempted", len(runner.evictedTargets))
	}
}

func TestModelEvictFailsWhenNoMatch(t *testing.T) {
	snap := makeDualGPUSnapshot()
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "cranium"}}})

	runner := &fakeModelRunner{}
	cmd := modelEvictCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"unknown-model", "--node", "cranium"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for unknown model")
	}
	if len(runner.evictedTargets) != 0 {
		t.Fatalf("unexpected eviction occurred: %+v", runner.evictedTargets)
	}
}

func TestModelResumeByReceiptID(t *testing.T) {
	snap := makeDualGPUSnapshot()
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "cranium"}}})

	tmpDir := t.TempDir()
	drainModelEventsBeforeTempCleanup(t)
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	receipt := modellife.EvictionReceipt{
		ID:              "mo-test-receipt",
		Node:            "cranium",
		Action:          "evict",
		Status:          models.ModelOperationCompleted,
		ReclaimedVRAMMB: 9842,
		EvictedInstances: []modellife.EvictedInstanceReceipt{
			{
				InstanceID:     "mi-cranium-llama.cpp-8082",
				Model:          "bonsai2-27b",
				SupervisorType: "systemd-user",
				SupervisorUnit: "bonsai2-27b.service",
			},
		},
	}
	if _, err := modellife.SaveEvictionReceipt(receipt); err != nil {
		t.Fatalf("SaveEvictionReceipt failed: %v", err)
	}

	runner := &fakeModelRunner{}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	defer func() { defaultModelRunner = prevRunner }()

	cmd := modelResumeCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--receipt", "mo-test-receipt"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("model resume failed: %v\n%s", err, buf.String())
	}

	if len(runner.resumedReceipts) != 1 {
		t.Fatalf("resumed receipts count = %d, want 1", len(runner.resumedReceipts))
	}
	if runner.resumedReceipts[0].ID != "mo-test-receipt" {
		t.Errorf("resumed receipt ID = %q, want mo-test-receipt", runner.resumedReceipts[0].ID)
	}
	if !strings.Contains(buf.String(), "resumed on cranium") {
		t.Errorf("output does not report resume: %q", buf.String())
	}
}

func TestModelResumeAwaitsReceiptPort(t *testing.T) {
	snap := makeDualGPUSnapshot()
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "cranium"}}})

	tmpDir := t.TempDir()
	drainModelEventsBeforeTempCleanup(t)
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	receipt := modellife.EvictionReceipt{
		ID:   "mo-await-port",
		Node: "cranium",
		EvictedInstances: []modellife.EvictedInstanceReceipt{
			{
				InstanceID:     "mi-cranium-llama.cpp-8082",
				Model:          "bonsai2-27b",
				Port:           8082,
				SupervisorType: "systemd-user",
				SupervisorUnit: "bonsai2-27b.service",
			},
		},
	}
	if _, err := modellife.SaveEvictionReceipt(receipt); err != nil {
		t.Fatal(err)
	}

	runner := &fakeModelRunner{}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	defer func() { defaultModelRunner = prevRunner }()

	cmd := modelResumeCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--receipt", "mo-await-port", "--timeout", "45s"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("model resume failed: %v\n%s", err, buf.String())
	}
	if len(runner.resumedReceipts) != 1 {
		t.Fatalf("resume calls = %d, want 1", len(runner.resumedReceipts))
	}
	if len(runner.awaitedPorts) != 1 || runner.awaitedPorts[0] != 8082 {
		t.Fatalf("awaited ports = %v, want [8082]", runner.awaitedPorts)
	}
	if len(runner.awaitTimeouts) != 1 || runner.awaitTimeouts[0] != 45*time.Second {
		t.Fatalf("await timeouts = %v, want 45s", runner.awaitTimeouts)
	}
}

func TestModelResumeSystemUnitStartsOnSystemBus(t *testing.T) {
	snap := makeDualGPUSnapshot()
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "cranium"}}})

	tmpDir := t.TempDir()
	drainModelEventsBeforeTempCleanup(t)
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	receipt := modellife.EvictionReceipt{
		ID:   "mo-system-unit",
		Node: "cranium",
		EvictedInstances: []modellife.EvictedInstanceReceipt{
			{
				Model:          "coder7b",
				Port:           8081,
				SupervisorType: "systemd-system",
				SupervisorUnit: "coder7b.service",
			},
		},
	}
	if _, err := modellife.SaveEvictionReceipt(receipt); err != nil {
		t.Fatal(err)
	}

	runner := &fakeModelRunner{}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	defer func() { defaultModelRunner = prevRunner }()

	cmd := modelResumeCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--receipt", "mo-system-unit"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("model resume failed: %v\n%s", err, buf.String())
	}
	if len(runner.resumedReceipts) != 1 {
		t.Fatalf("resume calls = %d, want 1", len(runner.resumedReceipts))
	}
	script := modellife.BuildResumeShellScript(runner.resumedReceipts[0])
	if strings.Contains(script, "--user") {
		t.Fatalf("system unit resume used the user bus: %s", script)
	}
	if !strings.Contains(script, "systemctl start 'coder7b.service'") {
		t.Fatalf("system unit resume script = %s", script)
	}
}

func TestModelResumeUnitNameRequiresKnownSupervisor(t *testing.T) {
	snap := makeDualGPUSnapshot()
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "cranium"}}})

	runner := &fakeModelRunner{}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	defer func() { defaultModelRunner = prevRunner }()

	cmd := modelResumeCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"missing-unit", "--node", "cranium"})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "supervisor type unknown") {
		t.Fatalf("error = %v, want supervisor type unknown\n%s", err, buf.String())
	}
	if len(runner.resumedReceipts) != 0 {
		t.Fatalf("resume ran without a supervisor: %+v", runner.resumedReceipts)
	}
}

func TestModelResumeUnitNameUsesSnapshotSupervisorAndPort(t *testing.T) {
	snap := makeDualGPUSnapshot()
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "cranium"}}})
	drainModelEventsBeforeTempCleanup(t)

	runner := &fakeModelRunner{}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	defer func() { defaultModelRunner = prevRunner }()

	cmd := modelResumeCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"bonsai2-27b", "--node", "cranium", "--timeout", "15s"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("model resume failed: %v\n%s", err, buf.String())
	}
	if len(runner.resumedReceipts) != 1 {
		t.Fatalf("resume calls = %d, want 1", len(runner.resumedReceipts))
	}
	got := runner.resumedReceipts[0].EvictedInstances
	if len(got) != 1 || got[0].SupervisorType != "systemd-user" || got[0].Port != 8082 {
		t.Fatalf("resumed instance = %+v, want systemd-user port 8082", got)
	}
	if len(runner.awaitedPorts) != 1 || runner.awaitedPorts[0] != 8082 {
		t.Fatalf("awaited ports = %v, want [8082]", runner.awaitedPorts)
	}
	script := modellife.BuildResumeShellScript(runner.resumedReceipts[0])
	if !strings.Contains(script, "systemctl --user start 'bonsai2-27b.service'") {
		t.Fatalf("unit resume script = %s", script)
	}
}
