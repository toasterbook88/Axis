package agent

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/state"
)

// hookBackend records every request and runs onCall (once, on its first call)
// so a test can swap the agent's backend at a deterministic point mid-turn.
type hookBackend struct {
	mu     sync.Mutex
	calls  [][]chat.Message
	onCall func()
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
// one while the turn is already running (inside the compaction call). The
// turn must finish on the backend and trust class it started with: local-only
// evidence must never reach the remote backend, and the new backend only
// takes effect on the next turn.
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
	local.onCall = func() { a.SetBackend(remote, BackendRemote) }

	big := strings.Repeat("tool output content ", 40)
	for i := 0; i < 6; i++ {
		a.conv.Append(chat.Message{Role: chat.RoleUser, Content: "do thing " + string(rune('a'+i))})
		a.conv.Append(chat.Message{Role: chat.RoleAssistant, Content: "ok"})
		a.conv.Append(chat.Message{Role: chat.RoleTool, ToolCallID: "c", Content: big})
	}

	if err := a.Run(context.Background(), "show nodes"); err != nil {
		t.Fatalf("Run: %v", err)
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
