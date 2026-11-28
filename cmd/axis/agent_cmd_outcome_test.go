package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

// runAgentCmd runs `axis agent <args>` through agentCmd with the startup
// backend replaced, so the command's own -p wiring (stream routing, piped
// confirm and its recorder, the JSON document) is what the test exercises.
func runAgentCmd(t *testing.T, be agent.ChatBackend, startErr error, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	prevLoader, prevSetup := loadAgentShellRuntime, agentStartupBackendFn
	t.Cleanup(func() { loadAgentShellRuntime, agentStartupBackendFn = prevLoader, prevSetup })
	loadAgentShellRuntime = func(context.Context) (*runtimectx.Context, error) { return readyRuntime(), nil }
	agentStartupBackendFn = func(agentStartupBackendParams) (agentStartupBackendResult, error) {
		if startErr != nil {
			return agentStartupBackendResult{}, startErr
		}
		return agentStartupBackendResult{ActiveTarget: ModelChoice{Model: "stub:1b"}, Backend: be}, nil
	}
	var out, errBuf bytes.Buffer
	cmd := agentCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SilenceUsage = true
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errBuf.String(), err
}

func TestAgentCmdJSONStdoutIsOneDocument(t *testing.T) {
	be := &scriptedBackend{steps: []chat.Message{answer("all nodes healthy")}}
	stdout, stderr, err := runAgentCmd(t, be, nil, "-p", "--format", "json", "check")
	if err != nil {
		t.Fatalf("err = %v\nstderr:\n%s", err, stderr)
	}
	doc := decodeAgentDoc(t, stdout) // fails if the stream reached stdout
	if !doc.OK || doc.Data.FinalText != "all nodes healthy" {
		t.Fatalf("doc = %+v", doc)
	}
	if !strings.Contains(stderr, "all nodes healthy") {
		t.Fatalf("json mode must stream the assistant text to stderr:\n%s", stderr)
	}
}

// The command must hand the piped confirm its recorder: without it a denied
// step is invisible and the run exits 0.
func TestAgentCmdRecordsDeniedStep(t *testing.T) {
	be := &scriptedBackend{steps: []chat.Message{
		toolCall("c1", "run_shell", `{"command":"git push origin main"}`),
		answer("could not push"),
	}}
	stdout, stderr, err := runAgentCmd(t, be, nil, "-p", "--format", "json", "--autonomy", "full", "push")
	if got := ExitCode(err); got != ExitErrAgentNeedsInput {
		t.Fatalf("exit code = %d (%v)\nstderr:\n%s", got, err, stderr)
	}
	if d := decodeAgentDoc(t, stdout).Data; len(d.PendingApprovals) != 1 || d.PendingApprovals[0].Clears != "interactive approval" {
		t.Fatalf("pending = %+v", d.PendingApprovals)
	}
}

func TestAgentCmdJSONStartupFailureWritesDocument(t *testing.T) {
	startErr := ExitCodeError{Code: ExitErrConfigLoad, Message: "no ready model"}
	stdout, _, err := runAgentCmd(t, nil, startErr, "-p", "--format", "json", "check")
	if got := ExitCode(err); got != ExitErrConfigLoad {
		t.Fatalf("exit code = %d (%v)", got, err)
	}
	doc := decodeAgentDoc(t, stdout)
	d := doc.Data
	if doc.OK || doc.Status != "failed" || d.ExitCode != ExitErrConfigLoad || d.Turns != 0 || !strings.Contains(d.Error, "no ready model") {
		t.Fatalf("doc = %+v", doc)
	}
	if !strings.Contains(stdout, `"pending_approvals":[]`) {
		t.Fatalf("pending_approvals not []: %s", stdout)
	}
}

func TestAgentCmdTextStartupFailureWritesNoDocument(t *testing.T) {
	stdout, _, err := runAgentCmd(t, nil, errors.New("no ready model"), "-p", "check")
	if err == nil || stdout != "" {
		t.Fatalf("err = %v, stdout = %q", err, stdout)
	}
}

func TestAgentCmdPrintRejectsZeroMaxTurns(t *testing.T) {
	_, _, err := runAgentCmd(t, &scriptedBackend{steps: []chat.Message{answer("x")}}, nil, "-p", "--max-turns", "0", "check")
	if err == nil || !strings.Contains(err.Error(), "--max-turns must be at least 1") {
		t.Fatalf("err = %v", err)
	}
}

// The positional one-shot without -p keeps exit 0 at the turn ceiling.
func TestAgentCmdPositionalCeilingKeepsExitZero(t *testing.T) {
	be := &scriptedBackend{steps: []chat.Message{toolCall("c1", "cluster_status", `{}`)}}
	_, stderr, err := runAgentCmd(t, be, nil, "--max-turns", "2", "--dry-run", "check")
	if err != nil {
		t.Fatalf("positional run at the ceiling returned %v", err)
	}
	if be.calls != 2 {
		t.Fatalf("backend rounds = %d, want 2\nstderr:\n%s", be.calls, stderr)
	}
}

// Flag errors checked before startup still produce the json document.
func TestAgentCmdJSONFlagErrorsWriteDocument(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-p", "--format", "json", "--autonomy", "bogus", "x"}, "unknown autonomy mode"},
		{[]string{"-p", "--format", "json", "--desk-lease", "lease.json", "x"}, "--desk-lease cannot be combined"},
		{[]string{"-p", "--format", "json", "--max-turns", "0", "x"}, "--max-turns must be at least 1"},
	} {
		stdout, _, err := runAgentCmd(t, &scriptedBackend{steps: []chat.Message{answer("x")}}, nil, tc.args...)
		if ExitCode(err) != ExitErrGeneric {
			t.Fatalf("%v: err = %v", tc.args, err)
		}
		if d := decodeAgentDoc(t, stdout).Data; d.ExitCode != ExitErrGeneric || !strings.Contains(d.Error, tc.want) {
			t.Fatalf("%v: data = %+v", tc.args, d)
		}
	}
}
