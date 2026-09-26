// Copyright (c) 2026 Smith Software Solutions
package a2a

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Handler serves authenticated A2A task send/get on an existing mux.
// Mount HandleSend / HandleGet behind the same bearer auth as /run (F1).
// Never register these on the public card path.
type Handler struct {
	Store   *Store
	Scope   ScopeTier // live scope; v1 wire-up is ScopeObserve
	Observe ObserveData
	// Now is optional clock override for tests.
	Now func() time.Time
}

// EnsureDefaults fills nil fields so handlers are safe to call.
func (h *Handler) EnsureDefaults() {
	if h.Store == nil {
		h.Store = NewStore(0)
	}
	if h.Scope == "" {
		h.Scope = ScopeObserve
	}
	if h.Now == nil {
		h.Now = time.Now
	}
}

// ServeTasks registers the A2A task routes. wrap must apply withAuth (or
// equivalent); when nil, routes are registered bare (tests only).
//
// Routes:
//
//	POST /a2a/v1/message:send
//	GET  /a2a/v1/tasks/{id}
//
// Binding: HTTP+JSON under /a2a/v1/ matching A2A 1.0 naming (message:send).
// Proposal-literal tasks/send is a future alias — no TCK claim.
func ServeTasks(mux *http.ServeMux, h *Handler, wrap func(http.HandlerFunc) http.HandlerFunc) {
	if h == nil || mux == nil {
		return
	}
	h.EnsureDefaults()
	if wrap == nil {
		wrap = func(next http.HandlerFunc) http.HandlerFunc { return next }
	}
	mux.HandleFunc("/a2a/v1/message:send", wrap(h.HandleSend))
	mux.HandleFunc("/a2a/v1/tasks/", wrap(h.HandleGet))
}

// HandleSend implements POST /a2a/v1/message:send.
func (h *Handler) HandleSend(w http.ResponseWriter, r *http.Request) {
	h.EnsureDefaults()
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req SendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	skillID := SkillIDFromRequest(req)
	if skillID == "" {
		writeErr(w, http.StatusBadRequest, "skillId is required")
		return
	}

	principal := PrincipalHashFromRequest(r)
	now := h.Now()
	task := &Task{
		ID:            NewID(),
		ContextID:     strings.TrimSpace(req.ContextID),
		SkillID:       skillID,
		PrincipalHash: principal,
		CreatedAt:     now,
		History:       []Message{req.Message},
		Metadata: map[string]any{
			"ownerSurface": OwnerSurfaceA2ATask,
			"scope":        string(h.Scope),
		},
		Status: TaskStatus{State: TaskStateWorking, Timestamp: now},
	}

	allow := AllowedSkillIDs(h.Scope)

	// Gate B / F4 / F2: exec-shaped and unknown / over-tier → reject fail-closed.
	// Never enter RunGuarded from this slice (observe-only wire-up).
	if IsExecShaped(skillID) || !allow[skillID] {
		reason := "skill not allowed for live scope"
		if IsExecShaped(skillID) {
			reason = "exec-shaped skill rejected on observe-only A2A surface (fail-closed)"
		}
		task.Status = TaskStatus{
			State:     TaskStateRejected,
			Timestamp: now,
			Message: &Message{
				Role:  "agent",
				Parts: []Part{{Type: "text", Text: reason}},
			},
		}
		task.Metadata["rejectReason"] = reason
		h.Store.Put(task)
		writeJSON(w, http.StatusOK, taskPublic(task))
		return
	}

	name, text, err := RunObserveSkill(h.Observe, skillID, TextFromMessage(req.Message))
	if err != nil {
		task.Status = TaskStatus{
			State:     TaskStateFailed,
			Timestamp: h.Now(),
			Message: &Message{
				Role:  "agent",
				Parts: []Part{{Type: "text", Text: err.Error()}},
			},
		}
		h.Store.Put(task)
		writeJSON(w, http.StatusOK, taskPublic(task))
		return
	}

	task.Artifacts = []Artifact{{
		Name:  name,
		Parts: []Part{{Type: "text", Text: text}},
	}}
	task.Status = TaskStatus{
		State:     TaskStateCompleted,
		Timestamp: h.Now(),
		Message: &Message{
			Role:  "agent",
			Parts: []Part{{Type: "text", Text: "observe skill completed"}},
		},
	}
	h.Store.Put(task)
	writeJSON(w, http.StatusOK, taskPublic(task))
}

// HandleGet implements GET /a2a/v1/tasks/{id}.
func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	h.EnsureDefaults()
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/a2a/v1/tasks/")
	id = strings.Trim(id, "/")
	if id == "" || strings.Contains(id, "/") {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	principal := PrincipalHashFromRequest(r)
	task, ok := h.Store.Get(id, principal)
	if !ok {
		// F6: missing and foreign principal look the same.
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, taskPublic(task))
}

// PrincipalHashFromRequest derives a stable principal id from the bearer
// token (or empty-auth sentinel). Used for task ownership binding (F6).
func PrincipalHashFromRequest(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	parts := strings.Fields(authHeader)
	token := ""
	if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
		token = parts[1]
	}
	sum := sha256.Sum256([]byte("a2a-principal:" + token))
	return hex.EncodeToString(sum[:])
}

func taskPublic(t *Task) Task {
	if t == nil {
		return Task{}
	}
	cp := *t
	cp.PrincipalHash = ""
	return cp
}

func writeErr(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": message})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
