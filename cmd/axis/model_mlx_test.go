package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/modelinventory"
	"github.com/toasterbook88/axis/internal/modellife"
	"github.com/toasterbook88/axis/internal/models"
)

func TestMLXHelpSaysHTTPAPIIsNotForProduction(t *testing.T) {
	cmd := modelStartCmd()
	if !strings.Contains(cmd.Long, "not for production") {
		t.Fatalf("long=%q", cmd.Long)
	}
	flag := cmd.Flags().Lookup("mlx-model")
	if flag == nil || !strings.Contains(flag.Usage, "not for production") {
		t.Fatalf("flag=%v", flag)
	}
}

func TestMLXModelIsMutuallyExclusiveWithWeightsAndOllama(t *testing.T) {
	for _, args := range [][]string{
		{"--node", "storage", "--weights", "/mnt/models/a.gguf", "--port", "8080", "--mlx-model", "/mnt/models/qwen"},
		{"--node", "storage", "--ollama-model", "mistral", "--mlx-model", "/mnt/models/qwen"},
	} {
		cmd := modelStartCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("args=%v err=%v", args, err)
		}
	}
}

func TestMLXStartUsesObservedServerAndDoesNotUseLlamaRunner(t *testing.T) {
	snap := testSnap()
	snap.Nodes[0].Resources.MemoryTopology = models.MemoryTopologyUnified
	snap.Nodes[0].Tools = append(snap.Nodes[0].Tools, models.ToolInfo{Name: "mlx_lm.server", Path: "/usr/local/bin/mlx_lm.server"})
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
		if strings.Contains(script, "import mlx_lm") {
			t.Fatal("observed mlx_lm.server must not probe python import")
		}
		return "", nil
	}
	t.Cleanup(func() { runNodeScript = prevScript })

	cmd := modelStartCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{
		"--node", "storage", "--mlx-model", "/mnt/models/qwen", "--port", "8080",
		"--prefill-step-size", "2", "--prompt-cache-bytes", "4096", "--kv-bits", "4",
		"--format", "json",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(runner.started) != 0 || len(runner.probed) != 0 {
		t.Fatalf("llama runner used: started=%v probed=%v", runner.started, runner.probed)
	}
	if len(scripts) != 2 {
		t.Fatalf("scripts=%d", len(scripts))
	}
	start := scripts[0]
	for _, want := range []string{
		"nohup", "/usr/local/bin/mlx_lm.server", "--model", "/mnt/models/qwen",
		"--port", "8080", "--host", "127.0.0.1",
		"--prefill-step-size", "2", "--prompt-cache-bytes", "4096", "--kv-bits", "4",
	} {
		if !strings.Contains(start, want) {
			t.Fatalf("start missing %s: %s", want, start)
		}
	}
	for _, banned := range []string{"llama-server", "-ngl", "num_gpu", "iogpu", "--quant"} {
		if strings.Contains(start, banned) {
			t.Fatalf("start contains %s: %s", banned, start)
		}
	}
	probe := scripts[1]
	if !strings.Contains(probe, "/v1/models") || !strings.Contains(probe, "mlx_lm.server") || strings.Contains(probe, "not llama-server") {
		t.Fatalf("probe=%s", probe)
	}
	if !strings.Contains(buf.String(), `"engine": "mlx"`) || !strings.Contains(buf.String(), `"device_kind": "unified"`) {
		t.Fatalf("receipt=%s", buf.String())
	}

	scripts = nil
	cmd = modelStartCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--node", "storage", "--mlx-model", "/mnt/models/qwen", "--port", "8080"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	plain := scripts[0]
	if strings.Contains(plain, "prefill") || strings.Contains(plain, "prompt-cache") || strings.Contains(plain, "kv-bits") {
		t.Fatalf("unset flags leaked: %s", plain)
	}
}

