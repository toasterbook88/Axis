package mcpclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/netutil"
)

func TestMCPHTTPClientRejectsCrossOriginRedirects(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetRequests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	resp, err := newMCPHTTPClient().Get(source.URL)
	if err != nil {
		t.Fatalf("GET source: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusTemporaryRedirect)
	}
	if got := targetRequests.Load(); got != 0 {
		t.Fatalf("cross-origin target requests = %d, want 0", got)
	}
}

// A redirect to the same host on another scheme (an https to http
// downgrade) is not followed either: the origin is scheme and host.
func TestMCPHTTPClientRejectsSchemeChangeRedirects(t *testing.T) {
	// Same host and port, http instead of https. Followed, the plain request
	// would reach this TLS server and get a 400; unfollowed, the caller sees
	// the redirect itself.
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+"/downgraded", http.StatusTemporaryRedirect)
	}))
	defer secure.Close()

	client := newMCPHTTPClient()
	client.Transport = secure.Client().Transport // trust the test certificate
	resp, err := client.Get(secure.URL)
	if err != nil {
		t.Fatalf("GET secure: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want %d: the https to http redirect was followed", resp.StatusCode, http.StatusTemporaryRedirect)
	}
}

// A same-origin redirect loop stops at Go's default limit of ten redirects
// instead of running until the context expires.
func TestMCPHTTPClientStopsSameOriginRedirectLoop(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/loop", http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	_, err := newMCPHTTPClient().Get(server.URL + "/loop")
	if err == nil {
		t.Fatal("redirect loop returned no error")
	}
	if got := hits.Load(); got != 10 {
		t.Fatalf("server saw %d requests, want 10 (the first and 9 redirects before the limit)", got)
	}
}

func TestMCPHTTPClientAllowsSameOriginRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/final", http.StatusTemporaryRedirect)
			return
		}
		_, _ = fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	resp, err := newMCPHTTPClient().Get(server.URL + "/start")
	if err != nil {
		t.Fatalf("GET source: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func certFingerprintHex(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	if srv.TLS == nil || len(srv.TLS.Certificates) == 0 || len(srv.TLS.Certificates[0].Certificate) == 0 {
		t.Fatal("test TLS server has no certificate")
	}
	sum := sha256.Sum256(srv.TLS.Certificates[0].Certificate[0])
	return hex.EncodeToString(sum[:])
}

func formatColonFingerprint(bare string) string {
	upper := strings.ToUpper(bare)
	parts := make([]string, 0, len(upper)/2)
	for i := 0; i < len(upper); i += 2 {
		parts = append(parts, upper[i:i+2])
	}
	return strings.Join(parts, ":")
}

func TestNewHTTPClientTLSCertFingerprintPinning(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "pinned-ok")
	}))
	defer srv.Close()

	fp := certFingerprintHex(t, srv)

	// Without pin, Go's default transport rejects the self-signed cert.
	unpinned, err := NewHTTPClient(config.MCPServerConfig{URL: srv.URL})
	if err != nil {
		t.Fatalf("NewHTTPClient unpinned: %v", err)
	}
	if resp, err := unpinned.Get(srv.URL); err == nil {
		resp.Body.Close()
		t.Fatal("expected self-signed cert to fail without tls_cert_fingerprint")
	}

	// Matching fingerprint (bare, colon-separated, and sha256: prefixed) succeeds.
	for _, format := range []string{fp, "sha256:" + fp, "SHA256:" + formatColonFingerprint(fp)} {
		client, err := NewHTTPClient(config.MCPServerConfig{
			URL:                srv.URL,
			TLSCertFingerprint: format,
		})
		if err != nil {
			t.Fatalf("NewHTTPClient(%q): %v", format, err)
		}
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatalf("GET with valid pin %q: %v", format, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	}

	// Wrong fingerprint fails handshake.
	wrongFP := strings.Repeat("00", 32)
	if wrongFP == fp {
		wrongFP = strings.Repeat("ff", 32)
	}
	badClient, err := NewHTTPClient(config.MCPServerConfig{
		URL:                srv.URL,
		TLSCertFingerprint: wrongFP,
	})
	if err != nil {
		t.Fatalf("NewHTTPClient wrong pin: %v", err)
	}
	if resp, err := badClient.Get(srv.URL); err == nil {
		resp.Body.Close()
		t.Fatal("expected mismatched tls_cert_fingerprint to fail TLS handshake")
	} else if !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Fatalf("unexpected mismatch error: %v", err)
	}
}

func TestNewHTTPClientRejectsPinOnPlainHTTP(t *testing.T) {
	_, err := NewHTTPClient(config.MCPServerConfig{
		URL:                "http://127.0.0.1:5648/mcp",
		TLSCertFingerprint: strings.Repeat("ab", 32),
	})
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("expected https error for pin on plain http, got %v", err)
	}
}

