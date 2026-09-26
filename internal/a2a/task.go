// Copyright (c) 2026 Smith Software Solutions
package a2a

import (
	"strings"
	"time"
)

// TaskState mirrors a minimal subset of A2A task lifecycle states used by
// slice 2v1 (sync send + get). Streaming / subscribe states are intentionally
// omitted while Capabilities.Streaming stays false.
type TaskState string

const (
	TaskStateSubmitted     TaskState = "submitted"
	TaskStateWorking       TaskState = "working"
	TaskStateCompleted     TaskState = "completed"
	TaskStateRejected      TaskState = "rejected"
	TaskStateFailed        TaskState = "failed"
	TaskStateInputRequired TaskState = "input-required"
)

// OwnerSurfaceA2ATask is the audit/safety surface label for mesh-delegated
// A2A task work. Kept in package a2a so the API facade and execution spine
// share one string; execution.OwnerSurfaceA2ATask aliases the same value.
const OwnerSurfaceA2ATask = "a2a-task"

// Part is one content fragment on a Message or Artifact (text-only in v1).
type Part struct {
	Type string `json:"type"` // "text"
	Text string `json:"text,omitempty"`
}

// Message is a minimal A2A message.
type Message struct {
	Role     string         `json:"role,omitempty"` // "user" | "agent"
	Parts    []Part         `json:"parts,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Artifact is a minimal A2A artifact attached to a completed task.
type Artifact struct {
	Name  string `json:"name,omitempty"`
	Parts []Part `json:"parts,omitempty"`
}

// Task is the in-memory A2A task record returned by send/get.
type Task struct {
	ID        string         `json:"id"`
	ContextID string         `json:"contextId,omitempty"`
	Status    TaskStatus     `json:"status"`
	Artifacts []Artifact     `json:"artifacts,omitempty"`
	History   []Message      `json:"history,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	SkillID   string         `json:"skillId,omitempty"`

	// PrincipalHash binds the task to the auth principal that created it (F6).
	// Never serialized on the wire.
	PrincipalHash string    `json:"-"`
	CreatedAt     time.Time `json:"-"`
	ExpiresAt     time.Time `json:"-"`
}

// TaskStatus is the A2A status envelope.
type TaskStatus struct {
	State     TaskState `json:"state"`
	Message   *Message  `json:"message,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// SendRequest is the HTTP+JSON body for POST /a2a/v1/message:send.
//
// Binding decision (design open Q1): HTTP+JSON under /a2a/v1/ matching A2A 1.0
// naming. Route is POST /a2a/v1/message:send (colon form; Go ServeMux accepts
// it as a literal path). Proposal-literal tasks/send is a future alias — this
// slice does not claim full TCK compliance.
type SendRequest struct {
	SkillID   string         `json:"skillId,omitempty"`
	ContextID string         `json:"contextId,omitempty"`
	Message   Message        `json:"message"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	// Confirm mirrors /run's confirm word. Required only for exec-tier paths;
	// observe skills ignore it. Slice 2v1 rejects exec skills before confirm.
	Confirm string `json:"confirm,omitempty"`
}

// TextFromMessage concatenates text parts from a message.
func TextFromMessage(m Message) string {
	var b strings.Builder
	for _, p := range m.Parts {
		if p.Type == "" || p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// SkillIDFromRequest resolves skillId from top-level field or message metadata.
func SkillIDFromRequest(req SendRequest) string {
	if id := strings.TrimSpace(req.SkillID); id != "" {
		return id
	}
	if req.Message.Metadata != nil {
		if v, ok := req.Message.Metadata["skillId"]; ok {
			if s, ok := v.(string); ok {
				return strings.TrimSpace(s)
			}
		}
		if v, ok := req.Message.Metadata["skill_id"]; ok {
			if s, ok := v.(string); ok {
				return strings.TrimSpace(s)
			}
		}
	}
	if req.Metadata != nil {
		if v, ok := req.Metadata["skillId"]; ok {
			if s, ok := v.(string); ok {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}
