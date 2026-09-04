package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/modelinventory"
	"github.com/toasterbook88/axis/internal/models"
)

func awaitTestSnapshot() *models.ClusterSnapshot {
	observed := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	return &models.ClusterSnapshot{
		Timestamp:   observed,
		Publication: &models.PublicationEnvelope{ID: "pub-await-test"},
		Nodes: []models.NodeFacts{{
			Name:        "storage",
			Identity:    models.NewNodeIdentity("storage-hw-id", "machine-id"),
			Status:      models.StatusComplete,
			CollectedAt: observed,
			ResidentModels: []models.ResidentModel{{
				Name:              "qwen3.8-27b",
				Runtime:           "llama.cpp",
				Port:              8082,
				Source:            "llama-server-ps",
				PID:               5555,
				Executable:        "/usr/local/bin/llama-server",
				ProcessOwner:      "axis",
				ProcessStartToken: "Fri Sep 4 10:00:00 2026",
			}},
		}},
	}
}

func TestRunModelAwait_SuccessJSON(t *testing.T) {
	snap := awaitTestSnapshot()
	inst := modelinventory.FromSnapshot(snap, "daemon-cache").Instances[0]

	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})

	runner := &fakeModelRunner{}
	cmd := modelAwaitCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)

	err := runModelAwait(context.Background(), cmd, inst.ID, 5*time.Second, 100*time.Millisecond, false, "test.sock", "json", runner)
	if err != nil {
		t.Fatalf("runModelAwait failed: %v", err)
	}

	var receipt models.ModelOperationReceipt
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatalf("unmarshal receipt JSON: %v (raw: %s)", err, out.String())
	}

	if receipt.Action != models.ModelOperationAwait {
		t.Errorf("action = %q, want await", receipt.Action)
	}
	if receipt.Status != models.ModelOperationCompleted {
		t.Errorf("status = %q, want completed", receipt.Status)
	}
	if receipt.Disposition != "ready" {
		t.Errorf("disposition = %q, want ready", receipt.Disposition)
	}
	if receipt.InstanceID != inst.ID {
		t.Errorf("instance_id = %q, want %q", receipt.InstanceID, inst.ID)
	}
	if receipt.Port != 8082 {
		t.Errorf("port = %d, want 8082", receipt.Port)
	}
	if len(runner.awaitedTargets) != 1 || runner.awaitedTargets[0] != inst.ID {
		t.Errorf("runner awaited = %v, want [%s]", runner.awaitedTargets, inst.ID)
	}
}

func TestRunModelAwait_SuccessText(t *testing.T) {
	snap := awaitTestSnapshot()
	inst := modelinventory.FromSnapshot(snap, "daemon-cache").Instances[0]

	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})

	runner := &fakeModelRunner{}
	cmd := modelAwaitCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)

	err := runModelAwait(context.Background(), cmd, inst.ID, 5*time.Second, 100*time.Millisecond, false, "test.sock", "text", runner)
	if err != nil {
		t.Fatalf("runModelAwait failed: %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "ready storage:8082 instance "+inst.ID) {
		t.Errorf("output does not contain expected ready line: %q", output)
	}
}

func TestRunModelAwait_MatchByModelName(t *testing.T) {
	snap := awaitTestSnapshot()
	inst := modelinventory.FromSnapshot(snap, "daemon-cache").Instances[0]

	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})

	runner := &fakeModelRunner{}
	cmd := modelAwaitCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)

	err := runModelAwait(context.Background(), cmd, "qwen3.8-27b", 5*time.Second, 100*time.Millisecond, false, "test.sock", "text", runner)
	if err != nil {
		t.Fatalf("runModelAwait by model name failed: %v", err)
	}

	if len(runner.awaitedTargets) != 1 || runner.awaitedTargets[0] != inst.ID {
		t.Errorf("runner awaited = %v, want [%s]", runner.awaitedTargets, inst.ID)
	}
}

func TestRunModelAwait_TimeoutExitCode4(t *testing.T) {
	snap := awaitTestSnapshot()
	inst := modelinventory.FromSnapshot(snap, "daemon-cache").Instances[0]

	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})

	runner := &fakeModelRunner{
		awaitErr: fmt.Errorf("timed out after 5s waiting for instance %s", inst.ID),
	}
	cmd := modelAwaitCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)

	err := runModelAwait(context.Background(), cmd, inst.ID, 5*time.Second, 100*time.Millisecond, false, "test.sock", "text", runner)
	if err == nil {
		t.Fatal("expected error on timeout, got nil")
	}

	if got := ExitCode(err); got != ExitErrCommandFail {
		t.Fatalf("ExitCode = %d, want %d", got, ExitErrCommandFail)
	}

	output := out.String()
	if !strings.Contains(output, "timeout storage:8082 instance "+inst.ID) {
		t.Errorf("output does not contain expected timeout line: %q", output)
	}
}

func TestRunModelAwait_UnknownInstanceExitCode4(t *testing.T) {
	snap := awaitTestSnapshot()
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})

	runner := &fakeModelRunner{}
	cmd := modelAwaitCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)

	err := runModelAwait(context.Background(), cmd, "mi-nonexistent", 5*time.Second, 100*time.Millisecond, false, "test.sock", "text", runner)
	if err == nil {
		t.Fatal("expected error for nonexistent instance, got nil")
	}

	if got := ExitCode(err); got != ExitErrCommandFail {
		t.Fatalf("ExitCode = %d, want %d", got, ExitErrCommandFail)
	}
}
