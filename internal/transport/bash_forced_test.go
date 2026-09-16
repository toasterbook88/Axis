package transport

import (
	"context"
	"testing"
)

// recordingExecutor captures every Run/RunWithStdin invocation.
type recordingExecutor struct {
	runs      []string
	stdinRuns []struct {
		cmd   string
		stdin []byte
	}
}

func (r *recordingExecutor) Connect(ctx context.Context) error { return nil }
func (r *recordingExecutor) Close() error                      { return nil }
func (r *recordingExecutor) Run(ctx context.Context, cmd string) (string, error) {
	r.runs = append(r.runs, cmd)
	return "", nil
}
func (r *recordingExecutor) RunWithStdin(ctx context.Context, cmd string, stdin []byte) (string, error) {
	r.stdinRuns = append(r.stdinRuns, struct {
		cmd   string
		stdin []byte
	}{cmd, stdin})
	return "", nil
}

func TestBashForcedExecutorRunDeliversWrappedCommand(t *testing.T) {
	inner := &recordingExecutor{}
	wrapped := WithBashForced(inner).(*BashForcedExecutor)

	if _, err := wrapped.Run(context.Background(), "echo hi"); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(inner.runs) != 1 {
		t.Fatalf("expected 1 inner run, got %d", len(inner.runs))
	}
	want := WrapBash("echo hi")
	if inner.runs[0] != want {
		t.Errorf("inner.Run cmd = %q, want %q", inner.runs[0], want)
	}
}

func TestBashForcedExecutorRunWithStdinDeliversWrappedCommandAndBytes(t *testing.T) {
	inner := &recordingExecutor{}
	wrapped := WithBashForced(inner).(*BashForcedExecutor)

	stdin := []byte("payload")
	if _, err := wrapped.RunWithStdin(context.Background(), "cat", stdin); err != nil {
		t.Fatalf("RunWithStdin error: %v", err)
	}
	if len(inner.stdinRuns) != 1 {
		t.Fatalf("expected 1 inner RunWithStdin, got %d", len(inner.stdinRuns))
	}
	want := WrapBash("cat")
	if inner.stdinRuns[0].cmd != want {
		t.Errorf("inner.RunWithStdin cmd = %q, want %q", inner.stdinRuns[0].cmd, want)
	}
	if string(inner.stdinRuns[0].stdin) != "payload" {
		t.Errorf("inner.RunWithStdin stdin = %q, want %q", inner.stdinRuns[0].stdin, "payload")
	}
}
