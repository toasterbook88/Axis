package execution

import (
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/transport"
)

// Regression (follow-up to #430): PR 430 moved the execution context off the
// SSH command string (stdin delivery), but the guarded-exec RUN command still
// begins with POSIX assignments — `export BEST_NODE=… ; _axis_ctx=…; trap …;
// bash -lc …` — built by RemoteExecPrefix. sshd runs remote commands as
// `$SHELL -c "<string>"`, and on nodes whose login shell is fish (observed:
// cachyos) fish rejects the assignments with
// "Unsupported use of '='. In fish, please use 'set …'" and exits 127 BEFORE
// bash -lc ever starts. Live-reproduced 2026-09-15: pinned run_on_node cachyos
// fails with status 127 while cranium (bash) succeeds.
// The execution path must wrap its executor in the same fish-safe bash
// launcher the facts collector already uses (/usr/bin/env bash … -c '<cmd>'),
// so every command — context write AND run command — is a pure external
// invocation the login shell can exec.
func TestRemoteExecutorWrapsBashForFishSafeDispatch(t *testing.T) {
	exec := NewRemoteExecutor(config.NodeConfig{
		Name:     "fish-node",
		Hostname: "fish-node.local",
		SSHUser:  "axis",
	})
	if exec == nil {
		t.Fatal("NewRemoteExecutor returned nil")
	}

	// The wrapper must be in the delivery chain: running any command through
	// the returned executor must present the remote side a /usr/bin/env bash
	// launcher, never a raw POSIX assignment string. Assert structurally via
	// the transport-level wrapper contract instead of dialing a real node.
	wrapped := transport.WithBashForced(toTransportExecutor(exec))
	if wrapped == nil {
		t.Fatal("WithBashForced returned nil")
	}
	if _, ok := wrapped.(RemoteExecutor); !ok {
		t.Fatalf("wrapper must satisfy execution.RemoteExecutor, got %T", wrapped)
	}

	// Idempotent: wrapping the wrapper again must return the same wrapper,
	// never a double wrap (each layer would quote the script one more time).
	if !isBashForced(transport.WithBashForced(toTransportExecutor(wrapped.(RemoteExecutor)))) || !isBashForced(wrapped) {
		t.Fatal("WithBashForced must be idempotent (no double wrap)")
	}

	// The wrapper must preserve the optional capability interfaces the
	// execution path type-asserts on.
	if _, ok := wrapped.(StreamingRemoteExecutor); !ok {
		t.Fatal("wrapper must forward StreamingRemoteExecutor (runRemoteWithOutput relies on it)")
	}
	if _, ok := wrapped.(transport.StdinRemoteExecutor); !ok {
		t.Fatal("wrapper must forward StdinRemoteExecutor (stdin context delivery relies on it)")
	}
	if _, ok := wrapped.(PortForwardingRemoteExecutor); !ok {
		t.Fatal("wrapper must forward PortForwardingRemoteExecutor (expose-ports relies on it)")
	}
}

// toTransportExecutor adapts the execution-side RemoteExecutor to the
// transport.Executor interface for wrapper tests. NewRemoteExecutor returns
// *transport.SSHExecutor, which satisfies both.
func toTransportExecutor(re RemoteExecutor) transport.Executor {
	if te, ok := re.(transport.Executor); ok {
		return te
	}
	return nil
}

func isBashForced(e RemoteExecutor) bool {
	_, ok := e.(*transport.BashForcedExecutor)
	return ok
}

func TestWrapBashNeutralizesPOSIXAssignmentsForFish(t *testing.T) {
	// The exact command shape runRemote builds (RemoteExecPrefix + trap +
	// bash -lc). Fish must never see it as shell syntax.
	cmd := `export BEST_NODE="cachyos" AXIS_CONTEXT_FILE="/tmp/axis-knows-1.json" AXIS_EXECUTION_MODE=exec; ` +
		`_axis_ctx="/tmp/axis-knows-1.json"; trap 'rm -f "$_axis_ctx"' EXIT; bash -lc 'echo hi'`

	wrapped := transport.WrapBash(cmd)
	if !strings.HasPrefix(wrapped, "/usr/bin/env bash --noprofile --norc -c ") {
		t.Fatalf("expected pure external bash launcher prefix, got %q", wrapped[:60])
	}
	// The original POSIX string must survive verbatim INSIDE the quoted
	// script argument — bash parses it, fish never does.
	if !strings.Contains(wrapped, `bash -lc`) || !strings.Contains(wrapped, `export BEST_NODE`) {
		t.Fatalf("script body lost in wrapping: %q", wrapped)
	}
}
