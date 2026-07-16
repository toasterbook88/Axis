// Package transport is STABLE — SSH command execution layer with host-key verification.
// It is part of the stable operator path.
package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	home, _ := os.UserHomeDir()
	if home != "" {
		configPath := filepath.Join(home, ".ssh", "config")
		if _, err := os.Stat(configPath); err == nil {
			args = append(args, "-F", configPath)
		}
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
	IdentitiesOnly       bool
	UserKnownHostsFiles  []string
	GlobalKnownHostsFile []string
	Hostname             string
	User                 string
	HostKeyAlias         string
	HostKeyAlgorithms    []string
	Port                 int
}

type probeRemoteAddr string

func (a probeRemoteAddr) Network() string { return "tcp" }
func (a probeRemoteAddr) String() string  { return string(a) }

var (
	hostKeyProbeOnce sync.Once
	hostKeyProbeKey  ssh.PublicKey
	hostKeyProbeErr  error
)

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
	Host               string
	Port               int
	User               string
	TimeoutSec         int
	ResolvedDialTarget string
	// DialFallbacks are alternate hostnames tried if the primary Host dial fails.
	DialFallbacks []string
	mu                 sync.RWMutex
	client             *ssh.Client
	handshakeLatencyMs int64
}

// NewSSHExecutor creates an SSH executor for a remote node.
func NewSSHExecutor(host string, port int, user string, timeoutSec int) *SSHExecutor {
	return &SSHExecutor{Host: host, Port: port, User: user, TimeoutSec: timeoutSec}
}

// SetDialFallbacks configures alternate dial hostnames (e.g. Tailscale after LAN).
func (e *SSHExecutor) SetDialFallbacks(hosts []string) {
	if e == nil {
		return
	}
	e.DialFallbacks = append([]string(nil), hosts...)
}

// NewSSHExecutorWithDialTarget creates an SSH executor with a specific IP override.
func NewSSHExecutorWithDialTarget(host string, port int, user string, timeoutSec int, resolvedDialTarget string) *SSHExecutor {
	return &SSHExecutor{
		Host:               host,
		Port:               port,
		User:               user,
		TimeoutSec:         timeoutSec,
		ResolvedDialTarget: resolvedDialTarget,
	}
}

// HandshakeLatencyMs returns the measured duration of the SSH connection and handshake.
func (e *SSHExecutor) HandshakeLatencyMs() int64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.handshakeLatencyMs
}

