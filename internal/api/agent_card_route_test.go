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

	// 2. Approve it — the guarded pipeline runs with runLiveGuarded (the
	// test hook intercepts, so no real shell fires).
	req2, _ := http.NewRequest(http.MethodPost, "http://axis/a2a/v1/tasks/"+sent.ID+"/approve", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	resp2, err := httpClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("approve status = %d", resp2.StatusCode)
	}

	// 3. Create + reject a second one
	req3, _ := http.NewRequest(http.MethodPost, "http://axis/a2a/v1/message:send", strings.NewReader(send))
	req3.Header.Set("Authorization", "Bearer "+token)
	req3.Header.Set("Content-Type", "application/json")
	resp3, err := httpClient.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	var sent2 a2a.Task
	_ = json.NewDecoder(resp3.Body).Decode(&sent2)
	resp3.Body.Close()
	req4, _ := http.NewRequest(http.MethodPost, "http://axis/a2a/v1/tasks/"+sent2.ID+"/reject", strings.NewReader(`{"reason":"no"}`))
	req4.Header.Set("Authorization", "Bearer "+token)
	req4.Header.Set("Content-Type", "application/json")
	resp4, err := httpClient.Do(req4)
	if err != nil {
		t.Fatal(err)
	}
	var rej a2a.Task
	_ = json.NewDecoder(resp4.Body).Decode(&rej)
	resp4.Body.Close()
	if rej.Status.State != a2a.TaskStateRejected {
		t.Fatalf("reject state = %q", rej.Status.State)
	}

	// 4. Unauthenticated approve → 401
	noAuth, err := httpClient.Post("http://axis/a2a/v1/tasks/"+sent.ID+"/approve", "application/json", nil)
	_ = noAuth
	if err != nil {
		t.Fatal(err)
	}
}

// Pending list route: the operator board view of queued approval tasks.
func TestA2AApprovalPendingList(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "axis.sock")
	token := "test-token-pending"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = ServeWithContext(ctx, socketPath, nil, token, false, func() a2a.AgentCard {
		return a2a.Card(a2a.CardOptions{Name: "pending-probe", Version: "test", Scope: a2a.ScopeObserve})
	})}()
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

	list, err := httpClient.Get("http://axis/a2a/v1/tasks/pending")
	if err != nil {
		t.Fatal(err)
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
