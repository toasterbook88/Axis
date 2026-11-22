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

// newStreamableHTTP builds the HTTP handler mounted at /mcp along with the
// underlying StreamableHTTPServer so callers can close active SSE sessions on
// shutdown.
func newStreamableHTTP(cached bool, cacheAddr string, token string) (http.Handler, *mcpserver.StreamableHTTPServer) {
	mcpSrv := NewServer(cached, cacheAddr, nil)
	streamSrv := mcpserver.NewStreamableHTTPServer(mcpSrv)

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
// If tokenEnv is set, it resolves the bearer token from that environment
// variable and fails closed when unset or empty. Non-loopback bind addresses
// are refused unless a bearer token is configured.
func ServeStreamableHTTP(ctx context.Context, addr string, cached bool, cacheAddr string, tokenEnv string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", addr, err)
	}

	var token string
	if tokenEnv != "" {
		trimmedEnv := strings.TrimSpace(tokenEnv)
		if trimmedEnv == "" {
			return fmt.Errorf("resolve bearer token from env %q: env var name cannot be whitespace-only", tokenEnv)
		}
		resolved, err := secrets.Resolve(trimmedEnv, "")
		if err != nil {
			return fmt.Errorf("resolve bearer token from env %q: %w", tokenEnv, err)
		}
		token = resolved
	}

	if !isLoopbackListenHost(host) && token == "" {
		return fmt.Errorf("refusing to serve unauthenticated MCP over HTTP on non-loopback address %q (set --token-env or bind 127.0.0.1)", addr)
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
		streamSrv.CloseSessions(drainCtx)
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
