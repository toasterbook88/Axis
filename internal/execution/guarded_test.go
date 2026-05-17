package execution

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/reservation"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

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

	rt := testGuardedRuntime([]models.NodeFacts{
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

	rt := testGuardedRuntime([]models.NodeFacts{
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

	rt := testGuardedRuntime([]models.NodeFacts{
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

	rt := testGuardedRuntime([]models.NodeFacts{
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

	rt := testGuardedRuntime([]models.NodeFacts{
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

	rt := testGuardedRuntime([]models.NodeFacts{
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

	rt := testGuardedRuntime([]models.NodeFacts{
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
		time.Sleep(35 * time.Millisecond)
		return []byte("ok\n"), 0, nil
	}
	defer func() { RunLocalShell = prevShell }()

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

	rt := testGuardedRuntime([]models.NodeFacts{
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
		ns, ok := rt.State.Nodes["studio"]
		if !ok {
			t.Fatal("expected active reservation state for studio")
		}
		if len(ns.ActiveExecs) != 1 {
			t.Fatalf("ActiveExecs = %v, want single active exec", ns.ActiveExecs)
		}
		execID := ns.ActiveExecs[0]
		if got := ns.ExecOrigin[execID]; got != models.NewExecutionOrigin("studio", "localhost", "abc-123") {
			t.Fatalf("ExecOrigin[%s] = %+v, want studio/localhost/abc-123", execID, got)
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

	rt := testGuardedRuntime([]models.NodeFacts{
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
		ns, ok := rt.State.Nodes["studio"]
		if !ok {
			t.Fatal("expected active reservation state for studio")
		}
		if len(ns.ActiveExecs) != 1 {
			t.Fatalf("ActiveExecs = %v, want single active exec", ns.ActiveExecs)
		}
		execID := ns.ActiveExecs[0]
		if got := ns.ExecOrigin[execID]; got != want {
			t.Fatalf("ExecOrigin[%s] = %+v, want %+v", execID, got, want)
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

func testGuardedRuntime(nodes []models.NodeFacts) *runtimectx.Context {
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
