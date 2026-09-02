package main

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
)

// A proven-free port stopped nothing. It must not print "stopped", and it must
// fail so shell automation can tell a no-op from a real transition.
func TestRunModelStopFreePortReportsNotRunning(t *testing.T) {
	stubModelSnapshot(t, testSnap())
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})

	runner := &fakeModelRunner{stopDisposition: modelStopNotRunning}
	cmd := modelStopCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)

	err := runModelStop(context.Background(), cmd, "storage", 8081, runner)
	if strings.Contains(out.String(), "stopped") {
		t.Fatalf("a free port must not report stopped, got %q", out.String())
	}
	if !strings.Contains(out.String(), "not_running") {
		t.Fatalf("expected not_running in output, got %q", out.String())
	}
	if got := ExitCode(err); got != ExitErrCommandFail {
		t.Fatalf("free-port stop: exit %d, want %d", got, ExitErrCommandFail)
	}
}

// Missing inspection tooling means the port was never observed. That is not
// the same as an empty port and must never be reported as not_running.
func TestRunModelStopInspectionUnavailableIsDistinct(t *testing.T) {
	stubModelSnapshot(t, testSnap())
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})

	runner := &fakeModelRunner{stopDisposition: modelStopInspectionUnavailable}
	cmd := modelStopCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)

	err := runModelStop(context.Background(), cmd, "storage", 8081, runner)
	if strings.Contains(out.String(), "not_running") {
		t.Fatalf("inspection failure must not be reported as not_running: %q", out.String())
	}
	if !strings.Contains(out.String(), "inspection_unavailable") {
		t.Fatalf("expected inspection_unavailable, got %q", out.String())
	}
	if got := ExitCode(err); got != ExitErrCommandFail {
		t.Fatalf("inspection-unavailable stop: exit %d, want %d", got, ExitErrCommandFail)
	}
}

func TestRunModelStopWrongOwnerFailsAndNamesTheReason(t *testing.T) {
	stubModelSnapshot(t, testSnap())
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})

	runner := &fakeModelRunner{stopDisposition: modelStopWrongOwner}
	cmd := modelStopCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)

	err := runModelStop(context.Background(), cmd, "storage", 8081, runner)
	if got := ExitCode(err); got != ExitErrCommandFail {
		t.Fatalf("wrong-owner stop: exit %d, want %d", got, ExitErrCommandFail)
	}
	if !strings.Contains(err.Error(), "llama-server") {
		t.Fatalf("error should explain the ownership refusal, got %v", err)
	}
}

func TestRunModelStopStoppedExitsZero(t *testing.T) {
	stubModelSnapshot(t, testSnap())
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})

	runner := &fakeModelRunner{stopDisposition: modelStopStopped}
	cmd := modelStopCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)

	if err := runModelStop(context.Background(), cmd, "storage", 8081, runner); err != nil {
		t.Fatalf("a real stop must exit 0, got %v (exit %d)", err, ExitCode(err))
	}
	if !strings.Contains(out.String(), "stopped storage:8081") {
		t.Fatalf("expected stopped line, got %q", out.String())
	}
}

// End-to-end at the shell layer: the real generated script, run against a port
// with no listener, must classify as not_running rather than success.
func TestShellStopClassifiesFreePortAsNotRunning(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	// A port in the ephemeral-but-unused range; the script only inspects it.
	const freePort = 39733
	script := shellStop(freePort)
	out, err := exec.Command("bash", "-c", script).CombinedOutput()

	disposition, cerr := classifyModelStop(string(out), err)
	if cerr != nil {
		t.Fatalf("classify: %v (output %q)", cerr, string(out))
	}
	if disposition != modelStopNotRunning {
		t.Fatalf("free port classified as %q, want %q (output %q)", disposition, modelStopNotRunning, string(out))
	}
}

// An unrecognized success must not be silently treated as a stop.
func TestClassifyModelStopRejectsUnmarkedSuccess(t *testing.T) {
	if _, err := classifyModelStop("", nil); err == nil {
		t.Fatal("unmarked success must be an error, not an assumed stop")
	}
	if _, err := classifyModelStop("", errors.New("boom")); err == nil {
		t.Fatal("expected the underlying error to surface")
	}
}