func TestNewHTTPClientPinnedPreservesRedirectPolicy(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	var redirectTo string
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTo, http.StatusTemporaryRedirect)
	}))
	defer secure.Close()

	client, err := NewHTTPClient(config.MCPServerConfig{
		URL:                secure.URL,
		TLSCertFingerprint: certFingerprintHex(t, secure),
	})
	if err != nil {
		t.Fatalf("NewHTTPClient pinned: %v", err)
	}

	// Cross-origin redirect must not be followed.
	redirectTo = target.URL
	resp, err := client.Get(secure.URL)
	if err != nil {
		t.Fatalf("GET cross-origin redirect: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect || targetHits.Load() != 0 {
		t.Fatalf("cross-origin redirect followed: status=%d targetHits=%d", resp.StatusCode, targetHits.Load())
	}

	// Scheme downgrade on same host must not be followed.
	redirectTo = strings.Replace(secure.URL, "https://", "http://", 1) + "/downgrade"
	resp, err = client.Get(secure.URL)
	if err != nil {
		t.Fatalf("GET scheme downgrade: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("scheme downgrade redirect followed: status=%d", resp.StatusCode)
	}
}

func TestResolveHeaders(t *testing.T) {
	t.Run("expands env vars in single pass without mutating config", func(t *testing.T) {
		t.Setenv("TEST_MCP_SECRET", "  tok${with}braces  ")
		t.Setenv("TEST_MCP_EXTRA", "v1")
		orig := map[string]string{
			"Authorization": "Bearer ${TEST_MCP_SECRET}",
			"X-Pair":        "${TEST_MCP_EXTRA}:${TEST_MCP_EXTRA}",
			"X-Literal":     "plain-value",
		}
		cfg := config.MCPServerConfig{Headers: orig}

		got, err := ResolveHeaders(cfg)
		if err != nil {
			t.Fatalf("ResolveHeaders: %v", err)
		}
		if got["Authorization"] != "Bearer tok${with}braces" {
			t.Fatalf("Authorization = %q, want %q", got["Authorization"], "Bearer tok${with}braces")
		}
		if got["X-Pair"] != "v1:v1" {
			t.Fatalf("X-Pair = %q, want v1:v1", got["X-Pair"])
		}
		if got["X-Literal"] != "plain-value" {
			t.Fatalf("X-Literal = %q, want plain-value", got["X-Literal"])
		}
		if orig["Authorization"] != "Bearer ${TEST_MCP_SECRET}" {
			t.Fatalf("ResolveHeaders mutated cfg.Headers: %q", orig["Authorization"])
		}
	})

	t.Run("fails closed on unset or malformed placeholder", func(t *testing.T) {
		for _, val := range []string{
			"Bearer ${UNSET_MCP_VAR_12345}",
			"Bearer ${UNCLOSED",
			"Bearer ${BAD-NAME}",
			"Bearer ${}",
		} {
			_, err := ResolveHeaders(config.MCPServerConfig{
				Headers: map[string]string{"Authorization": val},
			})
			if err == nil {
				t.Fatalf("ResolveHeaders(%q) succeeded, want error", val)
			}
		}
	})

	t.Run("resolves bearer_token_env and bearer_token_file", func(t *testing.T) {
		t.Setenv("TEST_BEARER_ENV", "  env-secret-token  ")
		got, err := ResolveHeaders(config.MCPServerConfig{
			BearerTokenEnv: "TEST_BEARER_ENV",
		})
		if err != nil {
			t.Fatalf("ResolveHeaders env: %v", err)
		}
		if got["Authorization"] != "Bearer env-secret-token" {
			t.Fatalf("Authorization = %q, want Bearer env-secret-token", got["Authorization"])
		}

		tokenFile := filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(tokenFile, []byte("file-secret-token\n"), 0o600); err != nil {
			t.Fatalf("write token file: %v", err)
		}
		got, err = ResolveHeaders(config.MCPServerConfig{
			BearerTokenEnv:  "UNSET_BEARER_FALLBACK_12345",
			BearerTokenFile: tokenFile,
		})
		if err != nil {
			t.Fatalf("ResolveHeaders file fallback: %v", err)
		}
		if got["Authorization"] != "Bearer file-secret-token" {
			t.Fatalf("Authorization = %q, want Bearer file-secret-token", got["Authorization"])
		}
	})

	t.Run("rejects conflicting Authorization header", func(t *testing.T) {
		t.Setenv("TEST_BEARER_CONFLICT", "secret")
		_, err := ResolveHeaders(config.MCPServerConfig{
			BearerTokenEnv: "TEST_BEARER_CONFLICT",
			Headers:        map[string]string{"authorization": "Bearer other"},
		})
		if err == nil || !strings.Contains(err.Error(), "cannot set both") {
			t.Fatalf("expected conflict error, got %v", err)
		}
	})
}

func TestConnectHTTPWithPinnedTLSAndBearerTokenEnv(t *testing.T) {
	netutil.ResetInternalAllowlist()
	netutil.AllowInternalHost("127.0.0.1")
	t.Cleanup(netutil.ResetInternalAllowlist)

	const wantToken = "secret-pinned-token-42"
	t.Setenv("TEST_PINNED_MCP_TOKEN", wantToken)

	mcpSrv := mcpserver.NewMCPServer("test-pinned", "0.1.0", mcpserver.WithToolCapabilities(false))
	mcpSrv.AddTool(
		mcp.NewTool("query_device_info", mcp.WithDescription("test tool")),
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText("device-ok"), nil
		},
	)
	streamSrv := mcpserver.NewStreamableHTTPServer(mcpSrv)

	mux := http.NewServeMux()
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+wantToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		streamSrv.ServeHTTP(w, r)
	}))

	tlsSrv := httptest.NewTLSServer(mux)
	defer tlsSrv.Close()

	sc, err := connectHTTP(context.Background(), "local_app", config.MCPServerConfig{
		Transport:          "http",
		URL:                tlsSrv.URL + "/mcp",
		BearerTokenEnv:     "TEST_PINNED_MCP_TOKEN",
		TLSCertFingerprint: "sha256:" + formatColonFingerprint(certFingerprintHex(t, tlsSrv)),
	})
	if err != nil {
		t.Fatalf("connectHTTP: %v", err)
	}
	defer sc.Close()

	if !sc.Connected() {
		t.Fatalf("expected connected server, err=%v", sc.Err)
	}
	if sc.ToolCount() != 1 || sc.Tools[0].Name != "query_device_info" {
		t.Fatalf("tools = %+v, want [query_device_info]", sc.Tools)
	}
}
