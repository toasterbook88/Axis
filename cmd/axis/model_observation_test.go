package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/modelplan"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

func TestLlamaServerStartRecordsPeakAndContext(t *testing.T) {
	snap := testSnap()
	zero := 0
	snap.Nodes[0].Resources.CPUCores = 8
	snap.Nodes[0].Resources.GPUs = []models.GPUInfo{{
		Vendor: "nvidia", Model: "RTX 4090", Index: &zero, IndexSource: models.IndexSourceNvidiaSMI,
		VRAMMB: 24576, VRAMFreeMB: 20000, VRAMFreeMeasured: true, Capabilities: []string{"cuda"},
	}}
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	useFakeModelRunner(t)
	var scripts []string
	runLlamaServerSample = func(_ context.Context, node models.NodeFacts, _ *config.NodeConfig, script string) (string, error) {
		if node.Name != "storage" {
			t.Fatalf("sampled node %q", node.Name)
		}
		scripts = append(scripts, script)
		return "axis-llama-sample:ram 40\naxis-llama-sample:vram 7\n", nil
	}

	cmd := modelStartCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{
		"--node", "storage", "--weights", "/mnt/models/a.gguf", "--port", "8081",
		"--ctx-size", "2048", "--main-gpu", "0", "--format", "json",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(scripts) != 1 {
		t.Fatalf("sample calls = %d", len(scripts))
	}
	if !strings.Contains(scripts[0], "nvidia-smi --query-gpu=index,memory.used --format=csv,noheader,nounits") || !strings.Contains(scripts[0], "_axis_want=0") {
		t.Fatalf("script = %s", scripts[0])
	}
	if strings.Contains(scripts[0], "s+=") {
		t.Fatalf("sample sums VRAM: %s", scripts[0])
	}
	var receipt models.ModelOperationReceipt
	if err := json.Unmarshal(buf.Bytes(), &receipt); err != nil {
		t.Fatalf("receipt: %v\n%s", err, buf.String())
	}
	if len(receipt.Warnings) != 0 {
		t.Fatalf("warnings = %v", receipt.Warnings)
	}
	obs := loadedLlamaObservation(t, "storage", "a.gguf")
	if obs.PeakRAMMB != 40 || obs.PeakVRAMMB != 7 || !obs.LastSuccess || obs.WallTimeMS < 1 {
		t.Fatalf("observation = %+v", obs)
	}
	if obs.Scope.Backend != "llama.cpp" || obs.Scope.Tool != "llama-server" || obs.Scope.Workload != models.ClassLlamaServer {
		t.Fatalf("scope = %+v", obs.Scope)
	}
	if obs.ContextTokens == nil || *obs.ContextTokens != 2048 || obs.DeviceIndex == nil || *obs.DeviceIndex != 0 {
		t.Fatalf("context/device = %+v %+v", obs.ContextTokens, obs.DeviceIndex)
	}
}

func TestLlamaServerStartWithoutDeviceSkipsVRAM(t *testing.T) {
	stubModelSnapshot(t, testSnap())
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	useFakeModelRunner(t)
	var script string
	runLlamaServerSample = func(_ context.Context, _ models.NodeFacts, _ *config.NodeConfig, got string) (string, error) {
		script = got
		return "axis-llama-sample:ram 11\naxis-llama-sample:vram 99\n", nil
	}
	cmd := modelStartCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--node", "storage", "--weights", "/mnt/models/a.gguf", "--port", "8081"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(script, "nvidia-smi") {
		t.Fatalf("unset device queried VRAM: %s", script)
	}
	obs := loadedLlamaObservation(t, "storage", "a.gguf")
	if obs.PeakRAMMB != 11 || obs.PeakVRAMMB != 0 || obs.DeviceIndex != nil {
		t.Fatalf("observation = %+v", obs)
	}
}

func TestLlamaServerRSSFailureWarnsAndStillStarts(t *testing.T) {
	stubModelSnapshot(t, testSnap())
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	useFakeModelRunner(t)
	runLlamaServerSample = func(context.Context, models.NodeFacts, *config.NodeConfig, string) (string, error) {
		return "", errors.New("ps failed")
	}
	cmd := modelStartCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--node", "storage", "--weights", "/mnt/models/a.gguf", "--port", "8081", "--format", "text"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "started /usr/local/bin/llama-server on storage:8081") || !strings.Contains(buf.String(), "warning: llama-server RSS sample failed") {
		t.Fatalf("output = %q", buf.String())
	}
	obs := loadedLlamaObservation(t, "storage", "a.gguf")
	if obs.PeakRAMMB != 0 || obs.PeakVRAMMB != 0 || !obs.LastSuccess {
		t.Fatalf("observation = %+v", obs)
	}
}

