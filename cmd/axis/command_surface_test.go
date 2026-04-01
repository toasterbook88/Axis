package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/chat"
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
	if !strings.Contains(stdout, "COMMANDS") || !strings.Contains(stdout, "facts") || !strings.Contains(stdout, "task") {
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
	if !strings.Contains(stdout, "AVAILABLE SCRIPTS") {
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
	restoreServe := stubServeHTTPAPI(t, func(_ context.Context, addr string, d serveDaemon, token string) error {
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
		cmd.SetArgs([]string{"--format", "json"})
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

func TestHandleSlashCommandModelsPrintsCatalog(t *testing.T) {
	restoreFormat := stubFormatChatCatalog(t, func(context.Context, string) string {
		return "formatted catalog"
	})
	defer restoreFormat()

	var buf bytes.Buffer
	conv := chat.NewConversation(4096)
	next := handleSlashCommand("/models", "phi4", conv, &buf)
	if next != "" {
		t.Fatalf("expected empty next model for /models, got %q", next)
	}
	if !strings.Contains(buf.String(), "formatted catalog") {
		t.Fatalf("expected formatted catalog output, got %q", buf.String())
	}
}

func TestHandleSlashCommandClear(t *testing.T) {
	conv := chat.NewConversation(4096)
	conv.Append(chat.Message{Role: chat.RoleSystem, Content: "sys"})
	conv.Append(chat.Message{Role: chat.RoleUser, Content: "hello"})

	var buf bytes.Buffer
	handleSlashCommand("/clear", "phi4", conv, &buf)

	// After clear, only system messages remain.
	if conv.Len() != 1 {
		t.Fatalf("expected 1 message after clear, got %d", conv.Len())
	}
	if !strings.Contains(buf.String(), "cleared") {
		t.Fatalf("expected cleared message, got %q", buf.String())
	}
}

func TestHandleSlashCommandHelp(t *testing.T) {
	conv := chat.NewConversation(4096)
	var buf bytes.Buffer
	handleSlashCommand("/help", "phi4", conv, &buf)

	if !strings.Contains(buf.String(), "/clear") || !strings.Contains(buf.String(), "/status") {
		t.Fatalf("expected help text with commands, got %q", buf.String())
	}
}

func TestHandleSlashCommandModelSwitch(t *testing.T) {
	conv := chat.NewConversation(4096)
	var buf bytes.Buffer
	next := handleSlashCommand("/model llama3", "phi4", conv, &buf)
	if next != "llama3" {
		t.Fatalf("expected next model 'llama3', got %q", next)
	}
}

func TestHandleSlashCommandUnknown(t *testing.T) {
	conv := chat.NewConversation(4096)
	var buf bytes.Buffer
	handleSlashCommand("/bogus", "phi4", conv, &buf)
	if !strings.Contains(buf.String(), "Unknown command") {
		t.Fatalf("expected unknown command message, got %q", buf.String())
	}
}

func TestHandleSlashCommandModelBare(t *testing.T) {
	conv := chat.NewConversation(4096)
	var buf bytes.Buffer
	next := handleSlashCommand("/model", "phi4", conv, &buf)
	if next != "" {
		t.Fatalf("expected empty next for bare /model, got %q", next)
	}
	if !strings.Contains(buf.String(), "Usage:") {
		t.Fatalf("expected usage message, got %q", buf.String())
	}
}

func TestChatCmdSingleShotStreamsResponse(t *testing.T) {
	// Mock Ollama server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/api/show":
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"modelfile":"test"}`)
		case r.URL.Path == "/api/chat":
			w.Header().Set("Content-Type", "application/x-ndjson")
			chunks := []map[string]interface{}{
				{"message": map[string]string{"role": "assistant", "content": "hello "}, "done": false},
				{"message": map[string]string{"role": "assistant", "content": "world"}, "done": true},
			}
			for _, c := range chunks {
				data, _ := json.Marshal(c)
				fmt.Fprintf(w, "%s\n", data)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// Inject test endpoint and model.
	prev := chatEndpoint
	chatEndpoint = srv.URL
	defer func() { chatEndpoint = prev }()

	restoreResolve := stubDefaultChatModelResolver(t, func(context.Context) string {
		return "test-model"
	})
	defer restoreResolve()

	cmd := chatCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"hi"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("chat single-shot Execute: %v", err)
	}

	if !strings.Contains(stdout.String(), "hello world") {
		t.Fatalf("expected streamed response, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "advisory") {
		t.Fatalf("expected advisory warning in stderr, got %q", stderr.String())
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

func (f *fakeServeDaemon) WatchConfig(context.Context, string) {}

func (f *fakeServeDaemon) WaitStopped(context.Context) {}

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

func stubServeHTTPAPI(t *testing.T, fn func(context.Context, string, serveDaemon, string) error) func() {
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
