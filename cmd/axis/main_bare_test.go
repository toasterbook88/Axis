package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// stubBare swaps the TTY seam and the agent launcher, returning a pointer to
// the launch count. Both are restored at test end.
func stubBare(t *testing.T, interactive bool) *int {
	t.Helper()
	prevTTY, prevRun := isInteractiveFn, runBareAxis
	calls := 0
	isInteractiveFn = func() bool { return interactive }
	runBareAxis = func(*cobra.Command) error { calls++; return nil }
	t.Cleanup(func() { isInteractiveFn, runBareAxis = prevTTY, prevRun })
	return &calls
}

func execRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestBareAxis_TTYRunsAgent(t *testing.T) {
	calls := stubBare(t, true)
	if _, err := execRoot(t); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("runBareAxis calls = %d, want 1", *calls)
	}
}

func TestBareAxis_NonTTYPrintsHelp(t *testing.T) {
	calls := stubBare(t, false)
	out, err := execRoot(t)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if *calls != 0 {
		t.Fatalf("runBareAxis calls = %d, want 0", *calls)
	}
	if !strings.Contains(out, "USAGE") && !strings.Contains(out, "Usage") {
		t.Fatalf("expected help output, got %q", out)
	}
}

func TestBareAxis_ArgsNeverRunAgent(t *testing.T) {
	calls := stubBare(t, true)
	if _, err := execRoot(t, "version"); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if *calls != 0 {
		t.Fatalf("runBareAxis calls = %d, want 0", *calls)
	}
}

func TestBareAxis_RunnerErrorPropagates(t *testing.T) {
	stubBare(t, true)
	runBareAxis = func(*cobra.Command) error { return context.Canceled }
	if _, err := execRoot(t); err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestTUICommandStillRegistered(t *testing.T) {
	root := newRootCmd()
	c, _, err := root.Find([]string{"tui"})
	if err != nil || c == nil || c.Name() != "tui" {
		t.Fatalf("tui not registered: %v %v", c, err)
	}
	if c.RunE == nil {
		t.Fatal("tui has no RunE")
	}
}

// The real launcher must resolve the agent subcommand and invoke its RunE
// with a non-nil context and no args, without starting a session.
func TestRunBareAxis_InvokesAgentRunE(t *testing.T) {
	root := newRootCmd()
	agent, _, err := root.Find([]string{"agent"})
	if err != nil || agent == nil || agent.Name() != "agent" {
		t.Fatalf("agent not found: %v", err)
	}
	var gotCtx context.Context
	var gotArgs []string
	calls := 0
	agent.RunE = func(c *cobra.Command, args []string) error {
		calls++
		gotCtx, gotArgs = c.Context(), args
		return context.DeadlineExceeded
	}
	if err := runBareAxis(root); err != context.DeadlineExceeded {
		t.Fatalf("err = %v, want propagated agent error", err)
	}
	if calls != 1 || gotCtx == nil || len(gotArgs) != 0 {
		t.Fatalf("calls=%d ctx=%v args=%v", calls, gotCtx, gotArgs)
	}
}
