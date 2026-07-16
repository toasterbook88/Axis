package facts

import (
	"context"
	"strings"

	"al.essio.dev/pkg/shellescape"

	"github.com/toasterbook88/axis/internal/transport"
)

// WrapBash returns a remote command that runs cmd under bash without profile/rc.
//
// The login shell may still wrap the outer invocation (sshd: $SHELL -c …), so
// multi-session collects remain expensive on slow login shells; prefer the
// one-shot fact bundle for remote nodes.
//
// Bash is resolved via PATH first, then common absolute locations including
// NixOS (/run/current-system/sw/bin/bash). Hard-coding only /bin/bash breaks
// non-FHS hosts.
func WrapBash(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return cmd
	}
	// Already wrapped with our launcher.
	if strings.Contains(cmd, "axis_bash_launcher") && strings.Contains(cmd, "--noprofile --norc -c ") {
		return cmd
	}
	quoted := shellescape.Quote(cmd)
	// Portable launcher: PATH bash, then FHS and NixOS locations.
	return `axis_bash_launcher=1; B=$(command -v bash 2>/dev/null || true); ` +
		`for c in /bin/bash /usr/bin/bash /run/current-system/sw/bin/bash; do ` +
		`[ -n "$B" ] && break; [ -x "$c" ] && B=$c; done; ` +
		`if [ -z "$B" ]; then printf '%s\n' 'axis: bash not found on remote PATH' >&2; exit 127; fi; ` +
		`exec "$B" --noprofile --norc -c ` + quoted
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
