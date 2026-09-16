package execution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/events"
	"github.com/toasterbook88/axis/internal/failures"
	"github.com/toasterbook88/axis/internal/git"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/reservation"
	"github.com/toasterbook88/axis/internal/state"
)

func TestMain(m *testing.M) {
	// Stub git status to be clean by default for all execution tests
	prevGit := GetGitRepoState
	GetGitRepoState = func(dir string) (git.RepoState, error) {
		return git.RepoState{IsRepo: true, IsDirty: false}, nil
	}

	// Redirect the asynchronous event log away from per-test HOME/.axis dirs.
	// The event worker keeps files open briefly; writing into t.TempDir()
	// can race with Go's TempDir cleanup and cause flaky "directory not
	// empty" failures.
	eventLogDir, err := os.MkdirTemp("", "axis-execution-events-*")
	if err != nil {
		panic(fmt.Sprintf("failed to create temp directory: %v", err))
	}
	if err := events.ResetTestLog(filepath.Join(eventLogDir, "events.jsonl")); err != nil {
		panic(fmt.Sprintf("ResetTestLog: %v", err))
	}

	// Run tests and clean up immediately since defer does not run before os.Exit.
	code := m.Run()
	if err := events.FlushEvents(5 * time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "warning: FlushEvents after execution tests: %v\n", err)
	}
	_ = os.RemoveAll(eventLogDir)
	GetGitRepoState = prevGit
	os.Exit(code)
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
			if strings.Contains(cmd, "base64 -d") {
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
			if callCount == 1 && strings.Contains(cmd, "base64 -d") {
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
			if strings.Contains(cmd, "base64 -d") {
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

func TestParseVMStatPageCountParseError(t *testing.T) {
	_, err := parseVMStatPageCount("Pages free: not-a-number.")
	if err == nil {
		t.Fatal("expected error for unparsable vm_stat count")
	}
	if !strings.Contains(err.Error(), "parse vm_stat count") {
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

func TestRecordFailureNilStore(t *testing.T) {
	// Should not panic
	recordFailure(nil, "desc", 1)
}
