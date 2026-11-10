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
// auth.StaticToken.

func ServeWithContext(ctx context.Context, addr string, cache snapshotCache, token string, pprof bool, cardFn func() a2a.AgentCard) error {
	return ServeWithTokenSource(ctx, addr, cache, auth.StaticToken(token), pprof, cardFn)
}

func registerRoutes(mux *http.ServeMux, cache snapshotCache, token string) {
	registerRoutesSource(mux, cache, auth.StaticToken(token))
}

func registerPprofRoutes(mux *http.ServeMux, token string) {
	registerPprofRoutesSource(mux, auth.StaticToken(token))
}
