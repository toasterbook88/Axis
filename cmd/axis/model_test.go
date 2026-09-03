package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/modellife"
	"github.com/toasterbook88/axis/internal/models"
)

type fakeModelRunner struct {
	started         [][]string
	stopped         []int
	stopTargets     []modellife.StopTarget
	probed          []int
	stopDisposition modelStopDisposition
	stopErr         error
}

func (f *fakeModelRunner) Start(_ context.Context, _ models.NodeFacts, _ *config.NodeConfig, plan modellife.StartPlan) error {
	f.started = append(f.started, append([]string(nil), plan.Argv...))
	return nil
}
func (f *fakeModelRunner) Stop(_ context.Context, _ models.NodeFacts, _ *config.NodeConfig, target modellife.StopTarget) (modelStopDisposition, error) {
	f.stopped = append(f.stopped, target.Port)
	f.stopTargets = append(f.stopTargets, target)
	if f.stopDisposition != "" {
		return f.stopDisposition, f.stopErr
	}
	return modelStopStopped, f.stopErr
}
func (f *fakeModelRunner) Probe(_ context.Context, _ models.NodeFacts, _ *config.NodeConfig, port int) error {
	f.probed = append(f.probed, port)
	return nil
}

func stubModelSnapshot(t *testing.T, snap *models.ClusterSnapshot) {
	t.Helper()
	prev := loadModelSnapshot
	loadModelSnapshot = func(context.Context) (*models.ClusterSnapshot, error) { return snap, nil }
	t.Cleanup(func() { loadModelSnapshot = prev })
}

func stubModelConfig(t *testing.T, cfg *config.Config) {
	t.Helper()
	prev := loadModelConfig
	loadModelConfig = func() (*config.Config, error) { return cfg, nil }
	t.Cleanup(func() { loadModelConfig = prev })
}

func testSnap() *models.ClusterSnapshot {
	return &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{{
			Name: "storage",
			Tools: []models.ToolInfo{
				{Name: "llama-server", Path: "/usr/local/bin/llama-server"},
			},
			Resources: &models.Resources{
				Volumes: []models.Volume{
					{Mount: "/mnt/models", Kind: "local"},
				},
			},
		}},
	}
}

func TestModelCommandWiresInventoryAndLifecycleCommands(t *testing.T) {
	cmd := modelCmd()
	for _, name := range []string{"list", "inspect", "start", "stop"} {
		if _, _, err := cmd.Find([]string{name}); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
}

func TestModelListDefaultsToDaemonCacheAndWritesCanonicalInventory(t *testing.T) {
	previous := loadModelInventory
	t.Cleanup(func() { loadModelInventory = previous })
	var gotLive bool
	loadModelInventory = func(_ context.Context, live bool, _ string) (models.ModelInventory, error) {
		gotLive = live
		return models.ModelInventory{
			Source: "daemon-cache", PublicationID: "pub-test-1",
			Instances: []models.ModelInstance{{
				ID: "mi-abc", Model: "model-a", Engine: "llama.cpp", Node: "node-a",
				State: models.ModelInstanceResident, Port: 8080,
			}},
		}, nil
	}

	cmd := modelListCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if gotLive {
		t.Fatal("model list silently performed live discovery by default")
	}
	var got models.ModelInventory
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out.String())
	}
	if got.Source != "daemon-cache" || got.PublicationID != "pub-test-1" || len(got.Instances) != 1 || got.Instances[0].ID != "mi-abc" {
		t.Fatalf("output = %#v", got)
	}
}

func TestModelListLiveIsExplicit(t *testing.T) {
	previous := loadModelInventory
	t.Cleanup(func() { loadModelInventory = previous })
	var gotLive bool
	loadModelInventory = func(_ context.Context, live bool, _ string) (models.ModelInventory, error) {
		gotLive = live
		return models.ModelInventory{Instances: []models.ModelInstance{}}, nil
	}

	cmd := modelListCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--live"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !gotLive {
		t.Fatal("--live did not select live discovery")
	}
}

func TestReadModelInventoryCacheFailureDoesNotHideLiveFallback(t *testing.T) {
	previousFetch := fetchModelInventorySnapshot
	previousLive := loadModelSnapshot
	t.Cleanup(func() {
		fetchModelInventorySnapshot = previousFetch
		loadModelSnapshot = previousLive
	})
	cacheErr := errors.New("cache unavailable")
	fetchModelInventorySnapshot = func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return nil, "", cacheErr
	}
	liveCalled := false
	loadModelSnapshot = func(context.Context) (*models.ClusterSnapshot, error) {
		liveCalled = true
		return &models.ClusterSnapshot{}, nil
	}

	_, err := readModelInventory(context.Background(), false, "test.sock")
	if !errors.Is(err, cacheErr) || !strings.Contains(err.Error(), "use --live") {
		t.Fatalf("error = %v, want cache error with explicit live guidance", err)
	}
	if liveCalled {
		t.Fatal("cache failure silently fell back to expensive live discovery")
	}
}

