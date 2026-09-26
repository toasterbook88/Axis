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
	"sort"
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
	now     func() time.Time
}

// NewApprovalQueue returns an empty queue. Nothing in the queue runs a
// command; the api approval route is the only caller that dispatches.
func NewApprovalQueue() *ApprovalQueue {
	return &ApprovalQueue{
		pending: make(map[string]*Task),
		now:     time.Now,
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
	if !IsExecShaped(t.SkillID) {
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
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// writeTaskJSON is the shared response writer for task-shaped replies.
func WriteTaskJSON(w http.ResponseWriter, status int, t *Task) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(t)
}

// HandleTasksDispatch serves GET /a2a/v1/tasks/{id}. Approve and reject are
// not registered here; internal/api owns those routes so a task cannot be
// promoted without the guarded runner. A nil Queue therefore has nothing to
// call and cannot panic.
func (h *Handler) HandleTasksDispatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "unroutable task path: " + r.URL.Path})
		return
	}
	h.HandleGet(w, r)
}
