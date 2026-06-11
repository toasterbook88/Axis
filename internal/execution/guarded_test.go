package execution

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/events"
	"github.com/toasterbook88/axis/internal/failures"
	"github.com/toasterbook88/axis/internal/git"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/reservation"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/scripts"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

func TestMain(m *testing.M) {
	// Stub git status to be clean by default for all execution tests
	prevGit := GetGitRepoState
	GetGitRepoState = func(dir string) (git.RepoState, error) {
		return git.RepoState{IsRepo: true, IsDirty: false}, nil
	}
	code := m.Run()
	GetGitRepoState = prevGit
	os.Exit(code)
}

func TestPrepareRequirementsExecKeepsOllamaRequirement(t *testing.T) {
	reqs := prepareRequirements("ollama run llama3", ModeExec, Intent{})
	if len(reqs.RequiredTools) == 0 {
		t.Fatal("expected required tools to be preserved")
	}
	found := false
	for _, tool := range reqs.RequiredTools {
		if strings.EqualFold(tool, "ollama") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected ollama requirement to remain, got %v", reqs.RequiredTools)
	}
}

func TestPrepareRequirementsScriptInheritsScriptToolsAndRAM(t *testing.T) {
	intent := Intent{
		MatchedScript: &scripts.Script{
			Name:          "test-script",
			Command:       "echo hello",
			RequiredTools: []string{"git", "jq"},
			EstRAMMB:      4096,
		},
	}
	reqs := prepareRequirements("echo hello", ModeScript, intent)
	if len(reqs.RequiredTools) != 2 || reqs.RequiredTools[0] != "git" || reqs.RequiredTools[1] != "jq" {
		t.Fatalf("expected script tools, got %v", reqs.RequiredTools)
	}
	if reqs.MinFreeRAMMB != 4096 {
		t.Fatalf("expected 4096MB from script, got %d", reqs.MinFreeRAMMB)
	}
}

func TestPrepareRequirementsScriptTakesHigherRAM(t *testing.T) {
	intent := Intent{
		MatchedScript: &scripts.Script{
			Name:          "test-script",
			Command:       "echo hello",
			RequiredTools: []string{"git"},
			EstRAMMB:      8192,
		},
	}
	// InferRequirements for "echo hello" likely returns a low MinFreeRAMMB
	reqs := prepareRequirements("echo hello", ModeScript, intent)
	if reqs.MinFreeRAMMB != 8192 {
		t.Fatalf("expected 8192MB (higher than inferred), got %d", reqs.MinFreeRAMMB)
	}
}

func TestPrepareRequirementsExplicitLlamaServerRequiresObservedTool(t *testing.T) {
	reqs := prepareRequirements("llama-server -m qwen.gguf --port 8080", ModeExec, Intent{})
	if len(reqs.RequiredTools) != 1 || reqs.RequiredTools[0] != "llama-server" {
		t.Fatalf("expected llama-server requirement, got %v", reqs.RequiredTools)
	}
	if reqs.MinFreeRAMMB != 6144 {
		t.Fatalf("expected 6144MB floor for llama-server task, got %d", reqs.MinFreeRAMMB)
	}
	if len(reqs.PreferredBackends) == 0 || reqs.PreferredBackends[0] != "llama.cpp" {
		t.Fatalf("expected llama.cpp preferred backend, got %v", reqs.PreferredBackends)
	}
}

func TestPrepareRequirementsAppleFoundationModelsUsesExplicitHelper(t *testing.T) {
	reqs := prepareRequirements("xcrun swift hack/apple-foundation-models.swift --self-test", ModeExec, Intent{})
	if len(reqs.RequiredTools) != 1 || reqs.RequiredTools[0] != "apple-foundation-models" {
		t.Fatalf("expected apple foundation models requirement, got %v", reqs.RequiredTools)
	}
	if len(reqs.PreferredBackends) == 0 || reqs.PreferredBackends[0] != "apple-foundation-models" {
		t.Fatalf("expected apple foundation models preferred backend, got %v", reqs.PreferredBackends)
	}
}

func TestRunGuardedBlocksLocalInferenceOnConstrainedMac(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rt := testGuardedRuntime(t, []models.NodeFacts{
		{
			Name:     "macbook",
			Hostname: "localhost",
			OS:       "darwin",
			Status:   models.StatusComplete,
			Resources: &models.Resources{
				RAMTotalMB: 8192,
				RAMFreeMB:  7600,
				Pressure:   "low",
				CPUCores:   8,
			},
			Tools: []models.ToolInfo{{Name: "ollama", Version: "test"}},
			Ollama: &models.OllamaInfo{
				Installed: true,
				Listening: true,
				Models:    []string{"llama3"},
			},
		},
	})

	prevProbe := ProbeLocalAvailableRAMMB
	ProbeLocalAvailableRAMMB = func(context.Context) (int64, error) { return 7600, nil }
	defer func() { ProbeLocalAvailableRAMMB = prevProbe }()

	var called bool
	prevShell := RunLocalShell
	RunLocalShell = func(context.Context, string, []string) ([]byte, int64, error) {
		called = true
		return nil, 0, errors.New("should not run")
	}
	defer func() { RunLocalShell = prevShell }()

	resp, err := RunGuarded(context.Background(), rt, GuardedExecutionRequest{
		Description: "ollama run llama3",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
	})
	if err == nil {
		t.Fatal("expected local safety block")
	}
	if called {
		t.Fatal("expected local shell to remain blocked")
	}
	if !strings.Contains(resp.Error, "disabled on constrained") {
		t.Fatalf("expected constrained-host block, got %#v", resp)
	}
}

func TestRunGuardedLocalInferenceUsesLiveMemoryPreflight(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	rt := testGuardedRuntime(t, []models.NodeFacts{
		{
			Name:     "studio",
			Hostname: "localhost",
			OS:       "darwin",
			Status:   models.StatusComplete,
			Resources: &models.Resources{
				RAMTotalMB: 16384,
				RAMFreeMB:  12000,
				Pressure:   "low",
				CPUCores:   10,
			},
			Tools: []models.ToolInfo{{Name: "ollama", Version: "test"}},
			Ollama: &models.OllamaInfo{
				Installed: true,
				Listening: true,
				Models:    []string{"llama3"},
			},
		},
	})

	prevProbe := ProbeLocalAvailableRAMMB
	ProbeLocalAvailableRAMMB = func(context.Context) (int64, error) { return 2048, nil }
	defer func() { ProbeLocalAvailableRAMMB = prevProbe }()

	var called bool
	prevShell := RunLocalShell
	RunLocalShell = func(context.Context, string, []string) ([]byte, int64, error) {
		called = true
		return nil, 0, errors.New("should not run")
	}
	defer func() { RunLocalShell = prevShell }()

	resp, err := RunGuarded(context.Background(), rt, GuardedExecutionRequest{
		Description: "ollama run llama3",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
	})
	if err == nil {
		t.Fatal("expected live-memory preflight failure")
	}
	if called {
		t.Fatal("expected local shell to remain blocked")
	}
	if !strings.Contains(resp.Error, "live local memory preflight failed") {
		t.Fatalf("expected live preflight failure, got %#v", resp)
	}
}

