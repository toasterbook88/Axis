// Package transport provides command execution abstractions.
// Today this package is backed by SSH, with room for future transports.
package transport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

var runSSHConfigCommand = func(ctx context.Context, host string, port int, user string) (string, error) {
	args := []string{"-G"}
	if port > 0 {
		args = append(args, "-p", strconv.Itoa(port))
	}
	if user != "" {
		args = append(args, "-l", user)
	}
	args = append(args, host)

	cmd := exec.CommandContext(ctx, "ssh", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

type resolvedSSHConfig struct {
	IdentityFiles        []string
	UserKnownHostsFiles  []string
	GlobalKnownHostsFile []string
}

// Executor abstracts command execution on a target node.
// This is the seam where axisd-based collection will later plug in.
type Executor interface {
	Connect(ctx context.Context) error
	Run(ctx context.Context, cmd string) (stdout string, err error)
	Close() error
}

// SSHExecutor executes commands on a remote node via SSH.
// This is the current remote transport mechanism.
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

	config, err := e.sshConfig(ctx)
	if err != nil {
		return fmt.Errorf("ssh config: %w", err)
	}

	addr := net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
	timeout := time.Duration(e.TimeoutSec) * time.Second

	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
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
	// The connect deadline is only for dialing and handshake. Clear it once the
	// SSH client is established so later session I/O isn't capped by an old ctx.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return fmt.Errorf("ssh clear deadline %s: %w", addr, err)
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
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := e.Connect(ctx); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", ctxErr
			}
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
		// codeql[go/command-injection] - cmd originates from operator-controlled config or UDS-auth-protected HTTP endpoint
		done <- session.Run(cmd)
	}()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		return "", ctx.Err()
	case err := <-done:
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
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

	go func() {
		<-ctx.Done()
		_ = session.Signal(ssh.SIGKILL)
	}()

	session.Stdout = stdout
	session.Stderr = stderr
	// codeql[go/command-injection] - cmd originates from operator-controlled config or UDS-auth-protected HTTP endpoint
	err = session.Run(cmd)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

func (e *SSHExecutor) sshConfig(ctx context.Context) (*ssh.ClientConfig, error) {
	var signers []ssh.Signer
	resolved := e.resolveSSHConfig(ctx)
	home, _ := os.UserHomeDir()

	// Try SSH agent first
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if conn, err := net.Dial("unix", sock); err == nil {
			if agentSigners, err := agent.NewClient(conn).Signers(); err == nil {
				signers = append(signers, agentSigners...)
			}
		}
	}

	for _, keyPath := range signerPaths(home, resolved) {
		data, err := os.ReadFile(keyPath)
		if err != nil {
			continue
		}
		signer, err := ssh.ParsePrivateKey(data)
		if err != nil {
			continue
		}
		signers = append(signers, signer)
	}

	if len(signers) == 0 {
		return nil, fmt.Errorf("no SSH keys or agent available")
	}

	// SSH Host Key verification is MANDATORY.
	// Users MUST populate their ~/.ssh/known_hosts for security.
	knownHostsPaths, err := existingKnownHostsPaths(home, resolved, e.Host, e.Port)
	if err != nil {
		return nil, err
	}

	hostKeyCallback, err := knownhosts.New(knownHostsPaths...)
	if err != nil {
		return nil, fmt.Errorf("failed to load known_hosts: %w", err)
	}

	return &ssh.ClientConfig{
		User:            e.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signers...)},
		HostKeyCallback: hostKeyCallback,
		Timeout:         time.Duration(e.TimeoutSec) * time.Second,
	}, nil
}

func (e *SSHExecutor) resolveSSHConfig(ctx context.Context) resolvedSSHConfig {
	output, err := runSSHConfigCommand(ctx, e.Host, e.Port, e.User)
	if err != nil {
		return resolvedSSHConfig{}
	}
	return parseSSHConfigDump(output)
}

func parseSSHConfigDump(output string) resolvedSSHConfig {
	var resolved resolvedSSHConfig

	for _, rawLine := range strings.Split(output, "\n") {
		fields := strings.Fields(strings.TrimSpace(rawLine))
		if len(fields) < 2 {
			continue
		}

		key := strings.ToLower(fields[0])
		values := fields[1:]

		switch key {
		case "identityfile":
			resolved.IdentityFiles = append(resolved.IdentityFiles, values...)
		case "userknownhostsfile":
			resolved.UserKnownHostsFiles = append(resolved.UserKnownHostsFiles, values...)
		case "globalknownhostsfile":
			resolved.GlobalKnownHostsFile = append(resolved.GlobalKnownHostsFile, values...)
		}
	}

	return resolved
}

func signerPaths(home string, resolved resolvedSSHConfig) []string {
	paths := make([]string, 0, len(resolved.IdentityFiles)+3)
	for _, path := range resolved.IdentityFiles {
		paths = append(paths, normalizedSSHPath(home, path))
	}
	for _, name := range []string{"id_ed25519", "id_rsa", "id_ecdsa"} {
		paths = append(paths, filepath.Join(home, ".ssh", name))
	}
	return uniqueNonEmptyPaths(paths)
}

func existingKnownHostsPaths(home string, resolved resolvedSSHConfig, host string, port int) ([]string, error) {
	candidates := make([]string, 0, len(resolved.UserKnownHostsFiles)+len(resolved.GlobalKnownHostsFile)+1)
	for _, path := range resolved.UserKnownHostsFiles {
		candidates = append(candidates, normalizedSSHPath(home, path))
	}
	for _, path := range resolved.GlobalKnownHostsFile {
		candidates = append(candidates, normalizedSSHPath(home, path))
	}
	if len(candidates) == 0 {
		candidates = append(candidates, filepath.Join(home, ".ssh", "known_hosts"))
	}

	candidates = uniqueNonEmptyPaths(candidates)
	existing := make([]string, 0, len(candidates))
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			existing = append(existing, path)
		}
	}
	if len(existing) > 0 {
		return existing, nil
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no known_hosts paths available")
	}
	first := candidates[0]
	return nil, fmt.Errorf("%s not found. To trust a host, run: ssh-keyscan -p %d %s >> %s", first, port, host, first)
}

func normalizedSSHPath(home, path string) string {
	path = strings.TrimSpace(path)
	if path == "" || strings.EqualFold(path, "none") {
		return ""
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}

func uniqueNonEmptyPaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out
}
