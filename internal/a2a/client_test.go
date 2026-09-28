// Copyright (c) 2026 Smith Software Solutions
package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewClient_Normalization(t *testing.T) {
	c1 := NewClient("127.0.0.1:8080/", "tok1", nil)
	if c1.BaseURL != "http://127.0.0.1:8080" {
		t.Fatalf("expected http://127.0.0.1:8080, got %s", c1.BaseURL)
	}
	if c1.Token != "tok1" {
		t.Fatalf("expected tok1, got %s", c1.Token)
	}
	if c1.HTTPClient == nil {
		t.Fatal("expected default HTTPClient")
	}

	c2 := NewClient("https://foundry.lan:8080", "tok2", &http.Client{Timeout: 5 * time.Second})
	if c2.BaseURL != "https://foundry.lan:8080" {
		t.Fatalf("expected https://foundry.lan:8080, got %s", c2.BaseURL)
	}
}

func TestClient_FetchCard(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/agent-card.json" {
			http.NotFound(w, r)
			return
		}
		card := Card(CardOptions{
			Name:    "test-node",
			Version: "0.19.4",
			Scope:   ScopeObserve,
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(card)
	}))
	defer ts.Close()

	client := NewClient(ts.URL, "tok", ts.Client())
	card, err := client.FetchCard(context.Background())
	if err != nil {
		t.Fatalf("FetchCard failed: %v", err)
	}
	if card.Name != "test-node" {
		t.Fatalf("expected card name test-node, got %s", card.Name)
	}
	if card.Capabilities.Streaming {
		t.Fatal("expected streaming to be false")
	}
}

func TestClient_Send(t *testing.T) {
	var gotAuth string
	var gotBody SendRequest

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/a2a/v1/message:send" {
			http.NotFound(w, r)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		if gotAuth != "Bearer test-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "unauthorized"})
			return
		}

		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		task := Task{
			ID:      "task-123",
			SkillID: gotBody.SkillID,
			Status: TaskStatus{
				State:     TaskStateCompleted,
				Timestamp: time.Now(),
			},
			Artifacts: []Artifact{
				{
					Name:  "status-result",
					Parts: []Part{{Type: "text", Text: "all nodes healthy"}},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(task)
	}))
	defer ts.Close()

	// 1. Success path
	client := NewClient(ts.URL, "test-secret", ts.Client())
	task, err := client.Send(context.Background(), SendRequest{
		SkillID: "axis-status",
		Message: Message{Role: "user", Parts: []Part{{Type: "text", Text: "run check"}}},
	})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if task.ID != "task-123" {
		t.Fatalf("expected task id task-123, got %s", task.ID)
	}
	if gotBody.SkillID != "axis-status" {
		t.Fatalf("expected skill axis-status in captured body, got: %s", gotBody.SkillID)
	}
	if len(gotBody.Message.Parts) == 0 || gotBody.Message.Parts[0].Text != "run check" {
		t.Fatalf("unexpected message in captured body: %+v", gotBody.Message)
	}
	if task.Status.State != TaskStateCompleted {
		t.Fatalf("expected task completed, got %s", task.Status.State)
	}
	if len(task.Artifacts) == 0 || task.Artifacts[0].Parts[0].Text != "all nodes healthy" {
		t.Fatalf("unexpected artifacts: %+v", task.Artifacts)
	}

	// 2. Unauth path
	badClient := NewClient(ts.URL, "wrong-token", ts.Client())
	_, err = badClient.Send(context.Background(), SendRequest{
		SkillID: "axis-status",
	})
	if err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("expected unauthorized error, got: %v", err)
	}
}

