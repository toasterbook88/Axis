package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type sshCommandResponse struct {
	stdout     string
	stderr     string
	exitStatus uint32
	delay      time.Duration
}

type sshTestServer struct {
	listener net.Listener
	config   *ssh.ServerConfig
	done     chan struct{}
}

func TestSSHExecutorConnectRunClose(t *testing.T) {
	clientKey, clientSigner := generateTestKeyPair(t)
	_, hostSigner := generateTestKeyPair(t)

	server := startSSHTestServer(t, clientSigner.PublicKey(), hostSigner, map[string]sshCommandResponse{
		"echo hi": {stdout: "hi\n"},
	})
	defer server.Close()

	home := writeSSHClientEnv(t, clientKey, hostSigner, server.Host(), server.Port())
	restore := stubSSHConfigEnv(t, home)
	defer restore()

	exec := NewSSHExecutor(server.Host(), server.Port(), "axis", 5)
	if err := exec.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if exec.client == nil {
		t.Fatal("expected ssh client after connect")
	}

	out, err := exec.Run(context.Background(), "echo hi")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "hi\n" {
		t.Fatalf("unexpected stdout: %q", out)
	}

	if err := exec.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if exec.client != nil {
		t.Fatal("expected client to be nil after close")
	}
	if err := exec.Close(); err != nil {
		t.Fatalf("second close should be harmless, got %v", err)
	}
}

func TestSSHExecutorRunIncludesStderrOnFailure(t *testing.T) {
	clientKey, clientSigner := generateTestKeyPair(t)
	_, hostSigner := generateTestKeyPair(t)

	server := startSSHTestServer(t, clientSigner.PublicKey(), hostSigner, map[string]sshCommandResponse{
		"fail": {stdout: "partial\n", stderr: "boom\n", exitStatus: 17},
	})
	defer server.Close()

	home := writeSSHClientEnv(t, clientKey, hostSigner, server.Host(), server.Port())
	restore := stubSSHConfigEnv(t, home)
	defer restore()

	exec := NewSSHExecutor(server.Host(), server.Port(), "axis", 5)
	out, err := exec.Run(context.Background(), "fail")
	if err == nil {
		t.Fatal("expected run failure")
	}
	if out != "partial\n" {
		t.Fatalf("unexpected partial stdout: %q", out)
	}
	if got := err.Error(); !bytes.Contains([]byte(got), []byte("stderr: boom")) {
		t.Fatalf("expected stderr in error, got %v", err)
	}
}