// Connect establishes the SSH client connection.
func (e *SSHExecutor) Connect(ctx context.Context) error {
	e.mu.Lock()
	if e.client != nil {
		e.mu.Unlock()
		return nil
	}
	e.mu.Unlock()

	resolved := e.resolveSSHConfig(ctx)
	hostKeyAddr := net.JoinHostPort(resolvedHostKeyName(resolved, e.Host), strconv.Itoa(resolvedPort(resolved, e.Port)))

	config, err := e.sshConfig(resolved, hostKeyAddr)
	if err != nil {
		return fmt.Errorf("ssh config: %w", err)
	}

	timeout := time.Duration(e.TimeoutSec) * time.Second
	dialer := net.Dialer{Timeout: timeout}

	// Helper to try a specific dial address while keeping hostKeyAddr for SSH crypto
	tryDial := func(dialAddr string) (ssh.Conn, <-chan ssh.NewChannel, <-chan *ssh.Request, int64, error) {
		start := time.Now()
		conn, err := dialer.DialContext(ctx, "tcp", dialAddr)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, nil, nil, 0, ctxErr
			}
			return nil, nil, nil, 0, fmt.Errorf("ssh dial %s: %w", dialAddr, err)
		}

		deadline, ok := ctx.Deadline()
		if !ok {
			deadline = time.Now().Add(timeout)
		}
		if err := conn.SetDeadline(deadline); err != nil {
			conn.Close()
			return nil, nil, nil, 0, fmt.Errorf("ssh set deadline %s: %w", dialAddr, err)
		}

		sshConn, chans, reqs, err := ssh.NewClientConn(conn, hostKeyAddr, config)
		if err != nil {
			conn.Close()
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, nil, nil, 0, ctxErr
			}
			if handshakeDeadlineExceeded(ctx, err) {
				return nil, nil, nil, 0, context.DeadlineExceeded
			}
			if hint := handshakeRemediation(err, resolvedHostKeyName(resolved, e.Host), resolvedPort(resolved, e.Port)); hint != "" {
				return nil, nil, nil, 0, fmt.Errorf("ssh handshake %s: %w; remediation: %s", dialAddr, err, hint)
			}
			return nil, nil, nil, 0, fmt.Errorf("ssh dial %s: %w", dialAddr, err)
		}

		// Clear deadline after handshake
		if err := conn.SetDeadline(time.Time{}); err != nil {
			conn.Close()
			return nil, nil, nil, 0, fmt.Errorf("ssh clear deadline %s: %w", dialAddr, err)
		}

		return sshConn, chans, reqs, time.Since(start).Milliseconds(), nil
	}

	var sshConn ssh.Conn
	var chans <-chan ssh.NewChannel
	var reqs <-chan *ssh.Request
	var handshakeLatencyMs int64

	// 1. Try the dynamic fast-path (ResolvedDialTarget) first
	if e.ResolvedDialTarget != "" {
		dialAddr := net.JoinHostPort(e.ResolvedDialTarget, strconv.Itoa(resolvedPort(resolved, e.Port)))
		sshConn, chans, reqs, handshakeLatencyMs, err = tryDial(dialAddr)
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			// Fast-path failed (e.g. DPI firewall blocked SSH handshake on a fast TCP link).
			// We swallow the error and fall back to the logical OS resolution.
			sshConn = nil
		}
	}

	// 2. Fall back to the logical host resolution, then optional dial fallbacks
	// (LAN primary → Tailscale secondary from nodes.yaml endpoints).
	if sshConn == nil {
		candidates := []string{resolvedDialHost(resolved, e.Host)}
		candidates = append(candidates, e.DialFallbacks...)
		var lastErr error
		for _, hostCand := range candidates {
			hostCand = strings.TrimSpace(hostCand)
			if hostCand == "" {
				continue
			}
			// Re-resolve ssh config for alternate hostnames when different from primary.
			candResolved := resolved
			if hostCand != e.Host {
				candResolved = e.resolveSSHConfigForHost(ctx, hostCand)
			}
			dialHost := resolvedDialHost(candResolved, hostCand)
			logicalAddr := net.JoinHostPort(dialHost, strconv.Itoa(resolvedPort(candResolved, e.Port)))
			// Host key verification uses the candidate hostname.
			hostKeyAddr = net.JoinHostPort(resolvedHostKeyName(candResolved, hostCand), strconv.Itoa(resolvedPort(candResolved, e.Port)))
			config, err = e.sshConfig(candResolved, hostKeyAddr)
			if err != nil {
				lastErr = err
				continue
			}
			sshConn, chans, reqs, handshakeLatencyMs, err = tryDial(logicalAddr)
			if err == nil {
				// Remember which host worked for subsequent sessions.
				e.Host = hostCand
				lastErr = nil
				break
			}
			lastErr = err
			sshConn = nil
		}
		if sshConn == nil {
			if lastErr != nil {
				return lastErr
			}
			return fmt.Errorf("ssh dial: no dial targets")
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client != nil {
		sshConn.Close()
		return nil
	}
	e.client = ssh.NewClient(sshConn, chans, reqs)
	e.handshakeLatencyMs = handshakeLatencyMs

	return nil
}

// Close terminates the SSH client connection.
func (e *SSHExecutor) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client != nil {
		err := e.client.Close()
		e.client = nil
		return err
	}
	return nil
}

// Run executes a command via SSH and returns stdout.
func (e *SSHExecutor) Run(ctx context.Context, cmd string) (string, error) {
	e.mu.RLock()
	client := e.client
	e.mu.RUnlock()
	if client == nil {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := e.Connect(ctx); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", ctxErr
			}
			return "", err
		}
		e.mu.RLock()
		client = e.client
		e.mu.RUnlock()
	}
	if client == nil {
		return "", fmt.Errorf("ssh client not connected")
	}

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	done := make(chan error, 1)
	go func() {
		// Intentional remote execution boundary: this transport forwards cmd
		// to the remote SSH session. Callers must ensure cmd is trusted and
		// correctly quoted or escaped for the remote shell context.
		// codeql[go/command-injection]
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
	if err := ctx.Err(); err != nil {
		return err
	}

	e.mu.RLock()
	client := e.client
	e.mu.RUnlock()
	if client == nil {
		if err := e.Connect(ctx); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return err
		}
		e.mu.RLock()
		client = e.client
		e.mu.RUnlock()
	}
	if client == nil {
		return fmt.Errorf("ssh client not connected")
	}

	session, err := client.NewSession()
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

	// Intentional remote execution boundary: this transport forwards cmd
	// to the remote SSH session. Callers must ensure cmd is trusted and
	// correctly quoted or escaped for the remote shell context.
	// codeql[go/command-injection]
	err = session.Run(cmd)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if err != nil {
		return fmt.Errorf("ssh stream %q on %s: %w", cmd, e.Host, err)
	}
	return err
}

