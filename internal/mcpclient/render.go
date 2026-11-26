package mcpclient

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// ContentParts renders a tool result into text parts for the CLI and the agent
// tool surface.
//
// It preserves the existing rendering for content blocks (text, image, other)
// and adds the case that mcp-go 1.6 made reachable: a result that carries only
// structuredContent with an empty content array. Up to 1.5.0
// mcp.ParseCallToolResult rejected such a response with "content is missing";
// from 1.6 it parses successfully, so a consumer that only walks Content would
// print nothing and lose the tool output without any error.
//
// A result with no content and no structured content still renders as no parts,
// so callers keep their current behaviour for genuinely empty results.
func ContentParts(res *mcp.CallToolResult) []string {
	if res == nil {
		return nil
	}
	if len(res.Content) == 0 {
		return structuredParts(res)
	}
	parts := make([]string, 0, len(res.Content))
	for _, content := range res.Content {
		switch c := content.(type) {
		case mcp.TextContent:
			parts = append(parts, c.Text)
		case mcp.ImageContent:
			parts = append(parts, fmt.Sprintf("[image: %s %d bytes]", c.MIMEType, len(c.Data)))
		default:
			b, err := json.MarshalIndent(content, "", "  ")
			if err != nil {
				parts = append(parts, fmt.Sprintf("[unrenderable content: %T]", content))
				continue
			}
			parts = append(parts, string(b))
		}
	}
	return parts
}

// structuredParts renders a structuredContent-only result as one JSON part.
// RawStructuredContent is preferred because it preserves the server's exact
// bytes; StructuredContent is the decoded form and is used as a fallback.
func structuredParts(res *mcp.CallToolResult) []string {
	if len(res.RawStructuredContent) > 0 {
		// Indent the original bytes. Decoding into any would turn every
		// JSON number into float64 and round integers above 2^53.
		var buf bytes.Buffer
		if err := json.Indent(&buf, res.RawStructuredContent, "", "  "); err == nil {
			return []string{buf.String()}
		}
		return []string{string(res.RawStructuredContent)}
	}
	if res.StructuredContent == nil {
		return nil
	}
	b, err := json.MarshalIndent(res.StructuredContent, "", "  ")
	if err != nil {
		return []string{fmt.Sprintf("%v", res.StructuredContent)}
	}
	return []string{string(b)}
}
