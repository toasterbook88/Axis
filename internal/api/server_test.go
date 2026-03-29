package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

type fakeCache struct {
	snap        *models.ClusterSnapshot
	meta        daemon.Metadata
	invalidated bool
	refreshed   bool
}

func (f *fakeCache) Snapshot() (*models.ClusterSnapshot, bool) {
	if f.snap == nil {
		return nil, false
	}
	return f.snap, true
}

func (f *fakeCache) Meta() daemon.Metadata {
	return f.meta
}

func (f *fakeCache) Invalidate() {
	f.invalidated = true
	f.snap = nil
	f.meta.Ready = false
}

func (f *fakeCache) RefreshNow(context.Context) error {
	f.refreshed = true
	f.snap = &models.ClusterSnapshot{Status: models.SnapshotHealthy}
	f.meta.Ready = true
	return nil
}

func TestHealthEndpoint(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, nil)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if payload["status"] != "ok" {
		t.Fatalf("expected status=ok, got %#v", payload["status"])
	}
	if payload["name"] != "axis" {
		t.Fatalf("expected name=axis, got %#v", payload["name"])
	}
}

func TestToolsEndpointIncludesExecutionSurface(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, nil)

	req := httptest.NewRequest(http.MethodGet, "/mcp/tools", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload ToolsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	var sawExecute, sawKnowledge bool
	for _, tool := range payload.Tools {
		switch tool.Name {
		case "axis_execute":
			sawExecute = true
		case "axis_knowledge":
			sawKnowledge = true
		}
	}

	if !sawExecute {
		t.Fatal("expected axis_execute tool in /mcp/tools")
	}
	if !sawKnowledge {
		t.Fatal("expected axis_knowledge tool in /mcp/tools")
	}
}

func TestSnapshotEndpointReturnsCachedSnapshot(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, &fakeCache{
		snap: &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Summary: models.ClusterSummary{
				TotalNodes: 1,
			},
		},
		meta: daemon.Metadata{
			Source:             "daemon-cache",
			Ready:              true,
			RefreshIntervalSec: 60,
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/snapshot", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload models.ClusterSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if payload.Summary.TotalNodes != 1 {
		t.Fatalf("expected total nodes 1, got %d", payload.Summary.TotalNodes)
	}
}