func TestMLXStartUsesPythonModuleOnlyAfterObservedImport(t *testing.T) {
	snap := testSnap()
	snap.Nodes[0].Resources.MemoryTopology = models.MemoryTopologyUnified
	snap.Nodes[0].Tools = []models.ToolInfo{
		{Name: "mlx_lm", Path: "/usr/local/bin/mlx_lm"},
		{Name: "python3", Path: "/usr/bin/python3"},
	}
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	prevScript := runNodeScript
	var scripts []string
	runNodeScript = func(_ context.Context, _ models.NodeFacts, _ *config.NodeConfig, script string) (string, error) {
		scripts = append(scripts, script)
		if strings.Contains(script, "import mlx_lm") {
			if !strings.Contains(script, "/usr/bin/python3") {
				t.Fatalf("import did not use the observed python: %s", script)
			}
			return "axis-mlx-import:ok", nil
		}
		return "", nil
	}
	t.Cleanup(func() { runNodeScript = prevScript })

	cmd := modelStartCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--node", "storage", "--mlx-model", "/mnt/models/qwen", "--port", "8080", "--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(scripts, "\n")
	if !strings.Contains(joined, "/usr/bin/python3") || !strings.Contains(joined, "-m") || !strings.Contains(joined, "mlx_lm.server") {
		t.Fatalf("scripts=%s", joined)
	}
	if strings.Contains(scripts[1], "/usr/local/bin/mlx_lm ") || strings.Contains(scripts[1], "mlx_lm server") {
		t.Fatalf("console tool was launched: %s", scripts[1])
	}
}

func TestMLXStartRefusesHubDiscreteFileAndOccupiedPort(t *testing.T) {
	snap := testSnap()
	snap.Nodes[0].Resources.MemoryTopology = models.MemoryTopologyUnified
	snap.Nodes[0].Tools = append(snap.Nodes[0].Tools, models.ToolInfo{Name: "mlx_lm.server", Path: "/usr/local/bin/mlx_lm.server"})
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	prevScript := runNodeScript
	runNodeScript = func(context.Context, models.NodeFacts, *config.NodeConfig, string) (string, error) {
		t.Fatal("refused start executed a script")
		return "", nil
	}
	t.Cleanup(func() { runNodeScript = prevScript })

	refuses := []struct {
		args []string
		want string
	}{
		{[]string{"--node", "storage", "--mlx-model", "mlx-community/Qwen", "--port", "8080"}, "hub"},
		{[]string{"--node", "storage", "--mlx-model", "/mnt/models/a.gguf", "--port", "8080"}, "directory"},
	}
	for _, tc := range refuses {
		cmd := modelStartCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetArgs(tc.args)
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("args=%v err=%v", tc.args, err)
		}
	}

	discrete := testSnap()
	discrete.Nodes[0].Tools = append(discrete.Nodes[0].Tools, models.ToolInfo{Name: "mlx_lm.server", Path: "/usr/local/bin/mlx_lm.server"})
	stubModelSnapshot(t, discrete)
	cmd := modelStartCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--node", "storage", "--mlx-model", "/mnt/models/qwen", "--port", "8080"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unified") {
		t.Fatalf("discrete err=%v", err)
	}

	occupied := testSnap()
	occupied.Nodes[0].Resources.MemoryTopology = models.MemoryTopologyUnified
	occupied.Nodes[0].Tools = append(occupied.Nodes[0].Tools, models.ToolInfo{Name: "mlx_lm.server", Path: "/usr/local/bin/mlx_lm.server"})
	occupied.Nodes[0].ResidentModels = []models.ResidentModel{{Name: "busy", Runtime: "llama.cpp", Port: 8080}}
	stubModelSnapshot(t, occupied)
	cmd = modelStartCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--node", "storage", "--mlx-model", "/mnt/models/qwen", "--port", "8080"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "occupied") {
		t.Fatalf("occupied err=%v", err)
	}
}

func TestMLXGenerationStopSetsEngineGuard(t *testing.T) {
	snap := generationStopSnapshot()
	snap.Nodes[0].ResidentModels[0].Runtime = "mlx"
	snap.Nodes[0].ResidentModels[0].Name = "qwen"
	snap.Nodes[0].ResidentModels[0].Executable = "/usr/local/bin/mlx_lm.server"
	want := modelinventory.FromSnapshot(snap, "daemon-cache").Instances[0]
	if want.Engine != models.EngineMLX {
		t.Fatalf("engine=%s", want.Engine)
	}
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	runner := &fakeModelRunner{stopDisposition: modelStopStopped}
	cmd := modelStopCmd()
	cmd.SetOut(&bytes.Buffer{})
	if err := runModelStopGeneration(context.Background(), cmd, want.GenerationID, "test.sock", "text", runner); err != nil {
		t.Fatal(err)
	}
	if len(runner.stopTargets) != 1 || runner.stopTargets[0].Engine != models.EngineMLX {
		t.Fatalf("targets=%#v", runner.stopTargets)
	}
	script := shellStopTarget(runner.stopTargets[0])
	if strings.Contains(script, "not llama-server") || !strings.Contains(script, "mlx_lm.server") {
		t.Fatalf("script=%s", script)
	}
	if !strings.Contains(script, want.ProcessStartToken) || !strings.Contains(script, `kill -KILL "4242"`) {
		t.Fatalf("generation evidence missing: %s", script)
	}
}

