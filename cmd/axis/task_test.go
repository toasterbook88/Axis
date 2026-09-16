package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

func TestTaskRunCmdAddsOwnerProvenanceToGuardedRequest(t *testing.T) {
	prevLoad := loadTaskRunRuntime
	prevPrepare := prepareTaskGuarded
	prevRunPrepared := runPreparedTaskGuarded
	t.Cleanup(func() {
		loadTaskRunRuntime = prevLoad
		prepareTaskGuarded = prevPrepare
		runPreparedTaskGuarded = prevRunPrepared
	})

	loadTaskRunRuntime = func(context.Context) (*runtimectx.Context, error) {
		return &runtimectx.Context{}, nil
	}
	prepareTaskGuarded = func(_ context.Context, _ *runtimectx.Context, req execution.GuardedExecutionRequest) (execution.PreparedExecution, error) {
		if req.OwnerSurface != execution.OwnerSurfaceTaskRun {
			t.Fatalf("OwnerSurface = %q, want %q", req.OwnerSurface, execution.OwnerSurfaceTaskRun)
		}
		return execution.PreparedExecution{
			Request: req,
			Result: execution.GuardedExecutionResult{
				Node: "alpha",
			},
		}, nil
	}
	runPreparedTaskGuarded = func(context.Context, execution.PreparedExecution) (execution.GuardedExecutionResult, error) {
		return execution.GuardedExecutionResult{OK: true, Node: "alpha"}, nil
	}

	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := taskRunCmd()
		cmd.SetArgs([]string{"--exec", "echo hello"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("task run Execute: %v", err)
	}
	if stdout != "" {
		t.Fatalf("expected no stdout, got %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
}

func TestTaskRunCmdTTYConfirmationProceedRunsPreparedExecution(t *testing.T) {
	prevLoad := loadTaskRunRuntime
	prevPrepare := prepareTaskGuarded
	prevRunPrepared := runPreparedTaskGuarded
	prevInTTY := taskRunStdinIsTerminal
	prevOutTTY := taskRunStdoutIsTerminal
	prevErrTTY := taskRunStderrIsTerminal
	t.Cleanup(func() {
		loadTaskRunRuntime = prevLoad
		prepareTaskGuarded = prevPrepare
		runPreparedTaskGuarded = prevRunPrepared
		taskRunStdinIsTerminal = prevInTTY
		taskRunStdoutIsTerminal = prevOutTTY
		taskRunStderrIsTerminal = prevErrTTY
	})

	loadTaskRunRuntime = func(context.Context) (*runtimectx.Context, error) {
		return &runtimectx.Context{}, nil
	}
	taskRunStdinIsTerminal = func() bool { return true }
	taskRunStdoutIsTerminal = func() bool { return true }
	taskRunStderrIsTerminal = func() bool { return true }

	prepareTaskGuarded = func(_ context.Context, _ *runtimectx.Context, req execution.GuardedExecutionRequest) (execution.PreparedExecution, error) {
		return execution.PreparedExecution{
			Request: req,
			Requirements: models.TaskRequirements{
				Workload: models.WorkloadProfileMatch{Class: models.ClassGoBuild},
			},
			Result: execution.GuardedExecutionResult{
				Node:     "alpha",
				FitScore: 88,
				IsLocal:  true,
			},
			ReservationMB: 3072,
			Command:       "echo hello",
		}, nil
	}

	var ran bool
	runPreparedTaskGuarded = func(_ context.Context, prepared execution.PreparedExecution) (execution.GuardedExecutionResult, error) {
		ran = true
		if prepared.Command != "echo hello" {
			t.Fatalf("prepared command = %q, want echo hello", prepared.Command)
		}
		return execution.GuardedExecutionResult{OK: true, Node: "alpha"}, nil
	}

	cmd := taskRunCmd()
	cmd.SetArgs([]string{"--exec", "echo hello"})
	cmd.SetIn(strings.NewReader("yes\n"))
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("task run Execute: %v", err)
	}
	if !ran {
		t.Fatal("expected prepared execution to run after confirmation")
	}
	if got := stderr.String(); !strings.Contains(got, "Proceed? [y/N]:") || !strings.Contains(got, "Reservation headroom: 3072MB") {
		t.Fatalf("expected confirmation summary, got %q", got)
	}
}

func TestTaskRunCmdTTYConfirmationDeclineAbortsWithoutExecuting(t *testing.T) {
	prevLoad := loadTaskRunRuntime
	prevPrepare := prepareTaskGuarded
	prevRunPrepared := runPreparedTaskGuarded
	prevInTTY := taskRunStdinIsTerminal
	prevOutTTY := taskRunStdoutIsTerminal
	prevErrTTY := taskRunStderrIsTerminal
	t.Cleanup(func() {
		loadTaskRunRuntime = prevLoad
		prepareTaskGuarded = prevPrepare
		runPreparedTaskGuarded = prevRunPrepared
		taskRunStdinIsTerminal = prevInTTY
		taskRunStdoutIsTerminal = prevOutTTY
		taskRunStderrIsTerminal = prevErrTTY
	})

	loadTaskRunRuntime = func(context.Context) (*runtimectx.Context, error) {
		return &runtimectx.Context{}, nil
	}
	taskRunStdinIsTerminal = func() bool { return true }
	taskRunStdoutIsTerminal = func() bool { return true }
	taskRunStderrIsTerminal = func() bool { return true }

	prepareTaskGuarded = func(_ context.Context, _ *runtimectx.Context, req execution.GuardedExecutionRequest) (execution.PreparedExecution, error) {
		return execution.PreparedExecution{
			Request: req,
			Requirements: models.TaskRequirements{
				Workload: models.WorkloadProfileMatch{Class: models.ClassGoBuild},
			},
			Result: execution.GuardedExecutionResult{
				Node:     "alpha",
				FitScore: 88,
			},
			ReservationMB: 2048,
			Command:       "echo hello",
		}, nil
	}

	runPreparedTaskGuarded = func(context.Context, execution.PreparedExecution) (execution.GuardedExecutionResult, error) {
		t.Fatal("did not expect execution after operator declined")
		return execution.GuardedExecutionResult{}, nil
	}

	cmd := taskRunCmd()
	cmd.SetArgs([]string{"--exec", "echo hello"})
	cmd.SetIn(strings.NewReader("n\n"))
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("task run Execute: %v", err)
	}
	if got := stderr.String(); !strings.Contains(got, "aborted; nothing executed") {
		t.Fatalf("expected abort message, got %q", got)
	}
}

func TestTaskRunCmdNonTTYSkipsConfirmationPrompt(t *testing.T) {
	prevLoad := loadTaskRunRuntime
	prevPrepare := prepareTaskGuarded
	prevRunPrepared := runPreparedTaskGuarded
	prevInTTY := taskRunStdinIsTerminal
	prevOutTTY := taskRunStdoutIsTerminal
	prevErrTTY := taskRunStderrIsTerminal
	t.Cleanup(func() {
		loadTaskRunRuntime = prevLoad
		prepareTaskGuarded = prevPrepare
		runPreparedTaskGuarded = prevRunPrepared
		taskRunStdinIsTerminal = prevInTTY
		taskRunStdoutIsTerminal = prevOutTTY
		taskRunStderrIsTerminal = prevErrTTY
	})

	loadTaskRunRuntime = func(context.Context) (*runtimectx.Context, error) {
		return &runtimectx.Context{}, nil
	}
	taskRunStdinIsTerminal = func() bool { return false }
	taskRunStdoutIsTerminal = func() bool { return false }
	taskRunStderrIsTerminal = func() bool { return false }

	prepareTaskGuarded = func(_ context.Context, _ *runtimectx.Context, req execution.GuardedExecutionRequest) (execution.PreparedExecution, error) {
		return execution.PreparedExecution{
			Request: req,
			Result: execution.GuardedExecutionResult{
				Node: "alpha",
			},
			Command: "echo hello",
		}, nil
	}

	var ran bool
	runPreparedTaskGuarded = func(context.Context, execution.PreparedExecution) (execution.GuardedExecutionResult, error) {
		ran = true
		return execution.GuardedExecutionResult{OK: true, Node: "alpha"}, nil
	}

	cmd := taskRunCmd()
	cmd.SetArgs([]string{"--exec", "echo hello"})
	cmd.SetIn(strings.NewReader("nope\n"))
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("task run Execute: %v", err)
	}
	if !ran {
		t.Fatal("expected non-tty execution to proceed without prompt")
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("expected no confirmation prompt on non-tty, got %q", got)
	}
}
