// Copyright (c) 2026 Smith Software Solutions
package a2a

import (
	"fmt"
	"strings"
)

// TerminalActor is the party reporting a task state.
// ActorClient is the caller of message:send. ActorAssignee is the agent
// that owns the task, which is this server after it actually runs the skill.
type TerminalActor int

const (
	ActorAssignee TerminalActor = iota
	ActorClient
)

// AllowTerminalReport refuses a client report of a state only the assignee
// may set. The assignee may report any state. Absence is not a state:
// callers must not treat silence as completion.
func AllowTerminalReport(actor TerminalActor, state TaskState) error {
	if actor != ActorClient {
		return nil
	}
	switch state {
	case TaskStateCompleted, TaskStateFailed, TaskStateRejected, TaskStateApproved:
		return fmt.Errorf("client cannot report task state %q; only the assignee can", state)
	default:
		return nil
	}
}

// clientAssertedState returns a task state the caller put on the send
// request. An assignee-only value wins when more than one key is set, so a
// non-terminal annotation cannot hide a completed claim.
func clientAssertedState(req SendRequest) (TaskState, bool) {
	states := append(statesFromMap(req.Metadata), statesFromMap(req.Message.Metadata)...)
	for _, st := range states {
		if AllowTerminalReport(ActorClient, st) != nil {
			return st, true
		}
	}
	if len(states) == 0 {
		return "", false
	}
	return states[0], true
}

func statesFromMap(m map[string]any) []TaskState {
	if len(m) == 0 {
		return nil
	}
	var out []TaskState
	for _, key := range []string{"state", "status", "taskState"} {
		raw, ok := m[key]
		if !ok || raw == nil {
			continue
		}
		switch v := raw.(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				out = append(out, TaskState(s))
			}
		case map[string]any:
			s, ok := v["state"].(string)
			if !ok {
				continue
			}
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, TaskState(s))
			}
		}
	}
	return out
}
