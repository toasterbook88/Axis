package facts

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

type fakeRemoteExecutor struct {
	connectErr error
	closed     bool
	exact      map[string]fakeRunResult
	contains   map[string]fakeRunResult
	runs       []string
}

type fakeRunResult struct {
	out string
	err error
}

func (f *fakeRemoteExecutor) Connect(context.Context) error {
	return f.connectErr
}

func (f *fakeRemoteExecutor) Run(_ context.Context, cmd string) (string, error) {
	f.runs = append(f.runs, cmd)
	if res, ok := f.exact[cmd]; ok {
		return res.out, res.err
	}
	// Match pre-wrap keys against bash-forced form used by RemoteCollector.
	for k, res := range f.exact {
		if WrapBash(k) == cmd {
			return res.out, res.err
		}
	}
	for needle, res := range f.contains {
		if strings.Contains(cmd, needle) {
			return res.out, res.err
		}
	}
	return "", fmt.Errorf("unexpected command: %s", cmd)
}
func (f *fakeRemoteExecutor) RunWithStdin(ctx context.Context, cmd string, stdin []byte) (string, error) {
	return f.Run(ctx, cmd)
}

func (f *fakeRemoteExecutor) Close() error {
	f.closed = true
	return nil
}

func TestRemoteCollectorMarksUnreachableNode(t *testing.T) {
	exec := &fakeRemoteExecutor{connectErr: fmt.Errorf("ssh timeout")}
	collector := NewRemoteCollector("down", "worker", "down.local", exec)

	facts, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect unreachable facts: %v", err)
	}
	if facts.Status != models.StatusUnreachable {
		t.Fatalf("expected unreachable, got %s", facts.Status)
	}
	if !strings.Contains(facts.Error, "ssh timeout") {
		t.Fatalf("expected ssh timeout error, got %q", facts.Error)
	}
}

func TestRemoteCollectorMarksPartialOnCollectorFailures(t *testing.T) {
	// Bundle failure is recorded as partial; discovery probes still run.
	exec := &fakeRemoteExecutor{
		contains: map[string]fakeRunResult{
			"__AXIS_BUNDLE_V1__": {err: fmt.Errorf("bundle failed")},
		},
		exact: map[string]fakeRunResult{
			OllamaDiscoveryScript:      {err: fmt.Errorf("no ollama")},
			DiskWeightsDiscoveryScript: {out: `{"weights":[],"truncated":false}`},
		},
	}

	collector := NewRemoteCollector("linux-node", "worker", "linux-node.local", exec)
	facts, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect partial facts: %v", err)
	}
	if facts.Status != models.StatusPartial {
		t.Fatalf("expected partial status, got %s", facts.Status)
	}
	if facts.Hostname != "linux-node.local" {
		t.Fatalf("expected configured hostname fallback, got %q", facts.Hostname)
	}
	if len(facts.PartialReasons) == 0 {
		t.Fatal("expected fact_bundle partial reason")
	}
	if facts.Ollama != nil {
		t.Fatalf("expected ollama info to stay nil on discovery error, got %+v", facts.Ollama)
	}
}

func TestDetectRemoteHostnameRejectsEmptyFallback(t *testing.T) {
	exec := &fakeRemoteExecutor{
		exact: map[string]fakeRunResult{
			"hostname": {out: "\n"},
			"uname -n": {out: "\n"},
		},
	}

	hostname, err := detectRemoteHostname(context.Background(), exec)
	if err == nil {
		t.Fatal("expected empty fallback hostname to fail")
	}
	if hostname != "" {
		t.Fatalf("hostname = %q, want empty string", hostname)
	}
}

func TestDetectLocalDarwinIdentityFailure(t *testing.T) {
	prev := runLocalIdentityCommand
	t.Cleanup(func() { runLocalIdentityCommand = prev })

	runLocalIdentityCommand = func(context.Context, string, ...string) (string, error) {
		return "", fmt.Errorf("ioreg not found")
	}

	if id := detectLocalDarwinIdentity(context.Background()); id != nil {
		t.Fatalf("expected nil on ioreg failure, got %+v", id)
	}
}

func TestEnsureAppleFoundationModelsHelperSourceReadError(t *testing.T) {
	prev := appleFoundationModelsReadFileFn
	t.Cleanup(func() { appleFoundationModelsReadFileFn = prev })

	appleFoundationModelsReadFileFn = func(string) ([]byte, error) {
		return nil, fmt.Errorf("permission denied")
	}

	if err := ensureAppleFoundationModelsHelperSource("/some/path.swift"); err == nil {
		t.Fatal("expected read error to propagate")
	}
}

func TestEnsureAppleFoundationModelsHelperSourceWriteError(t *testing.T) {
	prevRead := appleFoundationModelsReadFileFn
	prevWrite := appleFoundationModelsWriteFileFn
	t.Cleanup(func() {
		appleFoundationModelsReadFileFn = prevRead
		appleFoundationModelsWriteFileFn = prevWrite
	})

	appleFoundationModelsReadFileFn = func(string) ([]byte, error) {
		return nil, os.ErrNotExist
	}
	appleFoundationModelsWriteFileFn = func(string, []byte, os.FileMode) error {
		return fmt.Errorf("disk full")
	}

	if err := ensureAppleFoundationModelsHelperSource("/some/path.swift"); err == nil {
		t.Fatal("expected write error to propagate")
	}
}

func TestAppleFoundationModelsHelperUpToDateStatSourceError(t *testing.T) {
	prev := appleFoundationModelsReadFileFn
	t.Cleanup(func() { appleFoundationModelsReadFileFn = prev })

	appleFoundationModelsReadFileFn = func(name string) ([]byte, error) {
		if strings.Contains(name, ".swift") {
			return nil, fmt.Errorf("stat error")
		}
		return nil, os.ErrNotExist
	}

	_, err := appleFoundationModelsHelperUpToDate("/some/source.swift", "/some/binary")
	if err == nil {
		t.Fatal("expected stat source error to propagate")
	}
}

func TestDiscoverOllamaLocalErrorPath(t *testing.T) {
	prev := runOllamaDiscoveryFn
	t.Cleanup(func() { runOllamaDiscoveryFn = prev })

	runOllamaDiscoveryFn = func(context.Context) ([]byte, error) {
		return nil, fmt.Errorf("bash not found")
	}

	info, models := discoverOllamaLocal(context.Background())
	if info.Installed {
		t.Fatal("expected not installed on error")
	}
	if info.Error != "bash not found" {
		t.Fatalf("unexpected error: %q", info.Error)
	}
	if models != nil {
		t.Fatal("expected nil models on error")
	}
}

func TestDiscoverLlamaServerLocalErrorPath(t *testing.T) {
	prev := runLlamaServerDiscoveryFn
	t.Cleanup(func() { runLlamaServerDiscoveryFn = prev })

	runLlamaServerDiscoveryFn = func(context.Context) ([]byte, error) {
		return nil, fmt.Errorf("bash not found")
	}

	if models := discoverLlamaServerLocal(context.Background()); models != nil {
		t.Fatalf("expected nil on error, got %v", models)
	}
}

func TestDiscoverMLXLocalErrorPath(t *testing.T) {
	prev := runMLXDiscoveryFn
	t.Cleanup(func() { runMLXDiscoveryFn = prev })

	runMLXDiscoveryFn = func(context.Context) ([]byte, error) {
		return nil, fmt.Errorf("bash not found")
	}

	if models := discoverMLXLocal(context.Background()); models != nil {
		t.Fatalf("expected nil on error, got %v", models)
	}
}