func TestProbeFailureRecordsNoObservation(t *testing.T) {
	stubModelSnapshot(t, testSnap())
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	calls := 0
	runLlamaServerSample = func(context.Context, models.NodeFacts, *config.NodeConfig, string) (string, error) {
		calls++
		return "axis-llama-sample:ram 5\n", nil
	}
	cmd := modelStartCmd()
	cmd.SetOut(&bytes.Buffer{})
	err := runModelStart(context.Background(), cmd, "storage", "/mnt/models/a.gguf", 8081, &probeFailRunner{})
	if err == nil || !strings.Contains(err.Error(), "probe failed") {
		t.Fatalf("err = %v", err)
	}
	if calls != 0 {
		t.Fatalf("probe failure sampled %d times", calls)
	}
	loaded, loadErr := state.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(loaded.Observations) != 0 {
		t.Fatalf("observations = %+v", loaded.Observations)
	}
}

func TestWriterFailureStillRecordsLlamaObservation(t *testing.T) {
	stubModelSnapshot(t, testSnap())
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	runLlamaServerSample = func(context.Context, models.NodeFacts, *config.NodeConfig, string) (string, error) {
		return "axis-llama-sample:ram 12\n", nil
	}
	want := errors.New("writer unavailable")
	cmd := modelStartCmd()
	cmd.SetOut(rejectingOutputWriter{err: want})
	err := runModelStart(context.Background(), cmd, "storage", "/mnt/models/a.gguf", 8081, &fakeModelRunner{})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
	obs := loadedLlamaObservation(t, "storage", "a.gguf")
	if obs.PeakRAMMB != 12 {
		t.Fatalf("observation = %+v", obs)
	}
}

func TestObservationPersistFailureDoesNotRollBackStart(t *testing.T) {
	stubModelSnapshot(t, testSnap())
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	notDir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(notDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AXIS_HOME", notDir)
	useFakeModelRunner(t)
	runLlamaServerSample = func(context.Context, models.NodeFacts, *config.NodeConfig, string) (string, error) {
		return "axis-llama-sample:ram 9\n", nil
	}
	cmd := modelStartCmd()
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs([]string{"--node", "storage", "--weights", "/mnt/models/a.gguf", "--port", "8081"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "started ") || !strings.Contains(errBuf.String(), "execution observation persistence failed") {
		t.Fatalf("stdout=%q stderr=%q", out.String(), errBuf.String())
	}
}

func TestOllamaAndMLXStartsDoNotRecordLlamaObservation(t *testing.T) {
	snap := testSnap()
	snap.Nodes[0].Ollama = &models.OllamaInfo{Installed: true, Running: true, Listening: true}
	snap.Nodes[0].Resources.MemoryTopology = models.MemoryTopologyUnified
	snap.Nodes[0].Tools = append(snap.Nodes[0].Tools, models.ToolInfo{Name: "mlx_lm.server", Path: "/usr/local/bin/mlx_lm.server"})
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	useFakeModelRunner(t)
	calls := 0
	runLlamaServerSample = func(context.Context, models.NodeFacts, *config.NodeConfig, string) (string, error) {
		calls++
		return "", errors.New("llama sample must not run")
	}
	runNodeScript = func(context.Context, models.NodeFacts, *config.NodeConfig, string) (string, error) {
		return `{"models":[{"name":"mistral","model":"mistral"}]}`, nil
	}
	t.Cleanup(func() { runNodeScript = runOnNodeCapturing })

	cmd := modelStartCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--node", "storage", "--ollama-model", "mistral", "--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	cmd = modelStartCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--node", "storage", "--mlx-model", "/mnt/models/qwen", "--port", "8080", "--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("llama sample calls = %d", calls)
	}
	loaded, err := state.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Observations) != 0 {
		t.Fatalf("observations = %+v", loaded.Observations)
	}
}

