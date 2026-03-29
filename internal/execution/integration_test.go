package execution

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type scriptedRemoteStep struct {
	method             string
	stdout             string
	stderr             string
	err                error
	waitForContextDone bool
}

type remoteCall struct {
	method  string
	command string
}

type scriptedRemoteExecutor struct {
	t      *testing.T
	steps  []scriptedRemoteStep
	calls  []remoteCall
	closed bool
}

func TestRunGuardedRemoteUsesLoadedConfigAndStreamsOutput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	cfg := loadExecutionConfig(t, filepath.Join(t.TempDir(), "nodes.yaml"), "remote.example", 22)
	rt := testExecutionRuntime(cfg, &state.ClusterState{Nodes: map[string]state.NodeState{}}, &skills.Store{})

	executor := &scriptedRemoteExecutor{
		t: t,
		steps: []scriptedRemoteStep{
			{method: "run"},
			{method: "stream", stdout: "AXIS_GUARDED_OK\n", stderr: "remote warning\n"},
		},
	}
	restore := stubExecutionRemoteFactory(t, func(config.NodeConfig) RemoteExecutor {
		return executor
	})
	defer restore()

	var stdout, stderr bytes.Buffer
	resp, err := RunGuarded(context.Background(), rt, GuardedExecutionRequest{
		Description: "  printf 'AXIS_GUARDED_OK\\n'  ",
		Mode:        " EXEC ",
		Confirm:     " YES ",
		Stdout:      &stdout,
		Stderr:      &stderr,
	})
	if err != nil {
		t.Fatalf("RunGuarded: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK response, got %#v", resp)
	}
	if resp.Command != "printf 'AXIS_GUARDED_OK\\n'" {
		t.Fatalf("expected real command in response, got %q", resp.Command)
	}
	if resp.Node != "node-a" {
		t.Fatalf("expected node-a, got %q", resp.Node)
	}
	if resp.IsLocal {
		t.Fatal("expected remote execution path")
	}
	if resp.Output != "AXIS_GUARDED_OK\nremote warning" {
		t.Fatalf("unexpected combined output: %q", resp.Output)
	}
	if stdout.String() != "AXIS_GUARDED_OK\n" {
		t.Fatalf("unexpected streamed stdout: %q", stdout.String())
	}
	if stderr.String() != "remote warning\n" {
		t.Fatalf("unexpected streamed stderr: %q", stderr.String())
	}
	if len(rt.Skills.Skills) != 1 {
		t.Fatalf("expected learned skill record, got %d", len(rt.Skills.Skills))
	}
	if len(rt.State.Nodes) != 0 {
		t.Fatalf("expected reservation release cleanup, got %#v", rt.State.Nodes)
	}
	if !executor.closed {
		t.Fatal("expected remote executor to be closed")
	}

	if len(executor.calls) != 2 {
		t.Fatalf("expected two remote calls, got %d (%#v)", len(executor.calls), executor.calls)
	}
	if executor.calls[0].method != "run" || !strings.HasPrefix(executor.calls[0].command, "cat > /tmp/axis-knows-") {
		t.Fatalf("expected first call to upload context, got %#v", executor.calls[0])
	}
	if executor.calls[1].method != "stream" {
		t.Fatalf("expected second call to stream execution, got %#v", executor.calls[1])
	}
	for _, want := range []string{
		"BEST_NODE=node-a",
		"AXIS_EXECUTION_MODE=exec",
		"AXIS_CONFIRM=YES",
		"bash -lc",
		"printf",
		"AXIS_GUARDED_OK",
	} {
		if !strings.Contains(executor.calls[1].command, want) {
			t.Fatalf("expected %q in wrapped remote command %q", want, executor.calls[1].command)
		}
	}
}

func TestRunGuardedRemoteTimeoutPropagatesAndReleasesReservation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	cfg := loadExecutionConfig(t, filepath.Join(t.TempDir(), "nodes.yaml"), "remote.example", 22)
	rt := testExecutionRuntime(cfg, &state.ClusterState{Nodes: map[string]state.NodeState{}}, &skills.Store{})

	executor := &scriptedRemoteExecutor{
		t: t,
		steps: []scriptedRemoteStep{
			{method: "run"},
			{method: "run", waitForContextDone: true},
		},
	}
	restore := stubExecutionRemoteFactory(t, func(config.NodeConfig) RemoteExecutor {
		return executor
	})
	defer restore()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	resp, err := RunGuarded(ctx, rt, GuardedExecutionRequest{
		Description: "echo slow",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
	})
	if err == nil {
		t.Fatal("expected deadline exceeded")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	if resp.ExitCode != 1 {
		t.Fatalf("expected generic exit code 1 for timeout, got %d", resp.ExitCode)
	}
	if !strings.Contains(resp.Error, context.DeadlineExceeded.Error()) {
		t.Fatalf("expected timeout error in response, got %q", resp.Error)
	}
	if len(rt.State.Nodes) != 0 {
		t.Fatalf("expected reservation release cleanup, got %#v", rt.State.Nodes)
	}
	if len(rt.Skills.Failures) != 1 {
		t.Fatalf("expected failure record, got %d", len(rt.Skills.Failures))
	}
	if !executor.closed {
		t.Fatal("expected remote executor to be closed")
	}
}

