// Package transport provides command execution abstractions.
// Phase 1: SSHExecutor. Future: AgentExecutor (axisd).
package transport

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Executor abstracts command execution on a target node.
// This is the seam where axisd-based collection will later plug in.
type Executor interface {
	Run(ctx context.Context, cmd string) (stdout string, err error)
}

// SSHExecutor executes commands on a remote node via SSH.
// This is a Phase 1 temporary transport mechanism.
type SSHExecutor struct {
	Host       string
	Port       int
	User       string
	TimeoutSec int
}

// NewSSHExecutor creates an SSH executor for a remote node.
func NewSSHExecutor(host string, port int, user string, timeoutSec int) *SSHExecutor {
	return &SSHExecutor{Host: host, Port: port, User: user, TimeoutSec: timeoutSec}
}

// Run executes a command via SSH and returns stdout.
func (e *SSHExecutor) Run(ctx context.Context, cmd string) (string, error) {
	config, err := e.sshConfig()
	if err != nil {
		return "", fmt.Errorf("ssh config: %w", err)
	}

	addr := fmt.Sprintf("%s:%d", e.Host, e.Port)
	timeout := time.Duration(e.TimeoutSec) * time.Second

	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return "", fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	defer conn.Close()

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(timeout)
	}
	conn.SetDeadline(deadline)

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		return "", fmt.Errorf("ssh handshake %s: %w", addr, err)
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	if err := session.Run(cmd); err != nil {
		return stdout.String(), fmt.Errorf("ssh run %q on %s: %w (stderr: %s)",
			cmd, e.Host, err, stderr.String())
	}
	return stdout.String(), nil
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

	// Phase 1: accept all host keys. TODO: known_hosts verification.
	return &ssh.ClientConfig{
		User:            e.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signers...)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         time.Duration(e.TimeoutSec) * time.Second,
	}, nil
}
