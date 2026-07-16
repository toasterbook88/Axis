package facts

import (
	"context"
	"strings"

	"al.essio.dev/pkg/shellescape"

	"github.com/toasterbook88/axis/internal/transport"
)

// WrapBash returns a remote command that runs cmd under bash without profile/rc.
//
// Critical: sshd executes remote commands as `$SHELL -c "<string>"`. When $SHELL
// is fish, the string must be valid fish syntax (or a pure external command
// line). POSIX constructs like `name=value`, `for ...; do`, and `[ ]` fail under
// fish before bash is ever reached — which is exactly the slow-shell case this
// wrapper targets.
//
// Therefore the outer form is a pure external invocation with no shell logic:
//
//	/usr/bin/env bash --noprofile --norc -c '<script>'
//
// fish, bash, zsh, and dash all treat this as "run program env with args…".
// `env` resolves bash on PATH (works on NixOS non-FHS layouts). Absolute
// fallbacks are tried only via env's PATH, not via shell loops.
//
// Multi-session collects still pay login-shell startup once per session; prefer
// the one-shot fact bundle for remote nodes.
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

// bashForcedExecutor wraps an Executor so every Run is executed under bash.
type bashForcedExecutor struct {
	inner transport.Executor
}

func withBashForced(exec transport.Executor) transport.Executor {
	if exec == nil {
		return nil
	}
	if _, ok := exec.(*bashForcedExecutor); ok {
		return exec
	}
	return &bashForcedExecutor{inner: exec}
}

func (e *bashForcedExecutor) Connect(ctx context.Context) error {
	return e.inner.Connect(ctx)
}

func (e *bashForcedExecutor) Close() error {
	return e.inner.Close()
}

func (e *bashForcedExecutor) Run(ctx context.Context, cmd string) (string, error) {
	return e.inner.Run(ctx, WrapBash(cmd))
}

// HandshakeLatencyMs forwards to the inner executor when available.
func (e *bashForcedExecutor) HandshakeLatencyMs() int64 {
	if h, ok := e.inner.(interface{ HandshakeLatencyMs() int64 }); ok {
		return h.HandshakeLatencyMs()
	}
	return 0
}

// ConnectedHost forwards the host that actually connected (may be a dial fallback).
func (e *bashForcedExecutor) ConnectedHost() string {
	if h, ok := e.inner.(interface{ ConnectedHost() string }); ok {
		return h.ConnectedHost()
	}
	return ""
}
