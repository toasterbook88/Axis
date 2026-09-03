package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/modelinventory"
	"github.com/toasterbook88/axis/internal/modellife"
	"github.com/toasterbook88/axis/internal/models"
)

func generationStopSnapshot() *models.ClusterSnapshot {
	observed := time.Date(2026, 9, 3, 14, 0, 0, 0, time.UTC)
	return &models.ClusterSnapshot{
		Timestamp:   observed,
		Publication: &models.PublicationEnvelope{ID: "pub-generation-stop"},
		Nodes: []models.NodeFacts{{
			Name:        "storage",
			Identity:    models.NewNodeIdentity("storage-hardware-id", "machine-id"),
			Status:      models.StatusComplete,
			CollectedAt: observed,
			ResidentModels: []models.ResidentModel{{
				Name: "model-a", Runtime: "llama.cpp", Port: 8185, Source: "llama-server-ps",
				PID: 4242, Executable: "/opt/llama-server", ProcessOwner: "axis-user",
				ProcessStartToken: "Thu Sep 3 09:00:00 2026",
			}},
		}},
	}
}

func TestRunModelStopGenerationUsesDaemonCacheAndExactEvidence(t *testing.T) {
	snap := generationStopSnapshot()
	want := modelinventory.FromSnapshot(snap, "daemon-cache").Instances[0]
	previousFetch := fetchModelInventorySnapshot
	previousLive := loadModelSnapshot
	t.Cleanup(func() {
		fetchModelInventorySnapshot = previousFetch
		loadModelSnapshot = previousLive
	})
	fetchModelInventorySnapshot = func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return snap, "daemon-cache", nil
	}
	liveCalled := false
	loadModelSnapshot = func(context.Context) (*models.ClusterSnapshot, error) {
		liveCalled = true
		return nil, nil
	}
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	runner := &fakeModelRunner{stopDisposition: modelStopStopped}
	cmd := modelStopCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)

	if err := runModelStopGeneration(context.Background(), cmd, want.GenerationID, "test.sock", "json", runner); err != nil {
		t.Fatal(err)
	}
	if liveCalled {
		t.Fatal("generation stop performed a live fleet discovery")
	}
	if len(runner.stopTargets) != 1 {
		t.Fatalf("stop targets = %#v, want one", runner.stopTargets)
	}
	target := runner.stopTargets[0]
	if target.GenerationID != want.GenerationID || target.PID != 4242 || target.ProcessStartToken == "" {
		t.Fatalf("stop target lost generation evidence: %+v", target)
	}
	var receipt models.ModelOperationReceipt
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatalf("decode receipt: %v\n%s", err, out.String())
	}
	if receipt.Action != models.ModelOperationStop || receipt.Disposition != string(modelStopStopped) {
		t.Fatalf("receipt = %+v", receipt)
	}
	if !strings.HasPrefix(receipt.ID, "mo-") || receipt.Status != models.ModelOperationCompleted {
		t.Fatalf("receipt identity/status = %+v", receipt)
	}
	if receipt.GenerationID != want.GenerationID || receipt.InstanceID != want.ID || receipt.PublicationID != "pub-generation-stop" {
		t.Fatalf("receipt lost authority: %+v", receipt)
	}
}

func TestRunModelStopGenerationRejectsStableSlotID(t *testing.T) {
	snap := generationStopSnapshot()
	instance := modelinventory.FromSnapshot(snap, "daemon-cache").Instances[0]
	previousFetch := fetchModelInventorySnapshot
	t.Cleanup(func() { fetchModelInventorySnapshot = previousFetch })
	fetchModelInventorySnapshot = func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return snap, "daemon-cache", nil
	}
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	runner := &fakeModelRunner{}

	err := runModelStopGeneration(context.Background(), modelStopCmd(), instance.ID, "test.sock", "text", runner)
	if err == nil || !strings.Contains(err.Error(), "stable slot") {
		t.Fatalf("error = %v, want stable-slot refusal", err)
	}
	if len(runner.stopTargets) != 0 {
		t.Fatalf("stable slot reached runner: %#v", runner.stopTargets)
	}
}

func TestModelStopCommandAcceptsGenerationIDWithoutLegacyFlags(t *testing.T) {
	snap := generationStopSnapshot()
	instance := modelinventory.FromSnapshot(snap, "daemon-cache").Instances[0]
	previousFetch := fetchModelInventorySnapshot
	previousRunner := defaultModelRunner
	t.Cleanup(func() {
		fetchModelInventorySnapshot = previousFetch
		defaultModelRunner = previousRunner
	})
	fetchModelInventorySnapshot = func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return snap, "daemon-cache", nil
	}
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	runner := &fakeModelRunner{stopDisposition: modelStopStopped}
	defaultModelRunner = runner

	cmd := modelStopCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{instance.GenerationID, "--cache-addr", "test.sock", "--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(runner.stopTargets) != 1 || runner.stopTargets[0].GenerationID != instance.GenerationID {
		t.Fatalf("stop targets = %#v", runner.stopTargets)
	}
}

func TestRunModelStopGenerationHandlesMismatch(t *testing.T) {
	snap := generationStopSnapshot()
	instance := modelinventory.FromSnapshot(snap, "daemon-cache").Instances[0]
	previousFetch := fetchModelInventorySnapshot
	t.Cleanup(func() { fetchModelInventorySnapshot = previousFetch })
	fetchModelInventorySnapshot = func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return snap, "daemon-cache", nil
	}
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	runner := &fakeModelRunner{stopDisposition: modelStopGenerationMismatch}

	cmd := modelStopCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)

	err := runModelStopGeneration(context.Background(), cmd, instance.GenerationID, "test.sock", "json", runner)
	if err == nil {
		t.Fatal("expected error on generation mismatch, got nil")
	}
	exitErr, ok := err.(ExitCodeError)
	if !ok || exitErr.Code != ExitErrCommandFail {
		t.Fatalf("exit code = %v, want %d", err, ExitErrCommandFail)
	}
	var receipt models.ModelOperationReceipt
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatalf("decode receipt: %v\n%s", err, out.String())
	}
	if receipt.Status != models.ModelOperationRejected || receipt.Disposition != string(modelStopGenerationMismatch) {
		t.Fatalf("receipt = %+v", receipt)
	}
}

func TestShellStopTargetIncludesGenerationRevalidation(t *testing.T) {
	legacy := shellStopTarget(modellife.StopTarget{Port: 8080})
	if strings.Contains(legacy, "axis-stop-result:generation_mismatch") {
		t.Fatal("legacy stop unexpectedly included generation guard")
	}

	target := modellife.StopTarget{
		Port:              8080,
		PID:               4242,
		Executable:        "/opt/llama-server",
		ProcessOwner:      "axis-user",
		ProcessStartToken: "Thu Sep 3 09:00:00 2026",
		GenerationID:      "mg-test",
	}
	script := shellStopTarget(target)
	if !strings.Contains(script, "axis-stop-result:generation_mismatch") {
		t.Fatal("generation-bound stop omitted generation mismatch marker")
	}
	if !strings.Contains(script, "kill -KILL \"4242\"") {
		t.Fatal("generation-bound stop did not target specific PID 4242")
	}
	if !strings.Contains(script, "Thu Sep 3 09:00:00 2026") {
		t.Fatal("generation-bound stop omitted start token check")
	}
}
