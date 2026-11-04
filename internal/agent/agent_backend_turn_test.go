package agent

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/state"
)

// hookBackend records every request and runs onCall (once, on its first call)
// so a test can swap the agent's backend at a deterministic point mid-turn.
type hookBackend struct {
	mu       sync.Mutex
	calls    [][]chat.Message
	onCall   func()
	response *chat.Message
}

func (h *hookBackend) ChatStream(ctx context.Context, msgs []chat.Message, tools []chat.ToolDef, w io.Writer) (chat.Message, error) {
	h.mu.Lock()
	h.calls = append(h.calls, append([]chat.Message(nil), msgs...))
	hook := h.onCall
	h.onCall = nil
	h.mu.Unlock()
	if hook != nil {
		hook()
	}
	if h.response != nil {
		return *h.response, nil
	}
	return chat.Message{Role: chat.RoleAssistant, Content: "SUMMARY done"}, nil
}

func (h *hookBackend) callCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.calls)
}

func (h *hookBackend) sawText(s string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.calls {
		for _, m := range c {
			if strings.Contains(m.Content, s) {
				return true
			}
		}
	}
	return false
}

// TestTurnUsesOneBackendWhenSwappedMidTurn swaps a local backend for a remote
// one while the model round is running (inside the compaction call). The
// round must finish on its original backend and trust class: local-only
// evidence must never reach the remote backend, and the new backend only
// takes effect on the next model round (the next iteration of Run's loop,
// which can be inside the same operator prompt).
func TestTurnUsesOneBackendWhenSwappedMidTurn(t *testing.T) {
	remote := &hookBackend{}
	local := &hookBackend{}
	a := New(Config{
		Backend:              local,
		BackendSecurityClass: BackendLocal,
		MaxTurns:             1,
		MaxTokens:            200,
		Output:               io.Discard,
		// allowRawCommandEvidence stays false; decisions still go out in
		// local evidence, which is what a remote backend must not see.
		ToolContext: NewToolContext(&RuntimeView{
			State: &state.ClusterState{Decisions: []string{"placed LOCALONLY-marker on node-a"}},
		}, nil),
	})
	// Run and SetBackend execute on separate goroutines, with a rendezvous
	// inside compaction. No scheduler timing or sleeps determine the order.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	compacting := make(chan struct{})
	swapped := make(chan struct{})
	local.onCall = func() {
		close(compacting)
		select {
		case <-swapped:
		case <-ctx.Done():
		}
	}
	go func() {
		defer close(swapped)
		select {
		case <-compacting:
			a.SetBackend(remote, BackendRemote)
		case <-ctx.Done():
		}
	}()

	big := strings.Repeat("tool output content ", 40)
	for i := 0; i < 6; i++ {
		a.conv.Append(chat.Message{Role: chat.RoleUser, Content: "do thing " + string(rune('a'+i))})
		a.conv.Append(chat.Message{Role: chat.RoleAssistant, Content: "ok"})
		a.conv.Append(chat.Message{Role: chat.RoleTool, ToolCallID: "c", Content: big})
	}

	if err := a.Run(ctx, "show nodes"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("backend swap did not finish during compaction: %v", err)
	}
	if n := remote.callCount(); n != 0 {
		t.Fatalf("remote backend received %d call(s) in a turn that started local", n)
	}
	if remote.sawText("LOCALONLY-marker") {
		t.Fatal("local-only evidence reached the remote backend")
	}
	if n := local.callCount(); n < 2 {
		t.Fatalf("expected the original backend to serve compaction and stream, got %d call(s)", n)
	}
	if !local.sawText("LOCALONLY-marker") {
		t.Fatal("precondition: local evidence should reach the local backend")
	}
}

// A model swap is applied on the next loop iteration, even when that is part
// of the same operator prompt. Pinning the whole Run would be a regression.
func TestModelRoundSwitchAppliesWithinPrompt(t *testing.T) {
	remote := &hookBackend{}
	local := &hookBackend{response: &chat.Message{
		Role: chat.RoleAssistant,
		ToolCalls: []chat.ToolCall{{ID: "next-round", Function: chat.ToolCallFunction{
			Name: "cluster_status", Arguments: json.RawMessage(`{}`),
		}}},
	}}
	a := New(Config{
		Backend: local, BackendSecurityClass: BackendLocal,
		MaxTurns: 2, MaxTokens: 100000, Output: io.Discard, DryRun: true,
		ToolContext: NewToolContext(&RuntimeView{State: &state.ClusterState{
			Decisions: []string{"placed LOCALONLY-marker on node-a"},
		}}, nil),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	streaming := make(chan struct{})
	swapped := make(chan struct{})
	local.onCall = func() {
		close(streaming)
		select {
		case <-swapped:
		case <-ctx.Done():
		}
	}
	go func() {
		defer close(swapped)
		select {
		case <-streaming:
			a.SetBackend(remote, BackendRemote)
		case <-ctx.Done():
		}
	}()
	if err := a.Run(ctx, "show nodes"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("backend swap did not finish during streaming: %v", err)
	}
	if local.callCount() != 1 || remote.callCount() != 1 {
		t.Fatalf("expected local then remote model rounds; local=%d remote=%d", local.callCount(), remote.callCount())
	}
	if !local.sawText("LOCALONLY-marker") {
		t.Fatal("precondition: first local round must contain local evidence")
	}
	if remote.sawText("LOCALONLY-marker") {
		t.Fatal("local-only evidence persisted into the remote model round")
	}
}