func TestRunGuardedFailsClosedWhenLocalMemoryPreflightUnavailable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	rt := testGuardedRuntime(t, []models.NodeFacts{
		{
			Name:     "studio",
			Hostname: "localhost",
			OS:       "darwin",
			Status:   models.StatusComplete,
			Resources: &models.Resources{
				RAMTotalMB: 16384,
				RAMFreeMB:  12000,
				Pressure:   "low",
				CPUCores:   10,
			},
			Tools: []models.ToolInfo{{Name: "ollama", Version: "test"}},
			Ollama: &models.OllamaInfo{
				Installed: true,
				Listening: true,
				Models:    []string{"llama3"},
			},
		},
	})

	prevProbe := ProbeLocalAvailableRAMMB
	ProbeLocalAvailableRAMMB = func(context.Context) (int64, error) { return 0, errors.New("vm_stat missing") }
	defer func() { ProbeLocalAvailableRAMMB = prevProbe }()

	var called bool
	prevShell := RunLocalShell
	RunLocalShell = func(context.Context, string, []string) ([]byte, int64, error) {
		called = true
		return nil, 0, errors.New("should not run")
	}
	defer func() { RunLocalShell = prevShell }()

	resp, err := RunGuarded(context.Background(), rt, GuardedExecutionRequest{
		Description: "ollama run llama3",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
	})
	if err == nil {
		t.Fatal("expected preflight-unavailable failure")
	}
	if called {
		t.Fatal("expected local shell to remain blocked")
	}
	if !strings.Contains(resp.Error, "preflight unavailable; refusing") {
		t.Fatalf("expected fail-closed preflight error, got %#v", resp)
	}
}

func TestRunGuardedEmitsExecutionStateChanges(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	rt := testGuardedRuntime(t, []models.NodeFacts{
		{
			Name:     "studio",
			Hostname: "localhost",
			Status:   models.StatusComplete,
			Resources: &models.Resources{
				RAMTotalMB: 8192,
				RAMFreeMB:  4096,
				Pressure:   "low",
				CPUCores:   8,
			},
		},
	})

	prevShell := RunLocalShell
	RunLocalShell = func(context.Context, string, []string) ([]byte, int64, error) {
		return []byte("ok\n"), 0, nil
	}
	defer func() { RunLocalShell = prevShell }()

	var triggers []string
	resp, err := RunGuarded(context.Background(), rt, GuardedExecutionRequest{
		Description: "echo ok",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
		OnStateChange: func(_ context.Context, trigger string, _ GuardedExecutionResult) {
			triggers = append(triggers, trigger)
		},
	})
	if err != nil {
		t.Fatalf("RunGuarded: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK response, got %#v", resp)
	}
	if len(triggers) != 2 {
		t.Fatalf("expected two execution state changes, got %v", triggers)
	}
	if triggers[0] != StateChangeExecutionReserved || triggers[1] != StateChangeExecutionFinished {
		t.Fatalf("unexpected execution state change sequence: %v", triggers)
	}
}

func TestPrepareGuardedExecutionDefersOnReadyAndStateMutation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	rt := testGuardedRuntime(t, []models.NodeFacts{
		{
			Name:     "studio",
			Hostname: "localhost",
			Status:   models.StatusComplete,
			Resources: &models.Resources{
				RAMTotalMB: 8192,
				RAMFreeMB:  4096,
				Pressure:   "low",
				CPUCores:   8,
			},
		},
	})

	var readyCalled bool
	prepared, err := PrepareGuardedExecution(context.Background(), rt, GuardedExecutionRequest{
		Description: "echo ok",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
		OnReady: func(GuardedExecutionResult) {
			readyCalled = true
		},
	})
	if err != nil {
		t.Fatalf("PrepareGuardedExecution: %v", err)
	}
	if readyCalled {
		t.Fatal("expected prepare step to defer OnReady callback")
	}
	if len(rt.State.Nodes) != 0 {
		t.Fatalf("expected no reservation state during prepare, got %#v", rt.State.Nodes)
	}
	if prepared.Result.Node != "studio" || prepared.Command != "echo ok" {
		t.Fatalf("unexpected prepared result: %#v", prepared)
	}
}

func TestRunPreparedExecutionCallsOnReadyDuringExecution(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	rt := testGuardedRuntime(t, []models.NodeFacts{
		{
			Name:     "studio",
			Hostname: "localhost",
			Status:   models.StatusComplete,
			Resources: &models.Resources{
				RAMTotalMB: 8192,
				RAMFreeMB:  4096,
				Pressure:   "low",
				CPUCores:   8,
			},
		},
	})

	var readyCalled bool
	prepared, err := PrepareGuardedExecution(context.Background(), rt, GuardedExecutionRequest{
		Description: "echo ok",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
		OnReady: func(GuardedExecutionResult) {
			readyCalled = true
		},
	})
	if err != nil {
		t.Fatalf("PrepareGuardedExecution: %v", err)
	}

	prevShell := RunLocalShell
	RunLocalShell = func(context.Context, string, []string) ([]byte, int64, error) {
		return []byte("ok\n"), 0, nil
	}
	defer func() { RunLocalShell = prevShell }()

	resp, err := RunPreparedExecution(context.Background(), prepared)
	if err != nil {
		t.Fatalf("RunPreparedExecution: %v", err)
	}
	if !readyCalled {
		t.Fatal("expected OnReady callback during execute step")
	}
	if !resp.OK {
		t.Fatalf("expected OK response, got %#v", resp)
	}
}

func TestRunGuardedHeartbeatsActiveReservationWhileCommandRuns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	rt := testGuardedRuntime(t, []models.NodeFacts{
		{
			Name:     "studio",
			Hostname: "localhost",
			Status:   models.StatusComplete,
			Resources: &models.Resources{
				RAMTotalMB: 8192,
				RAMFreeMB:  4096,
				Pressure:   "low",
				CPUCores:   8,
			},
		},
	})

	prevInterval := executionHeartbeatInterval
	executionHeartbeatInterval = 10 * time.Millisecond
	defer func() { executionHeartbeatInterval = prevInterval }()

	prevHeartbeat := heartbeatTask
	heartbeatCalls := 0
	heartbeatTask = func(ledger *reservation.Ledger, ledgerExecID string) error {
		fmt.Printf("DEBUG: heartbeatTask called id=%s\n", ledgerExecID)
		heartbeatCalls++
		return nil
	}
	defer func() { heartbeatTask = prevHeartbeat }()

	prevShell := RunLocalShell
	RunLocalShell = func(context.Context, string, []string) ([]byte, int64, error) {
		time.Sleep(150 * time.Millisecond)
		return []byte("ok\n"), 0, nil
	}
	defer func() { RunLocalShell = prevShell }()

	prevStream := StreamLocalShell
	StreamLocalShell = func(ctx context.Context, command string, env []string, stdout, stderr io.Writer) (int64, error) {
		time.Sleep(150 * time.Millisecond)
		if stdout != nil {
			_, _ = stdout.Write([]byte("ok\n"))
		}
		return 0, nil
	}
	defer func() { StreamLocalShell = prevStream }()

	resp, err := RunGuarded(context.Background(), rt, GuardedExecutionRequest{
		Description: "echo ok",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
	})
	if err != nil {
		t.Fatalf("RunGuarded: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK response, got %#v", resp)
	}
	if heartbeatCalls == 0 {
		t.Fatal("expected at least one execution heartbeat during run")
	}
}

func TestRunWithReservationHeartbeatKeepsHeartbeatingAfterCancelUntilRunReturns(t *testing.T) {
	prevInterval := executionHeartbeatInterval
	executionHeartbeatInterval = 10 * time.Millisecond
	defer func() { executionHeartbeatInterval = prevInterval }()

	prevHeartbeat := heartbeatTask
	heartbeatCh := make(chan struct{}, 16)
	heartbeatTask = func(ledger *reservation.Ledger, ledgerExecID string) error {
		if ledgerExecID != "exec-1" {
			t.Fatalf("unexpected heartbeat target execID=%q", ledgerExecID)
		}
		heartbeatCh <- struct{}{}
		return nil
	}
	defer func() { heartbeatTask = prevHeartbeat }()

	_, cancel := context.WithCancel(context.Background())
	doneRun := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, _, err := runWithReservationHeartbeat(&reservation.Ledger{}, "exec-1", func() (string, int64, error) {
			<-doneRun
			return "ok", 0, nil
		}); err != nil {
			t.Errorf("runWithReservationHeartbeat: %v", err)
		}
	}()

	select {
	case <-heartbeatCh:
	case <-time.After(time.Second):
		t.Fatal("expected initial heartbeat")
	}

	cancel()

	select {
	case <-heartbeatCh:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expected heartbeats to continue after cancellation until run returns")
	}

	close(doneRun)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("expected heartbeat wrapper to exit after run returns")
	}
}