func TestModelListTextPropagatesWriterFailure(t *testing.T) {
	previous := loadModelInventory
	t.Cleanup(func() { loadModelInventory = previous })
	loadModelInventory = func(context.Context, bool, string) (models.ModelInventory, error) {
		return models.ModelInventory{Source: "daemon-cache", Instances: []models.ModelInstance{}}, nil
	}
	wantErr := errors.New("writer unavailable")
	cmd := modelListCmd()
	cmd.SetOut(rejectingOutputWriter{err: wantErr})
	if err := cmd.Execute(); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want writer failure", err)
	}
}

func TestModelInspectReturnsExactObservedInstance(t *testing.T) {
	previous := loadModelInventory
	t.Cleanup(func() { loadModelInventory = previous })
	loadModelInventory = func(context.Context, bool, string) (models.ModelInventory, error) {
		return models.ModelInventory{
			Source: "daemon-cache",
			Instances: []models.ModelInstance{
				{ID: "mi-first", Model: "first", State: models.ModelInstanceResident},
				{ID: "mi-target", Model: "target", Engine: "ollama", Node: "node-b", State: models.ModelInstanceResident, Port: 11434},
			},
		}, nil
	}

	cmd := modelInspectCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"mi-target", "--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var got models.ModelInspection
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out.String())
	}
	if got.Source != "daemon-cache" || got.Instance.ID != "mi-target" || got.Instance.Model != "target" {
		t.Fatalf("inspection = %#v", got)
	}
}

func TestModelInspectRejectsUnknownInstance(t *testing.T) {
	previous := loadModelInventory
	t.Cleanup(func() { loadModelInventory = previous })
	loadModelInventory = func(context.Context, bool, string) (models.ModelInventory, error) {
		return models.ModelInventory{Instances: []models.ModelInstance{}}, nil
	}

	cmd := modelInspectCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"mi-missing"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `model instance "mi-missing" not found`) {
		t.Fatalf("error = %v", err)
	}
}

func TestRunModelStartUsesPlanAndRunner(t *testing.T) {
	stubModelSnapshot(t, testSnap())
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	r := &fakeModelRunner{}
	cmd := modelStartCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := runModelStart(context.Background(), cmd, "storage", "/mnt/models/a.gguf", 8081, r); err != nil {
		t.Fatal(err)
	}
	if len(r.started) != 1 || len(r.probed) != 1 || r.probed[0] != 8081 {
		t.Fatalf("runner=%+v", r)
	}
	argv := strings.Join(r.started[0], " ")
	if !strings.Contains(argv, "--port 8081") || !strings.Contains(argv, "/mnt/models/a.gguf") {
		t.Fatalf("argv=%v", r.started[0])
	}
	if !strings.Contains(buf.String(), "started") {
		t.Fatalf("output=%q", buf.String())
	}
}

func TestRunModelStartPropagatesWriterFailureAfterStarting(t *testing.T) {
	stubModelSnapshot(t, testSnap())
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	runner := &fakeModelRunner{}
	wantErr := errors.New("writer unavailable")
	cmd := modelStartCmd()
	cmd.SetOut(rejectingOutputWriter{err: wantErr})

	err := runModelStart(context.Background(), cmd, "storage", "/mnt/models/a.gguf", 8081, runner)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want writer failure", err)
	}
	if len(runner.started) != 1 || len(runner.probed) != 1 {
		t.Fatalf("model lifecycle did not complete before reporting failure: %+v", runner)
	}
}

func TestRunModelStartRefusesNetworkPath(t *testing.T) {
	snap := testSnap()
	snap.Nodes[0].Resources.Volumes = []models.Volume{{Mount: "/mnt/nas", Kind: "network"}}
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	err := runModelStart(context.Background(), modelStartCmd(), "storage", "/mnt/nas/a.gguf", 8081, &fakeModelRunner{})
	if err == nil || !strings.Contains(err.Error(), "named local volume") {
		t.Fatalf("got %v", err)
	}
}