func TestSnapshotMetaEndpointReturnsCacheMetadata(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, &fakeCache{
		meta: daemon.Metadata{
			Source:             "daemon-cache",
			Ready:              true,
			RefreshIntervalSec: 60,
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/snapshot/meta", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload daemon.Metadata
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if payload.Source != "daemon-cache" {
		t.Fatalf("expected source daemon-cache, got %q", payload.Source)
	}
	if !payload.Ready {
		t.Fatal("expected ready=true")
	}
	if payload.RefreshIntervalSec != 60 {
		t.Fatalf("expected refresh interval sec 60, got %d", payload.RefreshIntervalSec)
	}
}

func TestToolsEndpointAliasReturnsSamePayload(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, nil)

	req := httptest.NewRequest(http.MethodGet, "/tools", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload ToolsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(payload.Tools) == 0 {
		t.Fatal("expected tools payload")
	}
}

func TestInvalidateEndpointCallsCacheInvalidate(t *testing.T) {
	cache := &fakeCache{
		snap: &models.ClusterSnapshot{Status: models.SnapshotHealthy},
		meta: daemon.Metadata{Ready: true},
	}
	mux := http.NewServeMux()
	registerRoutes(mux, cache)

	req := httptest.NewRequest(http.MethodPost, "/invalidate", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if !cache.invalidated {
		t.Fatal("expected cache invalidation to be triggered")
	}
}

func TestRefreshEndpointCallsCacheRefresh(t *testing.T) {
	cache := &fakeCache{
		meta: daemon.Metadata{Ready: false},
	}
	mux := http.NewServeMux()
	registerRoutes(mux, cache)

	req := httptest.NewRequest(http.MethodPost, "/refresh", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if !cache.refreshed {
		t.Fatal("expected cache refresh to be triggered")
	}
}

func TestResolveIntentMatchesNaturalLanguageScript(t *testing.T) {
	intent, err := resolveIntent("run a small local model with ollama inference", "script", &skills.Store{})
	if err != nil {
		t.Fatalf("expected natural-language script match, got %v", err)
	}
	if intent.matchedScript == nil {
		t.Fatal("expected a matched script")
	}
	if intent.matchedScript.Name != "ollama-run-smart" {
		t.Fatalf("expected ollama-run-smart, got %q", intent.matchedScript.Name)
	}
}

func TestRunEndpointRequiresExplicitMode(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, nil)

	req := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"description":"git status"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "mode is required") {
		t.Fatalf("expected mode-required error, got %q", rec.Body.String())
	}
}

func TestKnowledgeEndpointReturnsLiveWarningsAndSkills(t *testing.T) {
	restore := stubLiveRuntime(t, &runtimectx.Context{
		Snapshot: &models.ClusterSnapshot{
			Status: models.SnapshotDegraded,
			Warnings: []models.Warning{
				{Kind: "state", Message: "recovered local AXIS state"},
				{Kind: "skills", Message: "recovered learned skills store"},
			},
		},
		State: &state.ClusterState{Nodes: map[string]state.NodeState{}},
		Skills: &skills.Store{
			Skills:   []skills.LearnedSkill{{ID: "skill-1", Description: "echo hi", Command: "echo hi"}},
			Failures: []skills.LearnedFailure{{Description: "bad", Reason: "boom"}},
		},
	}, nil)
	defer restore()

	mux := http.NewServeMux()
	registerRoutes(mux, nil)

	req := httptest.NewRequest(http.MethodGet, "/knowledge", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload KnowledgeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if payload.Knowledge == nil {
		t.Fatal("expected knowledge payload")
	}
	if len(payload.Knowledge.Snapshot.Warnings) != 2 {
		t.Fatalf("expected 2 warnings, got %d", len(payload.Knowledge.Snapshot.Warnings))
	}
	if len(payload.Skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(payload.Skills))
	}
	if len(payload.Failures) != 1 {
		t.Fatalf("expected 1 failure, got %d", len(payload.Failures))
	}
}

func TestRunEndpointRequiresExplicitConfirmation(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, nil)

	req := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"description":"echo hi","mode":"exec"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "confirm must be YES") {
		t.Fatalf("expected confirm error, got %q", rec.Body.String())
	}
}

func TestRunTaskScriptModeFailsWithoutMatch(t *testing.T) {
	restore := stubLiveRuntime(t, testRuntimeContext(
		[]models.NodeFacts{testNode("node-a", "localhost", 8192, 4096, "low", "git")},
		[]config.NodeConfig{{Name: "node-a", Hostname: "localhost", SSHUser: "me"}},
		&state.ClusterState{Nodes: map[string]state.NodeState{}},
		&skills.Store{},
		nil,
	), nil)
	defer restore()

	resp, err := runTask(context.Background(), RunRequest{
		Description: "totally unknown workflow",
		Mode:        "script",
	})
	if err == nil {
		t.Fatal("expected error for unknown script/skill")
	}
	if !strings.Contains(resp.Error, "no known script or learned skill matches") {
		t.Fatalf("expected script-match error, got %q", resp.Error)
	}
}