func TestSSHExecutorRunHonorsContextCancellation(t *testing.T) {
	clientKey, clientSigner := generateTestKeyPair(t)
	_, hostSigner := generateTestKeyPair(t)

	server := startSSHTestServer(t, clientSigner.PublicKey(), hostSigner, map[string]sshCommandResponse{
		"sleep": {delay: 300 * time.Millisecond},
	})
	defer server.Close()

	home := writeSSHClientEnv(t, clientKey, hostSigner, server.Host(), server.Port())
	restore := stubSSHConfigEnv(t, home)
	defer restore()

	exec := NewSSHExecutor(server.Host(), server.Port(), "axis", 5)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := exec.Run(ctx, "sleep"); err == nil {
		t.Fatal("expected context cancellation")
	} else if err != context.DeadlineExceeded {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
}

func TestSSHExecutorStreamWritesOutput(t *testing.T) {
	clientKey, clientSigner := generateTestKeyPair(t)
	_, hostSigner := generateTestKeyPair(t)

	server := startSSHTestServer(t, clientSigner.PublicKey(), hostSigner, map[string]sshCommandResponse{
		"stream": {stdout: "hello\n", stderr: "warn\n"},
	})
	defer server.Close()

	home := writeSSHClientEnv(t, clientKey, hostSigner, server.Host(), server.Port())
	restore := stubSSHConfigEnv(t, home)
	defer restore()

	exec := NewSSHExecutor(server.Host(), server.Port(), "axis", 5)
	var stdout, stderr bytes.Buffer
	if err := exec.Stream(context.Background(), "stream", &stdout, &stderr); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if stdout.String() != "hello\n" {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
	if stderr.String() != "warn\n" {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
}

func TestSSHExecutorStreamHonorsContextCancellation(t *testing.T) {
	clientKey, clientSigner := generateTestKeyPair(t)
	_, hostSigner := generateTestKeyPair(t)

	server := startSSHTestServer(t, clientSigner.PublicKey(), hostSigner, map[string]sshCommandResponse{
		"sleep": {delay: 300 * time.Millisecond},
	})
	defer server.Close()

	home := writeSSHClientEnv(t, clientKey, hostSigner, server.Host(), server.Port())
	restore := stubSSHConfigEnv(t, home)
	defer restore()

	exec := NewSSHExecutor(server.Host(), server.Port(), "axis", 5)
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := exec.Stream(ctx, "sleep", &stdout, &stderr)
	if err == nil {
		t.Fatal("expected stream cancellation")
	}
	if err != context.DeadlineExceeded {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("expected prompt cancel return, got %s", elapsed)
	}
}

func TestSSHExecutorConnectFailsOnDial(t *testing.T) {
	exec := NewSSHExecutor("127.0.0.1", 1, "axis", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := exec.Connect(ctx); err == nil {
		t.Fatal("expected connect failure")
	}
}

func TestSSHExecutorConnectReportsHostKeyMismatchRemediation(t *testing.T) {
	clientKey, clientSigner := generateTestKeyPair(t)
	_, serverHostSigner := generateTestKeyPair(t)
	_, staleHostSigner := generateTestKeyPair(t)

	server := startSSHTestServer(t, clientSigner.PublicKey(), serverHostSigner, map[string]sshCommandResponse{
		"echo hi": {stdout: "hi\n"},
	})
	defer server.Close()

	home := writeSSHClientEnv(t, clientKey, staleHostSigner, server.Host(), server.Port())
	restore := stubSSHConfigEnv(t, home)
	defer restore()

	exec := NewSSHExecutor(server.Host(), server.Port(), "axis", 5)
	err := exec.Connect(context.Background())
	if err == nil {
		t.Fatal("expected host key mismatch")
	}

	msg := err.Error()
	if !strings.Contains(msg, "known_hosts key mismatch") {
		t.Fatalf("expected known_hosts mismatch guidance, got %q", msg)
	}
	if !strings.Contains(msg, "remediation:") {
		t.Fatalf("expected remediation guidance in error, got %q", msg)
	}
	target := fmt.Sprintf("[%s]:%d", server.Host(), server.Port())
	if !strings.Contains(msg, target) {
		t.Fatalf("expected host target %q in remediation, got %q", target, msg)
	}
	if !strings.Contains(msg, "ssh-keygen -R") {
		t.Fatalf("expected ssh-keygen hint in remediation, got %q", msg)
	}
}

func startSSHTestServer(t *testing.T, authorized ssh.PublicKey, hostSigner ssh.Signer, responses map[string]sshCommandResponse) *sshTestServer {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	config := &ssh.ServerConfig{
		NoClientAuth: false,
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), authorized.Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("unauthorized key for %s", conn.User())
		},
	}
	config.AddHostKey(hostSigner)

	server := &sshTestServer{
		listener: listener,
		config:   config,
		done:     make(chan struct{}),
	}

	go func() {
		defer close(server.done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveSSHConn(conn, config, responses)
		}
	}()

	return server
}

func serveSSHConn(conn net.Conn, config *ssh.ServerConfig, responses map[string]sshCommandResponse) {
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(reqs)

	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(ssh.UnknownChannelType, "unsupported channel type")
			continue
		}

		channel, requests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go func(ch ssh.Channel, in <-chan *ssh.Request) {
			defer ch.Close()
			for req := range in {
				switch req.Type {
				case "exec":
					var payload struct {
						Command string
					}
					if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
						req.Reply(false, nil)
						return
					}
					req.Reply(true, nil)
					resp := responses[payload.Command]
					if resp.delay > 0 {
						time.Sleep(resp.delay)
					}
					if resp.stdout != "" {
						_, _ = ch.Write([]byte(resp.stdout))
					}
					if resp.stderr != "" {
						_, _ = ch.Stderr().Write([]byte(resp.stderr))
					}
					_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{Status: resp.exitStatus}))
					return
				case "signal":
					req.Reply(true, nil)
				default:
					req.Reply(false, nil)
				}
			}
		}(channel, requests)
	}
}

func (s *sshTestServer) Close() {
	if s == nil {
		return
	}
	_ = s.listener.Close()
	<-s.done
}

func (s *sshTestServer) Host() string {
	host, _, _ := net.SplitHostPort(s.listener.Addr().String())
	return host
}

func (s *sshTestServer) Port() int {
	_, portStr, _ := net.SplitHostPort(s.listener.Addr().String())
	port, _ := net.LookupPort("tcp", portStr)
	return port
}

func generateTestKeyPair(t *testing.T) (*rsa.PrivateKey, ssh.Signer) {
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

func writeSSHClientEnv(t *testing.T, clientKey *rsa.PrivateKey, hostSigner ssh.Signer, host string, port int) string {
	t.Helper()
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o755); err != nil {
		t.Fatalf("mkdir .ssh: %v", err)
	}

	keyPath := filepath.Join(sshDir, "test_key")
	block := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(clientKey),
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write client key: %v", err)
	}

	knownHostsPath := filepath.Join(sshDir, "known_hosts")
	line := knownhosts.Line([]string{fmt.Sprintf("[%s]:%d", host, port)}, hostSigner.PublicKey())
	if err := os.WriteFile(knownHostsPath, []byte(line+"\n"), 0o644); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	return home
}

func stubSSHConfigEnv(t *testing.T, home string) func() {
	t.Helper()

	prevHome := os.Getenv("HOME")
	prevSock := os.Getenv("SSH_AUTH_SOCK")
	prevRunner := runSSHConfigCommand

	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	if err := os.Unsetenv("SSH_AUTH_SOCK"); err != nil {
		t.Fatalf("unset SSH_AUTH_SOCK: %v", err)
	}
	runSSHConfigCommand = func(ctx context.Context, host string, port int, user string) (string, error) {
		return "identityfile ~/.ssh/test_key\nuserknownhostsfile ~/.ssh/known_hosts\n", nil
	}

	return func() {
		runSSHConfigCommand = prevRunner
		_ = os.Setenv("HOME", prevHome)
		if prevSock == "" {
			_ = os.Unsetenv("SSH_AUTH_SOCK")
		} else {
			_ = os.Setenv("SSH_AUTH_SOCK", prevSock)
		}
	}
}
