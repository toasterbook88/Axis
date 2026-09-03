package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/toasterbook88/axis/internal/modelplan"
	"github.com/toasterbook88/axis/internal/models"
)

func TestRunModelPlanFromDiskWeight(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Timestamp: time.Now().UTC(),
		Publication: &models.PublicationEnvelope{
			ID: "pub-plan-test-1",
		},
		Nodes: []models.NodeFacts{
			{
				Name:   "gpu-worker",
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMFreeMB:  32000,
					RAMTotalMB: 64000,
					GPUs: []models.GPUInfo{
						{
							Model:        "NVIDIA GeForce RTX 4090",
							Vendor:       "nvidia",
							VRAMMB:       24576,
							Capabilities: []string{"cuda"},
						},
					},
				},
				DiskWeights: []models.DiskWeight{
					{
						Name:   "qwen2.5-7b",
						Path:   "/data/models/qwen2.5-7b.gguf",
						Bytes:  4 * 1024 * 1024 * 1024,
						Format: "gguf",
					},
				},
			},
			{
				Name:   "cpu-worker",
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMFreeMB:  8000,
					RAMTotalMB: 16000,
				},
			},
		},
	}
	stubModelSnapshot(t, snap)

	cmd := modelPlanCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{
		"qwen2.5-7b",
		"--format", "json",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	var plan modelplan.ModelPlacementPlan
	if err := json.Unmarshal(buf.Bytes(), &plan); err != nil {
		t.Fatalf("json unmarshal failed: %v\noutput=%s", err, buf.String())
	}

	if plan.Schema != "axis.model-plan/v1" {
		t.Errorf("plan.Schema = %q, want axis.model-plan/v1", plan.Schema)
	}
	if plan.Spec.Name != "qwen2.5-7b" {
		t.Errorf("plan.Spec.Name = %q, want qwen2.5-7b", plan.Spec.Name)
	}
	if plan.BestCandidate != "gpu-worker" {
		t.Errorf("plan.BestCandidate = %q, want gpu-worker", plan.BestCandidate)
	}
	if len(plan.Candidates) != 2 {
		t.Fatalf("candidates count = %d, want 2", len(plan.Candidates))
	}
	if plan.Candidates[0].Node != "gpu-worker" || !plan.Candidates[0].HasLocalWeights {
		t.Errorf("expected gpu-worker with local weights: %+v", plan.Candidates[0])
	}
}

func TestRunModelPlanFromSpecFile(t *testing.T) {
	spec := models.ModelSpec{
		Schema: "axis.model-spec/v1",
		ID:     "ms-custom-spec",
		Name:   "custom-llm",
		Format: models.ModelFormatGGUF,
		Memory: models.ModelMemoryRequirements{
			WeightSizeMB:      2048,
			ContextOverheadMB: 512,
			RuntimeOverheadMB: 256,
		},
		Accelerators: []models.AcceleratorType{models.AcceleratorCUDA, models.AcceleratorCPU},
	}

	tmpDir := t.TempDir()
	specFile := filepath.Join(tmpDir, "model-spec.json")
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specFile, data, 0644); err != nil {
		t.Fatal(err)
	}

	snap := &models.ClusterSnapshot{
		Timestamp: time.Now().UTC(),
		Nodes: []models.NodeFacts{
			{
				Name:   "box1",
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMFreeMB:  8000,
					RAMTotalMB: 16000,
				},
			},
		},
	}
	stubModelSnapshot(t, snap)

	cmd := modelPlanCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{
		specFile,
		"--format", "yaml",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	var plan modelplan.ModelPlacementPlan
	if err := yaml.Unmarshal(buf.Bytes(), &plan); err != nil {
		t.Fatalf("yaml unmarshal failed: %v\noutput=%s", err, buf.String())
	}
	if plan.Spec.ID != "ms-custom-spec" || plan.Spec.Name != "custom-llm" {
		t.Errorf("plan spec mismatch: %+v", plan.Spec)
	}
	if plan.BestCandidate != "box1" {
		t.Errorf("plan.BestCandidate = %q, want box1", plan.BestCandidate)
	}
}

func TestRunModelPlanPortConflictExclusion(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Timestamp: time.Now().UTC(),
		Nodes: []models.NodeFacts{
			{
				Name:   "busy-node",
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMFreeMB:  32000,
					RAMTotalMB: 64000,
				},
				ResidentModels: []models.ResidentModel{
					{
						Name:    "existing-llama",
						Runtime: "llama.cpp",
						Port:    8080,
					},
				},
			},
		},
	}
	stubModelSnapshot(t, snap)

	cmd := modelPlanCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{
		"/models/test.gguf",
		"--port", "8080",
		"--format", "json",
	})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected exit error when no candidates are eligible")
	}
	if got := ExitCode(err); got != ExitErrCommandFail {
		t.Fatalf("ExitCode = %d, want %d", got, ExitErrCommandFail)
	}

	var plan modelplan.ModelPlacementPlan
	if err := json.Unmarshal(buf.Bytes(), &plan); err != nil {
		t.Fatalf("json parse failed: %v\noutput=%s", err, buf.String())
	}
	if len(plan.Candidates) != 0 {
		t.Fatalf("expected 0 candidates, got %d", len(plan.Candidates))
	}
	if len(plan.Excluded) != 1 || plan.Excluded[0].Node != "busy-node" {
		t.Fatalf("expected busy-node to be excluded: %+v", plan.Excluded)
	}
	if !strings.Contains(plan.Excluded[0].Reasons[0], "occupied by resident model") {
		t.Errorf("exclusion reason mismatch: %v", plan.Excluded[0].Reasons)
	}
}

func TestRunModelPlanTextOutput(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Timestamp: time.Now().UTC(),
		Publication: &models.PublicationEnvelope{
			ID: "pub-text-out-test",
		},
		Nodes: []models.NodeFacts{
			{
				Name:   "worker1",
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMFreeMB:  16000,
					RAMTotalMB: 32000,
				},
			},
		},
	}
	stubModelSnapshot(t, snap)

	cmd := modelPlanCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{
		"/data/weights/model.gguf",
		"--format", "text",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "MODEL PLACEMENT PLAN: model") {
		t.Errorf("output missing title: %s", out)
	}
	if !strings.Contains(out, "worker1") {
		t.Errorf("output missing worker1: %s", out)
	}
	if !strings.Contains(out, "Recommended Placement: worker1") {
		t.Errorf("output missing recommendation: %s", out)
	}
}