func TestRunTaskPrependsRecoveredWarningsAndBlocksDangerousCommand(t *testing.T) {
	restore := stubLiveRuntime(t, testRuntimeContext(
		[]models.NodeFacts{testNode("node-a", "localhost", 8192, 4096, "low")},
		[]config.NodeConfig{{Name: "node-a", Hostname: "localhost", SSHUser: "me"}},
		&state.ClusterState{Nodes: map[string]state.NodeState{}},
		&skills.Store{},
		[]models.Warning{
			{Kind: "state", Message: "recovered local AXIS state"},
			{Kind: "skills", Message: "recovered learned skills store"},
		},
	), nil)
	defer restore()

	resp, err := runTask(context.Background(), RunRequest{
		Description: "rm -rf /",
		Mode:        "exec",
		Confirm:     "YES",
	})
	if err != nil {
		t.Fatalf("expected blocked response without execution error, got %v", err)
	}
	if !resp.Blocked {
		t.Fatal("expected command to be blocked")
	}
	if len(resp.Reasoning) < 2 {
		t.Fatalf("expected warning reasoning, got %#v", resp.Reasoning)
	}
	if resp.Reasoning[0] != "warning: recovered local AXIS state" {
		t.Fatalf("unexpected first reasoning entry: %q", resp.Reasoning[0])
	}
	if resp.Reasoning[1] != "warning: recovered learned skills store" {
		t.Fatalf("unexpected second reasoning entry: %q", resp.Reasoning[1])
	}
	if !strings.Contains(resp.BlockReason, "nuke root filesystem") {
		t.Fatalf("expected hard block reason, got %q", resp.BlockReason)
	}
}

func TestRunTaskRejectsReservationCapExceeded(t *testing.T) {
	restore := stubLiveRuntime(t, testRuntimeContext(
		[]models.NodeFacts{testNode("node-a", "localhost", 1536, 1536, "low")},
		[]config.NodeConfig{{Name: "node-a", Hostname: "localhost", SSHUser: "me"}},
		&state.ClusterState{
			Nodes: map[string]state.NodeState{
				"node-a": {ReservedMB: 512},
			},
		},
		&skills.Store{},
		nil,
	), nil)
	defer restore()

	resp, err := runTask(context.Background(), RunRequest{
		Description: "echo hi",
		Mode:        "exec",
		Confirm:     "YES",
	})
	if err == nil {
		t.Fatal("expected reservation cap error")
	}
	if !strings.Contains(resp.Error, "cannot reserve") {
		t.Fatalf("expected reservation error, got %q", resp.Error)
	}
}

func TestRunTaskExecutesLocalCommandAndRecordsSuccess(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	skillStore := &skills.Store{}
	clusterState := &state.ClusterState{Nodes: map[string]state.NodeState{}}
	restoreRuntime := stubLiveRuntime(t, testRuntimeContext(
		[]models.NodeFacts{testNode("node-a", "localhost", 8192, 4096, "low")},
		[]config.NodeConfig{{Name: "node-a", Hostname: "localhost", SSHUser: "me"}},
		clusterState,
		skillStore,
		nil,
	), nil)
	defer restoreRuntime()

	restoreShell := stubLocalShell(t, func(ctx context.Context, command string, env []string) ([]byte, error) {
		if command != "echo hi" {
			t.Fatalf("expected local command echo hi, got %q", command)
		}
		if !containsEnvPrefix(env, "AXIS_CONTEXT_FILE=") {
			t.Fatalf("expected AXIS_CONTEXT_FILE in env: %#v", env)
		}
		if !containsEnvPrefix(env, "BEST_NODE=node-a") {
			t.Fatalf("expected BEST_NODE in env: %#v", env)
		}
		return []byte("local ok"), nil
	})
	defer restoreShell()

	resp, err := runTask(context.Background(), RunRequest{
		Description: "echo hi",
		Mode:        "exec",
		Confirm:     "YES",
	})
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK response: %#v", resp)
	}
	if resp.Output != "local ok" {
		t.Fatalf("expected local output, got %q", resp.Output)
	}
	if resp.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", resp.ExitCode)
	}
	if len(skillStore.Skills) != 1 {
		t.Fatalf("expected learned skill to be recorded, got %d", len(skillStore.Skills))
	}
	if len(clusterState.Nodes) != 0 {
		t.Fatalf("expected reservation release cleanup, got %#v", clusterState.Nodes)
	}
}

