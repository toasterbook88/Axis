package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/a2a"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

// Verifies the well-known card route mounts on the real ServeWithContext mux
// and — unlike /snapshot — stays unauthenticated when an API token is set.
func TestServeWithContextMountsAgentCardRoute(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "axis.sock")
	token := "test-token-464"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = ServeWithContext(ctx, socketPath, nil, token, false, func() a2a.AgentCard {
			return a2a.Card(a2a.CardOptions{Name: "card-probe", Version: "test", Scope: a2a.ScopeObserve})
		})
	}()

	// Wait for the socket to accept.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", socketPath, time.Second)
		if err == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.DialTimeout("unix", socketPath, 2*time.Second)
			},
		},
	}
	resp, err := httpClient.Get("http://axis/.well-known/agent-card.json")
	if err != nil {
		t.Fatalf("GET card: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("card status = %d, want 200", resp.StatusCode)
	}
	var card a2a.AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatalf("decode card: %v", err)
	}
	if card.Name != "card-probe" {
		t.Fatalf("card name = %q", card.Name)
	}

	// Contrast: /snapshot (same mux) must require the token.
	noAuth, err := httpClient.Get("http://axis/snapshot")
	if err != nil {
		t.Fatalf("GET snapshot: %v", err)
	}
	noAuth.Body.Close()
	if noAuth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("snapshot without token = %d, want 401", noAuth.StatusCode)
	}
}

