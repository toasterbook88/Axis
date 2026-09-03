package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
)

func TestRunModelStartCacheFirstByDefault(t *testing.T) {
	snap := testSnap()
	snap.Publication = &models.PublicationEnvelope{
		ID: "pub-start-cache-test",
	}
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})

	var cacheCalled, liveCalled bool
	prevFetch := fetchModelInventorySnapshot
	fetchModelInventorySnapshot = func(_ context.Context, addr string) (*models.ClusterSnapshot, string, error) {
		cacheCalled = true
		if addr != "custom.sock" {
			t.Fatalf("cache addr = %q, want custom.sock", addr)
		}
		return snap, "daemon-cache", nil
	}
	t.Cleanup(func() { fetchModelInventorySnapshot = prevFetch })

	prevLive := loadModelSnapshot
	loadModelSnapshot = func(_ context.Context) (*models.ClusterSnapshot, error) {
		liveCalled = true
		return snap, nil
	}
	t.Cleanup(func() { loadModelSnapshot = prevLive })

	runner := &fakeModelRunner{}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	t.Cleanup(func() { defaultModelRunner = prevRunner })

	cmd := modelStartCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{
		"--node", "storage",
		"--weights", "/mnt/models/a.gguf",
		"--port", "8081",
		"--cache-addr", "custom.sock",
		"--format", "json",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if !cacheCalled {
		t.Fatal("expected fetchModelInventorySnapshot to be called")
	}
	if liveCalled {
		t.Fatal("expected loadModelSnapshot NOT to be called when running cache-first")
	}

	var receipt models.ModelOperationReceipt
	if err := json.Unmarshal(buf.Bytes(), &receipt); err != nil {
		t.Fatalf("json unmarshal receipt: %v\noutput=%s", err, buf.String())
	}
	if receipt.Schema != "axis.model-operation/v1" {
		t.Errorf("receipt.Schema = %q, want axis.model-operation/v1", receipt.Schema)
	}
	if receipt.Action != models.ModelOperationStart {
		t.Errorf("receipt.Action = %q, want %q", receipt.Action, models.ModelOperationStart)
	}
	if receipt.Status != models.ModelOperationCompleted {
		t.Errorf("receipt.Status = %q, want %q", receipt.Status, models.ModelOperationCompleted)
	}
	if receipt.Disposition != "started" {
		t.Errorf("receipt.Disposition = %q, want started", receipt.Disposition)
	}
	if receipt.Node != "storage" {
		t.Errorf("receipt.Node = %q, want storage", receipt.Node)
	}
	if receipt.Port != 8081 {
		t.Errorf("receipt.Port = %d, want 8081", receipt.Port)
	}
	if receipt.Volume != "/mnt/models" {
		t.Errorf("receipt.Volume = %q, want /mnt/models", receipt.Volume)
	}
	if receipt.Weights != "/mnt/models/a.gguf" {
		t.Errorf("receipt.Weights = %q, want /mnt/models/a.gguf", receipt.Weights)
	}
	if receipt.Model != "a.gguf" {
		t.Errorf("receipt.Model = %q, want a.gguf", receipt.Model)
	}
	if receipt.SnapshotSource != "daemon-cache" {
		t.Errorf("receipt.SnapshotSource = %q, want daemon-cache", receipt.SnapshotSource)
	}
	if receipt.PublicationID != "pub-start-cache-test" {
		t.Errorf("receipt.PublicationID = %q, want pub-start-cache-test", receipt.PublicationID)
	}
	if len(runner.started) != 1 || len(runner.probed) != 1 {
		t.Fatalf("runner executions: started=%+v probed=%+v", runner.started, runner.probed)
	}
}