func TestRunTaskInjectsTurboQuantEnvAndFlagsForLocalVerifiedBackend(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	skillStore := &skills.Store{}
	clusterState := &state.ClusterState{Nodes: map[string]state.NodeState{}}
	restoreRuntime := stubLiveRuntime(t, testRuntimeContext(
		[]models.NodeFacts{testTurboNode("node-a", "localhost", true)},
		[]config.NodeConfig{{Name: "node-a", Hostname: "localhost", SSHUser: "me"}},
		clusterState,
		skillStore,
		nil,
	), nil)
	defer restoreRuntime()

	restoreShell := stubLocalShell(t, func(ctx context.Context, command string, env []string) ([]byte, error) {
		if !strings.Contains(command, "--ctx-size 128000") {
			t.Fatalf("expected ctx-size injection, got %q", command)
		}
		if !strings.Contains(command, "--flash-attn") {
			t.Fatalf("expected flash-attn injection, got %q", command)
		}
		for _, want := range []string{
			"AXIS_TURBOQUANT=1",
			"AXIS_TURBOQUANT_STATUS=verified",
			"AXIS_TURBOQUANT_REQUESTED=1",
			"AXIS_TURBOQUANT_CONTEXT_TOKENS=128000",
		} {
			if !containsEnvPrefix(env, want) {
				t.Fatalf("expected %s in env: %#v", want, env)
			}
		}
		return []byte("local turbo ok"), nil
	})
	defer restoreShell()

	resp, err := runTask(context.Background(), RunRequest{
		Description: "llama-server -m model.gguf 128k book-length",
		Mode:        "exec",
		Confirm:     "YES",
	})
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK response: %#v", resp)
	}
	if !containsReason(resp.Reasoning, "turboquant injected --ctx-size 128000") {
		t.Fatalf("expected turboquant reasoning notes, got %#v", resp.Reasoning)
	}
}

func TestRunTaskLocalFailureRecordsFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	skillStore := &skills.Store{}
	restoreRuntime := stubLiveRuntime(t, testRuntimeContext(
		[]models.NodeFacts{testNode("node-a", "localhost", 8192, 4096, "low")},
		[]config.NodeConfig{{Name: "node-a", Hostname: "localhost", SSHUser: "me"}},
		&state.ClusterState{Nodes: map[string]state.NodeState{}},
		skillStore,
		nil,
	), nil)
	defer restoreRuntime()

	restoreShell := stubLocalShell(t, func(context.Context, string, []string) ([]byte, error) {
		return []byte("local bad"), exitError(t, 7)
	})
	defer restoreShell()

	resp, err := runTask(context.Background(), RunRequest{
		Description: "echo hi",
		Mode:        "exec",
		Confirm:     "YES",
	})
	if err == nil {
		t.Fatal("expected local execution error")
	}
	if resp.ExitCode != 7 {
		t.Fatalf("expected exit code 7, got %d", resp.ExitCode)
	}
	if len(skillStore.Failures) != 1 {
		t.Fatalf("expected failure record, got %d", len(skillStore.Failures))
	}
}

func TestRunTaskExecutesRemoteCommandAndRecordsSuccess(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	skillStore := &skills.Store{}
	clusterState := &state.ClusterState{Nodes: map[string]state.NodeState{}}
	restoreRuntime := stubLiveRuntime(t, testRuntimeContext(
		[]models.NodeFacts{testNode("node-a", "remote.example", 8192, 4096, "low")},
		[]config.NodeConfig{{Name: "node-a", Hostname: "remote.example", SSHUser: "me"}},
		clusterState,
		skillStore,
		nil,
	), nil)
	defer restoreRuntime()

	fakeExec := &fakeRemoteExec{
		outputs: []string{"", "remote ok"},
	}
	restoreRemote := stubRemoteFactory(t, func(config.NodeConfig) remoteExecutor {
		return fakeExec
	})
	defer restoreRemote()

	resp, err := runTask(context.Background(), RunRequest{
		Description: "echo hi",
		Mode:        "exec",
		Confirm:     "YES",
	})
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK response: %#v", resp)
	}
	if resp.Output != "remote ok" {
		t.Fatalf("expected remote output, got %q", resp.Output)
	}
	if len(fakeExec.commands) != 2 {
		t.Fatalf("expected two remote commands, got %d", len(fakeExec.commands))
	}
	if !strings.Contains(fakeExec.commands[1], "bash -lc") {
		t.Fatalf("expected remote execution wrapper, got %q", fakeExec.commands[1])
	}
	if len(skillStore.Skills) != 1 {
		t.Fatalf("expected learned skill to be recorded, got %d", len(skillStore.Skills))
	}
	if len(clusterState.Nodes) != 0 {
		t.Fatalf("expected reservation release cleanup, got %#v", clusterState.Nodes)
	}
	if !fakeExec.closed {
		t.Fatal("expected remote executor to be closed")
	}
}

