package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/toasterbook88/axis/internal/chat"
)

var schemaContinuationPattern = regexp.MustCompile(`tool_search\(query="([^"]+)", page=(\d+)\)`)
var listContinuationPattern = regexp.MustCompile(`Call tool_search with page=(\d+)`)

func searchPaginationPage(t *testing.T, r *ToolRegistry, args toolSearchArgs) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Execute(context.Background(), toolSearchName, raw)
	if err != nil {
		t.Fatal(err)
	}
	c := chat.NewConversation(1024)
	c.Append(chat.Message{Role: chat.RoleTool, Content: out})
	if len(out) > 2048 || !utf8.ValidString(out) || c.Messages()[0].Content != out {
		t.Fatalf("search page must survive Conversation intact: %d bytes", len(out))
	}
	return out
}

func advertisedSchemaContinuation(t *testing.T, out string) (toolSearchArgs, bool) {
	t.Helper()
	m := schemaContinuationPattern.FindStringSubmatch(out)
	if m == nil {
		return toolSearchArgs{}, false
	}
	page, err := strconv.Atoi(m[2])
	if err != nil {
		t.Fatal(err)
	}
	return toolSearchArgs{Query: m[1], Page: page}, true
}

func reconstructAdvertisedSchema(t *testing.T, r *ToolRegistry, args toolSearchArgs) string {
	t.Helper()
	var assembled strings.Builder
	for step := 0; step < 100; step++ {
		out := searchPaginationPage(t, r, args)
		if strings.HasPrefix(out, "<function-part ") {
			start := strings.Index(out, ">\n") + 2
			end := strings.Index(out, "\n</function-part>")
			if start < 2 || end < start {
				t.Fatalf("incomplete schema chunk: %q", out)
			}
			assembled.WriteString(out[start:end])
		} else {
			t.Fatalf("expected a schema chunk, got %q", out)
		}
		next, more := advertisedSchemaContinuation(t, out)
		if !more {
			return assembled.String()
		}
		args = next
	}
	t.Fatal("schema continuation did not finish")
	return ""
}

func noisySchemaQuery(name string) string {
	var names = []string{name}
	for i := 0; i < 15; i++ {
		names = append(names, fmt.Sprintf("missing_tool_name_%02d", i))
	}
	return "select:" + strings.Join(names, ",")
}

func TestToolSearch_ChunkContinuationIgnoresOptionalNotes(t *testing.T) {
	r := NewToolRegistry(nil)
	r.SetResultCapBytes(2048)
	r.add("mcp_pages_blob", strings.Repeat("\u03b1", 3000), json.RawMessage(`{"type":"object"}`), nil)
	got := reconstructAdvertisedSchema(t, r, toolSearchArgs{Query: noisySchemaQuery("mcp_pages_blob"), Page: 1})
	want, ok := marshalToolSearchLine(r.callableEntryMap()["mcp_pages_blob"], "", 0, 0)
	if !ok || got != want {
		t.Fatalf("advertised continuation changed schema bytes: got=%d want=%d", len(got), len(want))
	}
}

func TestToolSearch_PropertyContinuationIgnoresOptionalNotes(t *testing.T) {
	r := NewToolRegistry(nil)
	r.SetResultCapBytes(2048)
	props := make(map[string]any)
	for i := 0; i < 20; i++ {
		props[fmt.Sprintf("property_%02d", i)] = map[string]any{"type": "string", "description": strings.Repeat("d", 110)}
	}
	schema, _ := json.Marshal(map[string]any{"type": "object", "properties": props})
	r.add("mcp_pages_configure", "Configure things", schema, nil)
	args := toolSearchArgs{Query: noisySchemaQuery("mcp_pages_configure"), Page: 1}
	seen := make(map[string]bool)
	for step := 0; step < 100; step++ {
		out := searchPaginationPage(t, r, args)
		fns := extractFunctionPayloads(t, out)
		if len(fns) != 1 {
			t.Fatalf("expected a property page: %q", out)
		}
		var pageSchema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(fns[0].Parameters, &pageSchema); err != nil {
			t.Fatal(err)
		}
		for name := range pageSchema.Properties {
			if seen[name] {
				t.Fatalf("property %q repeated after following the advertised continuation", name)
			}
			seen[name] = true
		}
		next, more := advertisedSchemaContinuation(t, out)
		if !more {
			if len(seen) != len(props) {
				t.Fatalf("advertised continuation returned %d/%d properties", len(seen), len(props))
			}
			return
		}
		args = next
	}
	t.Fatal("property continuation did not finish")
}

