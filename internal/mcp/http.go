package axismcp

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/toasterbook88/axis/internal/secrets"
)

const streamableHTTPShutdownTimeout = 5 * time.Second

// defaultSessionIdleTTL bounds per-session transport state when a client
// disconnects without DELETE. mcp-go's sweeper removes that state after the TTL.
const defaultSessionIdleTTL = 15 * time.Minute

// newStreamableHTTP builds the HTTP handler mounted at /mcp along with the
// underlying StreamableHTTPServer so callers can close active SSE sessions on
// shutdown.
func newStreamableHTTP(cached bool, cacheAddr string, token string) (http.Handler, *mcpserver.StreamableHTTPServer) {
	return newStreamableHTTPWithTTL(cached, cacheAddr, token, defaultSessionIdleTTL)
}

func newStreamableHTTPWithTTL(cached bool, cacheAddr string, token string, sessionIdleTTL time.Duration) (http.Handler, *mcpserver.StreamableHTTPServer) {
	mcpSrv := NewServer(cached, cacheAddr, nil)
	streamSrv := mcpserver.NewStreamableHTTPServer(mcpSrv, mcpserver.WithSessionIdleTTL(sessionIdleTTL))

	var endpoint http.Handler = streamSrv
	if token != "" {
		endpoint = bearerAuthMiddleware(token, streamSrv)
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", endpoint)
	return mux, streamSrv
}

func bearerAuthMiddleware(token string, next http.Handler) http.Handler {
	expected := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") || subtle.ConstantTimeCompare([]byte(parts[1]), expected) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isLoopbackListenHost(host string) bool {
	h := strings.TrimSpace(host)
	if h == "" {
		return false
	}
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// ServeStreamableHTTP starts a Streamable HTTP MCP server on addr serving /mcp.
// HTTP always requires a bearer token from tokenEnv, including on loopback,
// because 127.0.0.1 is reachable by every local account. The listener is
// cleartext. A non-loopback bind also requires allowInsecure, which is the
// explicit opt-in for sending that bearer token over plain HTTP.
func ServeStreamableHTTP(ctx context.Context, addr string, cached bool, cacheAddr string, tokenEnv string, allowInsecure bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", addr, err)
	}

	trimmedEnv := strings.TrimSpace(tokenEnv)
	if trimmedEnv == "" {
		if strings.TrimSpace(tokenEnv) != tokenEnv || tokenEnv != "" {
			return fmt.Errorf("resolve bearer token from env %q: env var name cannot be whitespace-only", tokenEnv)
		}
		return fmt.Errorf("refusing to serve unauthenticated MCP over HTTP (set --token-env)")
	}
	token, err := secrets.Resolve(trimmedEnv, "")
	if err != nil {
		return fmt.Errorf("resolve bearer token from env %q: %w", tokenEnv, err)
	}

	if !isLoopbackListenHost(host) && !allowInsecure {
		return fmt.Errorf("refusing cleartext MCP over HTTP on non-loopback address %q (pass --allow-insecure-http or bind 127.0.0.1)", addr)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	return serveStreamableHTTPListener(ctx, ln, cached, cacheAddr, token)
}

func serveStreamableHTTPListener(ctx context.Context, ln net.Listener, cached bool, cacheAddr string, token string) error {
	handler, streamSrv := newStreamableHTTP(cached, cacheAddr, token)
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ln)
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		drainCtx, cancel := context.WithTimeout(context.Background(), streamableHTTPShutdownTimeout)
		defer cancel()
		// Shutdown cancels the idle sweeper and closes SSE sessions.
		// CloseSessions alone leaves the sweeper running.
		if err := streamSrv.Shutdown(drainCtx); err != nil {
			_ = srv.Close()
		}
		if err := srv.Shutdown(drainCtx); err != nil {
			_ = srv.Close()
		}
		err := <-errCh
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
