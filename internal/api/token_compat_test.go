package api

import (
	"context"
	"net/http"

	"github.com/toasterbook88/axis/internal/a2a"
	"github.com/toasterbook88/axis/internal/auth"
)

// String-token wrappers for the existing tests. Production code passes an
// auth.TokenSource (cmd/axis/serve.go uses ServeWithTokenSource); these keep
// the 60-odd test call sites unchanged and exercise the same code paths with
// auth.StaticToken. The route wrappers apply the Unix-socket policy, as those
// tests always assumed: an empty token passes through (F9).

func ServeWithContext(ctx context.Context, addr string, cache snapshotCache, token string, pprof bool, cardFn func() a2a.AgentCard) error {
	return ServeWithTokenSource(ctx, addr, cache, auth.StaticToken(token), pprof, cardFn)
}

func registerRoutes(mux *http.ServeMux, cache snapshotCache, token string) {
	registerRoutesSource(mux, cache, unixAuth(auth.StaticToken(token)))
}

func registerPprofRoutes(mux *http.ServeMux, token string) {
	registerPprofRoutesSource(mux, unixAuth(auth.StaticToken(token)))
}

// unixAuth is the startup policy ServeWithTokenSource builds for a Unix
// socket. It never errors for a Unix socket.
func unixAuth(src auth.TokenSource) apiAuth {
	a, _ := newAPIAuth(src, true)
	return a
}

// tcpAuth is the startup policy for a TCP listener; the source must be
// non-empty at startup.
func tcpAuth(t interface{ Fatal(...any) }, src auth.TokenSource) apiAuth {
	a, err := newAPIAuth(src, false)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
