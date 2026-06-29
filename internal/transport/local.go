package transport

import (
	"context"
	"fmt"
	"io"
	"os/exec"
)

// LocalExecutor executes commands directly on the local machine using os/exec.
// It bypasses SSH entirely, resolving loopback connectivity issues.
type LocalExecutor struct{}

// NewLocalExecutor creates a new local executor.
func NewLocalExecutor() *LocalExecutor {
	return &LocalExecutor{}
}

// Connect is a no-op for local execution.
func (e *LocalExecutor) Connect(ctx context.Context) error {
	return nil
}

// Run executes a command locally and returns stdout.
func (e *LocalExecutor) Run(ctx context.Context, cmd string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	// We use bash -c to preserve identical semantics to the SSH path,
	// which executes the raw command string in the user's remote shell.
	c := exec.CommandContext(ctx, "bash", "-c", cmd)
	out, err := c.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("local run %q: %w", cmd, err)
	}
	return string(out), nil
}

// Stream runs a command locally with realtime output to the provided writers.
func (e *LocalExecutor) Stream(ctx context.Context, cmd string, stdout, stderr io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c := exec.CommandContext(ctx, "bash", "-c", cmd)
	c.Stdout = stdout
	c.Stderr = stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("local stream %q: %w", cmd, err)
	}
	return nil
}

// ForwardLocal on the local machine simply returns the target remotePort,
// as the "remote" service is already available locally.
func (e *LocalExecutor) ForwardLocal(ctx context.Context, localPort, remotePort int) (int, func(), error) {
	return remotePort, func() {}, nil
}

// Close is a no-op for local execution.
func (e *LocalExecutor) Close() error {
	return nil
}