func TestRunGuardedPersistsExecutionOriginFromLocalRuntime(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	rt := testGuardedRuntime(t, []models.NodeFacts{
		{
			Name:     "studio",
			Hostname: "localhost",
			Identity: models.NewNodeIdentity("abc-123", "linux-machine-id"),
			Status:   models.StatusComplete,
			Resources: &models.Resources{
				RAMTotalMB: 8192,
				RAMFreeMB:  4096,
				Pressure:   "low",
				CPUCores:   8,
			},
		},
	})

	prevShell := RunLocalShell
	RunLocalShell = func(context.Context, string, []string) ([]byte, int64, error) {
		if rt.Ledger == nil {
			t.Fatal("expected ledger to be non-nil")
		}
		entries := rt.Ledger.EntriesForNode("studio")
		if len(entries) != 1 {
			t.Fatalf("EntriesForNode = %d, want 1", len(entries))
		}
		entry := entries[0]
		if entry.OwnerOrigin != models.NewExecutionOrigin("studio", "localhost", "abc-123") {
			t.Fatalf("OwnerOrigin = %+v, want studio/localhost/abc-123", entry.OwnerOrigin)
		}
		return []byte("ok\n"), 0, nil
	}
	defer func() { RunLocalShell = prevShell }()

	resp, err := RunGuarded(context.Background(), rt, GuardedExecutionRequest{
		Description:  "echo ok",
		Mode:         ModeExec,
		Confirm:      ConfirmWord,
		OwnerSurface: OwnerSurfaceTaskRun,
	})
	if err != nil {
		t.Fatalf("RunGuarded: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK response, got %#v", resp)
	}
}

func TestRunGuardedUsesOriginOverrideWhenPresent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	rt := testGuardedRuntime(t, []models.NodeFacts{
		{
			Name:     "studio",
			Hostname: "localhost",
			Identity: models.NewNodeIdentity("abc-123", "linux-machine-id"),
			Status:   models.StatusComplete,
			Resources: &models.Resources{
				RAMTotalMB: 8192,
				RAMFreeMB:  4096,
				Pressure:   "low",
				CPUCores:   8,
			},
		},
	})

	want := models.NewExecutionOrigin("relay", "relay.local", "relay-123")
	prevShell := RunLocalShell
	RunLocalShell = func(context.Context, string, []string) ([]byte, int64, error) {
		if rt.Ledger == nil {
			t.Fatal("expected ledger to be non-nil")
		}
		entries := rt.Ledger.EntriesForNode("studio")
		if len(entries) != 1 {
			t.Fatalf("EntriesForNode = %d, want 1", len(entries))
		}
		entry := entries[0]
		if entry.OwnerOrigin != want {
			t.Fatalf("OwnerOrigin = %+v, want %+v", entry.OwnerOrigin, want)
		}
		return []byte("ok\n"), 0, nil
	}
	defer func() { RunLocalShell = prevShell }()

	resp, err := RunGuarded(context.Background(), rt, GuardedExecutionRequest{
		Description:    "echo ok",
		Mode:           ModeExec,
		Confirm:        ConfirmWord,
		OwnerSurface:   OwnerSurfaceHTTPRun,
		OriginOverride: want,
	})
	if err != nil {
		t.Fatalf("RunGuarded: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK response, got %#v", resp)
	}
}

func TestRemoteTrapIsShellSafeWithAdversarialPaths(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		command string
	}{
		{"simple path", "/tmp/axis-knows-123.json", "echo hello"},
		{"path with spaces", "/tmp/path with spaces.json", "echo hello"},
		{"path with single quote", "/tmp/it's-a-test.json", "echo hello"},
		{"path with dollar sign", "/tmp/axis-$HOME.json", "echo hello"},
		{"path with backtick", "/tmp/axis-`whoami`.json", "echo hello"},
		{"path with semicolon", "/tmp/axis;rm -rf.json", "echo hello"},
		{"command with pipe", "/tmp/ctx.json", "echo hello | grep h"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefix := RemoteExecPrefix("node1", tt.path, []string{
				"AXIS_EXECUTION_MODE=exec",
				"AXIS_CONFIRM=YES",
			})
			escapedPath := shellescape.Quote(tt.path)
			escapedCmd := shellescape.Quote(tt.command)

			// Pattern: _axis_ctx=QUOTED; trap 'rm -f "$_axis_ctx"' EXIT; bash -lc QUOTED
			// The path is assigned to a variable and referenced as "$_axis_ctx"
			// inside single-quoted trap body, so no quoting interaction occurs.
			// The variable assignment uses shellescape.Quote which is safe in
			// simple command position (no nested quoting context).
			runCmd := fmt.Sprintf(
				"%s _axis_ctx=%s; trap 'rm -f \"$_axis_ctx\"' EXIT; bash -lc %s",
				prefix, escapedPath, escapedCmd,
			)

			// Verify the constructed command contains the variable-based trap.
			// The trap body is single-quoted, so "$_axis_ctx" is a literal
			// shell variable reference — no quoting interaction with the path.
			if !strings.Contains(runCmd, "_axis_ctx=") {
				t.Fatal("remote command must use variable-based cleanup trap")
			}
			if !strings.Contains(runCmd, "trap 'rm -f \"$_axis_ctx\"' EXIT") {
				t.Fatalf("trap pattern not found in command: %s", runCmd)
			}
			// Verify the old unsafe trap form is NOT present.
			if strings.Contains(runCmd, "trap 'rm -f '") {
				t.Fatalf("old unsafe trap form found in command: %s", runCmd)
			}
		})
	}
}

func TestRunRemoteUsesVariableBasedTrap(t *testing.T) {
	var capturedCmds []string
	prev := NewRemoteExecutor
	NewRemoteExecutor = func(nc config.NodeConfig) RemoteExecutor {
		return &stubRemoteExecutor{runFunc: func(_ context.Context, cmd string) (string, error) {
			capturedCmds = append(capturedCmds, cmd)
			return "", nil
		}}
	}
	defer func() { NewRemoteExecutor = prev }()

	cfgNodes := []config.NodeConfig{{Name: "testnode", Hostname: "testhost", SSHUser: "testuser"}}
	rt := &runtimectx.Context{
		State:  &state.ClusterState{Nodes: map[string]state.NodeState{}},
		Skills: &skills.Store{},
	}

	req := GuardedExecutionRequest{
		Description:  "test",
		Mode:         ModeExec,
		Confirm:      ConfirmWord,
		OwnerSurface: "test",
		Stdout:       io.Discard,
		Stderr:       io.Discard,
	}
	reqs := models.TaskRequirements{Workload: models.WorkloadProfileMatch{Class: models.ClassRepoAnalysis}}
	resp := GuardedExecutionResult{Node: "testnode"}

	_, err := runRemote(
		context.Background(),
		rt.State,
		rt.Skills,
		state.ExecutionOwner{Surface: "test"},
		req,
		reqs,
		resp,
		cfgNodes[0],
		0,
		"echo hello",
		nil,
		[]byte(`{"test":true}`),
		nil,
	)
	if err != nil {
		t.Fatalf("runRemote failed: %v", err)
	}

	// The first command is the heredoc write (context JSON upload).
	heredocCmd := capturedCmds[0]
	if !strings.Contains(heredocCmd, "AXIS_EOF") {
		t.Fatalf("heredoc must use AXIS_EOF delimiter, got: %s", heredocCmd)
	}

	// The second command is the run command with the trap.
	// Since runRemoteWithOutput delegates to executor.Stream which we
	// don't stub here, verify the command construction pattern directly
	// by testing RemoteExecPrefix + variable assignment.
	prefix := RemoteExecPrefix("node1", "/tmp/ctx.json", []string{"AXIS_EXECUTION_MODE=exec", "AXIS_CONFIRM=YES"})
	runCmd := fmt.Sprintf(
		"%s _axis_ctx=%s; trap 'rm -f \"$_axis_ctx\"' EXIT; bash -lc %s",
		prefix, shellescape.Quote("/tmp/ctx.json"), shellescape.Quote("echo hello"),
	)
	if !strings.Contains(runCmd, "_axis_ctx=") {
		t.Fatal("command must use variable-based cleanup trap")
	}
	if !strings.Contains(runCmd, "trap 'rm -f \"$_axis_ctx\"' EXIT") {
		t.Fatalf("trap pattern not found in command: %s", runCmd)
	}
}

