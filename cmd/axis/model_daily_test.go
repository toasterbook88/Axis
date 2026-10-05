package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
)

func TestModelStartDailyPathPicksListeningOllama(t *testing.T) {
	snap := &models.ClusterSnapshot{Nodes: []models.NodeFacts{
		{
			Name:   "zeta",
			Status: models.StatusComplete,
			Ollama: &models.OllamaInfo{Listening: true, Models: []string{"mistral:latest"}},
		},
		{
			Name:   "alpha",
			Status: models.StatusComplete,
			Ollama: &models.OllamaInfo{Listening: true, Models: []string{"mistral"}},
		},
		{Name: "partial", Status: models.StatusPartial, Ollama: &models.OllamaInfo{Listening: true, Models: []string{"mistral"}}},
	}}
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "alpha"}, {Name: "zeta"}}})
	var scripts []string
	prevScript := runNodeScript
	runNodeScript = func(_ context.Context, node models.NodeFacts, _ *config.NodeConfig, script string) (string, error) {
		if node.Name != "alpha" {
			t.Fatalf("ssh node=%s", node.Name)
		}
		scripts = append(scripts, script)
		return `{"models":[{"name":"mistral","model":"mistral"}]}`, nil
	}
	t.Cleanup(func() { runNodeScript = prevScript })
	prevRefresh := signalModelDaemonRefresh
	signalModelDaemonRefresh = func(context.Context, string, string) error { return nil }
	t.Cleanup(func() { signalModelDaemonRefresh = prevRefresh })

	cmd := modelStartCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"mistral"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "picked alpha: ollama already listening\n") {
		t.Fatalf("stdout=%q", got)
	}
	if !strings.Contains(got, "placed ollama model mistral on alpha operation ") {
		t.Fatalf("stdout=%q", got)
	}
	if len(scripts) != 1 || !strings.Contains(scripts[0], `grep -q '"done_reason":"load"'`) {
		t.Fatalf("scripts=%v", scripts)
	}
}

func TestModelStartDailyPathReportsResidentModelWithoutSSH(t *testing.T) {
	snap := testSnap()
	snap.Nodes[0].Status = models.StatusComplete
	snap.Nodes[0].ResidentModels = []models.ResidentModel{{Name: "mistral", Runtime: "llama.cpp", Port: 8080}}
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	var calls int
	prevScript := runNodeScript
	runNodeScript = func(context.Context, models.NodeFacts, *config.NodeConfig, string) (string, error) {
		calls++
		return "", nil
	}
	t.Cleanup(func() { runNodeScript = prevScript })
	runner := &fakeModelRunner{}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	t.Cleanup(func() { defaultModelRunner = prevRunner })

	cmd := modelStartCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"mistral", "--format", "text"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "picked storage: llama.cpp already serving mistral\n" {
		t.Fatalf("stdout=%q", buf.String())
	}
	if calls != 0 || len(runner.started) != 0 {
		t.Fatalf("calls=%d started=%v", calls, runner.started)
	}
}

func TestModelStartDailyPathJSONReportsAlreadyServing(t *testing.T) {
	snap := testSnap()
	snap.Nodes[0].Status = models.StatusComplete
	snap.Nodes[0].ResidentModels = []models.ResidentModel{{Name: "mistral", Runtime: "ollama"}}
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	cmd := modelStartCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"mistral", "--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, `"disposition": "already_serving"`) || !strings.Contains(got, `"node": "storage"`) || !strings.Contains(got, `"engine": "ollama"`) {
		t.Fatalf("receipt=%s", got)
	}
}

func TestModelStartDailyPathRefusesAFlagBoard(t *testing.T) {
	cmd := modelStartCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"mistral", "--node", "storage", "--ollama-model", "mistral"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "picks the node") || !strings.Contains(err.Error(), "--node") {
		t.Fatalf("err=%v", err)
	}
}

func TestModelStartDailyPathRefusesWhenNoRuntimeIsPresent(t *testing.T) {
	snap := testSnap()
	snap.Nodes[0].Status = models.StatusComplete
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	var calls int
	prevScript := runNodeScript
	runNodeScript = func(context.Context, models.NodeFacts, *config.NodeConfig, string) (string, error) {
		calls++
		return "", nil
	}
	t.Cleanup(func() { runNodeScript = prevScript })
	cmd := modelStartCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"mistral"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "no complete node already has a runtime") {
		t.Fatalf("err=%v", err)
	}
	if calls != 0 {
		t.Fatalf("ssh calls=%d", calls)
	}
}

func TestOllamaStartRefusesBeforeSSHWhenTheServerIsNotListening(t *testing.T) {
	snap := testSnap()
	snap.Nodes[0].Status = models.StatusComplete
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	var calls int
	prevScript := runNodeScript
	runNodeScript = func(context.Context, models.NodeFacts, *config.NodeConfig, string) (string, error) {
		calls++
		return `{"models":[{"name":"mistral"}]}`, nil
	}
	t.Cleanup(func() { runNodeScript = prevScript })
	cmd := modelStartCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--node", "storage", "--ollama-model", "mistral"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "no listening ollama") {
		t.Fatalf("err=%v", err)
	}
	if calls != 0 {
		t.Fatalf("ssh calls=%d", calls)
	}

	snap.Nodes[0].Status = models.StatusPartial
	snap.Nodes[0].Ollama = &models.OllamaInfo{Listening: true}
	cmd = modelStopCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--node", "storage", "--ollama-model", "mistral"})
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "refusing ollama ssh") {
		t.Fatalf("stop err=%v", err)
	}
	if calls != 0 {
		t.Fatalf("stop ssh calls=%d", calls)
	}
}
