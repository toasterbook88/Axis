package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
)

func TestModelStartMissingFlagsStillRequiredWithoutFromPlan(t *testing.T) {
	cmd := modelStartCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--format", "text"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `required flag(s) "node"`) {
		t.Fatalf("err=%v", err)
	}
}

func TestModelStartFromPlanRefusesPlanDefaultUntilPortChanged(t *testing.T) {
	snap := testSnap()
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	path := writeProfile(t, models.ModelRunProfile{
		Schema:       models.ModelRunSchema,
		Node:         "storage",
		Engine:       "llama.cpp",
		ToolName:     "llama-server",
		ArtifactKind: "weights-path",
		WeightsPath:  "/mnt/models/a.gguf",
		BindHost:     "127.0.0.1",
		Port:         8080,
		PortSource:   models.PortSourcePlanDefault,
	})
	runner := &fakeModelRunner{}
	prev := defaultModelRunner
	defaultModelRunner = runner
	t.Cleanup(func() { defaultModelRunner = prev })
	cmd := modelStartCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--from-plan", path, "--format", "text"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "plan-default") {
		t.Fatalf("err=%v", err)
	}
	if len(runner.started) != 0 {
		t.Fatalf("runner started=%v", runner.started)
	}

	cmd = modelStartCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--from-plan", path, "--port", "8082", "--format", "text"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/local/bin/llama-server", "-m", "/mnt/models/a.gguf", "--port", "8082", "--host", "127.0.0.1"}
	if len(runner.started) != 1 || !reflect.DeepEqual(runner.started[0], want) {
		t.Fatalf("argv=%#v", runner.started)
	}
}

func TestModelStartFromPlanRejectsPlacementDocument(t *testing.T) {
	stubModelSnapshot(t, testSnap())
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, []byte(`{"schema":"axis.model-plan/v1","best_candidate":"storage"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := modelStartCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--from-plan", path})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "expected axis.model-run/v1") {
		t.Fatalf("err=%v", err)
	}
}

func TestModelStartStringNGPULayersAndCtxWithoutFit(t *testing.T) {
	snap := testSnap()
	snap.Nodes[0].Resources.CPUCores = 8
	snap.Nodes[0].Resources.GPUs = []models.GPUInfo{{
		Vendor: "nvidia", Model: "RTX 4090", VRAMMB: 24576,
		VRAMFreeMB: 20000, VRAMFreeMeasured: true, Capabilities: []string{"cuda"},
	}}
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	runner := &fakeModelRunner{}
	prev := defaultModelRunner
	defaultModelRunner = runner
	t.Cleanup(func() { defaultModelRunner = prev })

	cmd := modelStartCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{
		"--node", "storage", "--weights", "/mnt/models/a.gguf", "--port", "8081",
		"--n-gpu-layers", "auto", "--ctx-size", "2048", "--format", "text",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/usr/local/bin/llama-server", "-m", "/mnt/models/a.gguf", "--port", "8081", "--host", "127.0.0.1",
		"-c", "2048", "-ngl", "auto",
	}
	if !reflect.DeepEqual(runner.started[0], want) {
		t.Fatalf("argv=%#v", runner.started)
	}

	runner.started = nil
	cmd = modelStartCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{
		"--node", "storage", "--weights", "/mnt/models/a.gguf", "--port", "8081",
		"--n-gpu-layers", "0",
	})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "n-gpu-layers") {
		t.Fatalf("zero layers err=%v", err)
	}
	if len(runner.started) != 0 {
		t.Fatalf("zero layers started=%v", runner.started)
	}
}

func TestModelPlanWriteProfileAndExplicitPort(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Publication: &models.PublicationEnvelope{ID: "pub-write"},
		Nodes: []models.NodeFacts{{
			Name:   "gpu-worker",
			Status: models.StatusComplete,
			Tools:  []models.ToolInfo{{Name: "llama-server", Path: "/usr/local/bin/llama-server"}},
			Resources: &models.Resources{
				RAMFreeMB: 32000, RAMTotalMB: 64000,
				Volumes: []models.Volume{{Mount: "/data/models", Kind: "local"}},
				GPUs: []models.GPUInfo{{
					Vendor: "nvidia", Model: "RTX 4090", VRAMMB: 24576,
					VRAMFreeMB: 16000, VRAMFreeMeasured: true, Capabilities: []string{"cuda"},
				}},
			},
			DiskWeights: []models.DiskWeight{{
				Name: "qwen2.5-7b", Path: "/data/models/qwen2.5-7b.gguf",
				Bytes: 4 * 1024 * 1024 * 1024, Format: "gguf",
			}},
		}},
	}
	stubModelSnapshot(t, snap)
	out := filepath.Join(t.TempDir(), "profile.json")
	cmd := modelPlanCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"qwen2.5-7b", "--port", "9001", "--format", "json", "--write-profile", out})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var planJSON map[string]any
	if err := json.Unmarshal(buf.Bytes(), &planJSON); err != nil {
		t.Fatal(err)
	}
	if planJSON["schema"] != "axis.model-plan/v1" || planJSON["best_candidate"] != "gpu-worker" {
		t.Fatalf("plan=%s", buf.String())
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := models.LoadModelRunProfile(raw)
	if err != nil {
		t.Fatalf("profile: %v\n%s", err, raw)
	}
	if profile.Schema != models.ModelRunSchema || profile.Port != 9001 || profile.PortSource != models.PortSourceExplicit {
		t.Fatalf("profile=%+v", profile)
	}
	if strings.Contains(string(raw), "best_candidate") || strings.Contains(string(raw), "axis.model-plan/v1") {
		t.Fatalf("write-profile must be the profile only:\n%s", raw)
	}
}

func TestRunModelStartDefaultPathStillUsesFunctionArgs(t *testing.T) {
	stubModelSnapshot(t, testSnap())
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	runner := &fakeModelRunner{}
	cmd := modelStartCmd()
	cmd.SetOut(&bytes.Buffer{})
	if err := runModelStart(context.Background(), cmd, "storage", "/mnt/models/a.gguf", 8081, runner); err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/local/bin/llama-server", "-m", "/mnt/models/a.gguf", "--port", "8081", "--host", "127.0.0.1"}
	if !reflect.DeepEqual(runner.started[0], want) {
		t.Fatalf("argv=%#v", runner.started)
	}
}

func writeProfile(t *testing.T, profile models.ModelRunProfile) string {
	t.Helper()
	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "profile.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