// ForwardLocal listens on a local port and forwards connections to a remote port on the host.
// If localPort is 0, it dynamically binds to a random free local port.
// Returns the bound local port, a cancellation function to stop forwarding, and any error.
func (e *SSHExecutor) ForwardLocal(ctx context.Context, localPort, remotePort int) (int, func(), error) {
	e.mu.RLock()
	client := e.client
	e.mu.RUnlock()
	if client == nil {
		if err := e.Connect(ctx); err != nil {
			return 0, nil, err
		}
		e.mu.RLock()
		client = e.client
		e.mu.RUnlock()
	}
	if client == nil {
		return 0, nil, fmt.Errorf("ssh client not connected")
	}

	localAddr := fmt.Sprintf("127.0.0.1:%d", localPort)
	listener, err := net.Listen("tcp", localAddr)
	if err != nil && localPort != 0 {
		// Try to bind dynamically
		fallbackListener, fallbackErr := net.Listen("tcp", "127.0.0.1:0")
		if fallbackErr == nil {
			boundPort := fallbackListener.Addr().(*net.TCPAddr).Port
			fmt.Fprintf(os.Stderr, "[AXIS] Warning: Local port %d is already in use. Bound dynamically to 127.0.0.1:%d instead.\n", localPort, boundPort)
			listener = fallbackListener
			err = nil
		}
	}
	if err != nil {
		return 0, nil, fmt.Errorf("local listen failed: %w", err)
	}

	boundPort := listener.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	var acceptWg sync.WaitGroup

	acceptWg.Add(1)
	go func() {
		defer acceptWg.Done()
		<-ctx.Done()
		_ = listener.Close()
	}()

	acceptWg.Add(1)
	go func() {
		defer acceptWg.Done()
		defer listener.Close()
		for {
			localConn, err := listener.Accept()
			if err != nil {
				select {
				case <-ctx.Done():
					return // closed intentionally
				default:
					return
				}
			}

			wg.Add(1)
			go func() {
				defer wg.Done()
				e.handleForwardConnection(ctx, localConn, remotePort)
			}()
		}
	}()

	closeFn := func() {
		cancel()
		_ = listener.Close()
		acceptWg.Wait()
		wg.Wait()
	}

	return boundPort, closeFn, nil
}

func (e *SSHExecutor) handleForwardConnection(ctx context.Context, localConn net.Conn, remotePort int) {
	defer localConn.Close()

	e.mu.RLock()
	client := e.client
	e.mu.RUnlock()
	if client == nil {
		fmt.Fprintf(os.Stderr, "ssh port-forward remote dial failed: client not connected\n")
		return
	}

	remoteAddr := fmt.Sprintf("127.0.0.1:%d", remotePort)
	remoteConn, err := client.Dial("tcp", remoteAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ssh port-forward remote dial failed to %s: %v\n", remoteAddr, err)
		return
	}
	defer remoteConn.Close()

	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(remoteConn, localConn)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(localConn, remoteConn)
		done <- struct{}{}
	}()

	select {
	case <-ctx.Done():
	case <-done:
	}
}

