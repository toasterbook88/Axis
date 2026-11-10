package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/auth"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

const (
	reloadTokOld = "1111111111111111111111111111111111111111111111111111111111111111"
	reloadTokNew = "2222222222222222222222222222222222222222222222222222222222222222"
)

func writeAtomic(t *testing.T, path, content string) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func doAuthed(t *testing.T, h http.Handler, method, path, token string) int {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// Strict swap (option A): after the file changes, the new token works and the
// old token gets 401 on v1, v2, A2A task and pprof routes.
func TestTokenFileRotationIsStrictAcrossRoutes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeAtomic(t, path, reloadTokOld)
	src, err := auth.NewFileToken(path, reloadTokOld)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go src.Run(ctx)

	mux := http.NewServeMux()
	registerRoutesSource(mux, &fakeCache{
		snap: &models.ClusterSnapshot{Status: models.SnapshotHealthy},
		meta: daemon.Metadata{Ready: true},
	}, src)
	registerPprofRoutesSource(mux, src)

	routes := []struct {
		method, path string
		okCode       int
	}{
		{http.MethodGet, "/snapshot", http.StatusOK},
		{http.MethodGet, "/v2/cluster", http.StatusOK},
		{http.MethodGet, "/a2a/v1/tasks/missing-task", http.StatusNotFound},
		{http.MethodGet, "/debug/pprof/", http.StatusOK},
	}
	for _, r := range routes {
		if got := doAuthed(t, mux, r.method, r.path, reloadTokOld); got != r.okCode {
			t.Fatalf("before rotation %s with old token = %d, want %d", r.path, got, r.okCode)
		}
	}

	writeAtomic(t, path, reloadTokNew+"\n")
	deadline := time.Now().Add(3 * time.Second)
	for src.Current() != reloadTokNew {
		if time.Now().After(deadline) {
			t.Fatal("token source did not pick up the rotated file")
		}
		time.Sleep(10 * time.Millisecond)
	}

	for _, r := range routes {
		if got := doAuthed(t, mux, r.method, r.path, reloadTokNew); got != r.okCode {
			t.Errorf("after rotation %s with new token = %d, want %d", r.path, got, r.okCode)
		}
		if got := doAuthed(t, mux, r.method, r.path, reloadTokOld); got != http.StatusUnauthorized {
			t.Errorf("after rotation %s with old token = %d, want 401", r.path, got)
		}
	}
}

// flipSource returns first on its first Current() call and second afterwards,
// so a handler that reads the source twice in one request would mix tokens.
type flipSource struct {
	calls         atomic.Int32
	first, second string
}

func (f *flipSource) Current() string {
	if f.calls.Add(1) == 1 {
		return f.first
	}
	return f.second
}

func serveSignedRun(t *testing.T, src auth.TokenSource, bearer, signKey string) int {
	t.Helper()
	restoreRuntime := stubLiveRuntime(t, &runtimectx.Context{}, nil)
	defer restoreRuntime()
	prev := runLiveGuarded
	t.Cleanup(func() { runLiveGuarded = prev })
	runLiveGuarded = func(context.Context, *runtimectx.Context, execution.GuardedExecutionRequest) (execution.GuardedExecutionResult, error) {
		return execution.GuardedExecutionResult{OK: true, Node: "alpha"}, nil
	}

	mux := http.NewServeMux()
	registerRoutesSource(mux, nil, src)
	body := `{"description":"echo ok","mode":"exec","confirm":"YES"}`
	req := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	origin := models.NewExecutionOrigin("upstream-node", "upstream.local", "abc-123")
	if err := auth.SetForwardedExecutionOriginHeaders(req.Header, origin, signKey, time.Now()); err != nil {
		t.Fatalf("SetForwardedExecutionOriginHeaders: %v", err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code
}

// One token snapshot per request: bearer auth and forwarded-origin HMAC use
// the same value even if the source changes between them.
func TestRunUsesOneTokenSnapshotPerRequest(t *testing.T) {
	src := &flipSource{first: reloadTokOld, second: reloadTokNew}
	if got := serveSignedRun(t, src, reloadTokOld, reloadTokOld); got != http.StatusOK {
		t.Fatalf("bearer T1 + origin signed with T1 = %d, want 200", got)
	}
	src2 := &flipSource{first: reloadTokOld, second: reloadTokNew}
	if got := serveSignedRun(t, src2, reloadTokOld, reloadTokNew); got != http.StatusBadRequest {
		t.Fatalf("bearer T1 + origin signed with T2 = %d, want 400", got)
	}
}

func TestForwardedOriginVerifiesAgainstRotatedToken(t *testing.T) {
	src := auth.StaticToken(reloadTokNew) // the state after a swap
	if got := serveSignedRun(t, src, reloadTokNew, reloadTokNew); got != http.StatusOK {
		t.Fatalf("origin signed with new token = %d, want 200", got)
	}
	if got := serveSignedRun(t, src, reloadTokNew, reloadTokOld); got != http.StatusBadRequest {
		t.Fatalf("origin signed with old token = %d, want 400", got)
	}
}

// Empty-token rules are unchanged: TCP refuses, pprof refuses, Unix-socket
// routes pass through only for an empty static token.
func TestTokenSourceEmptyTokenRulesUnchanged(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ServeWithTokenSource(ctx, "127.0.0.1:0", nil, auth.StaticToken(""), false, nil); err == nil || !strings.Contains(err.Error(), "api token") {
		t.Fatalf("expected TCP refusal, got %v", err)
	}
	mux := http.NewServeMux()
	registerPprofRoutesSource(mux, auth.StaticToken(""))
	if got := doAuthed(t, mux, http.MethodGet, "/debug/pprof/", ""); got != http.StatusUnauthorized {
		t.Fatalf("pprof with no token configured = %d, want 401", got)
	}
}

// withRequiredAuth fails closed per request too: a source that turns empty
// after startup must not open pprof.
func TestRequiredAuthFailsClosedIfSourceBecomesEmpty(t *testing.T) {
	src := &flipSource{first: reloadTokOld, second: ""}
	h := withRequiredAuthSource(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }, src)
	if got := doAuthed(t, h, http.MethodGet, "/debug/pprof/", ""); got != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", got)
	}
}
