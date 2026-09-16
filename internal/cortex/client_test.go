package cortex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestClient builds a Client pointed at the provided httptest servers.
// qdrantSrv may be nil when the test only exercises MCP paths.
func newTestClient(t *testing.T, mcpSrv *httptest.Server, qdrantSrv *httptest.Server) *Client {
	t.Helper()

	mcpPort := 0
	qdrantPort := 0

	if mcpSrv != nil {
		mcpPort = extractPort(t, mcpSrv.URL)
	}
	if qdrantSrv != nil {
		qdrantPort = extractPort(t, qdrantSrv.URL)
	}

	return NewClientWithOptions("127.0.0.1", "test-token", mcpPort, qdrantPort, 5*time.Second)
}

// mcpHandler returns an http.Handler that responds to JSON-RPC requests with
// the provided result payload (raw JSON). Use for tools/list responses which
// are returned directly without a content envelope.
func mcpHandler(method string, result any) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Method != method {
			http.Error(w, "unexpected method: "+req.Method, http.StatusBadRequest)
			return
		}
		raw, _ := json.Marshal(result)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rpcResponse{Result: raw})
	})
}

// toolCallHandler returns an http.Handler for tools/call requests.
// It wraps result in the FastMCP 3.x content envelope:
//
//	{"content":[{"type":"text","text":"<json>"}],"isError":false}
func toolCallHandler(result any) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Method != "tools/call" {
			http.Error(w, "unexpected method: "+req.Method, http.StatusBadRequest)
			return
		}
		payload, err := json.Marshal(result)
		if err != nil {
			http.Error(w, "marshal result: "+err.Error(), http.StatusInternalServerError)
			return
		}
		envelope := map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": string(payload)},
			},
			"isError": false,
		}
		envelopeRaw, err := json.Marshal(envelope)
		if err != nil {
			http.Error(w, "marshal envelope: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rpcResponse{Result: envelopeRaw})
	})
}

// qdrantHandler returns an http.Handler simulating the Qdrant collection endpoint.
func qdrantHandler(pointsCount int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{
				"points_count": pointsCount,
			},
		})
	})
}

// — Status tests —

func TestStatus_MCPUnreachableReturnsError(t *testing.T) {
	// Port 1 will refuse the connection immediately.
	client := NewClientWithOptions("127.0.0.1", "tok", 1, 1, 500*time.Millisecond)
	_, err := client.Status(context.Background())
	if err == nil {
		t.Fatal("expected error when MCP server is unreachable")
	}
}

func TestStatus_UnauthorizedReturnsDescriptiveError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := NewClientWithOptions("127.0.0.1", "bad-token",
		extractPort(t, srv.URL), 1, 2*time.Second)
	_, err := client.Status(context.Background())
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "authentication required") {
		t.Errorf("error %q should mention authentication required", err.Error())
	}
}

// — Recall tests —

func TestRecall_EmptyResultIsNotAnError(t *testing.T) {
	mcpSrv := httptest.NewServer(toolCallHandler([]MemoryHit{}))
	defer mcpSrv.Close()

	client := newTestClient(t, mcpSrv, nil)
	got, err := client.Recall(context.Background(), "nonexistent query")
	if err != nil {
		t.Fatalf("Recall: unexpected error on empty result: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 hits, got %d", len(got))
	}
}

// — Events tests —

func TestCallTool_IsErrorWithTextReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		envelope := map[string]any{
			"content": []map[string]any{{"type": "text", "text": "lock already held by session claude-123"}},
			"isError": true,
		}
		envelopeRaw, _ := json.Marshal(envelope)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rpcResponse{Result: envelopeRaw})
	}))
	defer srv.Close()

	client := NewClientWithOptions("127.0.0.1", "tok",
		extractPort(t, srv.URL), 1, 2*time.Second)
	_, err := client.AcquireLock(context.Background(), "res", "sess")
	if err == nil {
		t.Fatal("expected error when isError=true")
	}
	if !strings.Contains(err.Error(), "lock already held") {
		t.Errorf("error %q should contain server error text", err.Error())
	}
}

func TestCallTool_IsErrorWithNoContentReturnsGenericError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		envelope := map[string]any{"content": []any{}, "isError": true}
		envelopeRaw, _ := json.Marshal(envelope)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rpcResponse{Result: envelopeRaw})
	}))
	defer srv.Close()

	client := NewClientWithOptions("127.0.0.1", "tok",
		extractPort(t, srv.URL), 1, 2*time.Second)
	_, err := client.AcquireLock(context.Background(), "res", "sess")
	if err == nil {
		t.Fatal("expected error when isError=true with empty content")
	}
	if !strings.Contains(err.Error(), "isError=true") {
		t.Errorf("error %q should mention isError=true", err.Error())
	}
}

func TestRPCError_PropagatesCodeAndMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rpcResponse{
			Error: &rpcError{Code: -32601, Message: "method not found"},
		})
	}))
	defer srv.Close()

	client := NewClientWithOptions("127.0.0.1", "tok",
		extractPort(t, srv.URL), 1, 2*time.Second)
	_, err := client.Recall(context.Background(), "anything")
	if err == nil {
		t.Fatal("expected error from RPC error response")
	}
	if !strings.Contains(err.Error(), "-32601") {
		t.Errorf("error %q should contain RPC error code", err.Error())
	}
}

// extractPort parses the port integer from an httptest URL like "http://127.0.0.1:PORT".
func extractPort(t *testing.T, rawURL string) int {
	t.Helper()
	addr := strings.TrimPrefix(rawURL, "http://")
	parts := strings.SplitN(addr, ":", 2)
	if len(parts) != 2 {
		t.Fatalf("extractPort: unexpected URL format %q", rawURL)
	}
	var port int
	for _, c := range parts[1] {
		if c < '0' || c > '9' {
			break
		}
		port = port*10 + int(c-'0')
	}
	return port
}