func handshakeDeadlineExceeded(ctx context.Context, err error) bool {
	if ctx == nil || err == nil {
		return false
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Now().Before(deadline) {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func handshakeRemediation(err error, host string, port int) string {
	if err == nil || host == "" || port <= 0 {
		return ""
	}

	var keyErr *knownhosts.KeyError
	isMismatch := errors.As(err, &keyErr) && len(keyErr.Want) > 0
	if !isMismatch {
		isMismatch = strings.Contains(strings.ToLower(err.Error()), "knownhosts: key mismatch")
	}

	if isMismatch {
		return fmt.Sprintf("known_hosts key mismatch for [%s]:%d; verify host identity and refresh the known_hosts entry (for example: ssh-keygen -R '[%s]:%d')", host, port, host, port)
	}

	return ""
}

func (e *SSHExecutor) sshConfig(resolved resolvedSSHConfig, hostKeyAddr string) (*ssh.ClientConfig, error) {
	var signers []ssh.Signer
	home, _ := os.UserHomeDir()

	// When IdentitiesOnly yes is set, skip SSH agent and default key names
	// to match OpenSSH behavior: only offer explicitly configured identity files.
	if !resolved.IdentitiesOnly {
		// Try SSH agent first
		if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
			if conn, err := net.Dial("unix", sock); err == nil {
				if agentSigners, err := agent.NewClient(conn).Signers(); err == nil {
					signers = append(signers, agentSigners...)
				}
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
	knownHostsPaths, err := existingKnownHostsPaths(
		home,
		resolved,
		resolvedHostKeyName(resolved, e.Host),
		resolvedPort(resolved, e.Port),
	)
	if err != nil {
		return nil, err
	}

	hostKeyCallback, err := knownhosts.New(knownHostsPaths...)
	if err != nil {
		return nil, fmt.Errorf("failed to load known_hosts: %w", err)
	}

	hostKeyAlgorithms := preferredHostKeyAlgorithms(resolved, knownHostsPaths, hostKeyAddr)
	user := strings.TrimSpace(resolved.User)
	if user == "" {
		user = e.User
	}

	return &ssh.ClientConfig{
		User:              user,
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(signers...)},
		HostKeyCallback:   hostKeyCallback,
		HostKeyAlgorithms: hostKeyAlgorithms,
		Timeout:           time.Duration(e.TimeoutSec) * time.Second,
	}, nil
}

func (e *SSHExecutor) resolveSSHConfig(ctx context.Context) resolvedSSHConfig {
	return e.resolveSSHConfigForHost(ctx, e.Host)
}

func (e *SSHExecutor) resolveSSHConfigForHost(ctx context.Context, host string) resolvedSSHConfig {
	output, err := runSSHConfigCommand(ctx, host, e.Port, e.User)
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
		case "hostname":
			resolved.Hostname = values[0]
		case "user":
			resolved.User = values[0]
		case "port":
			if port, err := strconv.Atoi(values[0]); err == nil && port > 0 {
				resolved.Port = port
			}
		case "hostkeyalias":
			resolved.HostKeyAlias = values[0]
		case "hostkeyalgorithms":
			resolved.HostKeyAlgorithms = appendAlgorithmValues(resolved.HostKeyAlgorithms, values...)
		case "identityfile":
			resolved.IdentityFiles = append(resolved.IdentityFiles, values...)
		case "identitiesonly":
			if len(values) > 0 && strings.ToLower(values[0]) == "yes" {
				resolved.IdentitiesOnly = true
			}
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
	if !resolved.IdentitiesOnly {
		for _, name := range []string{"id_ed25519", "id_rsa", "id_ecdsa"} {
			paths = append(paths, filepath.Join(home, ".ssh", name))
		}
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

func appendAlgorithmValues(existing []string, values ...string) []string {
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			existing = append(existing, part)
		}
	}
	return existing
}

func resolvedDialHost(resolved resolvedSSHConfig, fallback string) string {
	if host := strings.TrimSpace(resolved.Hostname); host != "" {
		return host
	}
	return fallback
}

func resolvedHostKeyName(resolved resolvedSSHConfig, fallback string) string {
	if alias := strings.TrimSpace(resolved.HostKeyAlias); alias != "" {
		return alias
	}
	return resolvedDialHost(resolved, fallback)
}

func resolvedPort(resolved resolvedSSHConfig, fallback int) int {
	if resolved.Port > 0 {
		return resolved.Port
	}
	return fallback
}

func preferredHostKeyAlgorithms(resolved resolvedSSHConfig, knownHostsPaths []string, hostKeyAddr string) []string {
	if explicit := uniqueNonEmptyPaths(resolved.HostKeyAlgorithms); len(explicit) > 0 {
		return explicit
	}
	return knownHostKeyAlgorithms(knownHostsPaths, hostKeyAddr)
}

func knownHostKeyAlgorithms(paths []string, hostKeyAddr string) []string {
	if len(paths) == 0 || strings.TrimSpace(hostKeyAddr) == "" {
		return nil
	}
	callback, err := knownhosts.New(paths...)
	if err != nil {
		return nil
	}
	probeKey, err := hostKeyProbePublicKey()
	if err != nil {
		return nil
	}

	err = callback(hostKeyAddr, probeRemoteAddr(hostKeyAddr), probeKey)
	var keyErr *knownhosts.KeyError
	if !errors.As(err, &keyErr) || len(keyErr.Want) == 0 {
		return nil
	}

	var algorithms []string
	for _, known := range keyErr.Want {
		algorithms = append(algorithms, expandHostKeyAlgorithm(known.Key.Type())...)
	}
	return uniqueNonEmptyPaths(algorithms)
}

func expandHostKeyAlgorithm(algo string) []string {
	switch algo {
	case ssh.KeyAlgoRSA:
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	default:
		return []string{algo}
	}
}

func hostKeyProbePublicKey() (ssh.PublicKey, error) {
	hostKeyProbeOnce.Do(func() {
		pub, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			hostKeyProbeErr = err
			return
		}
		hostKeyProbeKey, hostKeyProbeErr = ssh.NewPublicKey(pub)
	})
	return hostKeyProbeKey, hostKeyProbeErr
}
