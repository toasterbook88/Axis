package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/modellife"
	"github.com/toasterbook88/axis/internal/models"
)

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

func TestModelEvictByModelName(t *testing.T) {
	snap := makeDualGPUSnapshot()
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "cranium"}}})

	tmpDir := t.TempDir()
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
