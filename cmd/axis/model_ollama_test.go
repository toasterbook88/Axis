package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/modelinventory"
	"github.com/toasterbook88/axis/internal/models"
)

func TestOllamaModelAndWeightsAreMutuallyExclusive(t *testing.T) {
	cmd := modelStartCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--node", "storage", "--weights", "/mnt/models/a.gguf", "--port", "8081", "--ollama-model", "mistral"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("err=%v", err)
	}
}

func TestOllamaStartPlacesOnExistingLoopbackServer(t *testing.T) {
	snap := testSnap()
	snap.Nodes[0].Ollama = &models.OllamaInfo{Installed: true, Running: true, Listening: true}
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	runner := &fakeModelRunner{}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	t.Cleanup(func() { defaultModelRunner = prevRunner })

	var scripts []string
	prevScript := runNodeScript
	runNodeScript = func(_ context.Context, _ models.NodeFacts, _ *config.NodeConfig, script string) (string, error) {
		scripts = append(scripts, script)
		return `{"models":[{"name":"mistral","model":"mistral"}]}`, nil
	}
	t.Cleanup(func() { runNodeScript = prevScript })

	cmd := modelStartCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{
		"--node", "storage", "--ollama-model", "mistral",
		"--ollama-keep-alive", "10m", "--ollama-num-ctx", "2048",
		"--format", "json",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(runner.started) != 0 || len(runner.probed) != 0 || len(runner.stopped) != 0 {
		t.Fatalf("runner was used: started=%v probed=%v stopped=%v", runner.started, runner.probed, runner.stopped)
	}
	if len(scripts) != 1 {
		t.Fatalf("scripts=%d", len(scripts))
	}
	script := scripts[0]
	if !strings.Contains(script, "http://127.0.0.1:11434/api/generate") || !strings.Contains(script, `"model":"mistral"`) || !strings.Contains(script, `"stream":false`) {
		t.Fatalf("script=%s", script)
	}
	if !strings.Contains(script, `"keep_alive":"10m"`) || !strings.Contains(script, `"num_ctx":2048`) {
		t.Fatalf("options missing: %s", script)
	}
	for _, banned := range []string{"num_gpu", "main_gpu", "num_batch", "num_thread", "ollama serve", "OLLAMA_HOST", "/v1/models", "prompt"} {
		if strings.Contains(script, banned) {
			t.Fatalf("script contains %s: %s", banned, script)
		}
	}
	if !strings.Contains(buf.String(), `"engine": "ollama"`) || !strings.Contains(buf.String(), `"model": "mistral"`) {
		t.Fatalf("receipt=%s", buf.String())
	}

	scripts = nil
	runNodeScript = func(_ context.Context, _ models.NodeFacts, _ *config.NodeConfig, script string) (string, error) {
		scripts = append(scripts, script)
		return "", context.DeadlineExceeded
	}
	cmd = modelStartCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--node", "storage", "--ollama-model", "mistral", "--format", "text"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("curl failure must refuse even when OllamaInfo.Listening is true")
	}
	if len(runner.started) != 0 {
		t.Fatal("curl failure started a process")
	}

	runNodeScript = func(_ context.Context, _ models.NodeFacts, _ *config.NodeConfig, script string) (string, error) {
		return `{"models":[]}`, nil
	}
	cmd = modelStartCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--node", "storage", "--ollama-model", "mistral"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "api/ps") {
		t.Fatalf("missing ps listing err=%v", err)
	}
}

func TestOllamaStopUnloadsWithoutKillingTheServer(t *testing.T) {
	snap := testSnap()
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	runner := &fakeModelRunner{}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	t.Cleanup(func() { defaultModelRunner = prevRunner })
	var scripts []string
	prevScript := runNodeScript
	runNodeScript = func(_ context.Context, _ models.NodeFacts, _ *config.NodeConfig, script string) (string, error) {
		scripts = append(scripts, script)
		return `{"models":[]}`, nil
	}
	t.Cleanup(func() { runNodeScript = prevScript })

	cmd := modelStopCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--node", "storage", "--ollama-model", "mistral"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(runner.stopTargets) != 0 || len(runner.stopped) != 0 {
		t.Fatalf("stop used the process killer: %#v", runner.stopTargets)
	}
	if len(scripts) != 1 || !strings.Contains(scripts[0], `"keep_alive":0`) {
		t.Fatalf("scripts=%v", scripts)
	}
	for _, banned := range []string{"kill", "systemctl", "fuser"} {
		if strings.Contains(scripts[0], banned) {
			t.Fatalf("unload contains %s", banned)
		}
	}

	runNodeScript = func(_ context.Context, _ models.NodeFacts, _ *config.NodeConfig, script string) (string, error) {
		return `{"models":[{"name":"mistral","model":"mistral"}]}`, nil
	}
	cmd = modelStopCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--node", "storage", "--ollama-model", "mistral"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "api/ps") {
		t.Fatalf("still listed err=%v", err)
	}
}

func TestOllamaGenerationStopDoesNotReachProcessKill(t *testing.T) {
	snap := generationStopSnapshot()
	snap.Nodes[0].ResidentModels[0].Runtime = "ollama"
	snap.Nodes[0].ResidentModels[0].Name = "mistral"
	snap.Nodes[0].ResidentModels[0].Port = 11434
	want := modelinventory.FromSnapshot(snap, "daemon-cache").Instances[0]
	if want.Engine != "ollama" {
		t.Fatalf("engine=%s", want.Engine)
	}
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	runner := &fakeModelRunner{}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	t.Cleanup(func() { defaultModelRunner = prevRunner })
	prevScript := runNodeScript
	runNodeScript = func(_ context.Context, _ models.NodeFacts, _ *config.NodeConfig, script string) (string, error) {
		if strings.Contains(script, "kill") || strings.Contains(script, "systemctl") {
			t.Fatalf("generation unload killed a process: %s", script)
		}
		return `{"models":[]}`, nil
	}
	t.Cleanup(func() { runNodeScript = prevScript })

	cmd := modelStopCmd()
	cmd.SetOut(&bytes.Buffer{})
	if err := runModelStopGeneration(context.Background(), cmd, want.GenerationID, "test.sock", "text", runner); err != nil {
		t.Fatal(err)
	}
	if len(runner.stopTargets) != 0 {
		t.Fatalf("process kill targets=%#v", runner.stopTargets)
	}
}