func TestRunModelStopRequiresValidPort(t *testing.T) {
	stubModelSnapshot(t, testSnap())
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	for _, port := range []int{-1, 0, 65536} {
		runner := &fakeModelRunner{}
		err := runModelStop(context.Background(), modelStopCmd(), "storage", port, runner)
		if err == nil || !strings.Contains(err.Error(), "between 1 and 65535") {
			t.Fatalf("port %d error = %v, want valid range error", port, err)
		}
		if len(runner.stopped) != 0 {
			t.Fatalf("port %d reached runner: %#v", port, runner.stopped)
		}
	}
}

func TestRunModelStopPropagatesWriterFailureAfterStopping(t *testing.T) {
	stubModelSnapshot(t, testSnap())
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	runner := &fakeModelRunner{}
	wantErr := errors.New("writer unavailable")
	cmd := modelStopCmd()
	cmd.SetOut(rejectingOutputWriter{err: wantErr})

	err := runModelStop(context.Background(), cmd, "storage", 8081, runner)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want writer failure", err)
	}
	if len(runner.stopped) != 1 || runner.stopped[0] != 8081 {
		t.Fatalf("model stop did not complete before reporting failure: %+v", runner)
	}
}

func TestShellStartRefusesOccupiedPort(t *testing.T) {
	dir := t.TempDir()
	writeModelTestExecutable(t, filepath.Join(dir, "fuser"), `#!/bin/sh
printf '%s\n' 4242
`)

	cmd := exec.Command("/bin/sh", "-c", shellStart([]string{"/bin/true"}, 8081))
	cmd.Env = []string{"PATH=" + dir}
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if err == nil || !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("error = %v, output = %q", err, out)
	}
	if !strings.Contains(string(out), "port 8081 already has listener pid(s) 4242") {
		t.Fatalf("output = %q", out)
	}
}

func TestShellStartAllowsFreePort(t *testing.T) {
	dir := t.TempDir()
	writeModelTestExecutable(t, filepath.Join(dir, "fuser"), `#!/bin/sh
exit 1
`)

	cmd := exec.Command("/bin/sh", "-c", shellStart([]string{"/bin/true"}, 8082))
	cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shellStart: %v: %s", err, out)
	}
}

func TestShellStartFailsWithoutPortInspectionTool(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("/bin/sh", "-c", shellStart([]string{"/bin/true"}, 8083))
	cmd.Env = []string{"PATH=" + dir}
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if err == nil || !errors.As(err, &exitErr) || exitErr.ExitCode() != 127 {
		t.Fatalf("error = %v, output = %q", err, out)
	}
	if !strings.Contains(string(out), "requires fuser or lsof") {
		t.Fatalf("output = %q", out)
	}
}

func TestShellProbeRequiresLlamaServerOwnerAndHealth(t *testing.T) {
	dir := t.TempDir()
	curlLog := filepath.Join(dir, "curl.log")
	writeModelTestExecutable(t, filepath.Join(dir, "lsof"), `#!/bin/sh
printf '%s\n' 4242
`)
	writeModelTestExecutable(t, filepath.Join(dir, "ps"), `#!/bin/sh
printf '%s\n' llama-server
`)
	writeModelTestExecutable(t, filepath.Join(dir, "curl"), `#!/bin/sh
printf '%s\n' "$*" > "$AXIS_TEST_LOG"
`)

	cmd := exec.Command("/bin/sh", "-c", shellProbe(8084))
	cmd.Env = []string{"PATH=" + dir, "AXIS_TEST_LOG=" + curlLog}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shellProbe: %v: %s", err, out)
	}
	data, err := os.ReadFile(curlLog)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); !strings.Contains(got, "http://127.0.0.1:8084/v1/models") {
		t.Fatalf("curl call = %q", got)
	}
}

func TestShellProbeRefusesMissingListener(t *testing.T) {
	dir := t.TempDir()
	writeModelTestExecutable(t, filepath.Join(dir, "lsof"), `#!/bin/sh
exit 1
`)

	cmd := exec.Command("/bin/sh", "-c", shellProbe(8085))
	cmd.Env = []string{"PATH=" + dir}
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if err == nil || !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("error = %v, output = %q", err, out)
	}
	if !strings.Contains(string(out), "no listener on port 8085") {
		t.Fatalf("output = %q", out)
	}
}

