package cortex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseServiceEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr string
	}{
		{name: "https with mcp path", raw: "https://cortex.example.test/mcp", want: "https://cortex.example.test/mcp"},
		{name: "bare host gets mcp path", raw: "https://cortex.example.test", want: "https://cortex.example.test/mcp"},
		{name: "root slash gets mcp path", raw: "https://cortex.example.test/", want: "https://cortex.example.test/mcp"},
		{name: "http with port and custom path", raw: " http://192.0.2.10:8200/mcp ", want: "http://192.0.2.10:8200/mcp"},
		{name: "empty", raw: "  ", wantErr: "empty"},
		{name: "relative", raw: "cortex.example.test/mcp", wantErr: "http or https"},
		{name: "unsupported scheme", raw: "ftp://cortex.example.test/mcp", wantErr: "http or https"},
		{name: "missing host", raw: "https:///mcp", wantErr: "host"},
		{name: "embedded credentials", raw: "https://user:pass@cortex.example.test/mcp", wantErr: "credentials"},
		{name: "query string", raw: "https://cortex.example.test/mcp?x=1", wantErr: "query"},
		{name: "fragment", raw: "https://cortex.example.test/mcp#x", wantErr: "query"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseServiceEndpoint(tc.raw)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ParseServiceEndpoint(%q) err = %v, want containing %q", tc.raw, err, tc.wantErr)
				}
				if strings.Contains(err.Error(), "pass") {
					t.Fatalf("error leaks URL credentials: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseServiceEndpoint(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("ParseServiceEndpoint(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParseQdrantEndpoint(t *testing.T) {
	got, err := ParseQdrantEndpoint("http://192.0.2.10:6333/")
	if err != nil || got != "http://192.0.2.10:6333" {
		t.Fatalf("ParseQdrantEndpoint = %q, %v; want trailing slash trimmed", got, err)
	}
	if _, err := ParseQdrantEndpoint("https://u:p@qdrant.example.test"); err == nil {
		t.Fatal("expected credentials in Qdrant URL to be rejected")
	}
}

func TestEndpointClient_UsesExactURLAndToken(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		mcpHandler("tools/list", map[string]any{"tools": []map[string]any{{"name": "recall"}}}).ServeHTTP(w, r)
	}))
	defer srv.Close()

	client := NewClientForEndpoint(srv.URL+"/svc/mcp", "", "tok", 2*time.Second)
	if client.MCPEndpoint() != srv.URL+"/svc/mcp" {
		t.Fatalf("MCPEndpoint = %q", client.MCPEndpoint())
	}
	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if gotPath != "/svc/mcp" || gotAuth != "Bearer tok" {
		t.Fatalf("request path=%q auth=%q", gotPath, gotAuth)
	}
	if status.MCPTools != 1 {
		t.Fatalf("MCPTools = %d, want 1", status.MCPTools)
	}
}

func TestEndpointClient_NoQdrantURLReportsUnknownMemories(t *testing.T) {
	mcpSrv := httptest.NewServer(mcpHandler("tools/list", map[string]any{"tools": []map[string]any{}}))
	defer mcpSrv.Close()

	status, err := NewClientForEndpoint(mcpSrv.URL+"/mcp", "", "", 2*time.Second).Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.MemoriesUnknown || status.Memories != 0 {
		t.Fatalf("memories=%d unknown=%v, want unknown with no Qdrant URL", status.Memories, status.MemoriesUnknown)
	}
}

func TestEndpointClient_QdrantURLCountsMemories(t *testing.T) {
	mcpSrv := httptest.NewServer(mcpHandler("tools/list", map[string]any{"tools": []map[string]any{}}))
	defer mcpSrv.Close()
	qSrv := httptest.NewServer(qdrantHandler(489))
	defer qSrv.Close()

	status, err := NewClientForEndpoint(mcpSrv.URL+"/mcp", qSrv.URL, "", 2*time.Second).Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.MemoriesUnknown || status.Memories != 489 {
		t.Fatalf("memories=%d unknown=%v, want 489 known", status.Memories, status.MemoriesUnknown)
	}
}

func TestStatus_QdrantFailureMarksMemoriesUnknown(t *testing.T) {
	mcpSrv := httptest.NewServer(mcpHandler("tools/list", map[string]any{"tools": []map[string]any{}}))
	defer mcpSrv.Close()

	client := NewClientWithOptions("127.0.0.1", "tok", extractPort(t, mcpSrv.URL), 1, 2*time.Second)
	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.MemoriesUnknown {
		t.Fatal("a failed Qdrant count must be reported as unknown, not as 0 memories")
	}
}

func TestEndpointClient_ContextCancellationBoundsHungServer(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := NewClientForEndpoint(srv.URL+"/mcp", "", "", 30*time.Second).Status(ctx)
	if err == nil {
		t.Fatal("expected error from hung server")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Status took %v; cancellation must bound the call", elapsed)
	}
	if calls.Load() != 1 {
		t.Fatalf("server saw %d calls, want 1", calls.Load())
	}
}

func TestStatus_QdrantMissingOrNullCountIsUnknownButZeroIsKnown(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		wantUnknown bool
	}{
		{"null count", `{"result":{"points_count":null}}`, true},
		{"missing count", `{"result":{}}`, true},
		{"zero count", `{"result":{"points_count":0}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mcpSrv := httptest.NewServer(mcpHandler("tools/list", map[string]any{"tools": []map[string]any{}}))
			defer mcpSrv.Close()
			qSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer qSrv.Close()

			status, err := NewClientForEndpoint(mcpSrv.URL+"/mcp", qSrv.URL, "", 2*time.Second).Status(context.Background())
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if status.MemoriesUnknown != tc.wantUnknown || status.Memories != 0 {
				t.Fatalf("memories=%d unknown=%v, want unknown=%v", status.Memories, status.MemoriesUnknown, tc.wantUnknown)
			}
		})
	}
}