func TestClient_Get(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "unauthorized"})
			return
		}
		if r.URL.Path == "/a2a/v1/tasks/task-abc" {
			task := Task{
				ID: "task-abc",
				Status: TaskStatus{
					State:     TaskStateWorking,
					Timestamp: time.Now(),
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(task)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "task not found"})
	}))
	defer ts.Close()

	client := NewClient(ts.URL, "token", ts.Client())

	// Empty ID validation
	if _, err := client.Get(context.Background(), ""); err == nil {
		t.Fatal("expected error on empty task id")
	}

	// Success
	task, err := client.Get(context.Background(), "task-abc")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if task.ID != "task-abc" || task.Status.State != TaskStateWorking {
		t.Fatalf("unexpected task: %+v", task)
	}

	// Not found
	if _, err := client.Get(context.Background(), "unknown"); err == nil {
		t.Fatal("expected error on not found task")
	}

	// Unauthorized
	unauthClient := NewClient(ts.URL, "wrong-token", ts.Client())
	if _, err := unauthClient.Get(context.Background(), "task-abc"); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("expected unauthorized error on Get, got: %v", err)
	}
}

func TestClient_ApproveAndReject(t *testing.T) {
	var gotApproveBody map[string]string
	var gotRejectBody map[string]string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "unauthorized"})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/a2a/v1/tasks/task-pending/approve" {
			_ = json.NewDecoder(r.Body).Decode(&gotApproveBody)
			task := Task{
				ID: "task-pending",
				Status: TaskStatus{
					State:     TaskStateApproved,
					Timestamp: time.Now(),
				},
			}
			_ = json.NewEncoder(w).Encode(task)
			return
		}
		if r.URL.Path == "/a2a/v1/tasks/task-pending/reject" {
			_ = json.NewDecoder(r.Body).Decode(&gotRejectBody)
			task := Task{
				ID: "task-pending",
				Status: TaskStatus{
					State:     TaskStateRejected,
					Timestamp: time.Now(),
					Message:   &Message{Role: "agent", Parts: []Part{{Type: "text", Text: gotRejectBody["reason"]}}},
				},
			}
			_ = json.NewEncoder(w).Encode(task)
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	client := NewClient(ts.URL, "token", ts.Client())

	// Approve
	task, err := client.Approve(context.Background(), "task-pending", "YES", "script")
	if err != nil {
		t.Fatalf("Approve failed: %v", err)
	}
	if task.Status.State != TaskStateApproved {
		t.Fatalf("expected approved, got %s", task.Status.State)
	}
	if gotApproveBody["confirm"] != "YES" || gotApproveBody["mode"] != "script" {
		t.Fatalf("unexpected approve body: %+v", gotApproveBody)
	}

	// Reject
	rejectedTask, err := client.Reject(context.Background(), "task-pending", "operator cancelled")
	if err != nil {
		t.Fatalf("Reject failed: %v", err)
	}
	if rejectedTask.Status.State != TaskStateRejected {
		t.Fatalf("expected rejected, got %s", rejectedTask.Status.State)
	}
	if gotRejectBody["reason"] != "operator cancelled" {
		t.Fatalf("unexpected reject reason: %s", gotRejectBody["reason"])
	}

	// Unauthorized
	unauthClient := NewClient(ts.URL, "bad-token", ts.Client())
	if _, err := unauthClient.Approve(context.Background(), "task-pending", "YES", "script"); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("expected unauthorized error on Approve, got: %v", err)
	}
	if _, err := unauthClient.Reject(context.Background(), "task-pending", "risk"); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("expected unauthorized error on Reject, got: %v", err)
	}
}

