package transport

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

type testSSHAgent struct {
	listener net.Listener
	done     chan struct{}
	wg       sync.WaitGroup
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	accepted atomic.Int64
	active   atomic.Int64
}

func startTestSSHAgent(t testing.TB, privateKey any) *testSSHAgent {
	t.Helper()

	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: privateKey}); err != nil {
		t.Fatalf("add agent key: %v", err)
	}

	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "agent.sock"))
	if err != nil {
		t.Fatalf("listen on agent socket: %v", err)
	}

	server := &testSSHAgent{
		listener: listener,
		done:     make(chan struct{}),
		conns:    make(map[net.Conn]struct{}),
	}
	go func() {
		defer close(server.done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			server.accepted.Add(1)
			server.active.Add(1)
			server.mu.Lock()
			server.conns[conn] = struct{}{}
			server.mu.Unlock()
			server.wg.Add(1)
			go func() {
				defer server.wg.Done()
				defer server.active.Add(-1)
				defer func() {
					server.mu.Lock()
					delete(server.conns, conn)
					server.mu.Unlock()
				}()
				defer conn.Close()
				_ = agent.ServeAgent(keyring, conn)
			}()
		}
	}()
	return server
}

func (s *testSSHAgent) Socket() string {
	return s.listener.Addr().String()
}

func (s *testSSHAgent) Close() {
	if s == nil {
		return
	}
	_ = s.listener.Close()
	<-s.done
	s.mu.Lock()
	for conn := range s.conns {
		_ = conn.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *testSSHAgent) waitInactive(t testing.TB) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.active.Load() == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("SSH-agent connections still active: %d", s.active.Load())
}

func TestSSHExecutorClosesAgentConnectionAfterHandshake(t *testing.T) {
	clientKey, clientSigner := generateTestKeyPair(t)
	_, hostSigner := generateTestKeyPair(t)
	agentServer := startTestSSHAgent(t, clientKey)
	defer agentServer.Close()

	server := startSSHTestServer(t, clientSigner.PublicKey(), hostSigner, map[string]sshCommandResponse{
		"echo hi": {stdout: "hi\n"},
	})
	defer server.Close()

	home := writeKnownHostsOnly(t, hostSigner, server.Host(), server.Port())
	restore := stubSSHAgentConfigEnv(t, home, agentServer.Socket(), "identitiesonly no\n")
	defer restore()

	exec := NewSSHExecutor(server.Host(), server.Port(), "axis", 5)
	if err := exec.Connect(context.Background()); err != nil {
		t.Fatalf("connect with agent: %v", err)
	}
	defer exec.Close()

	agentServer.waitInactive(t)
	if got := agentServer.accepted.Load(); got != 1 {
		t.Fatalf("agent connections = %d, want exactly one per handshake", got)
	}

	out, err := exec.Run(context.Background(), "echo hi")
	if err != nil {
		t.Fatalf("run after agent connection closed: %v", err)
	}
	if out != "hi\n" {
		t.Fatalf("unexpected stdout: %q", out)
	}
}

func TestSSHExecutorDoesNotAccumulateAgentConnections(t *testing.T) {
	clientKey, clientSigner := generateTestKeyPair(t)
	_, hostSigner := generateTestKeyPair(t)
	agentServer := startTestSSHAgent(t, clientKey)
	defer agentServer.Close()

	server := startSSHTestServer(t, clientSigner.PublicKey(), hostSigner, nil)
	defer server.Close()
	home := writeKnownHostsOnly(t, hostSigner, server.Host(), server.Port())
	restore := stubSSHAgentConfigEnv(t, home, agentServer.Socket(), "identitiesonly no\n")
	defer restore()

	const attempts = 12
	for i := 0; i < attempts; i++ {
		exec := NewSSHExecutor(server.Host(), server.Port(), "axis", 5)
		if err := exec.Connect(context.Background()); err != nil {
			t.Fatalf("connect %d: %v", i, err)
		}
		if err := exec.Close(); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
		agentServer.waitInactive(t)
	}
	if got := agentServer.accepted.Load(); got != attempts {
		t.Fatalf("agent connections = %d, want %d", got, attempts)
	}
}

