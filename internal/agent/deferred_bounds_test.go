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