func TestRunModelStartExplicitLive(t *testing.T) {
	snap := testSnap()
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})

	var cacheCalled, liveCalled bool
	prevFetch := fetchModelInventorySnapshot
	fetchModelInventorySnapshot = func(_ context.Context, _ string) (*models.ClusterSnapshot, string, error) {
		cacheCalled = true
		return snap, "daemon-cache", nil
	}
	t.Cleanup(func() { fetchModelInventorySnapshot = prevFetch })

	prevLive := loadModelSnapshot
	loadModelSnapshot = func(_ context.Context) (*models.ClusterSnapshot, error) {
		liveCalled = true
		return snap, nil
	}
	t.Cleanup(func() { loadModelSnapshot = prevLive })

	runner := &fakeModelRunner{}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	t.Cleanup(func() { defaultModelRunner = prevRunner })

	cmd := modelStartCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{
		"--node", "storage",
		"--weights", "/mnt/models/a.gguf",
		"--port", "8081",
		"--live",
		"--format", "text",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if cacheCalled {
		t.Fatal("expected fetchModelInventorySnapshot NOT to be called when --live is passed")
	}
	if !liveCalled {
		t.Fatal("expected loadModelSnapshot to be called when --live is passed")
	}
	if !strings.Contains(buf.String(), "started /usr/local/bin/llama-server on storage:8081 volume /mnt/models") {
		t.Fatalf("unexpected output: %q", buf.String())
	}
}

func TestRunModelStartEmitsYAMLReceipt(t *testing.T) {
	snap := testSnap()
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})

	runner := &fakeModelRunner{}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	t.Cleanup(func() { defaultModelRunner = prevRunner })

	cmd := modelStartCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{
		"--node", "storage",
		"--weights", "/mnt/models/a.gguf",
		"--port", "8081",
		"--format", "yaml",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	var receipt models.ModelOperationReceipt
	if err := yaml.Unmarshal(buf.Bytes(), &receipt); err != nil {
		t.Fatalf("yaml unmarshal failed: %v\noutput=%s", err, buf.String())
	}
	if receipt.Schema != "axis.model-operation/v1" {
		t.Errorf("receipt.Schema = %q", receipt.Schema)
	}
	if receipt.Action != models.ModelOperationStart {
		t.Errorf("receipt.Action = %q", receipt.Action)
	}
	if receipt.Status != models.ModelOperationCompleted {
		t.Errorf("receipt.Status = %q", receipt.Status)
	}
}

func TestRunModelStartRejectsOccupiedPort(t *testing.T) {
	snap := testSnap()
	snap.Publication = &models.PublicationEnvelope{ID: "pub-occupied-check"}
	snap.Nodes[0].ResidentModels = []models.ResidentModel{
		{
			Name:    "bitnet-2B",
			Runtime: "llama.cpp",
			Port:    8081,
		},
	}
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})

	runner := &fakeModelRunner{}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	t.Cleanup(func() { defaultModelRunner = prevRunner })

	cmd := modelStartCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{
		"--node", "storage",
		"--weights", "/mnt/models/a.gguf",
		"--port", "8081",
		"--format", "json",
	})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for occupied port")
	}
	if got := ExitCode(err); got != ExitErrCommandFail {
		t.Fatalf("ExitCode = %d, want %d", got, ExitErrCommandFail)
	}

	var receipt models.ModelOperationReceipt
	if parseErr := json.Unmarshal(buf.Bytes(), &receipt); parseErr != nil {
		t.Fatalf("json parse receipt: %v\noutput=%s", parseErr, buf.String())
	}
	if receipt.Status != models.ModelOperationRejected {
		t.Errorf("receipt.Status = %q, want %q", receipt.Status, models.ModelOperationRejected)
	}
	if receipt.Disposition != "port_occupied" {
		t.Errorf("receipt.Disposition = %q, want port_occupied", receipt.Disposition)
	}
	if !strings.Contains(receipt.Error, "already occupied by resident model") {
		t.Errorf("receipt.Error = %q", receipt.Error)
	}
	if len(runner.started) != 0 || len(runner.probed) != 0 {
		t.Fatalf("runner should not have been called: %+v", runner)
	}
}

type probeFailRunner struct {
	fakeModelRunner
}

func (p *probeFailRunner) Probe(_ context.Context, _ models.NodeFacts, _ *config.NodeConfig, _ int) error {
	return errors.New("connection refused on probe")
}

func TestRunModelStartFailsWhenProbeFails(t *testing.T) {
	snap := testSnap()
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})

	runner := &probeFailRunner{}
	cmd := modelStartCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := runModelStart(context.Background(), cmd, "storage", "/mnt/models/a.gguf", 8081, runner)
	if err == nil || !strings.Contains(err.Error(), "probe failed") {
		t.Fatalf("expected probe failed error, got: %v", err)
	}
	if !strings.Contains(buf.String(), "failed") {
		t.Fatalf("expected failure output, got: %q", buf.String())
	}
}