func TestRunModelPlanExcludesFreshLlamaPeak(t *testing.T) {
	stubModelSnapshot(t, llamaPlanSnapshot())
	if err := state.Update(func(st *state.ClusterState) error {
		st.RecordObservation(models.ExecutionObservation{
			Scope: models.ObservationScope{
				Node:      "tight",
				Workload:  models.ClassLlamaServer,
				Backend:   "llama.cpp",
				Tool:      "llama-server",
				ModelName: "qwen2.5-7b.gguf",
			},
			ObservedAt:  time.Now().UTC(),
			LastSuccess: true,
			WallTimeMS:  30,
			PeakRAMMB:   8000,
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	plan := executeModelPlan(t)
	if plan.BestCandidate != "wide" {
		t.Fatalf("best = %q", plan.BestCandidate)
	}
	if plan.Selected == nil || plan.Selected.Node != "wide" {
		t.Fatalf("selected = %+v", plan.Selected)
	}
	if len(plan.Candidates) != 1 || plan.Candidates[0].Node != "wide" {
		t.Fatalf("candidates = %+v", plan.Candidates)
	}
	var found bool
	for _, ex := range plan.Excluded {
		if ex.Node == "tight" {
			found = true
			if len(ex.Reasons) != 1 || ex.Reasons[0] != "empirical peak RAM 8000MB exceeds allocatable 512MB" {
				t.Fatalf("reasons = %#v", ex.Reasons)
			}
		}
	}
	if !found {
		t.Fatalf("excluded = %+v", plan.Excluded)
	}
}

func TestRunModelPlanWithoutObservationStaysPut(t *testing.T) {
	stubModelSnapshot(t, llamaPlanSnapshot())
	plan := executeModelPlan(t)
	if plan.BestCandidate != "tight" || len(plan.Candidates) != 2 {
		t.Fatalf("best=%s candidates=%+v", plan.BestCandidate, plan.Candidates)
	}
	if plan.Selected == nil || plan.Selected.Node != "tight" {
		t.Fatalf("selected = %+v", plan.Selected)
	}
}

func TestRunModelPlanReturnsStateLoadError(t *testing.T) {
	stubModelSnapshot(t, llamaPlanSnapshot())
	notDir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(notDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AXIS_HOME", notDir)
	cmd := modelPlanCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"qwen2.5-7b", "--format", "json"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected state load error")
	}
	if strings.Contains(err.Error(), "no eligible") {
		t.Fatalf("load error treated as no peak: %v", err)
	}
}

func TestRunModelPlanAllExcludedByPeakFails(t *testing.T) {
	snap := llamaPlanSnapshot()
	snap.Nodes = snap.Nodes[:1]
	stubModelSnapshot(t, snap)
	if err := state.Update(func(st *state.ClusterState) error {
		st.RecordObservation(models.ExecutionObservation{
			Scope: models.ObservationScope{
				Node: "tight", Workload: models.ClassLlamaServer, Backend: "llama.cpp",
				Tool: "llama-server", ModelName: "qwen2.5-7b.gguf",
			},
			ObservedAt: time.Now().UTC(), LastSuccess: true, WallTimeMS: 10, PeakRAMMB: 8000,
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cmd := modelPlanCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"qwen2.5-7b", "--format", "json"})
	err := cmd.Execute()
	if err == nil || ExitCode(err) != ExitErrCommandFail || !strings.Contains(err.Error(), "no eligible") {
		t.Fatalf("err = %v", err)
	}
	var plan modelplan.ModelPlacementPlan
	if jsonErr := json.Unmarshal(buf.Bytes(), &plan); jsonErr != nil {
		t.Fatalf("plan json: %v\n%s", jsonErr, buf.String())
	}
	if plan.BestCandidate != "" || plan.Selected != nil || len(plan.Candidates) != 0 {
		t.Fatalf("plan still names the excluded node: best=%q selected=%+v candidates=%+v", plan.BestCandidate, plan.Selected, plan.Candidates)
	}
}

func TestLlamaServerSampleScriptMaxRSSAndDeviceRow(t *testing.T) {
	dir := t.TempDir()
	writeSampleStub(t, dir, "fuser", "#!/bin/sh\necho '111 222'\n")
	writeSampleStub(t, dir, "ps", `#!/bin/sh
pid=
mode=
while [ $# -gt 0 ]; do
  case "$1" in
    -p) pid=$2; shift 2 ;;
    -o) mode=$2; shift 2 ;;
    *) shift ;;
  esac
done
case "$mode" in
  comm=) printf '%s\n' llama-server ;;
  rss=)
    case "$pid" in
      111) printf '%s\n' '  1500' ;;
      222) printf '%s\n' '  2048' ;;
      *) exit 1 ;;
    esac
    ;;
  *) exit 1 ;;
esac
`)
	writeSampleStub(t, dir, "nvidia-smi", "#!/bin/sh\nprintf '%s\n' '1, 9000' '0, 100'\n")
	zero := 0
	out, err := runSampleShell(dir, shellLlamaServerSample(8081, &zero))
	if err != nil {
		t.Fatalf("sample script: %v\n%s", err, out)
	}
	if !strings.Contains(out, "axis-llama-sample:ram 2") {
		t.Fatalf("max RSS missing: %q", out)
	}
	if strings.Contains(out, "axis-llama-sample:ram 3") || strings.Contains(out, "axis-llama-sample:vram 9100") || strings.Contains(out, "axis-llama-sample:vram 9000") {
		t.Fatalf("sample summed or picked the wrong row: %q", out)
	}
	if !strings.Contains(out, "axis-llama-sample:vram 100") {
		t.Fatalf("index 0 row missing: %q", out)
	}

	nilScript := shellLlamaServerSample(8081, nil)
	if strings.Contains(nilScript, "nvidia-smi") {
		t.Fatal("nil device index emits nvidia-smi")
	}
	marker := filepath.Join(dir, "smi-ran")
	writeSampleStub(t, dir, "nvidia-smi", "#!/bin/sh\ntouch "+marker+"\nexit 1\n")
	out, err = runSampleShell(dir, nilScript)
	if err != nil {
		t.Fatalf("nil-device script: %v\n%s", err, out)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("nil device index ran nvidia-smi")
	}

	writeSampleStub(t, dir, "ps", "#!/bin/sh\nexit 1\n")
	out, err = runSampleShell(dir, shellLlamaServerSample(8081, nil))
	if err == nil || strings.Contains(out, "axis-llama-sample:ram ") {
		t.Fatalf("ps failure err=%v out=%q", err, out)
	}

	writeSampleStub(t, dir, "fuser", "#!/bin/sh\nexit 1\n")
	writeSampleStub(t, dir, "ps", "#!/bin/sh\nprintf '%s\n' llama-server\n")
	out, err = runSampleShell(dir, shellLlamaServerSample(8081, nil))
	if err == nil {
		t.Fatalf("no listener was a successful sample: %q", out)
	}
}

func loadedLlamaObservation(t *testing.T, node, model string) models.ExecutionObservation {
	t.Helper()
	loaded, err := state.Load()
	if err != nil {
		t.Fatal(err)
	}
	obs, ok := loaded.Observation(models.ObservationScope{
		Node:      node,
		Workload:  models.ClassLlamaServer,
		Backend:   "llama.cpp",
		Tool:      "llama-server",
		ModelName: model,
	})
	if !ok || obs == nil {
		t.Fatalf("missing observation for %s/%s in %+v", node, model, loaded.Observations)
	}
	return *obs
}

func llamaPlanSnapshot() *models.ClusterSnapshot {
	return &models.ClusterSnapshot{
		Timestamp: time.Now().UTC(),
		Nodes: []models.NodeFacts{
			{
				Name:             "tight",
				Status:           models.StatusComplete,
				RAMAllocatableMB: 512,
				Resources: &models.Resources{
					RAMFreeMB: 100000, RAMTotalMB: 128000,
					Volumes: []models.Volume{{Mount: "/data/models", Kind: "local"}},
					GPUs: []models.GPUInfo{{
						Vendor: "nvidia", Model: "RTX 4090", VRAMMB: 24576,
						VRAMFreeMB: 20000, VRAMFreeMeasured: true, Capabilities: []string{"cuda"},
					}},
				},
				DiskWeights: []models.DiskWeight{{
					Name: "qwen2.5-7b", Path: "/data/models/qwen2.5-7b.gguf",
					Bytes: 4 * 1024 * 1024 * 1024, Format: "gguf",
				}},
			},
			{
				Name:             "wide",
				Status:           models.StatusComplete,
				RAMAllocatableMB: 64000,
				Resources:        &models.Resources{RAMFreeMB: 100000, RAMTotalMB: 128000},
			},
		},
	}
}

func executeModelPlan(t *testing.T) modelplan.ModelPlacementPlan {
	t.Helper()
	cmd := modelPlanCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"qwen2.5-7b", "--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var plan modelplan.ModelPlacementPlan
	if err := json.Unmarshal(buf.Bytes(), &plan); err != nil {
		t.Fatalf("plan: %v\n%s", err, buf.String())
	}
	return plan
}

func useFakeModelRunner(t *testing.T) {
	t.Helper()
	prev := defaultModelRunner
	defaultModelRunner = &fakeModelRunner{}
	t.Cleanup(func() { defaultModelRunner = prev })
}

func writeSampleStub(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func runSampleShell(dir, script string) (string, error) {
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin", "HOME=" + dir}
	out, err := cmd.CombinedOutput()
	return string(out), err
}