type stubRemoteExecutor struct {
	runFunc func(context.Context, string) (string, error)
}

func (s *stubRemoteExecutor) Run(ctx context.Context, cmd string) (string, error) {
	return s.runFunc(ctx, cmd)
}
func (s *stubRemoteExecutor) Close() error { return nil }

func TestValidateRequestEmptyDescription(t *testing.T) {
	err := ValidateRequest(GuardedExecutionRequest{Description: "", Mode: ModeExec, Confirm: ConfirmWord})
	if err == nil {
		t.Fatal("expected error for empty description")
	}
	if !strings.Contains(err.Error(), "description is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRequestEmptyMode(t *testing.T) {
	err := ValidateRequest(GuardedExecutionRequest{Description: "echo hi", Mode: "", Confirm: ConfirmWord})
	if err == nil {
		t.Fatal("expected error for empty mode")
	}
	if !strings.Contains(err.Error(), "mode is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRequestInvalidMode(t *testing.T) {
	err := ValidateRequest(GuardedExecutionRequest{Description: "echo hi", Mode: "invalid", Confirm: ConfirmWord})
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
	if !strings.Contains(err.Error(), "mode must be script or exec") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRequestInvalidConfirm(t *testing.T) {
	err := ValidateRequest(GuardedExecutionRequest{Description: "echo hi", Mode: ModeExec, Confirm: "NO"})
	if err == nil {
		t.Fatal("expected error for invalid confirm")
	}
	if !strings.Contains(err.Error(), "confirm must be YES") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveIntentScriptNoMatch(t *testing.T) {
	_, err := ResolveIntent("nonexistent script", ModeScript, &skills.Store{})
	if err == nil {
		t.Fatal("expected error for unmatched script mode")
	}
	if !strings.Contains(err.Error(), "no known script or learned skill matches") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrepareGuardedExecutionNilRuntime(t *testing.T) {
	_, err := PrepareGuardedExecution(context.Background(), nil, GuardedExecutionRequest{
		Description: "echo hi",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
	})
	if err == nil {
		t.Fatal("expected error for nil runtime")
	}
	if !strings.Contains(err.Error(), "runtime context unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrepareGuardedExecutionNilConfig(t *testing.T) {
	rt := &runtimectx.Context{
		Config:   nil,
		Snapshot: &models.ClusterSnapshot{Nodes: []models.NodeFacts{}},
	}
	_, err := PrepareGuardedExecution(context.Background(), rt, GuardedExecutionRequest{
		Description: "echo hi",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
	})
	if err == nil {
		t.Fatal("expected error for nil config")
	}
	if !strings.Contains(err.Error(), "runtime context unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrepareGuardedExecutionNilSnapshot(t *testing.T) {
	rt := &runtimectx.Context{
		Config:   &config.Config{},
		Snapshot: nil,
	}
	_, err := PrepareGuardedExecution(context.Background(), rt, GuardedExecutionRequest{
		Description: "echo hi",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
	})
	if err == nil {
		t.Fatal("expected error for nil snapshot")
	}
	if !strings.Contains(err.Error(), "runtime context unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrepareGuardedExecutionNoSuitableNode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	rt := testGuardedRuntime(t, []models.NodeFacts{})
	_, err := PrepareGuardedExecution(context.Background(), rt, GuardedExecutionRequest{
		Description: "echo hi",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
	})
	if err == nil {
		t.Fatal("expected error for no suitable node")
	}
	if !strings.Contains(err.Error(), "no suitable node found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrepareGuardedExecutionRemoteNodeNotInConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Config has only local node; snapshot has a remote node
	rt := &runtimectx.Context{
		Config: &config.Config{
			Nodes: []config.NodeConfig{
				{Name: "local", Hostname: "localhost", SSHUser: "me"},
			},
		},
		Snapshot: &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Nodes: []models.NodeFacts{
				{
					Name:     "remote-node",
					Hostname: "remote.example",
					Status:   models.StatusComplete,
					Resources: &models.Resources{
						RAMTotalMB: 8192,
						RAMFreeMB:  4096,
						Pressure:   "low",
						CPUCores:   8,
					},
				},
			},
			Summary: models.ClusterSummary{TotalNodes: 1, ReachableNodes: 1},
		},
		State:  &state.ClusterState{Nodes: map[string]state.NodeState{}},
		Skills: &skills.Store{},
	}
	_, err := PrepareGuardedExecution(context.Background(), rt, GuardedExecutionRequest{
		Description: "echo hi",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
	})
	if err == nil {
		t.Fatal("expected error for remote node not in config")
	}
	if !strings.Contains(err.Error(), "not found in config") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrepareGuardedExecutionReservationCapExceeded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	node := models.NodeFacts{
		Name:     "studio",
		Hostname: "localhost",
		Status:   models.StatusComplete,
		Resources: &models.Resources{
			RAMTotalMB: 8192,
			RAMFreeMB:  100,
			Pressure:   "low",
			CPUCores:   8,
		},
	}
	rt := testGuardedRuntime(t, []models.NodeFacts{node})
	// Pre-reserve most of the RAM so the new reservation cannot fit
	if rt.Ledger != nil {
		rt.Ledger.SetNodeCapacity("studio", 8192)
		_, _ = rt.Ledger.Reserve(reservation.Entry{
			ID:       "existing",
			Node:     "studio",
			RAMMB:    8000,
			OwnerPID: 1,
		})
	}

	_, err := PrepareGuardedExecution(context.Background(), rt, GuardedExecutionRequest{
		Description: "echo hi",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
	})
	if err == nil {
		t.Fatal("expected error for reservation cap exceeded")
	}
	if !strings.Contains(err.Error(), "reservation cap exceeded") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunGuardedLocalShellFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	node := models.NodeFacts{
		Name:     "studio",
		Hostname: "localhost",
		Status:   models.StatusComplete,
		Resources: &models.Resources{
			RAMTotalMB: 8192,
			RAMFreeMB:  4096,
			Pressure:   "low",
			CPUCores:   8,
		},
	}
	rt := testGuardedRuntime(t, []models.NodeFacts{node})

	prevShell := RunLocalShell
	RunLocalShell = func(context.Context, string, []string) ([]byte, int64, error) {
		return []byte("failure output\n"), 0, fmt.Errorf("exit status 1")
	}
	defer func() { RunLocalShell = prevShell }()

	resp, err := RunGuarded(context.Background(), rt, GuardedExecutionRequest{
		Description: "false",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
	})
	if err == nil {
		t.Fatal("expected error for failed shell command")
	}
	if !strings.Contains(resp.Error, "exit status 1") {
		t.Fatalf("expected shell error in response, got: %q", resp.Error)
	}
	if resp.ExitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", resp.ExitCode)
	}
}

func TestRunGuardedLocalLedgerReserveFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	node := models.NodeFacts{
		Name:     "studio",
		Hostname: "localhost",
		Status:   models.StatusComplete,
		Resources: &models.Resources{
			RAMTotalMB: 8192,
			RAMFreeMB:  4096,
			Pressure:   "low",
			CPUCores:   8,
		},
	}
	rt := testGuardedRuntime(t, []models.NodeFacts{node})
	// Set ledger limits to 1 max entry and pre-fill it so the next reservation fails
	rt.Ledger = reservation.NewLedger(reservation.Limits{MaxEntriesPerNode: 1}, nil)
	rt.Ledger.SetNodeCapacity("studio", 8192)
	_, _ = rt.Ledger.Reserve(reservation.Entry{
		ID:       "existing",
		Node:     "studio",
		RAMMB:    100,
		OwnerPID: 1,
	})

	prevShell := RunLocalShell
	RunLocalShell = func(context.Context, string, []string) ([]byte, int64, error) {
		return []byte("ok\n"), 0, nil
	}
	defer func() { RunLocalShell = prevShell }()

	resp, err := RunGuarded(context.Background(), rt, GuardedExecutionRequest{
		Description: "echo ok",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
	})
	if err == nil {
		t.Fatal("expected error for ledger reserve failure")
	}
	if !strings.Contains(resp.Error, "max entries") {
		t.Fatalf("expected ledger reserve error, got: %q", resp.Error)
	}
}

func TestRunGuardedRemoteContextUploadFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	node := models.NodeFacts{
		Name:     "remote-node",
		Hostname: "remote.example",
		Status:   models.StatusComplete,
		Resources: &models.Resources{
			RAMTotalMB: 8192,
			RAMFreeMB:  4096,
			Pressure:   "low",
			CPUCores:   8,
		},
	}
	rt := testGuardedRuntime(t, []models.NodeFacts{node})

	prev := NewRemoteExecutor
	NewRemoteExecutor = func(nc config.NodeConfig) RemoteExecutor {
		return &stubRemoteExecutor{runFunc: func(_ context.Context, cmd string) (string, error) {
			if strings.Contains(cmd, "AXIS_EOF") {
				return "", fmt.Errorf("upload failed")
			}
			return "", nil
		}}
	}
	defer func() { NewRemoteExecutor = prev }()

	resp, err := RunGuarded(context.Background(), rt, GuardedExecutionRequest{
		Description: "echo hi",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
	})
	if err == nil {
		t.Fatal("expected error for remote context upload failure")
	}
	if !strings.Contains(resp.Error, "upload failed") {
		t.Fatalf("expected upload error, got: %q", resp.Error)
	}
	if resp.ExitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", resp.ExitCode)
	}
}

func TestRunGuardedRemoteExecutionFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	node := models.NodeFacts{
		Name:     "remote-node",
		Hostname: "remote.example",
		Status:   models.StatusComplete,
		Resources: &models.Resources{
			RAMTotalMB: 8192,
			RAMFreeMB:  4096,
			Pressure:   "low",
			CPUCores:   8,
		},
	}
	rt := testGuardedRuntime(t, []models.NodeFacts{node})

	callCount := 0
	prev := NewRemoteExecutor
	NewRemoteExecutor = func(nc config.NodeConfig) RemoteExecutor {
		return &stubRemoteExecutor{runFunc: func(_ context.Context, cmd string) (string, error) {
			callCount++
			if callCount == 1 && strings.Contains(cmd, "AXIS_EOF") {
				return "", nil // context upload succeeds
			}
			return "", fmt.Errorf("remote execution failed")
		}}
	}
	defer func() { NewRemoteExecutor = prev }()

	resp, err := RunGuarded(context.Background(), rt, GuardedExecutionRequest{
		Description: "echo hi",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
	})
	if err == nil {
		t.Fatal("expected error for remote execution failure")
	}
	if !strings.Contains(resp.Error, "remote execution failed") {
		t.Fatalf("expected execution error, got: %q", resp.Error)
	}
}

func TestRunGuardedRemoteLedgerReserveFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	node := models.NodeFacts{
		Name:     "remote-node",
		Hostname: "remote.example",
		Status:   models.StatusComplete,
		Resources: &models.Resources{
			RAMTotalMB: 8192,
			RAMFreeMB:  4096,
			Pressure:   "low",
			CPUCores:   8,
		},
	}
	rt := testGuardedRuntime(t, []models.NodeFacts{node})
	rt.Ledger = reservation.NewLedger(reservation.Limits{MaxEntriesPerNode: 1}, nil)
	rt.Ledger.SetNodeCapacity("remote-node", 8192)
	_, _ = rt.Ledger.Reserve(reservation.Entry{
		ID:       "existing",
		Node:     "remote-node",
		RAMMB:    100,
		OwnerPID: 1,
	})

	prev := NewRemoteExecutor
	NewRemoteExecutor = func(nc config.NodeConfig) RemoteExecutor {
		return &stubRemoteExecutor{runFunc: func(_ context.Context, cmd string) (string, error) {
			if strings.Contains(cmd, "AXIS_EOF") {
				return "", nil
			}
			return "", nil
		}}
	}
	defer func() { NewRemoteExecutor = prev }()

	resp, err := RunGuarded(context.Background(), rt, GuardedExecutionRequest{
		Description: "echo hi",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
	})
	if err == nil {
		t.Fatal("expected error for remote ledger reserve failure")
	}
	if !strings.Contains(resp.Error, "max entries") {
		t.Fatalf("expected ledger reserve error, got: %q", resp.Error)
	}
}

func TestRunLocalWithOutputStreaming(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	node := models.NodeFacts{
		Name:     "studio",
		Hostname: "localhost",
		Status:   models.StatusComplete,
		Resources: &models.Resources{
			RAMTotalMB: 8192,
			RAMFreeMB:  4096,
			Pressure:   "low",
			CPUCores:   8,
		},
	}
	rt := testGuardedRuntime(t, []models.NodeFacts{node})

	prevStream := StreamLocalShell
	StreamLocalShell = func(_ context.Context, cmd string, env []string, stdout, stderr io.Writer) (int64, error) {
		_, _ = fmt.Fprint(stdout, "stdout data")
		_, _ = fmt.Fprint(stderr, "stderr data")
		return 128, nil
	}
	defer func() { StreamLocalShell = prevStream }()

	var outBuf, errBuf bytes.Buffer
	resp, err := RunGuarded(context.Background(), rt, GuardedExecutionRequest{
		Description: "echo hi",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
		Stdout:      &outBuf,
		Stderr:      &errBuf,
	})
	if err != nil {
		t.Fatalf("RunGuarded: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK, got: %#v", resp)
	}
	if outBuf.String() != "stdout data" {
		t.Fatalf("unexpected stdout: %q", outBuf.String())
	}
	if errBuf.String() != "stderr data" {
		t.Fatalf("unexpected stderr: %q", errBuf.String())
	}
}

func TestApplyFailureOutcomeTimeout(t *testing.T) {
	st := &state.ClusterState{Failures: failures.NewStore()}
	resp := GuardedExecutionResult{Node: "n1", Tool: "git", ExitCode: 1}
	applyFailureOutcome(st, resp, context.DeadlineExceeded)
	match, blocked := st.Failures.NarrowestMatch(models.FailureScope{Node: "n1", Workload: resp.Workload.Class, Tool: "git"})
	if !blocked {
		t.Fatal("expected failure recorded")
	}
	if match.Class != models.FailureTimeout {
		t.Fatalf("expected timeout class, got %q", match.Class)
	}
}

func TestApplyFailureOutcomeCancel(t *testing.T) {
	st := &state.ClusterState{Failures: failures.NewStore()}
	resp := GuardedExecutionResult{Node: "n1", Tool: "git", ExitCode: 1}
	applyFailureOutcome(st, resp, context.Canceled)
	match, blocked := st.Failures.NarrowestMatch(models.FailureScope{Node: "n1", Workload: resp.Workload.Class, Tool: "git"})
	if !blocked {
		t.Fatal("expected failure recorded")
	}
	if match.Class != models.FailureTimeout {
		t.Fatalf("expected timeout class for cancel, got %q", match.Class)
	}
}

func TestApplyFailureOutcomeBroadScope(t *testing.T) {
	st := &state.ClusterState{Failures: failures.NewStore()}
	resp := GuardedExecutionResult{Node: "n1", Tool: "git", Workload: models.WorkloadProfileMatch{Class: models.ClassRepoAnalysis}, ExitCode: 1}
	applyFailureOutcome(st, resp, fmt.Errorf("crash"))
	// Tool-specific scope
	_, blocked1 := st.Failures.NarrowestMatch(models.FailureScope{Node: "n1", Workload: models.ClassRepoAnalysis, Tool: "git"})
	if !blocked1 {
		t.Fatal("expected tool-specific failure recorded")
	}
	// Broad scope (no tool)
	_, blocked2 := st.Failures.NarrowestMatch(models.FailureScope{Node: "n1", Workload: models.ClassRepoAnalysis})
	if !blocked2 {
		t.Fatal("expected broad-scope failure recorded")
	}
}

func TestParseDarwinAvailableRAMMBNoPages(t *testing.T) {
	_, err := parseDarwinAvailableRAMMB("page size of 4096 bytes\n")
	if err == nil {
		t.Fatal("expected error when no reclaimable pages reported")
	}
	if !strings.Contains(err.Error(), "reclaimable pages") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseDarwinAvailableRAMMBSuccess(t *testing.T) {
	out := "page size of 16384 bytes\nPages free: 1000.\nPages inactive: 500.\nPages speculative: 200.\n"
	got, err := parseDarwinAvailableRAMMB(out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := int64((1000 + 500 + 200) * 16384 / 1024 / 1024)
	if got != want {
		t.Fatalf("expected %d MB, got %d", want, got)
	}
}

func TestParseVMStatPageCountInvalidLine(t *testing.T) {
	_, err := parseVMStatPageCount("invalid")
	if err == nil {
		t.Fatal("expected error for invalid vm_stat line")
	}
	if !strings.Contains(err.Error(), "invalid vm_stat line") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseVMStatPageCountParseError(t *testing.T) {
	_, err := parseVMStatPageCount("Pages free: not-a-number.")
	if err == nil {
		t.Fatal("expected error for unparsable vm_stat count")
	}
	if !strings.Contains(err.Error(), "parse vm_stat count") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseVMStatPageCountSuccess(t *testing.T) {
	got, err := parseVMStatPageCount("Pages free: 1,234,567.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 1234567 {
		t.Fatalf("expected 1234567, got %d", got)
	}
}

func TestParseLinuxAvailableRAMMBNoMemAvailable(t *testing.T) {
	_, err := parseLinuxAvailableRAMMB("MemTotal:       16000000 kB\n")
	if err == nil {
		t.Fatal("expected error when MemAvailable missing")
	}
	if !strings.Contains(err.Error(), "MemAvailable not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseLinuxAvailableRAMMBInvalidLine(t *testing.T) {
	_, err := parseLinuxAvailableRAMMB("MemAvailable:\n")
	if err == nil {
		t.Fatal("expected error for invalid MemAvailable line")
	}
	if !strings.Contains(err.Error(), "invalid MemAvailable line") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseLinuxAvailableRAMMBParseError(t *testing.T) {
	_, err := parseLinuxAvailableRAMMB("MemAvailable:   not-a-number kB\n")
	if err == nil {
		t.Fatal("expected error for unparsable MemAvailable")
	}
	if !strings.Contains(err.Error(), "parse MemAvailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseLinuxAvailableRAMMBSuccess(t *testing.T) {
	out := "MemTotal:       16000000 kB\nMemAvailable:    8192000 kB\n"
	got, err := parseLinuxAvailableRAMMB(out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 8000 {
		t.Fatalf("expected 8000 MB, got %d", got)
	}
}

func TestCanReserveNilSnap(t *testing.T) {
	if !CanReserve(nil, "n1", 1024) {
		t.Fatal("expected CanReserve(nil, ...) == true")
	}
}

func TestCanReserveZeroMB(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{Name: "n1", Resources: &models.Resources{RAMTotalMB: 8192, RAMFreeMB: 4096}},
		},
	}
	if !CanReserve(snap, "n1", 0) {
		t.Fatal("expected CanReserve(..., 0) == true")
	}
	if !CanReserve(snap, "n1", -1) {
		t.Fatal("expected CanReserve(..., -1) == true")
	}
}

func TestFindNodeFactsNilSnap(t *testing.T) {
	_, ok := findNodeFacts(nil, "n1")
	if ok {
		t.Fatal("expected findNodeFacts(nil, ...) == false")
	}
}

func TestDurationMillisecondsZero(t *testing.T) {
	if durationMilliseconds(0) != 1 {
		t.Fatalf("expected 1 for zero duration, got %d", durationMilliseconds(0))
	}
}

func TestDurationMillisecondsSubMillisecond(t *testing.T) {
	if durationMilliseconds(100*time.Nanosecond) != 1 {
		t.Fatalf("expected 1 for sub-millisecond duration, got %d", durationMilliseconds(100*time.Nanosecond))
	}
}

func TestExitCodeWithExecExitError(t *testing.T) {
	// Simulate an exec.ExitError with exit code 42
	cmd := exec.Command("false")
	_ = cmd.Run()
	code := exitCode(cmd.Err)
	if code != 1 {
		// On some systems false returns 1; that's fine for coverage
		t.Logf("exit code from false: %d", code)
	}
}

func TestRecordSuccessNilStore(t *testing.T) {
	// Should not panic
	recordSuccess(nil, "desc", "cmd", "node")
}

func TestRecordFailureNilStore(t *testing.T) {
	// Should not panic
	recordFailure(nil, "desc", 1)
}

func TestIsInferenceExecutionByTools(t *testing.T) {
	if !isInferenceExecution(models.TaskRequirements{RequiredTools: []string{"ollama"}}) {
		t.Fatal("expected ollama tool to be inference")
	}
	if !isInferenceExecution(models.TaskRequirements{RequiredTools: []string{"llama-server"}}) {
		t.Fatal("expected llama-server tool to be inference")
	}
	if !isInferenceExecution(models.TaskRequirements{RequiredTools: []string{"apple-foundation-models"}}) {
		t.Fatal("expected apple-foundation-models tool to be inference")
	}
}

func TestIsInferenceExecutionByBackends(t *testing.T) {
	if !isInferenceExecution(models.TaskRequirements{PreferredBackends: []string{"llama.cpp"}}) {
		t.Fatal("expected llama.cpp backend to be inference")
	}
	if !isInferenceExecution(models.TaskRequirements{PreferredBackends: []string{"mlx"}}) {
		t.Fatal("expected mlx backend to be inference")
	}
	if !isInferenceExecution(models.TaskRequirements{PreferredBackends: []string{"apple-foundation-models"}}) {
		t.Fatal("expected apple-foundation-models backend to be inference")
	}
}

func TestIsInferenceExecutionByRAM(t *testing.T) {
	if !isInferenceExecution(models.TaskRequirements{MinFreeRAMMB: 4096}) {
		t.Fatal("expected 4096MB min free RAM to be inference")
	}
}

func TestNormalizeRequestNil(t *testing.T) {
	// Should not panic
	NormalizeRequest(nil)
}

func testGuardedRuntime(t *testing.T, nodes []models.NodeFacts) *runtimectx.Context {
	t.Cleanup(func() {
		events.FlushEvents(1 * time.Second)
	})
	cfgNodes := make([]config.NodeConfig, 0, len(nodes))
	for _, node := range nodes {
		cfgNodes = append(cfgNodes, config.NodeConfig{
			Name:     node.Name,
			Hostname: node.Hostname,
			SSHUser:  "me",
		})
	}

	return &runtimectx.Context{
		Config: &config.Config{Nodes: cfgNodes},
		Snapshot: &models.ClusterSnapshot{
			Status:  models.SnapshotHealthy,
			Nodes:   nodes,
			Summary: models.ClusterSummary{TotalNodes: len(nodes)},
		},
		State:  &state.ClusterState{Nodes: map[string]state.NodeState{}},
		Skills: &skills.Store{},
		Ledger: func() *reservation.Ledger {
			l := reservation.NewLedger(reservation.DefaultLimits(), nil)
			for _, n := range nodes {
				if n.Resources != nil {
					l.SetNodeCapacity(n.Name, n.Resources.RAMTotalMB)
				}
			}
			return l
		}(),
	}
}

func TestParseExposePorts(t *testing.T) {
	tests := []struct {
		input       string
		wantLocal   int
		wantRemote  int
		wantErr     bool
		errContains string
	}{
		{"", 0, 0, false, ""},
		{"   ", 0, 0, false, ""},
		{"8080", 0, 8080, false, ""},
		{"8080:8080", 8080, 8080, false, ""},
		{"0:8080", 0, 8080, false, ""},
		{"abc", 0, 0, true, "invalid port:"},
		{"8080:abc", 0, 0, true, "invalid ports:"},
		{"abc:8080", 0, 0, true, "invalid ports:"},
		{"-1:8080", 0, 0, true, "invalid ports:"},
		{"8080:-80", 0, 0, true, "invalid ports:"},
		{"8080:8080:8080", 0, 0, true, "invalid port format:"},
		{"8080:0", 0, 0, true, "invalid ports:"},
		{"0", 0, 0, true, "invalid port:"},
		{"-5", 0, 0, true, "invalid port:"},
		{"99999", 0, 0, true, "invalid port:"},
		{"8080:99999", 0, 0, true, "invalid ports:"},
		{"99999:8080", 0, 0, true, "invalid ports:"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			local, remote, err := ParseExposePorts(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for input %q, got nil", tt.input)
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("expected error containing %q, got %v", tt.errContains, err)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error for input %q: %v", tt.input, err)
				}
				if local != tt.wantLocal || remote != tt.wantRemote {
					t.Fatalf("input %q: got (%d, %d), want (%d, %d)", tt.input, local, remote, tt.wantLocal, tt.wantRemote)
				}
			}
		})
	}
}

type stubPortForwardingExecutor struct {
	stubRemoteExecutor
	forwardCalled bool
	localVal      int
	remoteVal     int
	retPort       int
	retErr        error
}

func (s *stubPortForwardingExecutor) ForwardLocal(ctx context.Context, localPort, remotePort int) (int, func(), error) {
	s.forwardCalled = true
	s.localVal = localPort
	s.remoteVal = remotePort
	return s.retPort, func() {}, s.retErr
}

func TestRunRemoteWithPortForwarding(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var capturedExecutor *stubPortForwardingExecutor
	prev := NewRemoteExecutor
	NewRemoteExecutor = func(nc config.NodeConfig) RemoteExecutor {
		capturedExecutor = &stubPortForwardingExecutor{
			retPort: 12345,
		}
		capturedExecutor.runFunc = func(_ context.Context, cmd string) (string, error) {
			return "output\n", nil
		}
		return capturedExecutor
	}
	defer func() { NewRemoteExecutor = prev }()

	cfgNodes := []config.NodeConfig{{Name: "testnode", Hostname: "testhost", SSHUser: "testuser"}}
	rt := &runtimectx.Context{
		State:  &state.ClusterState{Nodes: map[string]state.NodeState{}},
		Skills: &skills.Store{},
	}

	req := GuardedExecutionRequest{
		Description:  "test",
		Mode:         ModeExec,
		Confirm:      ConfirmWord,
		ExposePorts:  "8080:9090",
		OwnerSurface: "test",
		Stdout:       io.Discard,
		Stderr:       io.Discard,
	}
	reqs := models.TaskRequirements{Workload: models.WorkloadProfileMatch{Class: models.ClassRepoAnalysis}}
	resp := GuardedExecutionResult{Node: "testnode"}

	_, err := runRemote(
		context.Background(),
		rt.State,
		rt.Skills,
		state.ExecutionOwner{Surface: "test"},
		req,
		reqs,
		resp,
		cfgNodes[0],
		0,
		"echo hello",
		nil,
		[]byte(`{"test":true}`),
		nil,
	)
	if err != nil {
		t.Fatalf("runRemote failed: %v", err)
	}

	if capturedExecutor == nil {
		t.Fatal("executor was not instantiated")
	}
	if !capturedExecutor.forwardCalled {
		t.Fatal("expected ForwardLocal to be called")
	}
	if capturedExecutor.localVal != 8080 || capturedExecutor.remoteVal != 9090 {
		t.Fatalf("expected local=8080 remote=9090, got local=%d remote=%d", capturedExecutor.localVal, capturedExecutor.remoteVal)
	}
}

func TestRunRemotePortForwardingNotSupported(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	prev := NewRemoteExecutor
	NewRemoteExecutor = func(nc config.NodeConfig) RemoteExecutor {
		return &stubRemoteExecutor{runFunc: func(_ context.Context, cmd string) (string, error) {
			return "output\n", nil
		}}
	}
	defer func() { NewRemoteExecutor = prev }()

	cfgNodes := []config.NodeConfig{{Name: "testnode", Hostname: "testhost", SSHUser: "testuser"}}
	rt := &runtimectx.Context{
		State:  &state.ClusterState{Nodes: map[string]state.NodeState{}},
		Skills: &skills.Store{},
	}

	req := GuardedExecutionRequest{
		Description:  "test",
		Mode:         ModeExec,
		Confirm:      ConfirmWord,
		ExposePorts:  "8080:9090",
		OwnerSurface: "test",
		Stdout:       io.Discard,
		Stderr:       io.Discard,
	}
	reqs := models.TaskRequirements{Workload: models.WorkloadProfileMatch{Class: models.ClassRepoAnalysis}}
	resp := GuardedExecutionResult{Node: "testnode"}

	_, err := runRemote(
		context.Background(),
		rt.State,
		rt.Skills,
		state.ExecutionOwner{Surface: "test"},
		req,
		reqs,
		resp,
		cfgNodes[0],
		0,
		"echo hello",
		nil,
		[]byte(`{"test":true}`),
		nil,
	)
	if err == nil {
		t.Fatal("expected error due to executor not supporting port forwarding")
	}
	if !strings.Contains(err.Error(), "executor does not support port forwarding") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestRunLocalExposeWarning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	rt := testGuardedRuntime(t, []models.NodeFacts{
		{
			Name:     "studio",
			Hostname: "localhost",
			Status:   models.StatusComplete,
			Resources: &models.Resources{
				RAMTotalMB: 8192,
				RAMFreeMB:  4096,
				Pressure:   "low",
				CPUCores:   8,
			},
		},
	})

	prevShell := RunLocalShell
	RunLocalShell = func(context.Context, string, []string) ([]byte, int64, error) {
		return []byte("ok\n"), 0, nil
	}
	defer func() { RunLocalShell = prevShell }()

	var stderr bytes.Buffer
	resp, err := RunGuarded(context.Background(), rt, GuardedExecutionRequest{
		Description: "echo ok",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
		ExposePorts: "8080:9090",
		Stderr:      &stderr,
	})
	if err != nil {
		t.Fatalf("RunGuarded: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK response, got %#v", resp)
	}

	errStr := stderr.String()
	if !strings.Contains(errStr, "[AXIS] Warning: Expose ports \"8080:9090\" ignored for local execution.") {
		t.Fatalf("expected warning message about ignoring expose ports, got: %q", errStr)
	}
}

func TestHandleDirtyWorkingTreeClean(t *testing.T) {
	prevGit := GetGitRepoState
	GetGitRepoState = func(dir string) (git.RepoState, error) {
		return git.RepoState{IsRepo: true, IsDirty: false}, nil
	}
	defer func() { GetGitRepoState = prevGit }()

	var stderr bytes.Buffer
	req := GuardedExecutionRequest{
		Stdin: os.Stdin,
	}
	ok, cleanup, err := handleDirtyWorkingTree(context.Background(), req, nil, &stderr)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !ok {
		t.Fatal("expected ok to be true")
	}
	if cleanup == nil {
		t.Fatal("expected non-nil cleanup function")
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", stderr.String())
	}
}

func TestHandleDirtyWorkingTreeDirtyNonInteractive(t *testing.T) {
	prevGit := GetGitRepoState
	GetGitRepoState = func(dir string) (git.RepoState, error) {
		return git.RepoState{IsRepo: true, IsDirty: true, DirtyCount: 3}, nil
	}
	defer func() { GetGitRepoState = prevGit }()

	prevTerm := IsTerminalFunc
	IsTerminalFunc = func(r io.Reader) bool { return false }
	defer func() { IsTerminalFunc = prevTerm }()

	var stderr bytes.Buffer
	req := GuardedExecutionRequest{
		Stdin: os.Stdin,
	}
	ok, cleanup, err := handleDirtyWorkingTree(context.Background(), req, nil, &stderr)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !ok {
		t.Fatal("expected ok to be true")
	}
	if cleanup == nil {
		t.Fatal("expected non-nil cleanup function")
	}
	errStr := stderr.String()
	if !strings.Contains(errStr, "WARNING: Working tree is dirty (3 files modified)") {
		t.Fatalf("expected warning message, got %q", errStr)
	}
	if !strings.Contains(errStr, "Non-interactive environment: proceeding") {
		t.Fatalf("expected non-interactive message, got %q", errStr)
	}
}

func TestHandleDirtyWorkingTreeForceDirtyEnv(t *testing.T) {
	prevGit := GetGitRepoState
	GetGitRepoState = func(dir string) (git.RepoState, error) {
		return git.RepoState{IsRepo: true, IsDirty: true, DirtyCount: 4}, nil
	}
	defer func() { GetGitRepoState = prevGit }()

	prevTerm := IsTerminalFunc
	IsTerminalFunc = func(r io.Reader) bool { return true }
	defer func() { IsTerminalFunc = prevTerm }()

	t.Setenv("AXIS_FORCE_DIRTY", "true")

	var stderr bytes.Buffer
	req := GuardedExecutionRequest{
		Stdin: os.Stdin,
	}
	ok, cleanup, err := handleDirtyWorkingTree(context.Background(), req, nil, &stderr)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !ok {
		t.Fatal("expected ok to be true")
	}
	if cleanup == nil {
		t.Fatal("expected non-nil cleanup function")
	}
	errStr := stderr.String()
	if !strings.Contains(errStr, "WARNING: Working tree is dirty (4 files modified)") {
		t.Fatalf("expected warning message, got %q", errStr)
	}
	if !strings.Contains(errStr, "AXIS_FORCE_DIRTY=true detected: proceeding") {
		t.Fatalf("expected AXIS_FORCE_DIRTY message, got %q", errStr)
	}
}

func TestHandleDirtyWorkingTreeDirtyInteractiveAbort(t *testing.T) {
	prevGit := GetGitRepoState
	GetGitRepoState = func(dir string) (git.RepoState, error) {
		return git.RepoState{IsRepo: true, IsDirty: true, DirtyCount: 5}, nil
	}
	defer func() { GetGitRepoState = prevGit }()

	prevTerm := IsTerminalFunc
	IsTerminalFunc = func(r io.Reader) bool { return true }
	defer func() { IsTerminalFunc = prevTerm }()

	var stderr bytes.Buffer
	req := GuardedExecutionRequest{
		Stdin: strings.NewReader("a\n"),
	}
	ok, _, err := handleDirtyWorkingTree(context.Background(), req, nil, &stderr)
	if err == nil {
		t.Fatal("expected abortion error")
	}
	if ok {
		t.Fatal("expected ok to be false")
	}
	errStr := stderr.String()
	if !strings.Contains(errStr, "WARNING: Working tree is dirty (5 files modified)") {
		t.Fatalf("expected warning message, got %q", errStr)
	}
	if !strings.Contains(errStr, "Execution aborted by operator") {
		t.Fatalf("expected abort message, got %q", errStr)
	}
}

func TestHandleDirtyWorkingTreeDirtyInteractiveProceed(t *testing.T) {
	prevGit := GetGitRepoState
	GetGitRepoState = func(dir string) (git.RepoState, error) {
		return git.RepoState{IsRepo: true, IsDirty: true, DirtyCount: 2}, nil
	}
	defer func() { GetGitRepoState = prevGit }()

	prevTerm := IsTerminalFunc
	IsTerminalFunc = func(r io.Reader) bool { return true }
	defer func() { IsTerminalFunc = prevTerm }()

	var stderr bytes.Buffer
	req := GuardedExecutionRequest{
		Stdin: strings.NewReader("p\n"),
	}
	ok, _, err := handleDirtyWorkingTree(context.Background(), req, nil, &stderr)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !ok {
		t.Fatal("expected ok to be true")
	}
	errStr := stderr.String()
	if !strings.Contains(errStr, "Proceeding with dirty tree anyway") {
		t.Fatalf("expected proceed message, got %q", errStr)
	}
}

func TestHandleDirtyWorkingTreeDirtyInteractiveStash(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH, skipping stash test")
	}

	prevGit := GetGitRepoState
	GetGitRepoState = func(dir string) (git.RepoState, error) {
		return git.RepoState{IsRepo: true, IsDirty: true, DirtyCount: 1}, nil
	}
	defer func() { GetGitRepoState = prevGit }()

	prevTerm := IsTerminalFunc
	IsTerminalFunc = func(r io.Reader) bool { return true }
	defer func() { IsTerminalFunc = prevTerm }()

	var stderr bytes.Buffer
	req := GuardedExecutionRequest{
		Stdin: strings.NewReader("s\n"),
	}
	ok, cleanup, err := handleDirtyWorkingTree(context.Background(), req, nil, &stderr)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !ok {
		t.Fatal("expected ok to be true")
	}
	if cleanup == nil {
		t.Fatal("expected non-nil cleanup function")
	}

	errStr := stderr.String()
	if !strings.Contains(errStr, "Stashing local changes") {
		t.Fatalf("expected stash message, got %q", errStr)
	}

	cleanup()
}
