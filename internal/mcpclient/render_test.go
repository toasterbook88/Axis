package mcpclient

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// TestContentPartsRendersContentBlocks pins the pre-existing rendering so the
// refactor from the CLI/agent loops to this helper is behaviour-preserving.
func TestContentPartsRendersContentBlocks(t *testing.T) {
	res := &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{Type: "text", Text: "hello"},
			mcp.ImageContent{Type: "image", MIMEType: "image/png", Data: "abc"},
		},
	}
	got := ContentParts(res)
	want := []string{"hello", "[image: image/png 3 bytes]"}
	if len(got) != len(want) {
		t.Fatalf("ContentParts returned %d parts, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("part %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestContentPartsStructuredOnly is the regression this change exists for: from
// mcp-go 1.6 a tool may answer with structuredContent and an empty content
// array. Parsing succeeds, so a consumer that walks only Content renders
// nothing and the tool output disappears without an error.
func TestContentPartsStructuredOnly(t *testing.T) {
	raw := json.RawMessage(`{"nodes":2,"status":"healthy"}`)
	res := &mcp.CallToolResult{
		Content:              nil,
		StructuredContent:    map[string]any{"nodes": float64(2), "status": "healthy"},
		RawStructuredContent: raw,
	}

	got := ContentParts(res)
	if len(got) != 1 {
		t.Fatalf("structured-only result rendered %d parts, want 1: %#v", len(got), got)
	}
	if !strings.Contains(got[0], `"nodes"`) || !strings.Contains(got[0], `"healthy"`) {
		t.Fatalf("structured payload not preserved: %q", got[0])
	}
	if strings.TrimSpace(got[0]) == "" {
		t.Fatal("structured-only result rendered an empty part")
	}
}

// TestContentPartsStructuredWithoutRaw covers the decoded-only shape, where the
// client never retained the original bytes.
func TestContentPartsStructuredWithoutRaw(t *testing.T) {
	res := &mcp.CallToolResult{
		StructuredContent: map[string]any{"ok": true},
	}
	got := ContentParts(res)
	if len(got) != 1 || !strings.Contains(got[0], `"ok": true`) {
		t.Fatalf("decoded structured content not rendered: %#v", got)
	}
}

// TestContentPartsPrefersContentWhenBothPresent keeps the established output
// stable when a server sends both representations.
func TestContentPartsPrefersContentWhenBothPresent(t *testing.T) {
	res := &mcp.CallToolResult{
		Content:           []mcp.Content{mcp.TextContent{Type: "text", Text: "text form"}},
		StructuredContent: map[string]any{"k": "v"},
	}
	got := ContentParts(res)
	if len(got) != 1 || got[0] != "text form" {
		t.Fatalf("expected the content form only, got %#v", got)
	}
}

// TestContentPartsEmpty guards callers that expect no output for a genuinely
// empty result, and the nil result path used by error wrappers.
func TestContentPartsEmpty(t *testing.T) {
	if got := ContentParts(&mcp.CallToolResult{}); got != nil {
		t.Fatalf("empty result rendered %#v, want nil", got)
	}
	if got := ContentParts(nil); got != nil {
		t.Fatalf("nil result rendered %#v, want nil", got)
	}
}
