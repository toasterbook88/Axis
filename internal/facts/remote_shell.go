package facts

import (
	"context"
	"strings"

	"al.essio.dev/pkg/shellescape"

	"github.com/toasterbook88/axis/internal/transport"
)

// WrapBash returns a remote command that runs cmd under bash without profile/rc.
// The login shell may still wrap the outer invocation (sshd: $SHELL -c …), so
// multi-session collects remain expensive on slow login shells; prefer the
// one-shot fact bundle for remote nodes.
//
// Uses /bin/bash when present on typical Linux/macOS hosts. If /bin/bash is
// missing, sshd still surfaces a clear error and the collector falls back to
// partial/unreachable handling — same as any other remote command failure.
func WrapBash(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return cmd
	}
	// Already wrapped.
	if strings.HasPrefix(cmd, "/bin/bash --noprofile --norc -c ") {
		return cmd
	}
	return "/bin/bash --noprofile --norc -c " + shellescape.Quote(cmd)
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
