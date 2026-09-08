package main

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
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

// End-to-end at the shell layer, using the real generated script against a port
// with no listener. The expected disposition depends on whether this machine can
// inspect port ownership at all, and both outcomes are part of the contract:
// with fuser or lsof the port is provably empty (not_running); without either
// the port was never observed, which must report inspection_unavailable and must
// never be downgraded to not_running.
func TestShellStopClassifiesFreePortByInspectionCapability(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	canInspect := false
	for _, tool := range []string{"fuser", "lsof"} {
		if _, err := exec.LookPath(tool); err == nil {
			canInspect = true
			break
		}
	}
	if canInspect {
		if _, err := exec.LookPath("ps"); err != nil {
			canInspect = false
		}
	}

	// A port in the ephemeral-but-unused range; the script only inspects it.
	const freePort = 39733
	out, err := exec.Command("bash", "-c", shellStop(freePort)).CombinedOutput()

	disposition, cerr := classifyModelStop(string(out), err)
	if cerr != nil {
		t.Fatalf("classify: %v (output %q)", cerr, string(out))
	}

	want := modelStopInspectionUnavailable
	if canInspect {
		want = modelStopNotRunning
	}
	if disposition != want {
		t.Fatalf("free port classified as %q, want %q (canInspect=%v, output %q)",
			disposition, want, canInspect, string(out))
	}
	// Whichever branch ran, the free port must never be reported as stopped.
	if disposition == modelStopStopped {
		t.Fatal("a port with no listener must never classify as stopped")
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

func TestRunModelStopDiscoveryBypass(t *testing.T) {
	cases := []struct {
		name       string
		targetNode string
		cfgNodes   []config.NodeConfig
		cacheSnap  *models.ClusterSnapshot
		cacheErr   error
		expected   string
	}{
		{
			name:       "local node bypasses live discovery when cache unreachable",
			targetNode: "local-node",
			cfgNodes:   []config.NodeConfig{{Name: "local-node", Hostname: "127.0.0.1"}},
			cacheErr:   errors.New("daemon cache unreachable"),
			expected:   "stopped local-node:8081",
		},
		{
			name:       "remote node resolves from daemon cache without live discovery",
			targetNode: "remote-worker",
			cfgNodes:   []config.NodeConfig{{Name: "remote-worker", Hostname: "10.0.0.5"}},
			cacheSnap:  &models.ClusterSnapshot{Nodes: []models.NodeFacts{{Name: "remote-worker"}}},
			expected:   "stopped remote-worker:8081",
		},
		{
			name:       "omitted node defaults to local node without live discovery",
			targetNode: "",
			cfgNodes:   []config.NodeConfig{{Name: "primary-local", Hostname: "127.0.0.1"}},
			cacheErr:   errors.New("daemon cache down"),
			expected:   "stopped primary-local:8081",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prevFetch := fetchModelInventorySnapshot
			fetchModelInventorySnapshot = func(context.Context, string) (*models.ClusterSnapshot, string, error) {
				if tc.cacheErr != nil {
					return nil, "", tc.cacheErr
				}
				return tc.cacheSnap, "daemon-cache", nil
			}
			prevLoad := loadModelSnapshot
			loadModelSnapshot = func(context.Context) (*models.ClusterSnapshot, error) {
				t.Fatal("loadModelSnapshot must NOT be called")
				return nil, errors.New("live discovery forbidden")
			}
			t.Cleanup(func() {
				fetchModelInventorySnapshot = prevFetch
				loadModelSnapshot = prevLoad
			})

			stubModelConfig(t, &config.Config{Nodes: tc.cfgNodes})

			runner := &fakeModelRunner{stopDisposition: modelStopStopped}
			cmd := modelStopCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)

			if err := runModelStop(context.Background(), cmd, tc.targetNode, 8081, runner); err != nil {
				t.Fatalf("runModelStop failed: %v", err)
			}
			if !strings.Contains(out.String(), tc.expected) {
				t.Fatalf("expected %q, got %q", tc.expected, out.String())
			}
		})
	}
}
