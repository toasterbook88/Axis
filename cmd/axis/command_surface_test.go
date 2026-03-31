package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

func TestTaskCommandSurfaceWiresSubcommands(t *testing.T) {
	cmd := taskCmd()
	if got := cmd.Name(); got != "task" {
		t.Fatalf("taskCmd name = %q, want task", got)
	}

	var names []string
	for _, child := range cmd.Commands() {
		names = append(names, child.Name())
	}
	want := []string{"context", "place", "run"}
	for _, name := range want {
		if !containsString(names, name) {
			t.Fatalf("expected task subcommand %q, got %v", name, names)
		}
	}
}

func TestCommandSurfacesWireExpectedSubcommands(t *testing.T) {
	tests := []struct {
		cmd  *cobra.Command
		want []string
	}{
		{taskCmd(), []string{"place", "context", "run"}},
		{daemonCmd(), []string{"status", "invalidate", "refresh", "restart"}},
		{mcpCmd(), []string{"serve"}},
		{scriptsCmd(), []string{"list"}},
		{contextCmd(), []string{"show", "clear"}},
	}

	for _, tt := range tests {
		for _, name := range tt.want {
			if _, _, err := tt.cmd.Find([]string{name}); err != nil {
				t.Fatalf("%s missing subcommand %q: %v", tt.cmd.Use, name, err)
			}
		}
	}
}