func TestRunTaskInjectsTurboQuantEnvAndFlagsForRemoteVerifiedBackend(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	skillStore := &skills.Store{}
	clusterState := &state.ClusterState{Nodes: map[string]state.NodeState{}}
	restoreRuntime := stubLiveRuntime(t, testRuntimeContext(
		[]models.NodeFacts{testTurboNode("node-a", "remote.example", true)},
		[]config.NodeConfig{{Name: "node-a", Hostname: "remote.example", SSHUser: "me"}},
		clusterState,
		skillStore,
		nil,
	), nil)
	defer restoreRuntime()

	fakeExec := &fakeRemoteExec{
		outputs: []string{"", "remote turbo ok"},
	}
	restoreRemote := stubRemoteFactory(t, func(config.NodeConfig) remoteExecutor {
		return fakeExec
	})
	defer restoreRemote()

	resp, err := runTask(context.Background(), RunRequest{
		Description: "llama-server -m model.gguf 128k book-length",
		Mode:        "exec",
		Confirm:     "YES",
	})
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK response: %#v", resp)
	}
	if len(fakeExec.commands) != 2 {
		t.Fatalf("expected two remote commands, got %d", len(fakeExec.commands))
	}
	runCmd := fakeExec.commands[1]
	for _, want := range []string{
		"AXIS_TURBOQUANT=1",
		"AXIS_TURBOQUANT_STATUS=verified",
		"AXIS_TURBOQUANT_REQUESTED=1",
		"--ctx-size 128000",
		"--flash-attn",
	} {
		if !strings.Contains(runCmd, want) {
			t.Fatalf("expected %q in remote command %q", want, runCmd)
		}
	}
}

func TestRunTaskRemoteFailureRecordsFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	skillStore := &skills.Store{}
	restoreRuntime := stubLiveRuntime(t, testRuntimeContext(
		[]models.NodeFacts{testNode("node-a", "remote.example", 8192, 4096, "low")},
		[]config.NodeConfig{{Name: "node-a", Hostname: "remote.example", SSHUser: "me"}},
		&state.ClusterState{Nodes: map[string]state.NodeState{}},
		skillStore,
		nil,
	), nil)
	defer restoreRuntime()

	fakeExec := &fakeRemoteExec{
		outputs: []string{"", "remote bad"},
		errs:    []error{nil, exitError(t, 9)},
	}
	restoreRemote := stubRemoteFactory(t, func(config.NodeConfig) remoteExecutor {
		return fakeExec
	})
	defer restoreRemote()

	resp, err := runTask(context.Background(), RunRequest{
		Description: "echo hi",
		Mode:        "exec",
		Confirm:     "YES",
	})
	if err == nil {
		t.Fatal("expected remote execution error")
	}
	if resp.ExitCode != 9 {
		t.Fatalf("expected exit code 9, got %d", resp.ExitCode)
	}
	if len(skillStore.Failures) != 1 {
		t.Fatalf("expected failure record, got %d", len(skillStore.Failures))
	}
}

type fakeRemoteExec struct {
	outputs  []string
	errs     []error
	commands []string
	closed   bool
}

func (f *fakeRemoteExec) Run(_ context.Context, command string) (string, error) {
	f.commands = append(f.commands, command)
	idx := len(f.commands) - 1
	var out string
	if idx < len(f.outputs) {
		out = f.outputs[idx]
	}
	var err error
	if idx < len(f.errs) {
		err = f.errs[idx]
	}
	return out, err
}

