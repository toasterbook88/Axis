// Copyright (c) 2026 Smith Software Solutions
package a2a

// Task lifecycle extension for the approval-queue slice (slice 3).
//
// Invariant (from HANDOFF-antigravity-a2a-slice2-scope-decision-2026-09-26.md
// and the schema-mask contract): a task whose skill is exec-shaped is NEVER
// executed by the daemon itself. It enters the queue in Pending state and
// only an explicit operator approval (via the authed approval route)
// promotes it to execution. There is no auto-approval path, no timer-based
// approval, and no batch approve. Rejected tasks are terminal.

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// TaskStatePending: created, awaiting operator approval. Never executed in
// this state.
const TaskStatePending TaskState = "pending"

// TaskStateApproved: operator-approved and handed to the execution pipeline.
// Terminal from the queue's perspective — the executor owns the outcome.
const TaskStateApproved TaskState = "approved"

// ApprovalQueue holds exec-shaped tasks pending operator approval. Tasks in
// the queue are inert: nothing reads them for execution until Approve fires.
type ApprovalQueue struct {
	mu      sync.Mutex
	pending map[string]*Task
	// Approved delivers tasks promoted by the operator. The daemon's
	// execution pump consumes this channel; capacity bounds memory.
	Approved chan *Task
	now      func() time.Time
}

// NewApprovalQueue returns an empty queue.
func NewApprovalQueue() *ApprovalQueue {
	return &ApprovalQueue{
		pending:  make(map[string]*Task),
		Approved: make(chan *Task, 16),
		now:      time.Now,
	}
}

// Enqueue places an exec-shaped task in Pending state. Returns the stored
// copy for the response. A task already queued under the same ID is replaced
// (idempotent create).
func (q *ApprovalQueue) Enqueue(t *Task) *Task {
	if t == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	t.Status = TaskStatus{State: TaskStatePending, Timestamp: now}
	cp := *t
	q.pending[t.ID] = &cp
	out := &cp
	return out
}

// Approve promotes a pending task out of the queue. ok=false when the id is
// unknown, already decided, or the task is not exec-shaped. The caller (the
// approve route) then dispatches the returned task to the guarded pipeline.
func (q *ApprovalQueue) Approve(id string) (*Task, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	t, ok := q.pending[id]
	if !ok {
		return nil, false
	}
	if t.SkillID != "guarded-exec" && t.SkillID != "workspace-write" {
		// Only exec-shaped tasks ride the approval queue; observe tasks
		// complete inline in the send handler.
		return nil, false
	}
	delete(q.pending, id)
	t.Status = TaskStatus{State: TaskStateApproved, Timestamp: q.now()}
	cp := *t
	return &cp, true
}

// Reject marks a pending task rejected and removes it. Terminal.
func (q *ApprovalQueue) Reject(id, reason string) (*Task, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	t, ok := q.pending[id]
	if !ok {
		return nil, false
	}
	delete(q.pending, id)
	t.Status = TaskStatus{
		State:     TaskStateRejected,
		Timestamp: q.now(),
		Message:   &Message{Role: "agent", Parts: []Part{{Type: "text", Text: reason}}},
	}
	cp := *t
	return &cp, true
}

// Pending returns copies of all queued tasks (operator board view).
func (q *ApprovalQueue) Pending() []Task {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Task, 0, len(q.pending))
	for _, t := range q.pending {
		out = append(out, *t)
	}
	return out
}

// writeTaskJSON is the shared response writer for task-shaped replies.
func WriteTaskJSON(w http.ResponseWriter, status int, t *Task) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(t)
}

// HandleApprove implements POST /a2a/v1/tasks/{id}/approve. Requires the
// same bearer auth as the send route (mounted behind the same wrap).
func (h *Handler) HandleApprove(w http.ResponseWriter, r *http.Request) {
	h.EnsureDefaults()
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/a2a/v1/tasks/")
	id = strings.TrimSuffix(id, "/approve")
	if id == "" || strings.Contains(id, "/") {
		writeErr(w, http.StatusNotFound, "unknown task")
		return
	}
	t, ok := h.Queue.Approve(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "task not pending")
		return
	}
	WriteTaskJSON(w, http.StatusOK, t)
}

// HandleReject implements POST /a2a/v1/tasks/{id}/reject.
func (h *Handler) HandleReject(w http.ResponseWriter, r *http.Request) {
	h.EnsureDefaults()
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/a2a/v1/tasks/")
	id = strings.TrimSuffix(id, "/reject")
	if id == "" || strings.Contains(id, "/") {
		writeErr(w, http.StatusNotFound, "unknown task")
		return
	}
	body := struct {
		Reason string `json:"reason"`
	}{}
	_ = json.NewDecoder(r.Body).Decode(&body)
	t, ok := h.Queue.Reject(id, body.Reason)
	if !ok {
		writeErr(w, http.StatusNotFound, "task not pending")
		return
	}
	WriteTaskJSON(w, http.StatusOK, t)
}

// HandleTasksDispatch routes /a2a/v1/tasks/{id}[/approve|/reject] and
// /a2a/v1/tasks/{id} to their handlers based on method + path suffix.
// Registered on the same mux pattern as HandleGet (registered later in
// ServeTasks) so approve/reject are reached before the generic GET.
func (h *Handler) HandleTasksDispatch(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/a2a/v1/tasks/")
	switch {
	case strings.HasSuffix(path, "/approve") && r.Method == http.MethodPost:
		r.URL.Path = "/a2a/v1/tasks/" + strings.TrimSuffix(path, "/approve")
		h.HandleApprove(w, r)
	case strings.HasSuffix(path, "/reject") && r.Method == http.MethodPost:
		r.URL.Path = "/a2a/v1/tasks/" + strings.TrimSuffix(path, "/reject")
		h.HandleReject(w, r)
	default:
		if r.Method != http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "unroutable task path: " + r.URL.Path})
			return
		}
		h.HandleGet(w, r)
	}
}