func TestClient_TaskID_PathEscaped(t *testing.T) {
	var capturedPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Task{ID: "task-1", Status: TaskStatus{State: TaskStateCompleted}})
	}))
	defer ts.Close()

	client := NewClient(ts.URL, "tok", ts.Client())
	specialID := "job/123?test#part"

	_, err := client.Get(context.Background(), specialID)
	if err != nil {
		t.Fatalf("Get with special ID failed: %v", err)
	}
	expectedGet := "/a2a/v1/tasks/job%2F123%3Ftest%23part"
	if capturedPath != expectedGet {
		t.Fatalf("expected path %s, got %s", expectedGet, capturedPath)
	}

	_, err = client.Approve(context.Background(), specialID, "YES", "script")
	if err != nil {
		t.Fatalf("Approve with special ID failed: %v", err)
	}
	expectedApprove := "/a2a/v1/tasks/job%2F123%3Ftest%23part/approve"
	if capturedPath != expectedApprove {
		t.Fatalf("expected path %s, got %s", expectedApprove, capturedPath)
	}

	_, err = client.Reject(context.Background(), specialID, "risk")
	if err != nil {
		t.Fatalf("Reject with special ID failed: %v", err)
	}
	expectedReject := "/a2a/v1/tasks/job%2F123%3Ftest%23part/reject"
	if capturedPath != expectedReject {
		t.Fatalf("expected path %s, got %s", expectedReject, capturedPath)
	}
}

func TestDecodeTaskOrError_StructuredAndCapped(t *testing.T) {
	// 1. Structured error extracted cleanly
	tsJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "specific failure reason"})
	}))
	defer tsJSON.Close()

	client := NewClient(tsJSON.URL, "", tsJSON.Client())
	_, err := client.Get(context.Background(), "t1")
	if err == nil || !strings.Contains(err.Error(), "specific failure reason") || !strings.Contains(err.Error(), "400") {
		t.Fatalf("expected structured error message and status code, got: %v", err)
	}

	// 2. Unstructured error fallback
	tsRaw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("plain text fatal crash"))
	}))
	defer tsRaw.Close()

	clientRaw := NewClient(tsRaw.URL, "", tsRaw.Client())
	_, err = clientRaw.Get(context.Background(), "t1")
	if err == nil || !strings.Contains(err.Error(), "plain text fatal crash") || !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected raw text error message and status code, got: %v", err)
	}

	// 3. Oversized error payload capped at 1 MB
	tsHuge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		buf := bytes.Repeat([]byte("X"), 2<<20) // 2 MB
		_, _ = w.Write(buf)
	}))
	defer tsHuge.Close()

	clientHuge := NewClient(tsHuge.URL, "", tsHuge.Client())
	_, err = clientHuge.Get(context.Background(), "t1")
	if err == nil {
		t.Fatal("expected error on huge response")
	}
	prefix := "server error (500 Internal Server Error): "
	if !strings.HasPrefix(err.Error(), prefix) {
		t.Fatalf("unexpected error prefix: %v", err)
	}
	cappedBody := strings.TrimPrefix(err.Error(), prefix)
	if len(cappedBody) != 1<<20 {
		t.Fatalf("expected capped body length of %d bytes (1MB), got %d", 1<<20, len(cappedBody))
	}
}

// TestDecodeTaskOrError_LargeSuccessPayloadUncapped guards the success path
// against the error-path body cap. A task whose artifacts exceed the cap (a
// large guarded-exec log dump, for example) must still decode: the cap exists
// to bound memory on hostile or misrouted error pages, not to truncate valid
// task responses into an "unexpected end of JSON input" decode failure.
func TestDecodeTaskOrError_LargeSuccessPayloadUncapped(t *testing.T) {
	big := strings.Repeat("L", (1<<20)+4096) // comfortably over 1 MiB

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Task{
			ID:      "task-big",
			SkillID: "guarded-exec",
			Status:  TaskStatus{State: TaskStateCompleted, Timestamp: time.Now()},
			Artifacts: []Artifact{{
				Name:  "exec-output",
				Parts: []Part{{Type: "text", Text: big}},
			}},
		})
	}))
	defer ts.Close()

	client := NewClient(ts.URL, "", ts.Client())
	task, err := client.Get(context.Background(), "task-big")
	if err != nil {
		t.Fatalf("large success payload must decode, got: %v", err)
	}
	if len(task.Artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(task.Artifacts))
	}
	if task.Artifacts[0].Parts[0].Text != big {
		t.Fatalf("artifact text was truncated: got %d bytes, want %d",
			len(task.Artifacts[0].Parts[0].Text), len(big))
	}
}