func TestShellStopUsesFuserWhenAvailable(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "fuser.log")
	writeModelTestExecutable(t, filepath.Join(dir, "fuser"), `#!/bin/sh
printf '%s\n' "$*" >> "$AXIS_TEST_LOG"
printf '%s\n' "$AXIS_TEST_PID"
exit 0
`)
	writeModelTestExecutable(t, filepath.Join(dir, "lsof"), `#!/bin/sh
exit 99
`)
	writeModelTestExecutable(t, filepath.Join(dir, "ps"), `#!/bin/sh
printf '%s\n' llama-server
`)

	sleeper := exec.Command("sleep", "30")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = sleeper.Process.Kill()
			_ = sleeper.Wait()
		}
	})

	cmd := exec.Command("/bin/sh", "-c", shellStop(8081))
	cmd.Env = []string{
		"PATH=" + dir,
		"AXIS_TEST_LOG=" + logPath,
		"AXIS_TEST_PID=" + strconv.Itoa(sleeper.Process.Pid),
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shellStop: %v: %s", err, out)
	}
	if err := sleeper.Wait(); err == nil {
		t.Fatal("expected listener process to be killed")
	}
	waited = true
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "8081/tcp") {
		t.Fatalf("fuser calls = %q", got)
	}
}

func TestShellStopFallsBackToLsof(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "lsof.log")
	writeModelTestExecutable(t, filepath.Join(dir, "lsof"), `#!/bin/sh
printf '%s\n' "$*" > "$AXIS_TEST_LOG"
exit 1
`)

	cmd := exec.Command("/bin/sh", "-c", shellStop(8082))
	cmd.Env = []string{"PATH=" + dir, "AXIS_TEST_LOG=" + logPath}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shellStop: %v: %s", err, out)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); !strings.Contains(got, "-tiTCP:8082 -sTCP:LISTEN") {
		t.Fatalf("lsof call = %q", got)
	}
}

func TestShellStopFailsWhenNoPortToolExists(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("/bin/sh", "-c", shellStop(8083))
	cmd.Env = []string{"PATH=" + dir}
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if err == nil || !errors.As(err, &exitErr) || exitErr.ExitCode() != 127 {
		t.Fatalf("error = %v, output = %q", err, out)
	}
	if !strings.Contains(string(out), "requires fuser or lsof") {
		t.Fatalf("output = %q", out)
	}
}

func TestShellStopRefusesNonLlamaServerListener(t *testing.T) {
	dir := t.TempDir()
	writeModelTestExecutable(t, filepath.Join(dir, "lsof"), `#!/bin/sh
printf '%s\n' 4242
`)
	writeModelTestExecutable(t, filepath.Join(dir, "ps"), `#!/bin/sh
printf '%s\n' postgres
`)

	cmd := exec.Command("/bin/sh", "-c", shellStop(5432))
	cmd.Env = []string{"PATH=" + dir}
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if err == nil || !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("error = %v, output = %q", err, out)
	}
	if !strings.Contains(string(out), "not llama-server") || !strings.Contains(string(out), "postgres") {
		t.Fatalf("output = %q", out)
	}
}

func writeModelTestExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestResolveModelNodeRefusesUnconfiguredRemoteNode(t *testing.T) {
	stubModelSnapshot(t, testSnap())
	stubModelConfig(t, &config.Config{})

	_, _, err := resolveModelNode(context.Background(), "storage")
	if err == nil || !strings.Contains(err.Error(), "no configuration entry") {
		t.Fatalf("expected unconfigured remote-node error, got %v", err)
	}
}

func TestResolveModelNodeAllowsUnconfiguredLocalNode(t *testing.T) {
	snap := testSnap()
	snap.Nodes[0].Hostname = "127.0.0.1"
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{})

	node, cfgNode, err := resolveModelNode(context.Background(), "storage")
	if err != nil {
		t.Fatalf("resolve local node: %v", err)
	}
	if node.Name != "storage" || cfgNode != nil {
		t.Fatalf("node=%+v cfgNode=%+v", node, cfgNode)
	}
}

func TestRunOnNodeRefusesUnconfiguredRemoteNode(t *testing.T) {
	node := models.NodeFacts{Name: "remote-node", Hostname: "remote-node.invalid"}

	err := runOnNode(context.Background(), node, nil, "true")
	if err == nil || !strings.Contains(err.Error(), "no configuration entry") {
		t.Fatalf("expected unconfigured remote-node error, got %v", err)
	}
}

func TestRunOnNodeAllowsUnconfiguredLocalNode(t *testing.T) {
	node := models.NodeFacts{Name: "local-node", Hostname: "127.0.0.1"}

	if err := runOnNode(context.Background(), node, nil, "true"); err != nil {
		t.Fatalf("run local node: %v", err)
	}
}