func (f *fakeRemoteExec) Close() error {
	f.closed = true
	return nil
}

func stubLiveRuntime(t *testing.T, rt *runtimectx.Context, err error) func() {
	t.Helper()
	prev := loadLiveRuntime
	loadLiveRuntime = func(context.Context) (*runtimectx.Context, error) {
		return rt, err
	}
	return func() {
		loadLiveRuntime = prev
	}
}

func stubLocalShell(t *testing.T, fn func(context.Context, string, []string) ([]byte, error)) func() {
	t.Helper()
	prev := runLocalShell
	runLocalShell = fn
	return func() {
		runLocalShell = prev
	}
}

func stubRemoteFactory(t *testing.T, fn func(config.NodeConfig) remoteExecutor) func() {
	t.Helper()
	prev := newRemoteExecutor
	newRemoteExecutor = fn
	return func() {
		newRemoteExecutor = prev
	}
}

func testRuntimeContext(nodes []models.NodeFacts, cfgNodes []config.NodeConfig, st *state.ClusterState, store *skills.Store, warnings []models.Warning) *runtimectx.Context {
	return &runtimectx.Context{
		Config: &config.Config{Nodes: cfgNodes},
		Snapshot: &models.ClusterSnapshot{
			Status:   models.SnapshotHealthy,
			Nodes:    nodes,
			Summary:  summarizeNodes(nodes),
			Warnings: warnings,
		},
		State:  st,
		Skills: store,
	}
}

func summarizeNodes(nodes []models.NodeFacts) models.ClusterSummary {
	summary := models.ClusterSummary{TotalNodes: len(nodes)}
	for _, node := range nodes {
		if node.Status == models.StatusComplete || node.Status == models.StatusPartial {
			summary.ReachableNodes++
		}
		if node.Resources == nil {
			continue
		}
		summary.TotalRAMMB += node.Resources.RAMTotalMB
		summary.TotalFreeRAMMB += node.Resources.RAMFreeMB
		summary.TotalReservedMB += node.Resources.RAMReservedMB
		summary.TotalAllocatableMB += node.Resources.RAMAllocatableMB
	}
	return summary
}

func testNode(name, hostname string, totalRAM, freeRAM int64, pressure string, tools ...string) models.NodeFacts {
	node := models.NodeFacts{
		Name:     name,
		Hostname: hostname,
		Status:   models.StatusComplete,
		Resources: &models.Resources{
			RAMTotalMB: totalRAM,
			RAMFreeMB:  freeRAM,
			Pressure:   pressure,
			CPUCores:   8,
		},
	}
	for _, tool := range tools {
		node.Tools = append(node.Tools, models.ToolInfo{Name: tool, Version: "test"})
	}
	return node
}

func testTurboNode(name, hostname string, verified bool) models.NodeFacts {
	node := testNode(name, hostname, 16384, 8192, "low", "llama-server")
	node.Resources.PressureSource = "linux-psi"
	node.Resources.PressureStall10 = 6.5
	node.TurboQuant = &models.TurboQuantInfo{
		Supported:    true,
		Verified:     verified,
		Backends:     []string{"llama.cpp"},
		Capabilities: []string{"backend-probed", "ctx-size-flag", "flash-attn-flag", "llama.cpp-runtime"},
	}
	return node
}

func containsEnvPrefix(env []string, want string) bool {
	for _, item := range env {
		if item == want || strings.HasPrefix(item, want) {
			return true
		}
	}
	return false
}

func containsReason(reasoning []string, want string) bool {
	for _, reason := range reasoning {
		if reason == want {
			return true
		}
	}
	return false
}

func exitError(t *testing.T, code int) error {
	t.Helper()
	cmd := exec.Command("bash", "-lc", fmt.Sprintf("exit %d", code))
	if err := cmd.Run(); err != nil {
		return err
	}
	t.Fatalf("expected non-zero exit code %d", code)
	return nil
}

func writeTestConfig(t *testing.T, home string, content string) {
	t.Helper()
	cfgPath := filepath.Join(home, ".axis", "nodes.yaml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}