// Slice 3: the approval routes end-to-end — create (observe-only), approve
// exec-shaped (dispatches through the guarded pipeline), reject.
func TestA2AApprovalRoutesEndToEnd(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "axis.sock")
	token := "test-token-approval"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = ServeWithContext(ctx, socketPath, nil, token, false, func() a2a.AgentCard {
			return a2a.Card(a2a.CardOptions{Name: "approval-probe", Version: "test", Scope: a2a.ScopeObserve})
		})
	}()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", socketPath, time.Second)
		if err == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.DialTimeout("unix", socketPath, 2*time.Second)
			},
		},
	}

	// 1. Create an exec-shaped task via message:send
	send := `{"skillId":"guarded-exec","message":{"role":"user","parts":[{"type":"text","text":"echo approved-exec"}]}}`
	req, _ := http.NewRequest(http.MethodPost, "http://axis/a2a/v1/message:send", strings.NewReader(send))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var sent a2a.Task
	_ = json.NewDecoder(resp.Body).Decode(&sent)
	resp.Body.Close()
	if sent.Status.State != a2a.TaskStatePending {
		t.Fatalf("exec-shaped send state = %q, want pending", sent.Status.State)
	}

	var calls int
	var got execution.GuardedExecutionRequest
	prevRun := runLiveGuarded
	runLiveGuarded = func(_ context.Context, _ *runtimectx.Context, req execution.GuardedExecutionRequest) (execution.GuardedExecutionResult, error) {
		calls++
		got = req
		return execution.GuardedExecutionResult{OK: true, Description: req.Description, Mode: req.Mode}, nil
	}
	t.Cleanup(func() { runLiveGuarded = prevRun })
	prevLoad := loadLiveRuntime
	loadLiveRuntime = func(context.Context) (*runtimectx.Context, error) {
		return &runtimectx.Context{}, nil
	}
	t.Cleanup(func() { loadLiveRuntime = prevLoad })

	// Missing confirm leaves the task pending and does not run it.
	denied := postJSON(t, httpClient, "http://axis/a2a/v1/tasks/"+sent.ID+"/approve", token, `{"mode":"exec"}`)
	denied.Body.Close()
	if denied.StatusCode != http.StatusBadRequest {
		t.Fatalf("approve without confirm = %d, want 400", denied.StatusCode)
	}
	if calls != 0 {
		t.Fatalf("runner calls after denied approve = %d, want 0", calls)
	}
	still := getTask(t, httpClient, sent.ID, token)
	if still.Status.State != a2a.TaskStatePending {
		t.Fatalf("state after denied approve = %q, want pending", still.Status.State)
	}

	// confirm=YES and mode=exec is the only promotion. One runner call.
	approved := postJSON(t, httpClient, "http://axis/a2a/v1/tasks/"+sent.ID+"/approve", token, `{"confirm":"YES","mode":"exec"}`)
	approved.Body.Close()
	if approved.StatusCode != http.StatusOK {
		t.Fatalf("approve status = %d, want 200", approved.StatusCode)
	}
	if calls != 1 {
		t.Fatalf("runner calls = %d, want 1", calls)
	}
	if got.Confirm != "YES" || got.Mode != "exec" || got.OwnerSurface != execution.OwnerSurfaceA2ATask {
		t.Fatalf("runner request confirm=%q mode=%q surface=%q", got.Confirm, got.Mode, got.OwnerSurface)
	}
	if got.Description != "echo approved-exec" {
		t.Fatalf("runner description = %q", got.Description)
	}
	done := getTask(t, httpClient, sent.ID, token)
	if done.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("state after approve = %q, want completed", done.Status.State)
	}
	again := postJSON(t, httpClient, "http://axis/a2a/v1/tasks/"+sent.ID+"/approve", token, `{"confirm":"YES","mode":"exec"}`)
	again.Body.Close()
	if again.StatusCode != http.StatusNotFound {
		t.Fatalf("second approve = %d, want 404", again.StatusCode)
	}
	if calls != 1 {
		t.Fatalf("runner calls after second approve = %d, want 1", calls)
	}

	// The other URL shape is not a promotion path.
	legacy := postJSON(t, httpClient, "http://axis/a2a/v1/tasks/approve/"+sent.ID, token, `{"confirm":"YES","mode":"exec"}`)
	legacy.Body.Close()
	if calls != 1 {
		t.Fatalf("runner calls via legacy URL = %d, want 1", calls)
	}

	// Unauthenticated approve is 401 and does not run a newly queued task.
	send2 := `{"skillId":"workspace-write","message":{"role":"user","parts":[{"type":"text","text":"write the thing"}]}}`
	reqW, _ := http.NewRequest(http.MethodPost, "http://axis/a2a/v1/message:send", strings.NewReader(send2))
	reqW.Header.Set("Authorization", "Bearer "+token)
	reqW.Header.Set("Content-Type", "application/json")
	respW, err := httpClient.Do(reqW)
	if err != nil {
		t.Fatal(err)
	}
	var writeTask a2a.Task
	_ = json.NewDecoder(respW.Body).Decode(&writeTask)
	respW.Body.Close()
	if writeTask.Status.State != a2a.TaskStatePending {
		t.Fatalf("workspace-write send state = %q, want pending", writeTask.Status.State)
	}
	noAuth, err := httpClient.Post("http://axis/a2a/v1/tasks/"+writeTask.ID+"/approve", "application/json", strings.NewReader(`{"confirm":"YES","mode":"exec"}`))
	if err != nil {
		t.Fatal(err)
	}
	noAuth.Body.Close()
	if noAuth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth approve = %d, want 401", noAuth.StatusCode)
	}
	if calls != 1 {
		t.Fatalf("runner calls after unauth approve = %d, want 1", calls)
	}
	if getTask(t, httpClient, writeTask.ID, token).Status.State != a2a.TaskStatePending {
		t.Fatal("unauth approve changed task state")
	}

	// workspace-write uses the same runner contract.
	writeOK := postJSON(t, httpClient, "http://axis/a2a/v1/tasks/"+writeTask.ID+"/approve", token, `{"confirm":"YES","mode":"exec"}`)
	writeOK.Body.Close()
	if writeOK.StatusCode != http.StatusOK {
		t.Fatalf("workspace-write approve = %d, want 200", writeOK.StatusCode)
	}
	if calls != 2 {
		t.Fatalf("runner calls = %d, want 2", calls)
	}
	if got.Description != "write the thing" || got.OwnerSurface != execution.OwnerSurfaceA2ATask {
		t.Fatalf("workspace-write request description=%q surface=%q", got.Description, got.OwnerSurface)
	}

	// Reject persists the operator reason. An empty reason does not decide the task.
	send3 := `{"skillId":"guarded-exec","message":{"role":"user","parts":[{"type":"text","text":"do not run"}]}}`
	req3, _ := http.NewRequest(http.MethodPost, "http://axis/a2a/v1/message:send", strings.NewReader(send3))
	req3.Header.Set("Authorization", "Bearer "+token)
	req3.Header.Set("Content-Type", "application/json")
	resp3, err := httpClient.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	var sent3 a2a.Task
	_ = json.NewDecoder(resp3.Body).Decode(&sent3)
	resp3.Body.Close()
	empty := postJSON(t, httpClient, "http://axis/a2a/v1/tasks/"+sent3.ID+"/reject", token, `{}`)
	empty.Body.Close()
	if empty.StatusCode != http.StatusBadRequest {
		t.Fatalf("reject without reason = %d, want 400", empty.StatusCode)
	}
	if getTask(t, httpClient, sent3.ID, token).Status.State != a2a.TaskStatePending {
		t.Fatal("empty reject changed task state")
	}
	rejected := postJSON(t, httpClient, "http://axis/a2a/v1/tasks/"+sent3.ID+"/reject", token, `{"reason":"operator said no"}`)
	rejected.Body.Close()
	if rejected.StatusCode != http.StatusOK {
		t.Fatalf("reject status = %d, want 200", rejected.StatusCode)
	}
	rej := getTask(t, httpClient, sent3.ID, token)
	if rej.Status.State != a2a.TaskStateRejected {
		t.Fatalf("state after reject = %q, want rejected", rej.Status.State)
	}
	if rej.Status.Message == nil || len(rej.Status.Message.Parts) == 0 || rej.Status.Message.Parts[0].Text != "operator said no" {
		t.Fatalf("reject reason = %#v", rej.Status.Message)
	}
	if calls != 2 {
		t.Fatalf("runner calls after reject = %d, want 2", calls)
	}
}

