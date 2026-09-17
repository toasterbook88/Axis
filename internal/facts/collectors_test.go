package facts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/transport"
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
		if transport.WrapBash(k) == cmd {
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

// --- Ollama discovery script: hermetic, real bash -c -------------------------
//
// The ollama probe ships as a bash script executed via `bash -c`. Its tests
// historically keyed a fake executor on the script *string* and returned canned
// JSON, so the script itself was never executed and two compounding defects in
// its `running` expression went unobserved:
//
//  1. self-match — `pgrep -f "$OLLAMA_BIN"` matched the probe's own `bash -c`
//     argv, because that argv embeds the script text, which contained the
//     literal binary path in its fallback list;
//  2. quote-loss — the value came from `$( [ -n \"$PGREP\" ] && echo true ... )`
//     inside a double-quoted echo, where the escaped quotes reach `test` as
//     literal characters instead of quoting the expansion.
//
// Together they produced `[: too many arguments` -> `running:false` on a host
// serving models, and (quotes intact, nothing matched) `running:true` on a host
// with no daemon at all. These tests execute the real script through `bash -c`
// with a sandboxed PATH and a controlled process table.

// writeFactStub installs an executable stub named `name` into dir.
func writeFactStub(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// fakeProcPgrepStub returns a `pgrep` stub that searches a controlled process
// table under $FAKE_PROC_ROOT instead of the live host's /proc. Each file in
// that root is one process cmdline. This keeps the self-match scenario testable
// without the real host's processes contaminating the result.
const fakeProcPgrepStub = `
pat=""
exact=0
while [ $# -gt 0 ]; do
  case "$1" in
    -f) ;;
    -x) exact=1;;
    -*) ;;
    *) pat="$1";;
  esac
  shift
done
root="${FAKE_PROC_ROOT:-}"
[ -n "$root" ] || exit 1
n=0
for f in "$root"/*; do
  [ -f "$f" ] || continue
  n=$((n+1))
  if [ "$exact" = "1" ]; then
    first=$(tr '\0' ' ' < "$f" | awk '{print $1}')
    if [ "$(basename "$first")" = "$pat" ]; then echo $((4000+n)); fi
  else
    if grep -qE -- "$pat" "$f" 2>/dev/null; then echo $((4000+n)); fi
  fi
done
exit 0
`

// ollamaProbeSandbox builds a sandboxed PATH containing a stub ollama binary,
// the given pgrep implementation, and stubs for every other host tool the script
// touches, so the probe runs hermetically with no network access.
func ollamaProbeSandbox(t *testing.T, pgrepBody string) []string {
	t.Helper()
	bin := t.TempDir()
	writeFactStub(t, bin, "ollama", `case "$1" in
  list) echo "NAME ID SIZE"; echo "stub:7b aaa 1GB";;
  ps) echo "";;
  --version) echo "ollama version 0.0.0-stub";;
  *) echo "";;
esac`)
	writeFactStub(t, bin, "pgrep", pgrepBody)
	writeFactStub(t, bin, "lsof", "exit 1")
	writeFactStub(t, bin, "ss", "exit 1")
	writeFactStub(t, bin, "netstat", "exit 1")
	writeFactStub(t, bin, "curl", "exit 1")
	return withSandboxedPATH(bin)
}

// runOllamaProbe executes the real discovery script through `bash -c` — the
// exact invocation used by internal/facts/local.go — and returns its payload.
func runOllamaProbe(t *testing.T, env []string) models.OllamaInfo {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	cmd := exec.Command("bash", "-c", OllamaDiscoveryScript)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script: %v\n%s", err, out)
	}
	var payload ollamaDiscoveryPayload
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json %q: %v", bytes.TrimSpace(out), err)
	}
	return payload.OllamaInfo
}

// TestOllamaDiscoveryScriptReportsRunningFromDaemon is the positive control:
// with a daemon in the process table the probe must report running.
func TestOllamaDiscoveryScriptReportsRunningFromDaemon(t *testing.T) {
	procRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(procRoot, "1"), []byte("/usr/local/bin/ollama serve"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := append(ollamaProbeSandbox(t, fakeProcPgrepStub), "FAKE_PROC_ROOT="+procRoot)
	if got := runOllamaProbe(t, env); !got.Running {
		t.Fatalf("running = false with a daemon present; want true")
	}
}

// TestOllamaDiscoveryScriptReportsNotRunningWhenNothingMatches is the
// false-positive regression. The historical expression reported `running:true`
// when pgrep matched nothing, because the test was `[ -n "$PGREP" ]` with the
// quotes passed to `test` literally and then word-split. A host with no daemon
// must report running=false.
func TestOllamaDiscoveryScriptReportsNotRunningWhenNothingMatches(t *testing.T) {
	procRoot := t.TempDir() // empty process table
	env := append(ollamaProbeSandbox(t, fakeProcPgrepStub), "FAKE_PROC_ROOT="+procRoot)
	got := runOllamaProbe(t, env)
	if got.Running {
		t.Fatalf("running = true with an empty process table; want false")
	}
	if !got.Installed {
		t.Fatalf("installed = false; the stub binary should satisfy discovery")
	}
}

// TestOllamaDiscoveryScriptIgnoresProbeSelfMatch is the self-match regression.
// The probe's own `bash -c` argv embeds this script's text. When that text
// contained the literal binary path, `pgrep -f` matched the probe itself and the
// host reported as running with no daemon. Here the process table holds exactly
// that text and nothing else; the probe must report running=false.
func TestOllamaDiscoveryScriptIgnoresProbeSelfMatch(t *testing.T) {
	procRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(procRoot, "1"), []byte("bash -c "+OllamaDiscoveryScript), 0o644); err != nil {
		t.Fatal(err)
	}
	env := append(ollamaProbeSandbox(t, fakeProcPgrepStub), "FAKE_PROC_ROOT="+procRoot)
	if got := runOllamaProbe(t, env); got.Running {
		t.Fatal("running = true, but the only match was the probe's own script text (self-match)")
	}
}

// TestOllamaDiscoveryScriptToleratesMultiplePIDs guards the `[: too many
// arguments` failure: a pgrep that emits several numeric lines must not corrupt
// the `running` value or the JSON.
func TestOllamaDiscoveryScriptToleratesMultiplePIDs(t *testing.T) {
	env := ollamaProbeSandbox(t, `echo 4242; echo 9999; echo 12345`)
	got := runOllamaProbe(t, env)
	if !got.Running {
		t.Fatalf("running = false with numeric PIDs present; want true")
	}
}

// TestOllamaDiscoveryScriptRejectsNonNumericPgrepOutput guards the numeric
// validation: a non-PID line (e.g. a cmdline echoed by a misbehaving pgrep) must
// not be accepted as evidence of a running daemon.
func TestOllamaDiscoveryScriptRejectsNonNumericPgrepOutput(t *testing.T) {
	env := ollamaProbeSandbox(t, `echo "bash -c set -o pipefail"`)
	if got := runOllamaProbe(t, env); got.Running {
		t.Fatalf("running = true for non-numeric pgrep output; want false")
	}
}
