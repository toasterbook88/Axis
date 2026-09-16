package transport

import (
	"context"
	"fmt"
	"io"
	"strings"

	"al.essio.dev/pkg/shellescape"
)

// WrapBash returns a remote command that runs cmd under bash without profile/rc.
//
// Critical: sshd executes remote commands as `$SHELL -c "<string>"`. When $SHELL
// is fish, the string must be valid fish syntax (or a pure external command
// line). POSIX constructs like `name=value`, `for ...; do`, and `[ ]` fail under
// fish before bash is ever reached — which is exactly the slow-shell case this
// wrapper targets (observed live 2026-09-15: guarded-execution run commands
// built by RemoteExecPrefix failed with fish "Unsupported use of '='" and
// status 127 on cachyos).
//
// Therefore the outer form is a pure external invocation with no shell logic:
//
//	/usr/bin/env bash --noprofile --norc -c '<script>'
//
// fish, bash, zsh, and dash all treat this as "run program env with args…".
// `env` resolves bash on PATH (works on NixOS non-FHS layouts).
func WrapBash(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return cmd
	}
	// Already wrapped.
	if strings.HasPrefix(cmd, "/usr/bin/env bash --noprofile --norc -c ") {
		return cmd
	}
	return "/usr/bin/env bash --noprofile --norc -c " + shellescape.Quote(cmd)
}

// BashForcedExecutor wraps an Executor so every command is delivered through
// WrapBash — a pure external `/usr/bin/env bash -c '<script>'` invocation the
// remote login shell (bash, zsh, dash, or fish) can exec without parsing
// POSIX shell syntax itself.
type BashForcedExecutor struct {
	inner Executor
}

// WithBashForced wraps exec so every Run/RunWithStdin/Stream presents the
// remote side a fish-safe bash launcher. Idempotent; returns nil for nil.
// Optional capability interfaces the guarded-execution path type-asserts on
// (streaming, stdin delivery, port forwarding) are forwarded so the wrapper
// is transparent to callers.
func WithBashForced(exec Executor) Executor {
	if exec == nil {
		return nil
	}
	if _, ok := exec.(*BashForcedExecutor); ok {
		return exec
	}
	return &BashForcedExecutor{inner: exec}
}

func (e *BashForcedExecutor) Connect(ctx context.Context) error {
	return e.inner.Connect(ctx)
}

// RunWithStdin forwards stdin through the bash wrapper: `/usr/bin/env bash
// --noprofile --norc -c "cmd"` passes its own stdin to the executed command,
// so payload bytes reach the remote pipeline. The inner executor must implement
// StdinRemoteExecutor.
func (e *BashForcedExecutor) RunWithStdin(ctx context.Context, cmd string, stdin []byte) (string, error) {
	stdinExec, ok := e.inner.(StdinRemoteExecutor)
	if !ok {
		return "", fmt.Errorf("inner executor %T does not support stdin delivery", e.inner)
	}
	return stdinExec.RunWithStdin(ctx, WrapBash(cmd), stdin)
}

func (e *BashForcedExecutor) Run(ctx context.Context, cmd string) (string, error) {
	return e.inner.Run(ctx, WrapBash(cmd))
}

func (e *BashForcedExecutor) Close() error {
	return e.inner.Close()
}

// HandshakeLatencyMs forwards to the inner executor when available.
func (e *BashForcedExecutor) HandshakeLatencyMs() int64 {
	if h, ok := e.inner.(interface{ HandshakeLatencyMs() int64 }); ok {
		return h.HandshakeLatencyMs()
	}
	return 0
}

// ConnectedHost forwards the host that actually connected (may be a dial fallback).
func (e *BashForcedExecutor) ConnectedHost() string {
	if h, ok := e.inner.(interface{ ConnectedHost() string }); ok {
		return h.ConnectedHost()
	}
	return ""
}

// Stream forwards realtime streaming output through the bash wrapper.
func (e *BashForcedExecutor) Stream(ctx context.Context, cmd string, stdout, stderr io.Writer) error {
	streamer, ok := e.inner.(interface {
		Stream(context.Context, string, io.Writer, io.Writer) error
	})
	if !ok {
		// Inner has no streaming; degrade to Run with combined output going
		// to stdout so callers still receive the bytes.
		out, err := e.inner.Run(ctx, WrapBash(cmd))
		if stdout != nil {
			_, _ = io.WriteString(stdout, out)
		}
		return err
	}
	return streamer.Stream(ctx, WrapBash(cmd), stdout, stderr)
}

// ForwardLocal forwards ephemeral SSH port forwarding when the inner executor
// supports it. Forwarding setup commands are external invocations already;
// they are not POSIX scripts and need no wrapping.
func (e *BashForcedExecutor) ForwardLocal(ctx context.Context, localPort, remotePort int) (int, func(), error) {
	forwarder, ok := e.inner.(interface {
		ForwardLocal(context.Context, int, int) (int, func(), error)
	})
	if !ok {
		return 0, nil, errNoPortForwarding
	}
	return forwarder.ForwardLocal(ctx, localPort, remotePort)
}

var errNoPortForwarding = fmt.Errorf("inner executor does not support port forwarding")
