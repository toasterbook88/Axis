package axismcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcpproto "github.com/mark3labs/mcp-go/mcp"
	"github.com/toasterbook88/axis/internal/config"
	axismcpclient "github.com/toasterbook88/axis/internal/mcpclient"
	"github.com/toasterbook88/axis/internal/netutil"
)

func TestStreamableHTTPInitializeAndListTools(t *testing.T) {
	h, _ := newStreamableHTTP(false, "", "")
	srv := httptest.NewServer(h)
	defer srv.Close()

	client, err := mcpclient.NewStreamableHttpClient(srv.URL + "/mcp")
	if err != nil {
		t.Fatalf("NewStreamableHttpClient: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	initReq := mcpproto.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcpproto.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcpproto.Implementation{Name: "test-client", Version: "0.1.0"}

	if _, err := client.Initialize(ctx, initReq); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	toolsRes, err := client.ListTools(ctx, mcpproto.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(toolsRes.Tools) != 21 {
		t.Fatalf("ListTools returned %d tools, want 21", len(toolsRes.Tools))
	}
}

func TestStreamableHTTPBearerAuthMiddleware(t *testing.T) {
	const token = "axis-http-mcp-secret"
	h, _ := newStreamableHTTP(false, "", token)
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Missing Authorization header -> 401.
	resp, err := http.Post(srv.URL+"/mcp", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST without auth: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing auth status = %d, want 401", resp.StatusCode)
	}

	// Wrong token -> 401.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer wrong-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST wrong auth: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong auth status = %d, want 401", resp.StatusCode)
	}

	// Matching token with case-insensitive scheme succeeds through MCP handshake.
	client, err := mcpclient.NewStreamableHttpClient(
		srv.URL+"/mcp",
		transport.WithHTTPHeaders(map[string]string{
			"Authorization": "bearer " + token,
		}),
	)
	if err != nil {
		t.Fatalf("NewStreamableHttpClient: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	initReq := mcpproto.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcpproto.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcpproto.Implementation{Name: "auth-client", Version: "0.1.0"}
	if _, err := client.Initialize(ctx, initReq); err != nil {
		t.Fatalf("Initialize with valid bearer token: %v", err)
	}
}

func TestStreamableHTTPConnectViaAxisMCPClientPinnedTLS(t *testing.T) {
	netutil.ResetInternalAllowlist()
	netutil.AllowInternalHost("127.0.0.1")
	t.Cleanup(netutil.ResetInternalAllowlist)

	const token = "axis-e2e-streamable-secret"
	t.Setenv("TEST_AXIS_E2E_TOKEN", token)

	h, _ := newStreamableHTTP(false, "", token)
	tlsSrv := httptest.NewTLSServer(h)
	defer tlsSrv.Close()

	sum := sha256.Sum256(tlsSrv.TLS.Certificates[0].Certificate[0])
	fp := "sha256:" + hex.EncodeToString(sum[:])

	reg := axismcpclient.NewRegistry()
	defer reg.Close()
	reg.ConnectAll(context.Background(), &config.Config{
		MCPServers: map[string]config.MCPServerConfig{
			"axis_http": {
				Transport:          "http",
				URL:                tlsSrv.URL + "/mcp",
				BearerTokenEnv:     "TEST_AXIS_E2E_TOKEN",
				TLSCertFingerprint: fp,
			},
		},
	})

	sc := reg.Get("axis_http")
	if sc == nil || !sc.Connected() {
		var err error
		if sc != nil {
			err = sc.Err
		}
		t.Fatalf("expected axis_http connected over pinned TLS, err=%v", err)
	}
	if sc.ToolCount() != 21 {
		t.Fatalf("ToolCount = %d, want 21", sc.ToolCount())
	}
}

func TestServeStreamableHTTPGuards(t *testing.T) {
	// Non-loopback without token is refused.
	for _, addr := range []string{"0.0.0.0:42426", ":42426", "192.0.2.10:42426"} {
		err := ServeStreamableHTTP(context.Background(), addr, false, "", "")
		if err == nil || !strings.Contains(err.Error(), "non-loopback") {
			t.Fatalf("ServeStreamableHTTP(%q) = %v, want non-loopback refusal", addr, err)
		}
	}

	// Unset --token-env fails closed even on loopback.
	err := ServeStreamableHTTP(context.Background(), "127.0.0.1:0", false, "", "UNSET_AXIS_MCP_HTTP_TOKEN_12345")
	if err == nil || !strings.Contains(err.Error(), "resolve bearer token") {
		t.Fatalf("ServeStreamableHTTP unset token env = %v, want resolve error", err)
	}

	// Whitespace-only --token-env fails closed instead of serving unauthenticated.
	err = ServeStreamableHTTP(context.Background(), "127.0.0.1:0", false, "", "   ")
	if err == nil || !strings.Contains(err.Error(), "whitespace-only") {
		t.Fatalf("ServeStreamableHTTP whitespace token env = %v, want whitespace-only error", err)
	}

	// Invalid listen address fails.
	err = ServeStreamableHTTP(context.Background(), "not-host-port", false, "", "")
	if err == nil || !strings.Contains(err.Error(), "invalid listen address") {
		t.Fatalf("ServeStreamableHTTP invalid addr = %v, want invalid listen address", err)
	}
}

func TestServeStreamableHTTPLoopbackWithTokenEnvAndCancel(t *testing.T) {
	t.Setenv("TEST_SERVE_MCP_HTTP_TOKEN", "loopback-secret")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	if err := ServeStreamableHTTP(ctx, "127.0.0.1:0", false, "", "TEST_SERVE_MCP_HTTP_TOKEN"); err != nil {
		t.Fatalf("ServeStreamableHTTP loopback with tokenEnv: %v", err)
	}
}

func TestServeStreamableHTTPGracefulShutdownWithActiveSSEStream(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	baseURL := "http://" + ln.Addr().String() + "/mcp"

	srvCtx, srvCancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- serveStreamableHTTPListener(srvCtx, ln, false, "", "")
	}()

	// Initialize a stateful Streamable HTTP session so GET /mcp opens a live SSE stream.
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"sse-test","version":"0.1.0"}}}`
	initReq, err := http.NewRequest(http.MethodPost, baseURL, strings.NewReader(initBody))
	if err != nil {
		t.Fatalf("NewRequest POST: %v", err)
	}
	initReq.Header.Set("Content-Type", "application/json")
	initReq.Header.Set("Accept", "application/json, text/event-stream")
	initResp, err := http.DefaultClient.Do(initReq)
	if err != nil {
		t.Fatalf("POST initialize: %v", err)
	}
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	initResp.Body.Close()
	if initResp.StatusCode != http.StatusOK || sessionID == "" {
		t.Fatalf("initialize status=%d sessionID=%q, want 200 with Mcp-Session-Id", initResp.StatusCode, sessionID)
	}

	// Hold an open GET /mcp SSE stream (no client cancellation) while canceling the server.
	sseReq, err := http.NewRequest(http.MethodGet, baseURL, nil)
	if err != nil {
		t.Fatalf("NewRequest GET: %v", err)
	}
	sseReq.Header.Set("Accept", "text/event-stream")
	sseReq.Header.Set("Mcp-Session-Id", sessionID)
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatalf("GET /mcp SSE: %v", err)
	}
	defer sseResp.Body.Close()
	if sseResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /mcp status = %d, want 200", sseResp.StatusCode)
	}

	// Cancel server context: CloseSessions + Shutdown must exit promptly without hanging.
	srvCancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveStreamableHTTPListener returned error on shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ServeStreamableHTTP hung during shutdown with active GET /mcp SSE stream")
	}
}
