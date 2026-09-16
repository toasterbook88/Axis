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

func queryTestSnapshot() *models.ClusterSnapshot {
	observed := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	return &models.ClusterSnapshot{
		Timestamp:   observed,
		Publication: &models.PublicationEnvelope{ID: "pub-query-test"},
		Nodes: []models.NodeFacts{
			{
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
			},
			{
				Name:        "compute",
				Identity:    models.NewNodeIdentity("compute-hw-id", "machine-id-2"),
				Status:      models.StatusComplete,
				CollectedAt: observed,
				ResidentModels: []models.ResidentModel{{
					Name:              "qwen3.8-27b",
					Runtime:           "llama.cpp",
					Port:              8085,
					Source:            "llama-server-ps",
					PID:               6666,
					Executable:        "/usr/local/bin/llama-server",
					ProcessOwner:      "axis",
					ProcessStartToken: "Fri Sep 4 10:05:00 2026",
				}},
			},
		},
	}
}

func TestRunModelQuery_SuccessText(t *testing.T) {
	snap := queryTestSnapshot()
	inst := modelinventory.FromSnapshot(snap, "daemon-cache").Instances[0]

	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{
		Nodes: []config.NodeConfig{{Name: "storage"}, {Name: "compute"}},
	})

	runner := &fakeModelRunner{}
	cmd := modelQueryCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)

	err := runModelQuery(context.Background(), cmd, inst.ID, "what is 2+2?", "", 256, 0.7, false, "test.sock", "text", runner)
	if err != nil {
		t.Fatalf("runModelQuery failed: %v", err)
	}

	output := strings.TrimSpace(out.String())
	want := "mock response to: what is 2+2?"
	if output != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
}

func TestRunModelQuery_SuccessJSON(t *testing.T) {
	snap := queryTestSnapshot()
	inst := modelinventory.FromSnapshot(snap, "daemon-cache").Instances[0]

	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{
		Nodes: []config.NodeConfig{{Name: "storage"}, {Name: "compute"}},
	})

	runner := &fakeModelRunner{}
	cmd := modelQueryCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)

	err := runModelQuery(context.Background(), cmd, inst.ID, "hello world", "", 128, 0.0, false, "test.sock", "json", runner)
	if err != nil {
		t.Fatalf("runModelQuery failed: %v", err)
	}

	var receipt models.ModelOperationReceipt
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatalf("unmarshal receipt: %v (raw: %s)", err, out.String())
	}

	if receipt.Action != models.ModelOperationQuery {
		t.Errorf("action = %q, want query", receipt.Action)
	}
	if receipt.Status != models.ModelOperationCompleted {
		t.Errorf("status = %q, want completed", receipt.Status)
	}
	if receipt.Disposition != "answered" {
		t.Errorf("disposition = %q, want answered", receipt.Disposition)
	}
	if receipt.InstanceID != inst.ID {
		t.Errorf("instance_id = %q, want %q", receipt.InstanceID, inst.ID)
	}
	if receipt.ResponseText != "mock response to: hello world" {
		t.Errorf("response_text = %q, want 'mock response to: hello world'", receipt.ResponseText)
	}
	if receipt.PromptTokens != 10 || receipt.CompletionTokens != 5 {
		t.Errorf("token counts unexpected: prompt=%d completion=%d", receipt.PromptTokens, receipt.CompletionTokens)
	}
}

func TestRunModelQuery_FilterByNode(t *testing.T) {
	snap := queryTestSnapshot()
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{
		Nodes: []config.NodeConfig{{Name: "storage"}, {Name: "compute"}},
	})

	runner := &fakeModelRunner{}
	cmd := modelQueryCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)

	err := runModelQuery(context.Background(), cmd, "qwen3.8-27b", "ping", "compute", 128, 0.5, false, "test.sock", "text", runner)
	if err != nil {
		t.Fatalf("runModelQuery with node filter failed: %v", err)
	}

	if len(runner.queriedTargets) != 1 {
		t.Fatalf("queriedTargets len = %d, want 1", len(runner.queriedTargets))
	}
	// Target should be the compute instance, not storage
	computeInst := modelinventory.FromSnapshot(snap, "daemon-cache").Instances[0]
	for _, inst := range modelinventory.FromSnapshot(snap, "daemon-cache").Instances {
		if inst.Node == "compute" {
			computeInst = inst
			break
		}
	}
	if runner.queriedTargets[0] != computeInst.ID {
		t.Errorf("queried target = %q, want compute instance %q", runner.queriedTargets[0], computeInst.ID)
	}
}

func TestRunModelQuery_EmptyPromptFails(t *testing.T) {
	cmd := modelQueryCmd()
	err := runModelQuery(context.Background(), cmd, "qwen3.8-27b", "   ", "", 128, 0.5, false, "test.sock", "text", &fakeModelRunner{})
	if err == nil {
		t.Fatal("expected error on empty prompt, got nil")
	}
}

func TestRunModelQuery_UnknownTargetExitCode4(t *testing.T) {
	snap := queryTestSnapshot()
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})

	cmd := modelQueryCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)

	err := runModelQuery(context.Background(), cmd, "nonexistent-model", "test", "", 128, 0.5, false, "test.sock", "text", &fakeModelRunner{})
	if err == nil {
		t.Fatal("expected error for nonexistent model, got nil")
	}

	if got := ExitCode(err); got != ExitErrCommandFail {
		t.Fatalf("ExitCode = %d, want %d", got, ExitErrCommandFail)
	}
}

func TestRunModelQuery_FailureExitCode4(t *testing.T) {
	snap := queryTestSnapshot()
	var inst models.ModelInstance
	for _, it := range modelinventory.FromSnapshot(snap, "daemon-cache").Instances {
		if it.Node == "storage" {
			inst = it
			break
		}
	}

	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{
		Nodes: []config.NodeConfig{{Name: "storage"}, {Name: "compute"}},
	})

	runner := &fakeModelRunner{
		queryErr: fmt.Errorf("connection refused to engine"),
	}
	cmd := modelQueryCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)

	err := runModelQuery(context.Background(), cmd, inst.ID, "hello", "", 128, 0.5, false, "test.sock", "text", runner)
	if err == nil {
		t.Fatal("expected error on query failure, got nil")
	}

	if got := ExitCode(err); got != ExitErrCommandFail {
		t.Fatalf("ExitCode = %d, want %d", got, ExitErrCommandFail)
	}

	output := out.String()
	if !strings.Contains(output, "query failed on storage:8082") {
		t.Errorf("output does not contain failure message: %q", output)
	}
}
