// Package transport provides command execution abstractions.
// Phase 1: SSHExecutor. Future: AgentExecutor (axisd).
package transport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Executor abstracts command execution on a target node.
// This is the seam where axisd-based collection will later plug in.
type Executor interface {
	Connect(ctx context.Context) error
	Run(ctx context.Context, cmd string) (stdout string, err error)
	Close() error
}

// SSHExecutor executes commands on a remote node via SSH.
// This is a Phase 1 temporary transport mechanism.
type SSHExecutor struct {
	Host       string
	Port       int
	User       string
	TimeoutSec int
	client     *ssh.Client
}

// NewSSHExecutor creates an SSH executor for a remote node.
func NewSSHExecutor(host string, port int, user string, timeoutSec int) *SSHExecutor {
	return &SSHExecutor{Host: host, Port: port, User: user, TimeoutSec: timeoutSec}
}

// Connect establishes the SSH client connection.
func (e *SSHExecutor) Connect(ctx context.Context) error {
	if e.client != nil {
		return nil
	}

	config, err := e.sshConfig()
	if err != nil {
		return fmt.Errorf("ssh config: %w", err)
	}

	addr := net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
	timeout := time.Duration(e.TimeoutSec) * time.Second

	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return fmt.Errorf("ssh dial %s: %w", addr, err)
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(timeout)
	}
	conn.SetDeadline(deadline)

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		conn.Close()
		return fmt.Errorf("ssh handshake %s: %w", addr, err)
	}
	e.client = ssh.NewClient(sshConn, chans, reqs)

	return nil
}

// Close terminates the SSH client connection.
func (e *SSHExecutor) Close() error {
	if e.client != nil {
		err := e.client.Close()
		e.client = nil
		return err
	}
	return nil
}

// Run executes a command via SSH and returns stdout.
func (e *SSHExecutor) Run(ctx context.Context, cmd string) (string, error) {
	if e.client == nil {
		if err := e.Connect(ctx); err != nil {
			return "", err
		}
	}

	session, err := e.client.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	done := make(chan error, 1)
	go func() {
		done <- session.Run(cmd)
	}()

	select {
	case <-ctx.Done():
		session.Signal(ssh.SIGKILL)
		return "", ctx.Err()
	case err := <-done:
		if err != nil {
			return stdout.String(), fmt.Errorf("ssh run %q on %s: %w (stderr: %s)",
				cmd, e.Host, err, stderr.String())
		}
		return stdout.String(), nil
	}
}

// Stream runs a command with realtime output to the provided writers.
// Used for long-running tasks. Same SSH setup as Run().
func (e *SSHExecutor) Stream(ctx context.Context, cmd string, stdout, stderr io.Writer) error {
	if e.client == nil {
		if err := e.Connect(ctx); err != nil {
			return err
		}
	}

	session, err := e.client.NewSession()
	if err != nil {
		return fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	// Connect context cancellation to SSH signal if needed
	go func() {
		<-ctx.Done()
		session.Signal(ssh.SIGKILL)
	}()

	session.Stdout = stdout
	session.Stderr = stderr
	return session.Run(cmd)
}

func (e *SSHExecutor) sshConfig() (*ssh.ClientConfig, error) {
	var signers []ssh.Signer

	// Try SSH agent first
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if conn, err := net.Dial("unix", sock); err == nil {
			if agentSigners, err := agent.NewClient(conn).Signers(); err == nil {
				signers = append(signers, agentSigners...)
			}
		}
	}

	// Fallback to key files
	if len(signers) == 0 {
		home, _ := os.UserHomeDir()
		for _, name := range []string{"id_ed25519", "id_rsa", "id_ecdsa"} {
			data, err := os.ReadFile(filepath.Join(home, ".ssh", name))
			if err != nil {
				continue
			}
			signer, err := ssh.ParsePrivateKey(data)
			if err != nil {
				continue
			}
			signers = append(signers, signer)
		}
	}

	if len(signers) == 0 {
		return nil, fmt.Errorf("no SSH keys or agent available")
	}

	// Opt-in insecure mode
	insecure := os.Getenv("AXIS_SSH_INSECURE") == "1"
	var hostKeyCallback ssh.HostKeyCallback

	if insecure {
		hostKeyCallback = ssh.InsecureIgnoreHostKey()
	} else {
		home, _ := os.UserHomeDir()
		knownHostsPath := filepath.Join(home, ".ssh", "known_hosts")

		cb, err := knownhosts.New(knownHostsPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("~/.ssh/known_hosts not found. Run 'ssh-keyscan <host> >> ~/.ssh/known_hosts' or set AXIS_SSH_INSECURE=1")
			}
			return nil, fmt.Errorf("failed to load known_hosts (set AXIS_SSH_INSECURE=1 to bypass): %w", err)
		}
		hostKeyCallback = cb
	}

	return &ssh.ClientConfig{
		User:            e.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signers...)},
		HostKeyCallback: hostKeyCallback,
		Timeout:         time.Duration(e.TimeoutSec) * time.Second,
	}, nil
}