func TestToolSearch_PropertyPaginationPreservesExactNumbers(t *testing.T) {
	r := NewToolRegistry(nil)
	r.SetResultCapBytes(2048)
	props := map[string]json.RawMessage{"record_id": json.RawMessage(`{"type":"integer","enum":[9007199254740993]}`)}
	for i := 0; i < 20; i++ {
		props[fmt.Sprintf("other_%02d", i)], _ = json.Marshal(map[string]string{"type": "string", "description": strings.Repeat("x", 150)})
	}
	schema, _ := json.Marshal(map[string]any{"type": "object", "properties": props})
	r.add("mcp_pages_numbers", "configure", schema, nil)
	args := toolSearchArgs{Query: "select:mcp_pages_numbers", Page: 1}
	for step := 0; step < 100; step++ {
		out := searchPaginationPage(t, r, args)
		if strings.Contains(out, `"record_id"`) {
			if !strings.Contains(out, "9007199254740993") || strings.Contains(out, "9007199254740992") {
				t.Fatalf("schema enum was rounded: %s", out)
			}
			return
		}
		next, more := advertisedSchemaContinuation(t, out)
		if !more {
			t.Fatal("integer enum was never returned")
		}
		args = next
	}
	t.Fatal("number schema continuation did not finish")
}

func TestToolSearch_FirstOversizedKeywordMatchKeepsAllMatchesReachable(t *testing.T) {
	r := NewToolRegistry(nil)
	r.SetResultCapBytes(2048)
	for i := 0; i < 150; i++ {
		r.add(fmt.Sprintf("mcp_a_misc_%03d", i), "misc", json.RawMessage(`{"type":"object"}`), nil)
	}
	var names []string
	for i := 0; i < 20; i++ {
		name := fmt.Sprintf("mcp_z_lookup_%02d_%s", i, strings.Repeat("x", 30))
		desc := "tail"
		if i == 0 {
			desc = strings.Repeat("x", 5000)
		}
		names = append(names, name)
		r.add(name, desc, json.RawMessage(`{"type":"object"}`), nil)
	}
	if strings.Contains(formatDeferredCatalog(r.DeferredCatalogEntries()), names[19]) {
		t.Fatal("last keyword match must be omitted from the bounded prompt catalog")
	}
	args := toolSearchArgs{Query: "lookup", MaxResults: 20, Page: 1}
	seen := make(map[string]bool)
	for step := 0; step < 100; step++ {
		out := searchPaginationPage(t, r, args)
		if strings.Contains(out, "<function-part ") {
			t.Fatal("multi-tool search must retain list pagination instead of returning only the first schema")
		}
		for _, fn := range extractFunctionPayloads(t, out) {
			seen[fn.Name] = true
		}
		if strings.Contains(out, "exceeds multi-tool page limit") {
			exact, ok := advertisedSchemaContinuation(t, out)
			if !ok || exact.Page != 1 {
				t.Fatalf("oversized list entry needs a separate schema continuation: %q", out)
			}
			full := reconstructAdvertisedSchema(t, r, exact)
			fns := extractFunctionPayloads(t, full)
			if len(fns) != 1 || fns[0].Name != names[0] || fns[0].Description != strings.Repeat("x", 5000) {
				t.Fatal("exact-name continuation lost the first matched schema")
			}
			seen[fns[0].Name] = true
		}
		m := listContinuationPattern.FindStringSubmatch(out)
		if m == nil {
			if len(seen) != len(names) || !seen[names[19]] {
				t.Fatalf("list continuation made %d/%d keyword matches reachable", len(seen), len(names))
			}
			return
		}
		args.Page, _ = strconv.Atoi(m[1])
	}
	t.Fatal("matched-tool list continuation did not finish")
}