// Pending list route: the operator board view of queued approval tasks.
func TestA2AApprovalPendingList(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "axis.sock")
	token := "test-token-pending"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = ServeWithContext(ctx, socketPath, nil, token, false, func() a2a.AgentCard {
			return a2a.Card(a2a.CardOptions{Name: "pending-probe", Version: "test", Scope: a2a.ScopeObserve})
		})
	}()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", socketPath, time.Second)
		if err == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	httpClient := &http.Client{Transport: &http.Transport{DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
		return net.DialTimeout("unix", socketPath, 2*time.Second)
	}}}

	req, _ := http.NewRequest(http.MethodPost, "http://axis/a2a/v1/message:send", strings.NewReader(`{"skillId":"guarded-exec","message":{"role":"user","parts":[{"type":"text","text":"x"}]}}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	listReq, err := http.NewRequest(http.MethodGet, "http://axis/a2a/v1/tasks/pending", nil)
	if err != nil {
		t.Fatal(err)
	}
	listReq.Header.Set("Authorization", "Bearer "+token)
	list, err := httpClient.Do(listReq)
	if err != nil {
		t.Fatal(err)
	}
	if list.StatusCode != http.StatusOK {
		t.Fatalf("pending list status = %d, want 200", list.StatusCode)
	}
	var body struct {
		Tasks []a2a.Task `json:"tasks"`
	}
	_ = json.NewDecoder(list.Body).Decode(&body)
	list.Body.Close()
	if len(body.Tasks) != 1 {
		t.Fatalf("pending list = %d tasks, want 1", len(body.Tasks))
	}
	if body.Tasks[0].Status.State != a2a.TaskStatePending {
		t.Fatalf("pending task state = %q", body.Tasks[0].Status.State)
	}
}

func postJSON(t *testing.T, client *http.Client, url, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func getTask(t *testing.T, client *http.Client, id, token string) a2a.Task {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://axis/a2a/v1/tasks/"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var task a2a.Task
	if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get task %s = %d", id, resp.StatusCode)
	}
	return task
}
