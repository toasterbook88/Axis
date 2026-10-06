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
	snap.Nodes[0].Status = models.StatusComplete
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
	snap.Nodes[0].Status = models.StatusComplete
	snap.Nodes[0].Ollama = &models.OllamaInfo{Installed: true, Listening: true}
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

	var refreshes int
	prevRefresh := signalModelDaemonRefresh
	signalModelDaemonRefresh = func(context.Context, string, string) error {
		refreshes++
		return nil
	}
	t.Cleanup(func() { signalModelDaemonRefresh = prevRefresh })

	cmd := modelStopCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--node", "storage", "--ollama-model", "mistral"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if refreshes != 1 {
		t.Fatalf("daemon refreshes=%d", refreshes)
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
	if refreshes != 1 {
		t.Fatalf("failed unload refreshed the daemon: %d", refreshes)
	}
}

func TestOllamaGenerationStopDoesNotReachProcessKill(t *testing.T) {
	snap := generationStopSnapshot()
	snap.Nodes[0].ResidentModels[0].Runtime = "ollama"
	snap.Nodes[0].ResidentModels[0].Name = "mistral"
	snap.Nodes[0].ResidentModels[0].Port = 11434
	snap.Nodes[0].Ollama = &models.OllamaInfo{Installed: true, Listening: true}
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
	var refreshes int
	prevRefresh := signalModelDaemonRefresh
	signalModelDaemonRefresh = func(context.Context, string, string) error {
		refreshes++
		return nil
	}
	t.Cleanup(func() { signalModelDaemonRefresh = prevRefresh })

	cmd := modelStopCmd()
	cmd.SetOut(&bytes.Buffer{})
	if err := runModelStopGeneration(context.Background(), cmd, want.GenerationID, "test.sock", "text", runner); err != nil {
		t.Fatal(err)
	}
	if refreshes != 1 {
		t.Fatalf("daemon refreshes=%d", refreshes)
	}
	if len(runner.stopTargets) != 0 {
		t.Fatalf("process kill targets=%#v", runner.stopTargets)
	}
}

func TestOllamaStartTextNamesTheModel(t *testing.T) {
	snap := testSnap()
	snap.Nodes[0].Status = models.StatusComplete
	snap.Nodes[0].Ollama = &models.OllamaInfo{Installed: true, Listening: true}
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	prevScript := runNodeScript
	runNodeScript = func(context.Context, models.NodeFacts, *config.NodeConfig, string) (string, error) {
		return `{"models":[{"name":"mistral:latest","model":"mistral:latest"}]}`, nil
	}
	t.Cleanup(func() { runNodeScript = prevScript })
	prevRefresh := signalModelDaemonRefresh
	signalModelDaemonRefresh = func(context.Context, string, string) error { return nil }
	t.Cleanup(func() { signalModelDaemonRefresh = prevRefresh })

	cmd := modelStartCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--node", "storage", "--ollama-model", "mistral", "--format", "text"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "placed ollama model mistral on storage operation ") {
		t.Fatalf("receipt=%q", got)
	}
	if strings.Contains(got, "started ") || strings.Contains(got, ":0") {
		t.Fatalf("receipt used the llama-server line: %q", got)
	}
}

func TestOllamaStartRefusesProfileRefusals(t *testing.T) {
	stubModelSnapshot(t, testSnap())
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	path := writeProfile(t, models.ModelRunProfile{
		Schema:       models.ModelRunSchema,
		Node:         "storage",
		Engine:       models.EngineOllama,
		ArtifactKind: models.ArtifactOllamaModelName,
		OllamaModel:  "mistral",
		BindHost:     "127.0.0.1",
		Refusals:     []string{"weights are not on a named local volume"},
	})
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
	cmd.SetArgs([]string{"--from-plan", path, "--format", "text"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "named local volume") {
		t.Fatalf("err=%v", err)
	}
	if calls != 0 {
		t.Fatalf("refusals still reached ollama: %d", calls)
	}
}

// TestModelStopCmdDefinesLiveFlag executes "model stop --live" for every stop
// form: --ollama-model and legacy --node/--port must resolve live (no daemon
// cache read), and a generation ID must refuse --live instead of ignoring it.
func TestModelStopCmdDefinesLiveFlag(t *testing.T) {
	snap := testSnap()
	snap.Nodes[0].Status = models.StatusComplete
	snap.Nodes[0].Ollama = &models.OllamaInfo{Installed: true, Listening: true}
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})

	var cacheCalls, liveCalls int
	prevFetch, prevLive := fetchModelInventorySnapshot, loadModelSnapshot
	t.Cleanup(func() { fetchModelInventorySnapshot, loadModelSnapshot = prevFetch, prevLive })
	fetchModelInventorySnapshot = func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		cacheCalls++
		return snap, "daemon-cache", nil
	}
	loadModelSnapshot = func(context.Context) (*models.ClusterSnapshot, error) {
		liveCalls++
		return snap, nil
	}
	runner := &fakeModelRunner{stopDisposition: modelStopStopped}
	prevRunner, prevScript, prevRefresh := defaultModelRunner, runNodeScript, signalModelDaemonRefresh
	t.Cleanup(func() {
		defaultModelRunner, runNodeScript, signalModelDaemonRefresh = prevRunner, prevScript, prevRefresh
	})
	defaultModelRunner = runner
	runNodeScript = func(context.Context, models.NodeFacts, *config.NodeConfig, string) (string, error) {
		return `{"models":[]}`, nil
	}
	signalModelDaemonRefresh = func(context.Context, string, string) error { return nil }

	run := func(args ...string) error {
		cacheCalls, liveCalls = 0, 0
		cmd := modelStopCmd()
		cmd.SetOut(new(bytes.Buffer))
		cmd.SetErr(new(bytes.Buffer))
		cmd.SetArgs(args)
		return cmd.Execute()
	}

	if err := run("--node", "storage", "--ollama-model", "mistral", "--live", "--format", "json"); err != nil {
		t.Fatalf("ollama stop --live: %v", err)
	}
	if liveCalls != 1 || cacheCalls != 0 {
		t.Fatalf("ollama stop --live: live=%d cache=%d, want live only", liveCalls, cacheCalls)
	}

	if err := run("--node", "storage", "--port", "8081", "--live"); err != nil {
		t.Fatalf("legacy stop --live: %v", err)
	}
	if liveCalls != 1 || cacheCalls != 0 {
		t.Fatalf("legacy stop --live: live=%d cache=%d, want live only", liveCalls, cacheCalls)
	}
	if err := run("--node", "storage", "--port", "8081"); err != nil {
		t.Fatalf("legacy stop: %v", err)
	}
	if cacheCalls != 1 || liveCalls != 0 {
		t.Fatalf("legacy stop without --live: live=%d cache=%d, want cache only", liveCalls, cacheCalls)
	}

	stopsBefore := len(runner.stopTargets)
	err := run("gen-123", "--live")
	if err == nil || !strings.Contains(err.Error(), "--live cannot be combined with a generation ID") {
		t.Fatalf("generation stop --live must be refused, got %v", err)
	}
	if len(runner.stopTargets) != stopsBefore || liveCalls != 0 || cacheCalls != 0 {
		t.Fatalf("refused generation stop still acted: stops=%d live=%d cache=%d", len(runner.stopTargets)-stopsBefore, liveCalls, cacheCalls)
	}
}