func TestRootCommandShowsHelpInsteadOfRoutingToChat(t *testing.T) {
	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := newRootCmd()
		cmd.SetArgs(nil)
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("root Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
	if !strings.Contains(stdout, "Available Commands:") || !strings.Contains(stdout, "facts") || !strings.Contains(stdout, "task") {
		t.Fatalf("expected root help output, got %q", stdout)
	}
	if strings.Contains(stdout, "discover") {
		t.Fatalf("expected discover to be removed from root help, got %q", stdout)
	}
}

func TestVersionCommandPrintsVersion(t *testing.T) {
	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := versionCmd()
		cmd.SetArgs(nil)
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("versionCmd Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
	if !strings.Contains(stdout, "axis "+Version) {
		t.Fatalf("expected version output, got %q", stdout)
	}
}

func TestScriptsListCommandPrintsRegistry(t *testing.T) {
	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := scriptsCmd()
		cmd.SetArgs([]string{"list"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("scripts list Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
	if !strings.Contains(stdout, "AVAILABLE MOLE-STYLE SCRIPTS:") {
		t.Fatalf("expected scripts banner, got %q", stdout)
	}
	if !strings.Contains(stdout, "Run a script with: axis task run --script") {
		t.Fatalf("expected usage hint, got %q", stdout)
	}
}

func TestMCPServeCmdRejectsUnsupportedTransport(t *testing.T) {
	cmd := mcpServeCmd()
	cmd.SetArgs([]string{"--transport", "http"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected unsupported transport error")
	}
	if !strings.Contains(err.Error(), `unsupported transport "http"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMCPServeCmdCallsServeStdio(t *testing.T) {
	restore := stubServeMCPStdio(t, func(cached bool, addr string) error {
		if !cached {
			t.Fatal("expected cached=true")
		}
		if addr != "127.0.0.1:5000" {
			t.Fatalf("cache addr = %q, want 127.0.0.1:5000", addr)
		}
		return nil
	})
	defer restore()

	cmd := mcpServeCmd()
	cmd.SetArgs([]string{"--cached", "--cache-addr", "127.0.0.1:5000"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("mcp serve Execute: %v", err)
	}
}

func TestServeCmdStartsDaemonAndCallsServeAPI(t *testing.T) {
	fake := &fakeServeDaemon{}
	restoreDaemon := stubServeDaemonFactory(t, func(time.Duration) serveDaemon {
		return fake
	})
	defer restoreDaemon()
	restoreServe := stubServeHTTPAPI(t, func(addr string, d serveDaemon, token string) error {
		if addr != "127.0.0.1:5151" {
			t.Fatalf("addr = %q, want 127.0.0.1:5151", addr)
		}
		if d != fake {
			t.Fatalf("expected injected daemon instance")
		}
		return nil
	})
	defer restoreServe()

	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := serveCmd()
		cmd.SetArgs([]string{"--addr", "127.0.0.1:5151", "--refresh", "2m"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("serve Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
	if !fake.started {
		t.Fatal("expected daemon Start to be called")
	}
	if !strings.Contains(stdout, "AXIS HTTP API listening on http://127.0.0.1:5151") {
		t.Fatalf("expected serve banner, got %q", stdout)
	}
}

func TestServeSurfaceUsesUnifiedHealthPath(t *testing.T) {
	mux := http.NewServeMux()
	daemon.RegisterRoutes(mux, nil)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from /health, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("expected health payload, got %q", rec.Body.String())
	}

	redirect := httptest.NewRecorder()
	mux.ServeHTTP(redirect, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if redirect.Code != http.StatusPermanentRedirect {
		t.Fatalf("expected 308 from /healthz, got %d", redirect.Code)
	}
	if got := redirect.Header().Get("Location"); got != "/health" {
		t.Fatalf("expected /health redirect, got %q", got)
	}
}

func TestServeSurfaceUsesUnifiedToolsPath(t *testing.T) {
	mux := http.NewServeMux()
	daemon.RegisterRoutes(mux, nil)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tools", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from /tools, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"axis_execute"`) || !strings.Contains(rec.Body.String(), `"axis_knowledge"`) {
		t.Fatalf("expected tool catalog, got %q", rec.Body.String())
	}

	redirect := httptest.NewRecorder()
	mux.ServeHTTP(redirect, httptest.NewRequest(http.MethodGet, "/mcp/tools", nil))
	if redirect.Code != http.StatusPermanentRedirect {
		t.Fatalf("expected 308 from /mcp/tools, got %d", redirect.Code)
	}
	if got := redirect.Header().Get("Location"); got != "/tools" {
		t.Fatalf("expected /tools redirect, got %q", got)
	}
}

func TestFactsCmdUsesCollectorOutput(t *testing.T) {
	restoreHost := stubCurrentHostname(t, func() (string, error) {
		return "test-host", nil
	})
	defer restoreHost()
	restoreFacts := stubCollectLocalFacts(t, func(context.Context, string) (*models.NodeFacts, error) {
		return &models.NodeFacts{Name: "test-host", Status: models.StatusComplete}, nil
	})
	defer restoreFacts()

	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := factsCmd()
		cmd.SetArgs([]string{"--format", "yaml"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("facts Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
	if !strings.Contains(stdout, "name: test-host") {
		t.Fatalf("expected YAML facts output, got %q", stdout)
	}
}

func TestFactsCmdFallsBackToErrorNodeOnCollectorError(t *testing.T) {
	restoreHost := stubCurrentHostname(t, func() (string, error) {
		return "test-host", nil
	})
	defer restoreHost()
	restoreFacts := stubCollectLocalFacts(t, func(context.Context, string) (*models.NodeFacts, error) {
		return nil, errors.New("boom")
	})
	defer restoreFacts()

	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := factsCmd()
		cmd.SetArgs(nil)
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("facts Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
	if !strings.Contains(stdout, `"status": "error"`) || !strings.Contains(stdout, `"name": "test-host"`) {
		t.Fatalf("expected error node output, got %q", stdout)
	}
}

func TestDiscoverLiveSnapshotUsesRuntime(t *testing.T) {
	restore := stubStatusRuntimeLoader(t, func(context.Context) (*runtimectx.Context, error) {
		return &runtimectx.Context{
			Snapshot: &models.ClusterSnapshot{
				Summary: models.ClusterSummary{TotalNodes: 2},
			},
		}, nil
	})
	defer restore()

	snap, source, err := discoverLiveSnapshot(context.Background())
	if err != nil {
		t.Fatalf("discoverLiveSnapshot: %v", err)
	}
	if source != "live" {
		t.Fatalf("source = %q, want live", source)
	}
	if snap.Summary.TotalNodes != 2 {
		t.Fatalf("expected total nodes 2, got %d", snap.Summary.TotalNodes)
	}
}

func TestStatusCmdUsesLiveLoaderForOutput(t *testing.T) {
	restoreLive := stubStatusLiveLoader(t, func(context.Context) (*models.ClusterSnapshot, string, error) {
		return &models.ClusterSnapshot{
			Summary: models.ClusterSummary{TotalNodes: 2},
		}, "live", nil
	})
	defer restoreLive()

	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := statusCmd()
		cmd.SetArgs([]string{"--format", "json"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("status Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
	if !strings.Contains(stdout, `"total_nodes": 2`) {
		t.Fatalf("expected live snapshot JSON, got %q", stdout)
	}
}

func TestStatusCmdUsesCacheWrapperWhenRequested(t *testing.T) {
	restoreCache := stubStatusCachedLoader(t, func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return &models.ClusterSnapshot{
			Summary: models.ClusterSummary{TotalNodes: 3},
		}, "daemon-cache", nil
	})
	defer restoreCache()

	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := statusCmd()
		cmd.SetArgs([]string{"--cached", "--format", "json"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("status Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
	if !strings.Contains(stdout, `"source": "daemon-cache"`) || !strings.Contains(stdout, `"total_nodes": 3`) {
		t.Fatalf("expected cached wrapper output, got %q", stdout)
	}
}

func TestAppendWarningIfMissingDeduplicates(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Warnings: []models.Warning{{Kind: "state", Message: "recovered"}},
	}
	appendWarningIfMissing(snap, models.Warning{Kind: "state", Message: "recovered"})
	appendWarningIfMissing(snap, models.Warning{Kind: "skills", Message: "recovered skills"})

	if len(snap.Warnings) != 2 {
		t.Fatalf("expected deduped warnings, got %#v", snap.Warnings)
	}
}

func TestRemoteExecPrefixIncludesQuotedEnv(t *testing.T) {
	got := remoteExecPrefix("node a", "/tmp/context file", []string{"ALPHA=beta gamma", "EMPTY="})
	for _, want := range []string{
		"BEST_NODE='node a'",
		"AXIS_CONTEXT_FILE='/tmp/context file'",
		"ALPHA='beta gamma'",
		"EMPTY=''",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in %q", want, got)
		}
	}
}

func TestPrintWarningWritesStderr(t *testing.T) {
	stdout, stderr, err := captureProcessOutput(t, func() error {
		printWarning(errors.New("careful"))
		return nil
	})
	if err != nil {
		t.Fatalf("printWarning: %v", err)
	}
	if stdout != "" {
		t.Fatalf("expected no stdout, got %q", stdout)
	}
	if !strings.Contains(stderr, "warning: careful") {
		t.Fatalf("expected warning on stderr, got %q", stderr)
	}
}

func TestResolveChatModelUsesRequestedValue(t *testing.T) {
	if got := resolveChatModel(" llama3 "); got != "llama3" {
		t.Fatalf("resolveChatModel() = %q, want llama3", got)
	}
}

func TestResolveChatModelFallsBackToResolver(t *testing.T) {
	restore := stubDefaultChatModelResolver(t, func(context.Context) string {
		return "default-model"
	})
	defer restore()

	if got := resolveChatModel(""); got != "default-model" {
		t.Fatalf("resolveChatModel() = %q, want default-model", got)
	}
}

func TestHandleChatMetaCommandModelsPrintsCatalog(t *testing.T) {
	restoreFormat := stubFormatChatCatalog(t, func(context.Context, string) string {
		return "formatted catalog"
	})
	defer restoreFormat()

	stdout, stderr, err := captureProcessOutput(t, func() error {
		handled, next := handleChatMetaCommand("/models", "phi4")
		if !handled || next != "" {
			t.Fatalf("expected handled /models with empty next, got handled=%v next=%q", handled, next)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("handleChatMetaCommand: %v", err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
	if !strings.Contains(stdout, "formatted catalog") {
		t.Fatalf("expected formatted catalog output, got %q", stdout)
	}
}

func TestChatCmdSingleShotUsesGenerateStream(t *testing.T) {
	restoreResolve := stubDefaultChatModelResolver(t, func(context.Context) string {
		return "phi4"
	})
	defer restoreResolve()
	restoreGenerate := stubGenerateChatStream(t, func(ctx context.Context, model, prompt string, w io.Writer) error {
		if model != "phi4" {
			t.Fatalf("model = %q, want phi4", model)
		}
		if prompt != "hello world" {
			t.Fatalf("prompt = %q, want hello world", prompt)
		}
		_, _ = io.WriteString(w, "chat ok")
		return nil
	})
	defer restoreGenerate()

	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := chatCmd()
		cmd.SetArgs([]string{"hello", "world"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("chat Execute: %v", err)
	}
	if !strings.Contains(stderr, "axis chat is experimental") {
		t.Fatalf("expected experimental warning, got %q", stderr)
	}
	if !strings.Contains(stdout, "AXIS [Model: phi4] | Thinking...") || !strings.Contains(stdout, "chat ok") {
		t.Fatalf("expected chat output, got %q", stdout)
	}
}

type fakeServeDaemon struct {
	started bool
	ctx     context.Context
}

func (f *fakeServeDaemon) Start(ctx context.Context) {
	f.started = true
	f.ctx = ctx
}

func (f *fakeServeDaemon) Snapshot() (*models.ClusterSnapshot, bool) {
	return nil, false
}

func (f *fakeServeDaemon) Meta() daemon.Metadata {
	return daemon.Metadata{}
}

func (f *fakeServeDaemon) Invalidate() {}

func (f *fakeServeDaemon) RefreshNow(context.Context) error { return nil }

func stubServeMCPStdio(t *testing.T, fn func(bool, string) error) func() {
	t.Helper()
	prev := serveMCPStdio
	serveMCPStdio = fn
	return func() {
		serveMCPStdio = prev
	}
}

func stubServeDaemonFactory(t *testing.T, fn func(time.Duration) serveDaemon) func() {
	t.Helper()
	prev := newServeDaemon
	newServeDaemon = fn
	return func() {
		newServeDaemon = prev
	}
}

func stubServeHTTPAPI(t *testing.T, fn func(string, serveDaemon, string) error) func() {
	t.Helper()
	prev := serveHTTPAPI
	serveHTTPAPI = fn
	return func() {
		serveHTTPAPI = prev
	}
}

func stubCurrentHostname(t *testing.T, fn func() (string, error)) func() {
	t.Helper()
	prev := currentHostname
	currentHostname = fn
	return func() {
		currentHostname = prev
	}
}

func stubCollectLocalFacts(t *testing.T, fn func(context.Context, string) (*models.NodeFacts, error)) func() {
	t.Helper()
	prev := collectLocalFacts
	collectLocalFacts = fn
	return func() {
		collectLocalFacts = prev
	}
}

func stubStatusRuntimeLoader(t *testing.T, fn func(context.Context) (*runtimectx.Context, error)) func() {
	t.Helper()
	prev := loadStatusRuntime
	loadStatusRuntime = fn
	return func() {
		loadStatusRuntime = prev
	}
}

func stubStatusCachedLoader(t *testing.T, fn func(context.Context, string) (*models.ClusterSnapshot, string, error)) func() {
	t.Helper()
	prev := fetchStatusSnapshot
	fetchStatusSnapshot = fn
	return func() {
		fetchStatusSnapshot = prev
	}
}

func stubStatusLiveLoader(t *testing.T, fn func(context.Context) (*models.ClusterSnapshot, string, error)) func() {
	t.Helper()
	prev := loadStatusLiveSnapshot
	loadStatusLiveSnapshot = fn
	return func() {
		loadStatusLiveSnapshot = prev
	}
}

func stubDefaultChatModelResolver(t *testing.T, fn func(context.Context) string) func() {
	t.Helper()
	prev := resolveDefaultChatModel
	resolveDefaultChatModel = fn
	return func() {
		resolveDefaultChatModel = prev
	}
}

func stubFormatChatCatalog(t *testing.T, fn func(context.Context, string) string) func() {
	t.Helper()
	prev := formatChatCatalog
	formatChatCatalog = fn
	return func() {
		formatChatCatalog = prev
	}
}

func stubGenerateChatStream(t *testing.T, fn func(context.Context, string, string, io.Writer) error) func() {
	t.Helper()
	prev := generateChatStream
	generateChatStream = fn
	return func() {
		generateChatStream = prev
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