func TestRunGuardedRemoteConnectFailureReturnsDialError(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skipf("ssh not available: %v", err)
	}

	clientKey, _ := generateExecutionTestKeyPair(t)
	_, hostSigner := generateExecutionTestKeyPair(t)

	home := writeExecutionSSHHome(t, clientKey, hostSigner, "127.0.0.1", 1)
	t.Setenv("HOME", home)
	t.Setenv("SSH_AUTH_SOCK", "")

	cfg := loadExecutionConfig(t, filepath.Join(home, ".axis", "nodes.yaml"), "127.0.0.1", 1)
	rt := testExecutionRuntime(cfg, &state.ClusterState{Nodes: map[string]state.NodeState{}}, &skills.Store{})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	resp, err := RunGuarded(ctx, rt, GuardedExecutionRequest{
		Description: "echo hi",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
	})
	if err == nil {
		t.Fatal("expected SSH dial failure")
	}
	if !strings.Contains(resp.Error, "ssh dial") {
		t.Fatalf("expected dial error in response, got %q", resp.Error)
	}
	if resp.ExitCode != 1 {
		t.Fatalf("expected generic exit code 1 for dial error, got %d", resp.ExitCode)
	}
	if len(rt.State.Nodes) != 0 {
		t.Fatalf("expected no reservation persisted on early connect failure, got %#v", rt.State.Nodes)
	}
}

func (s *scriptedRemoteExecutor) Run(ctx context.Context, command string) (string, error) {
	s.t.Helper()
	step := s.nextStep("run")
	s.calls = append(s.calls, remoteCall{method: "run", command: command})
	if step.waitForContextDone {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return step.stdout, step.err
}

func (s *scriptedRemoteExecutor) Stream(ctx context.Context, command string, stdout, stderr io.Writer) error {
	s.t.Helper()
	step := s.nextStep("stream")
	s.calls = append(s.calls, remoteCall{method: "stream", command: command})
	if step.waitForContextDone {
		<-ctx.Done()
		return ctx.Err()
	}
	if step.stdout != "" {
		if _, err := io.WriteString(stdout, step.stdout); err != nil {
			return err
		}
	}
	if step.stderr != "" {
		if _, err := io.WriteString(stderr, step.stderr); err != nil {
			return err
		}
	}
	return step.err
}

func (s *scriptedRemoteExecutor) Close() error {
	s.closed = true
	return nil
}

func (s *scriptedRemoteExecutor) nextStep(method string) scriptedRemoteStep {
	s.t.Helper()
	if len(s.steps) == 0 {
		s.t.Fatalf("unexpected remote %s call", method)
	}
	step := s.steps[0]
	s.steps = s.steps[1:]
	if step.method != method {
		s.t.Fatalf("expected next remote step %q, got %q", step.method, method)
	}
	return step
}

func stubExecutionRemoteFactory(t *testing.T, fn func(config.NodeConfig) RemoteExecutor) func() {
	t.Helper()
	prev := NewRemoteExecutor
	NewRemoteExecutor = fn
	return func() {
		NewRemoteExecutor = prev
	}
}

func testExecutionRuntime(cfg *config.Config, st *state.ClusterState, store *skills.Store) *runtimectx.Context {
	return &runtimectx.Context{
		Config: cfg,
		Snapshot: &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Nodes: []models.NodeFacts{
				{
					Name:     "node-a",
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
			Summary: models.ClusterSummary{
				TotalNodes:     1,
				ReachableNodes: 1,
				TotalRAMMB:     8192,
				TotalFreeRAMMB: 4096,
			},
		},
		State:  st,
		Skills: store,
	}
}

func loadExecutionConfig(t *testing.T, path, host string, port int) *config.Config {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}

	content := fmt.Sprintf(`nodes:
  - name: node-a
    hostname: %s
    ssh_user: axis
    ssh_port: %d
    timeout_sec: 2
`, host, port)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

func generateExecutionTestKeyPair(t *testing.T) (*rsa.PrivateKey, ssh.Signer) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	return key, signer
}

func writeExecutionSSHHome(t *testing.T, clientKey *rsa.PrivateKey, hostSigner ssh.Signer, host string, port int) string {
	t.Helper()

	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o755); err != nil {
		t.Fatalf("mkdir .ssh: %v", err)
	}

	block := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(clientKey),
	}
	if err := os.WriteFile(filepath.Join(sshDir, "id_rsa"), pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write client key: %v", err)
	}

	knownHosts := knownhosts.Line([]string{fmt.Sprintf("[%s]:%d", host, port)}, hostSigner.PublicKey()) + "\n"
	if err := os.WriteFile(filepath.Join(sshDir, "known_hosts"), []byte(knownHosts), 0o644); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}

	sshConfig := "Host *\n  IdentityFile ~/.ssh/id_rsa\n  UserKnownHostsFile ~/.ssh/known_hosts\n  IdentitiesOnly yes\n"
	if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte(sshConfig), 0o644); err != nil {
		t.Fatalf("write ssh config: %v", err)
	}

	return home
}
