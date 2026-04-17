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

func TestStatus_HealthyCluster(t *testing.T) {
	toolsPayload := map[string]any{
		"tools": []map[string]any{
			{"name": "recall"},
			{"name": "acquire_lock"},
			{"name": "release_lock"},
			{"name": "publish_event"},
			{"name": "list_events"},
		},
	}
	mcpSrv := httptest.NewServer(mcpHandler("tools/list", toolsPayload))
	defer mcpSrv.Close()

	qdrantSrv := httptest.NewServer(qdrantHandler(142))
	defer qdrantSrv.Close()

	client := newTestClient(t, mcpSrv, qdrantSrv)
	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: unexpected error: %v", err)
	}
	if status.Status != "healthy" {
		t.Errorf("status = %q, want %q", status.Status, "healthy")
	}
	if status.MCPTools != 5 {
		t.Errorf("MCPTools = %d, want 5", status.MCPTools)
	}
	if status.Memories != 142 {
		t.Errorf("Memories = %d, want 142", status.Memories)
	}
}

func TestStatus_QdrantFailureDoesNotMaskHealthyMCP(t *testing.T) {
	toolsPayload := map[string]any{"tools": []map[string]any{{"name": "recall"}}}
	mcpSrv := httptest.NewServer(mcpHandler("tools/list", toolsPayload))
	defer mcpSrv.Close()

	// No qdrant server — use port 1 which will refuse connections.
	client := NewClientWithOptions("127.0.0.1", "tok",
		extractPort(t, mcpSrv.URL), 1, 2*time.Second)

	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: unexpected error when MCP is healthy but Qdrant is down: %v", err)
	}
	if status.Status != "healthy" {
		t.Errorf("status = %q, want %q", status.Status, "healthy")
	}
	if status.Memories != 0 {
		t.Errorf("Memories should be 0 when Qdrant is unreachable, got %d", status.Memories)
	}
}

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

func TestRecall_ReturnsParsedHits(t *testing.T) {
	hits := []MemoryHit{
		{Content: "ClusterSnapshot holds []NodeFacts", Score: 0.91},
		{Content: "PlacementDecision has FitScore 0-100", Score: 0.87},
	}

	mcpSrv := httptest.NewServer(toolCallHandler(hits))
	defer mcpSrv.Close()

	client := newTestClient(t, mcpSrv, nil)
	got, err := client.Recall(context.Background(), "ClusterSnapshot struct")
	if err != nil {
		t.Fatalf("Recall: unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 hits, got %d", len(got))
	}
	if got[0].Content != hits[0].Content {
		t.Errorf("hit[0].Content = %q, want %q", got[0].Content, hits[0].Content)
	}
	if got[0].Score != hits[0].Score {
		t.Errorf("hit[0].Score = %.2f, want %.2f", got[0].Score, hits[0].Score)
	}
}

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

func TestEvents_ReturnsParsedEvents(t *testing.T) {
	events := []Event{
		{ID: "ev-1", Type: "test_failure", Payload: json.RawMessage(`"pkg: internal/placement"`), CreatedAt: "2026-04-15T00:00:00Z"},
		{ID: "ev-2", Type: "deploy_start", Payload: json.RawMessage(`"node: foundry"`), CreatedAt: "2026-04-15T00:01:00Z"},
	}

	mcpSrv := httptest.NewServer(toolCallHandler(events))
	defer mcpSrv.Close()

	client := newTestClient(t, mcpSrv, nil)
	got, err := client.Events(context.Background(), 5)
	if err != nil {
		t.Fatalf("Events: unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 events, got %d", len(got))
	}
	if got[0].Type != "test_failure" {
		t.Errorf("event[0].Type = %q, want %q", got[0].Type, "test_failure")
	}
}

func TestEvents_ZeroLimitDefaultsToTen(t *testing.T) {
	var capturedArgs map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		if p, ok := req.Params.(map[string]any); ok {
			if args, ok := p["arguments"].(map[string]any); ok {
				capturedArgs = args
			}
		}
		envelope := map[string]any{
			"content": []map[string]any{{"type": "text", "text": "[]"}},
			"isError": false,
		}
		envelopeRaw, _ := json.Marshal(envelope)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rpcResponse{Result: envelopeRaw})
	}))
	defer srv.Close()

	client := NewClientWithOptions("127.0.0.1", "tok",
		extractPort(t, srv.URL), 1, 2*time.Second)
	_, _ = client.Events(context.Background(), 0)

	if capturedArgs == nil {
		t.Fatal("no arguments captured")
	}
	limit, _ := capturedArgs["limit"].(float64)
	if int(limit) != 10 {
		t.Errorf("limit = %v, want 10", capturedArgs["limit"])
	}
}

// — Lock tests —

func TestAcquireLock_AcquiredStatus(t *testing.T) {
	result := LockResult{Status: "ACQUIRED", Resource: "file:cmd/axis/chat.go", SessionID: "claude-1747000000"}

	mcpSrv := httptest.NewServer(toolCallHandler(result))
	defer mcpSrv.Close()

	client := newTestClient(t, mcpSrv, nil)
	got, err := client.AcquireLock(context.Background(), "file:cmd/axis/chat.go", "claude-1747000000")
	if err != nil {
		t.Fatalf("AcquireLock: unexpected error: %v", err)
	}
	if got.Status != "ACQUIRED" {
		t.Errorf("Status = %q, want ACQUIRED", got.Status)
	}
}

func TestAcquireLock_ConflictStatus(t *testing.T) {
	result := LockResult{Status: "CONFLICT", Resource: "file:cmd/axis/chat.go"}

	mcpSrv := httptest.NewServer(toolCallHandler(result))
	defer mcpSrv.Close()

	client := newTestClient(t, mcpSrv, nil)
	got, err := client.AcquireLock(context.Background(), "file:cmd/axis/chat.go", "claude-1747000001")
	if err != nil {
		t.Fatalf("AcquireLock: unexpected error for CONFLICT response: %v", err)
	}
	if got.Status != "CONFLICT" {
		t.Errorf("Status = %q, want CONFLICT", got.Status)
	}
}

func TestReleaseLock_Success(t *testing.T) {
	mcpSrv := httptest.NewServer(toolCallHandler(map[string]any{"released": true}))
	defer mcpSrv.Close()

	client := newTestClient(t, mcpSrv, nil)
	if err := client.ReleaseLock(context.Background(), "file:cmd/axis/chat.go"); err != nil {
		t.Fatalf("ReleaseLock: unexpected error: %v", err)
	}
}

// — isError envelope tests —

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
