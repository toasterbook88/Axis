package axismcp

import (
	"os"
	"strings"
	"testing"
)

// TestDoc_DefenseInDepthParagraph asserts that the package doc comment
// documents the defense-in-depth contract: client-supplied MCP tool
// annotations (readOnlyHint, destructiveHint, idempotentHint, openWorldHint)
// are advisory metadata and cannot weaken the safety or execution layers.
//
// The text is the single most important operator-facing reminder in this
// package: it makes explicit that hints are untrusted and the authoritative
// authority for write decisions is internal/safety and internal/execution.
//
// The test reads doc.go directly so the assertion stays in sync with the
// actual package comment; an out-of-band mirrored copy would drift.
func TestDoc_DefenseInDepthParagraph(t *testing.T) {
	data, err := os.ReadFile("doc.go")
	if err != nil {
		t.Fatalf("read doc.go: %v", err)
	}
	doc := string(data)

	for _, want := range []string{
		"Defense in depth",
		"advisory metadata",
		"internal/safety",
		"internal/execution",
		"untrusted",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("package doc missing required phrase %q (defense-in-depth contract)", want)
		}
	}
}