func TestOllamaStopFailedUnloadWritesReceipt(t *testing.T) {
	snap := testSnap()
	snap.Nodes[0].Status = models.StatusComplete
	snap.Nodes[0].Ollama = &models.OllamaInfo{Installed: true, Listening: true}
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	runner := &fakeModelRunner{}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	t.Cleanup(func() { defaultModelRunner = prevRunner })

	prevScript := runNodeScript
	runNodeScript = func(context.Context, models.NodeFacts, *config.NodeConfig, string) (string, error) {
		return "", fmt.Errorf("connection refused")
	}
	t.Cleanup(func() { runNodeScript = prevScript })

	var refreshes int
	prevRefresh := signalModelDaemonRefresh
	signalModelDaemonRefresh = func(context.Context, string, string) error {
		refreshes++
		return nil
	}
	t.Cleanup(func() { signalModelDaemonRefresh = prevRefresh })

	cmd := modelStopCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--node", "storage", "--ollama-model", "mistral", "--format", "json"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error from failed unload")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err=%v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"status": "failed"`) {
		t.Fatalf("receipt missing failed status: %s", out)
	}
	if !strings.Contains(out, `"disposition": "failed"`) {
		t.Fatalf("receipt missing failed disposition: %s", out)
	}
	if !strings.Contains(out, `"error": "ollama unload failed: connection refused"`) {
		t.Fatalf("receipt missing error: %s", out)
	}
	if refreshes != 0 {
		t.Fatalf("failed unload refreshed daemon: %d", refreshes)
	}
}

func TestOllamaStopStartedAtBeforeUnload(t *testing.T) {
	snap := testSnap()
	snap.Nodes[0].Status = models.StatusComplete
	snap.Nodes[0].Ollama = &models.OllamaInfo{Installed: true, Listening: true}
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	runner := &fakeModelRunner{}
	prevRunner := defaultModelRunner
	defaultModelRunner = runner
	t.Cleanup(func() { defaultModelRunner = prevRunner })

	var unloadTime time.Time
	prevScript := runNodeScript
	runNodeScript = func(_ context.Context, _ models.NodeFacts, _ *config.NodeConfig, script string) (string, error) {
		unloadTime = time.Now().UTC()
		return `{"models":[]}`, nil
	}
	t.Cleanup(func() { runNodeScript = prevScript })

	prevRefresh := signalModelDaemonRefresh
	signalModelDaemonRefresh = func(context.Context, string, string) error { return nil }
	t.Cleanup(func() { signalModelDaemonRefresh = prevRefresh })

	cmd := modelStopCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--node", "storage", "--ollama-model", "mistral", "--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if unloadTime.IsZero() {
		t.Fatal("unload was never called")
	}
	var receipt struct {
		StartedAt   time.Time `json:"started_at"`
		CompletedAt time.Time `json:"completed_at"`
	}
	if err := json.Unmarshal(buf.Bytes(), &receipt); err != nil {
		t.Fatalf("decode receipt: %v\n%s", err, buf.String())
	}
	if receipt.StartedAt.IsZero() || receipt.CompletedAt.IsZero() {
		t.Fatalf("receipt missing timestamps: %s", buf.String())
	}
	if receipt.StartedAt.After(unloadTime) {
		t.Fatalf("started_at %s is after the unload at %s", receipt.StartedAt.Format(time.RFC3339Nano), unloadTime.Format(time.RFC3339Nano))
	}
	if receipt.CompletedAt.Before(unloadTime) {
		t.Fatalf("completed_at %s is before the unload at %s", receipt.CompletedAt.Format(time.RFC3339Nano), unloadTime.Format(time.RFC3339Nano))
	}
}