func TestSSHExecutorClosesAgentConnectionAfterHandshakeFailure(t *testing.T) {
	clientKey, _ := generateTestKeyPair(t)
	_, unauthorizedSigner := generateTestKeyPair(t)
	_, hostSigner := generateTestKeyPair(t)
	agentServer := startTestSSHAgent(t, clientKey)
	defer agentServer.Close()

	server := startSSHTestServer(t, unauthorizedSigner.PublicKey(), hostSigner, nil)
	defer server.Close()
	home := writeKnownHostsOnly(t, hostSigner, server.Host(), server.Port())
	restore := stubSSHAgentConfigEnv(t, home, agentServer.Socket(), "identitiesonly no\n")
	defer restore()

	exec := NewSSHExecutor(server.Host(), server.Port(), "axis", 5)
	if err := exec.Connect(context.Background()); err == nil {
		t.Fatal("expected authentication failure")
	}
	agentServer.waitInactive(t)
	if got := agentServer.accepted.Load(); got != 1 {
		t.Fatalf("agent connections = %d, want 1", got)
	}
}

func TestSSHConfigClosesAgentConnectionOnConfigFailure(t *testing.T) {
	clientKey, _ := generateTestKeyPair(t)
	agentServer := startTestSSHAgent(t, clientKey)
	defer agentServer.Close()

	home := t.TempDir()
	restore := stubSSHAgentConfigEnv(t, home, agentServer.Socket(), "identitiesonly no\n")
	defer restore()

	exec := NewSSHExecutor("missing-host", 22, "axis", 5)
	if _, err := exec.sshConfig(resolvedSSHConfig{}, "missing-host:22"); err == nil {
		t.Fatal("expected missing known_hosts failure")
	}
	agentServer.waitInactive(t)
	if got := agentServer.accepted.Load(); got != 1 {
		t.Fatalf("agent connections = %d, want 1", got)
	}
}

func TestSSHConfigIdentitiesOnlySkipsAgent(t *testing.T) {
	clientKey, clientSigner := generateTestKeyPair(t)
	_, hostSigner := generateTestKeyPair(t)
	agentServer := startTestSSHAgent(t, clientKey)
	defer agentServer.Close()

	server := startSSHTestServer(t, clientSigner.PublicKey(), hostSigner, nil)
	defer server.Close()
	home := writeSSHClientEnv(t, clientKey, hostSigner, server.Host(), server.Port())
	restore := stubSSHAgentConfigEnv(t, home, agentServer.Socket(), "identitiesonly yes\nidentityfile ~/.ssh/test_key\n")
	defer restore()

	exec := NewSSHExecutor(server.Host(), server.Port(), "axis", 5)
	if err := exec.Connect(context.Background()); err != nil {
		t.Fatalf("connect with IdentitiesOnly: %v", err)
	}
	defer exec.Close()
	if got := agentServer.accepted.Load(); got != 0 {
		t.Fatalf("agent connections = %d, want 0 with IdentitiesOnly", got)
	}
}

func writeKnownHostsOnly(t testing.TB, hostSigner ssh.Signer, host string, port int) string {
	t.Helper()
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o755); err != nil {
		t.Fatalf("mkdir .ssh: %v", err)
	}
	line := knownhosts.Line([]string{fmt.Sprintf("[%s]:%d", host, port)}, hostSigner.PublicKey())
	if err := os.WriteFile(filepath.Join(sshDir, "known_hosts"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	return home
}

func stubSSHAgentConfigEnv(t testing.TB, home, socket, extra string) func() {
	t.Helper()
	previousHome := os.Getenv("HOME")
	previousSocket := os.Getenv("SSH_AUTH_SOCK")
	previousRunner := runSSHConfigCommand
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	if err := os.Setenv("SSH_AUTH_SOCK", socket); err != nil {
		t.Fatalf("set SSH_AUTH_SOCK: %v", err)
	}
	runSSHConfigCommand = func(context.Context, string, int, string) (string, error) {
		return "userknownhostsfile ~/.ssh/known_hosts\nidentityfile ~/.ssh/missing\n" + extra, nil
	}
	return func() {
		runSSHConfigCommand = previousRunner
		_ = os.Setenv("HOME", previousHome)
		if previousSocket == "" {
			_ = os.Unsetenv("SSH_AUTH_SOCK")
		} else {
			_ = os.Setenv("SSH_AUTH_SOCK", previousSocket)
		}
	}
}
