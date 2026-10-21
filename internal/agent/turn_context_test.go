package agent

import (
	"reflect"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/chat"
)

func TestInjectTurnContextKeepsSystemPromptStable(t *testing.T) {
	base := []chat.Message{
		{Role: chat.RoleSystem, Content: "static instructions"},
		{Role: chat.RoleUser, Content: "earlier question"},
		{Role: chat.RoleAssistant, Content: "earlier answer"},
		{Role: chat.RoleUser, Content: "current question"},
	}
	a := injectTurnContext(base, "<live_cluster_context>age: 11s</live_cluster_context>")
	b := injectTurnContext(base, "<live_cluster_context>age: 47s</live_cluster_context>")

	// Everything before the newest user message is byte-identical across
	// turns, so a server-side prompt cache can reuse it.
	for i := 0; i < 3; i++ {
		if !reflect.DeepEqual(a[i], b[i]) {
			t.Fatalf("message %d differs between turns: %+v vs %+v", i, a[i], b[i])
		}
	}
	if a[0].Content != "static instructions" {
		t.Fatalf("system prompt changed: %q", a[0].Content)
	}
	if !strings.HasPrefix(a[3].Content, "<live_cluster_context>age: 11s") || !strings.HasSuffix(a[3].Content, "current question") {
		t.Fatalf("context not placed before the newest question: %q", a[3].Content)
	}
	if base[3].Content != "current question" {
		t.Fatalf("input slice was modified: %q", base[3].Content)
	}
}

func TestInjectTurnContextJoinsBlocksAndSkipsBlank(t *testing.T) {
	msgs := []chat.Message{{Role: chat.RoleSystem, Content: "s"}, {Role: chat.RoleUser, Content: "q"}}
	out := injectTurnContext(msgs, "evidence", "  ", "", "snapshot")
	if got, want := out[1].Content, "evidence\n\nsnapshot\n\nq"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	same := injectTurnContext(msgs, "", " ")
	if len(same) != 2 || same[1].Content != "q" {
		t.Fatalf("blank blocks should be a no-op, got %+v", same)
	}
}

func TestInjectTurnContextAfterToolResults(t *testing.T) {
	// Later turns of one run append assistant tool calls and tool results
	// after the user message; the context stays with the user message.
	msgs := []chat.Message{
		{Role: chat.RoleSystem, Content: "s"},
		{Role: chat.RoleUser, Content: "q"},
		{Role: chat.RoleAssistant, ToolCalls: []chat.ToolCall{{ID: "1"}}},
		{Role: chat.RoleTool, ToolCallID: "1", Content: "result"},
	}
	out := injectTurnContext(msgs, "ctx")
	if out[1].Content != "ctx\n\nq" || out[3].Content != "result" {
		t.Fatalf("unexpected placement: %+v", out)
	}
}

func TestInjectTurnContextFallsBackWithoutUserMessage(t *testing.T) {
	withSys := injectTurnContext([]chat.Message{{Role: chat.RoleSystem, Content: "s"}}, "ctx")
	if withSys[0].Content != "s\n\nctx" {
		t.Fatalf("fallback to system message: %+v", withSys)
	}
	none := injectTurnContext(nil, "ctx")
	if len(none) != 1 || none[0].Role != chat.RoleSystem || none[0].Content != "ctx" {
		t.Fatalf("fallback to new system message: %+v", none)
	}
}
