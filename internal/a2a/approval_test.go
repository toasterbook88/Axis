package a2a

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func execTask(id string) *Task {
	return &Task{
		ID:      id,
		SkillID: "guarded-exec",
		Status:  TaskStatus{State: TaskStateWorking},
	}
}

func TestApprovalQueue_EnqueueIsPending(t *testing.T) {
	q := NewApprovalQueue()
	tk := q.Enqueue(execTask("t1"))
	if tk.Status.State != TaskStatePending {
		t.Fatalf("enqueued state = %q, want pending", tk.Status.State)
	}
	if len(q.Pending()) != 1 {
		t.Fatalf("pending count = %d", len(q.Pending()))
	}
}

func TestApprovalQueue_ApproveDispatchesOnce(t *testing.T) {
	q := NewApprovalQueue()
	q.Enqueue(execTask("t1"))
	tk, ok := q.Approve("t1")
	if !ok {
		t.Fatal("approve failed")
	}
	if tk.Status.State != TaskStateApproved {
		t.Fatalf("approved state = %q", tk.Status.State)
	}
	// Synchronous approve: the task leaves the queue; the approve route
	// dispatches it to the guarded pipeline with the approver waiting.
	if _, ok := q.Approve("t1"); ok {
		t.Fatal("double-approve must fail (task removed from queue)")
	}
}

func TestApprovalQueue_ApproveUnknownFails(t *testing.T) {
	q := NewApprovalQueue()
	if _, ok := q.Approve("missing"); ok {
		t.Fatal("approve of unknown id must fail")
	}
}

func TestApprovalQueue_RejectIsTerminal(t *testing.T) {
	q := NewApprovalQueue()
	q.Enqueue(execTask("t1"))
	tk, ok := q.Reject("t1", "no reason given")
	if !ok || tk.Status.State != TaskStateRejected {
		t.Fatalf("reject failed: ok=%v state=%v", ok, tk.Status.State)
	}
	if len(q.Pending()) != 0 {
		t.Fatal("rejected task must leave the queue")
	}
}

func TestApprovalQueue_NonExecTaskNotApprovable(t *testing.T) {
	q := NewApprovalQueue()
	tk := q.Enqueue(&Task{ID: "t1", SkillID: "axis-status", Status: TaskStatus{State: TaskStateWorking}})
	_ = tk
	if _, ok := q.Approve("t1"); ok {
		t.Fatal("observe-skill tasks must not ride the approval queue")
	}
}

func TestApprovalRoutes_EndToEnd(t *testing.T) {
	h := &Handler{Store: NewStore(0), Scope: ScopeObserve, Queue: NewApprovalQueue(), Now: func() time.Time { return time.Unix(0, 0) }}
	mux := http.NewServeMux()
	ServeTasks(mux, h, nil)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 1. send an exec-shaped task → lands in the approval queue as pending
	send := `{"skillId":"guarded-exec","message":{"role":"user","parts":[{"type":"text","text":"run the thing"}]}}`
	resp, err := http.Post(srv.URL+"/a2a/v1/message:send", "application/json", strings.NewReader(send))
	if err != nil {
		t.Fatal(err)
	}
	var sent Task
	_ = json.NewDecoder(resp.Body).Decode(&sent)
	resp.Body.Close()
	if sent.Status.State != TaskStatePending {
		t.Fatalf("exec-shaped send state = %q, want pending", sent.Status.State)
	}
	if len(h.Queue.Pending()) != 1 {
		t.Fatalf("queue count = %d, want 1", len(h.Queue.Pending()))
	}

	// This mux does not promote. The only approve/reject routes live on the
	// api server, which is the package that can call the guarded runner.
	resp2, err := http.Post(srv.URL+"/a2a/v1/tasks/"+sent.ID+"/approve", "application/json", strings.NewReader(`{"confirm":"YES","mode":"exec"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode == http.StatusOK {
		t.Fatalf("bare a2a approve status = %d; this mux must not promote", resp2.StatusCode)
	}
	if len(h.Queue.Pending()) != 1 {
		t.Fatalf("queue count after bare approve = %d, want 1", len(h.Queue.Pending()))
	}
	gotResp, err := http.Get(srv.URL + "/a2a/v1/tasks/" + sent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got Task
	_ = json.NewDecoder(gotResp.Body).Decode(&got)
	gotResp.Body.Close()
	if got.Status.State != TaskStatePending {
		t.Fatalf("store state after bare approve = %q, want pending", got.Status.State)
	}

	resp4, err := http.Post(srv.URL+"/a2a/v1/tasks/"+sent.ID+"/reject", "application/json", strings.NewReader(`{"reason":"no"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp4.Body.Close()
	if resp4.StatusCode == http.StatusOK {
		t.Fatalf("bare a2a reject status = %d; this mux must not decide the task", resp4.StatusCode)
	}
	if len(h.Queue.Pending()) != 1 {
		t.Fatalf("queue count after bare reject = %d, want 1", len(h.Queue.Pending()))
	}
}

func TestApproveRouteNilQueueDoesNotPanic(t *testing.T) {
	h := &Handler{Store: NewStore(0), Scope: ScopeObserve, Now: func() time.Time { return time.Unix(0, 0) }}
	mux := http.NewServeMux()
	ServeTasks(mux, h, nil)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/a2a/v1/tasks/abc/approve", "application/json", strings.NewReader(`{"confirm":"YES","mode":"exec"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("nil queue approve status = %d, want 400", resp.StatusCode)
	}
}
