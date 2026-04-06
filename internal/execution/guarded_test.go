package execution

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
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
	RunLocalShell = func(context.Context, string, []string) ([]byte, error) {
		called = true
		return nil, errors.New("should not run")
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
	RunLocalShell = func(context.Context, string, []string) ([]byte, error) {
		called = true
		return nil, errors.New("should not run")
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
	RunLocalShell = func(context.Context, string, []string) ([]byte, error) {
		called = true
		return nil, errors.New("should not run")
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
	RunLocalShell = func(context.Context, string, []string) ([]byte, error) {
		return []byte("ok\n"), nil
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
	heartbeatTask = func(st *state.ClusterState, node, execID string) error {
		heartbeatCalls++
		return st.HeartbeatTask(node, execID)
	}
	defer func() { heartbeatTask = prevHeartbeat }()

	prevShell := RunLocalShell
	RunLocalShell = func(context.Context, string, []string) ([]byte, error) {
		time.Sleep(35 * time.Millisecond)
		return []byte("ok\n"), nil
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
	heartbeatTask = func(_ *state.ClusterState, node, execID string) error {
		if node != "alpha" || execID != "exec-1" {
			t.Fatalf("unexpected heartbeat target node=%q execID=%q", node, execID)
		}
		heartbeatCh <- struct{}{}
		return nil
	}
	defer func() { heartbeatTask = prevHeartbeat }()

	ctx, cancel := context.WithCancel(context.Background())
	doneRun := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := runWithReservationHeartbeat(ctx, &state.ClusterState{}, "alpha", "exec-1", func() (string, error) {
			<-doneRun
			return "ok", nil
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
	RunLocalShell = func(context.Context, string, []string) ([]byte, error) {
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
		return []byte("ok\n"), nil
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
	RunLocalShell = func(context.Context, string, []string) ([]byte, error) {
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
		return []byte("ok\n"), nil
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
	}
}