func TestShellStopMLXGuardMatchesServerNotPythonOrConsole(t *testing.T) {
	cases := []struct {
		name    string
		comm    string
		args    string
		allow   bool
		engine  string
		wantOut string
	}{
		{name: "python", comm: "python", args: "python app.py", wantOut: "wrong_owner"},
		{name: "mlx console", comm: "mlx_lm", args: "mlx_lm server --port 8080", wantOut: "wrong_owner"},
		{name: "basename", comm: "/usr/local/bin/mlx_lm.server", args: "mlx_lm.server --model /mnt/models/qwen", allow: true},
		{name: "module", comm: "python", args: "python -m mlx_lm.server --model /mnt/models/qwen", allow: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, alive, err := runMLXStopScript(t, tc.comm, tc.args, models.EngineMLX)
			if tc.allow {
				if err != nil || alive {
					t.Fatalf("err=%v alive=%v out=%s", err, alive, out)
				}
				if !strings.Contains(out, modelStopMarker+"stopped") {
					t.Fatalf("out=%s", out)
				}
				return
			}
			var exitErr *exec.ExitError
			if err == nil || !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || !alive {
				t.Fatalf("err=%v alive=%v out=%s", err, alive, out)
			}
			if !strings.Contains(out, tc.wantOut) || strings.Contains(out, modelStopMarker+"stopped") {
				t.Fatalf("out=%s", out)
			}
		})
	}

	out, alive, err := runMLXStopScript(t, "python", "python -m mlx_lm.server", "")
	var exitErr *exec.ExitError
	if err == nil || !errors.As(err, &exitErr) || !alive || !strings.Contains(out, "not llama-server") {
		t.Fatalf("legacy python stop err=%v alive=%v out=%s", err, alive, out)
	}
}

func runMLXStopScript(t *testing.T, comm, args, engine string) (string, bool, error) {
	t.Helper()
	targetProc := stubFirstCommand("", "sleep", "30")
	if err := targetProc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = targetProc.Process.Kill()
		_, _ = targetProc.Process.Wait()
	})
	pid := fmt.Sprintf("%d", targetProc.Process.Pid)
	dir := t.TempDir()
	writeModelTestExecutable(t, filepath.Join(dir, "fuser"), "#!/bin/sh\nprintf '%s\\n' "+pid+"\n")
	writeModelTestExecutable(t, filepath.Join(dir, "lsof"), "#!/bin/sh\nprintf '%s\\n' "+pid+"\n")
	writeModelTestExecutable(t, filepath.Join(dir, "ps"), fmt.Sprintf(`#!/bin/sh
case "$*" in
  *"-o args="*) printf '%%s\n' %s ;;
  *) printf '%%s\n' %s ;;
esac
`, shellQuote(args), shellQuote(comm)))
	cmd := exec.Command("/bin/sh", "-c", shellStopTarget(modellife.StopTarget{Port: 8080, Engine: engine}))
	cmd.Env = []string{stubFirstPATH(dir)}
	out, err := cmd.CombinedOutput()
	text := string(out)
	alive := processStillRunning(targetProc.Process.Pid)
	// kill(2) can return before /proc shows the victim as dead.
	if strings.Contains(text, modelStopMarker+"stopped") {
		deadline := time.Now().Add(500 * time.Millisecond)
		for alive && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
			alive = processStillRunning(targetProc.Process.Pid)
		}
	}
	return text, alive, err
}

func processStillRunning(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	text := string(data)
	i := strings.LastIndex(text, ")")
	if i < 0 || i+2 >= len(text) {
		return true
	}
	state := text[i+2]
	return state != 'Z' && state != 'X'
}
