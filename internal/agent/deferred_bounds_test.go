package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/mcpclient"
)

func TestDeferredTools_CatalogSummaryHardCap(t *testing.T) {
	for _, toolCount := range []int{150, 500} {
		t.Run(strconv.Itoa(toolCount), func(t *testing.T) {
			r := NewToolRegistry(NewToolContext(&RuntimeView{}, nil))
			r.RegisterMCPTools(buildLargeMCPRegistry(1, toolCount))
			entries := r.DeferredCatalogEntries()
			out := formatDeferredCatalog(entries)
			if len(out) > deferredCatalogSummaryLimit {
				t.Fatalf("%d-tool deferred catalog is %d bytes; limit is %d", toolCount, len(out), deferredCatalogSummaryLimit)
			}
			shown := strings.Count(out, "\n- ")
			omitted := toolCount - shown
			if shown == 0 || omitted == 0 || !strings.Contains(out, fmt.Sprintf("%d deferred tools omitted", omitted)) || !strings.Contains(out, "tool_search") {
				t.Fatalf("catalog must list a bounded prefix and an accurate searchable omission notice: %q", out)
			}
			name := entries[len(entries)-1].Def.Function.Name
			if strings.Contains(out, "- "+name+"\n") {
				t.Fatalf("expected last sorted tool to be omitted: %q", name)
			}
			args, _ := json.Marshal(toolSearchArgs{Query: "select:" + name})
			found, err := r.Execute(context.Background(), toolSearchName, args)
			if err != nil || !strings.Contains(found, name) {
				t.Fatalf("tool omitted from prompt must remain searchable: result=%q err=%v", found, err)
			}
		})
	}
}

func TestToolSearch_OversizedMetadataDoesNotTruncate(t *testing.T) {
	for _, server := range []string{strings.Repeat("s", 2000), strings.Repeat("\u03b1", 1000)} {
		t.Run(server[:2], func(t *testing.T) {
			r := NewToolRegistry(NewToolContext(&RuntimeView{}, nil))
			name := "mcp_" + server + "_lookup"
			reg := mcpclient.NewRegistry()
			reg.Add(&mcpclient.ServerConnection{
				Name:       server,
				InitResult: &mcp.InitializeResult{},
				Tools: []mcp.Tool{{
					Name:        "lookup",
					Description: strings.Repeat("schema description ", 300),
					InputSchema: mcp.ToolInputSchema{Type: "object"},
				}},
			})
			r.RegisterMCPTools(reg)
			r.SetResultCapBytes(2048)
			for _, page := range []int{1, 999999} {
				args, _ := json.Marshal(toolSearchArgs{Query: "select:" + name, Page: page})
				out, err := r.Execute(context.Background(), toolSearchName, args)
				if err != nil {
					t.Fatal(err)
				}
				c := chat.NewConversation(1024)
				c.Append(chat.Message{Role: chat.RoleTool, Content: out})
				if len(out) > 2048 || strings.Contains(c.Messages()[0].Content, "… [truncated:") {
					t.Fatalf("tool_search returned %d bytes for cap=2048; Conversation truncated page %d", len(out), page)
				}
				if !utf8.ValidString(out) || strings.Contains(out, "<function") || !strings.Contains(out, "metadata exceeds the output size limit") {
					t.Fatalf("oversized metadata must return a complete readable failure: %q", out)
				}
			}
		})
	}
}

func TestToolSearch_MultiToolOversizedMetadataDoesNotTruncate(t *testing.T) {
	for _, toolCount := range []int{2, 3} {
		t.Run(strconv.Itoa(toolCount), func(t *testing.T) {
			server := strings.Repeat("s", 2000)
			name := "mcp_" + server + "_lookup"
			reg := mcpclient.NewRegistry()
			for _, srv := range []string{"short", server, "tail"}[:toolCount] {
				reg.Add(&mcpclient.ServerConnection{
					Name:       srv,
					InitResult: &mcp.InitializeResult{},
					Tools:      []mcp.Tool{{Name: "lookup", InputSchema: mcp.ToolInputSchema{Type: "object"}}},
				})
			}
			r := NewToolRegistry(NewToolContext(&RuntimeView{}, nil))
			r.RegisterMCPTools(reg)
			r.SetResultCapBytes(2048)
			query := "select:mcp_short_lookup," + name
			if toolCount == 3 {
				query += ",mcp_tail_lookup"
			}
			args, _ := json.Marshal(toolSearchArgs{Query: query, Page: 2})
			out, err := r.Execute(context.Background(), toolSearchName, args)
			if err != nil {
				t.Fatal(err)
			}
			c := chat.NewConversation(1024)
			c.Append(chat.Message{Role: chat.RoleTool, Content: out})
			if len(out) > 2048 || strings.Contains(c.Messages()[0].Content, "… [truncated:") {
				t.Fatalf("multi-tool oversized metadata page is %d bytes and must fit the 2048-byte cap", len(out))
			}
			if !strings.Contains(out, "metadata exceeds the output size limit") || !strings.Contains(out, fmt.Sprintf("Showing page 2/%d.", toolCount)) {
				t.Fatalf("oversized metadata page must retain a readable failure and pagination status: %q", out)
			}
			if toolCount == 3 {
				if !strings.Contains(out, "Call tool_search with page=3 for remaining schemas.") || !strings.Contains(out, "mcp_tail_lookup") {
					t.Fatalf("oversized metadata page lost continuation guidance: %q", out)
				}
				args, _ = json.Marshal(toolSearchArgs{Query: query, Page: 3})
				tail, err := r.Execute(context.Background(), toolSearchName, args)
				if err != nil {
					t.Fatal(err)
				}
				fns := extractFunctionPayloads(t, tail)
				if len(fns) != 1 || fns[0].Name != "mcp_tail_lookup" {
					t.Fatalf("next page lost the remaining schema: %q", tail)
				}
			}
		})
	}
}
