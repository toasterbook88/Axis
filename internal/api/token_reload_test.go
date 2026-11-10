package api

import (
	"context"
	"errors"
	"net"
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
	authn := tcpAuth(t, src)
	registerRoutesSource(mux, &fakeCache{
		snap: &models.ClusterSnapshot{Status: models.SnapshotHealthy},
		meta: daemon.Metadata{Ready: true},
	}, authn)
	registerPprofRoutesSource(mux, authn)

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

// Refresh reports the same sequence as Current.
func (f *flipSource) Refresh() string { return f.Current() }

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
	// The policy a TCP listener gets with a non-empty startup token, built
	// directly so the startup check does not consume flipSource's first value.
	registerRoutesSource(mux, nil, apiAuth{src: src})
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
	registerPprofRoutesSource(mux, unixAuth(auth.StaticToken("")))
	if got := doAuthed(t, mux, http.MethodGet, "/debug/pprof/", ""); got != http.StatusUnauthorized {
		t.Fatalf("pprof with no token configured = %d, want 401", got)
	}
}

// withRequiredAuth fails closed per request too: a source that turns empty
// after startup must not open pprof.
func TestRequiredAuthFailsClosedIfSourceBecomesEmpty(t *testing.T) {
	src := &flipSource{first: reloadTokOld, second: ""}
	h := withRequiredAuthSource(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }, unixAuth(src)) // startup sees a token
	if got := doAuthed(t, h, http.MethodGet, "/debug/pprof/", ""); got != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", got)
	}
}

// A client that re-read a rotated token file is accepted on its first request,
// without waiting for a poll (no Run loop here at all). The old token is
// rejected afterwards, and a wrong bearer never gets in through the refresh.
func TestRotatedTokenAcceptedBeforeNextPoll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeAtomic(t, path, reloadTokOld)
	src, err := auth.NewFileToken(path, reloadTokOld)
	if err != nil {
		t.Fatal(err)
	}
	h := withAuthSource(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }, tcpAuth(t, src))

	writeAtomic(t, path, reloadTokNew)
	if got := doAuthed(t, h, http.MethodGet, "/snapshot", reloadTokNew); got != http.StatusOK {
		t.Fatalf("rotated token = %d, want 200 without waiting for a poll", got)
	}
	if got := doAuthed(t, h, http.MethodGet, "/snapshot", reloadTokOld); got != http.StatusUnauthorized {
		t.Fatalf("old token after swap = %d, want 401", got)
	}
	bogus := strings.Repeat("9", 64)
	if got := doAuthed(t, h, http.MethodGet, "/snapshot", bogus); got != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d, want 401", got)
	}
	if got := src.Current(); got != reloadTokNew {
		t.Fatalf("Current() = %q, want the rotated token", got)
	}
}

// The refreshed token becomes the request's snapshot, so the forwarded-origin
// HMAC on /run verifies against the token that authenticated the request.
func TestRefreshedTokenIsTheRequestSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeAtomic(t, path, reloadTokOld)
	src, err := auth.NewFileToken(path, reloadTokOld)
	if err != nil {
		t.Fatal(err)
	}
	writeAtomic(t, path, reloadTokNew)
	if got := serveSignedRun(t, src, reloadTokNew, reloadTokNew); got != http.StatusOK {
		t.Fatalf("bearer + origin signed with the rotated token = %d, want 200", got)
	}
}

// A source whose refresh yields "" never lets a mismatched bearer through.
// refreshEmptySource has a token but returns "" from Refresh.
type refreshEmptySource struct{}

func (refreshEmptySource) Current() string { return reloadTokOld }
func (refreshEmptySource) Refresh() string { return "" }

func TestRefreshToEmptyFailsClosed(t *testing.T) {
	for name, a := range map[string]apiAuth{
		"tcp":  tcpAuth(t, refreshEmptySource{}),
		"unix": unixAuth(refreshEmptySource{}),
	} {
		h := withAuthSource(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }, a)
		if got := doAuthed(t, h, http.MethodGet, "/snapshot", reloadTokNew); got != http.StatusUnauthorized {
			t.Fatalf("%s: got %d, want 401", name, got)
		}
	}
}

// A source that turns empty between the first read and the refresh in one
// request also fails closed.
func TestRefreshToEmptyFailsClosedLegacyFlip(t *testing.T) {
	src := &flipSource{first: reloadTokOld, second: ""}
	h := withAuthSource(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }, apiAuth{src: src})
	if got := doAuthed(t, h, http.MethodGet, "/snapshot", reloadTokNew); got != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", got)
	}
}

// A wrong bearer (a stale client or an attacker) just before a rotation does
// not delay the new token: there is no refresh slot to use up.
func TestWrongBearerBeforeRotationDoesNotBlockNewToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeAtomic(t, path, reloadTokOld)
	src, err := auth.NewFileToken(path, reloadTokOld)
	if err != nil {
		t.Fatal(err)
	}
	h := withAuthSource(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }, tcpAuth(t, src))

	bogus := strings.Repeat("9", 64)
	for i := 0; i < 50; i++ {
		if got := doAuthed(t, h, http.MethodGet, "/snapshot", bogus); got != http.StatusUnauthorized {
			t.Fatalf("wrong token = %d, want 401", got)
		}
	}
	writeAtomic(t, path, reloadTokNew)
	if got := doAuthed(t, h, http.MethodGet, "/snapshot", reloadTokNew); got != http.StatusOK {
		t.Fatalf("rotated token right after wrong bearers = %d, want 200", got)
	}
}

// Copilot review (Review 4): a TCP server whose source returns "" after
// startup must not open protected routes. The empty snapshot gets 401 on /run
// and on a normal route, with or without a bearer.
func TestTCPSourceTurningEmptyAfterStartupFailsClosed(t *testing.T) {
	src := &flipSource{first: reloadTokOld, second: ""}
	mux := http.NewServeMux()
	registerRoutesSource(mux, &fakeCache{
		snap: &models.ClusterSnapshot{Status: models.SnapshotHealthy},
		meta: daemon.Metadata{Ready: true},
	}, tcpAuth(t, src)) // startup check consumes the non-empty first value

	for _, tc := range []struct{ method, path, bearer string }{
		{http.MethodPost, "/run", ""},
		{http.MethodPost, "/run", reloadTokOld},
		{http.MethodGet, "/snapshot", ""},
		{http.MethodGet, "/v2/cluster", reloadTokOld},
	} {
		if got := doAuthed(t, mux, tc.method, tc.path, tc.bearer); got != http.StatusUnauthorized {
			t.Fatalf("%s %s (bearer=%t) with an empty snapshot = %d, want 401", tc.method, tc.path, tc.bearer != "", got)
		}
	}
}

// The same holds on a Unix socket whose token was non-empty at startup: F9
// pass-through is only for a socket started without a token.
func TestUnixSourceTurningEmptyAfterStartupFailsClosed(t *testing.T) {
	src := &flipSource{first: reloadTokOld, second: ""}
	h := withAuthSource(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }, unixAuth(src))
	if got := doAuthed(t, h, http.MethodGet, "/snapshot", ""); got != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", got)
	}
}

// F9 is unchanged: a Unix socket started with an empty static token passes
// requests through, and a TCP listener with an empty token is refused.
func TestEmptyStartupTokenPolicy(t *testing.T) {
	a, err := newAPIAuth(auth.StaticToken(""), true)
	if err != nil || !a.allowEmpty {
		t.Fatalf("unix + empty: allowEmpty=%v err=%v, want true, nil", a.allowEmpty, err)
	}
	h := withAuthSource(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }, a)
	if got := doAuthed(t, h, http.MethodGet, "/snapshot", ""); got != http.StatusOK {
		t.Fatalf("unix + empty static token = %d, want 200 (F9)", got)
	}
	if _, err := newAPIAuth(auth.StaticToken(""), false); !errors.Is(err, errNoTokenOnTCP) {
		t.Fatalf("tcp + empty: err=%v, want errNoTokenOnTCP", err)
	}
	if a, err := newAPIAuth(auth.StaticToken(reloadTokOld), true); err != nil || a.allowEmpty {
		t.Fatalf("unix + token: allowEmpty=%v err=%v, want false, nil", a.allowEmpty, err)
	}
}

// Serve end to end on a Unix socket with an empty token still serves
// protected routes without auth (F9), through ServeWithTokenSource's policy.
func TestServeUnixSocketEmptyTokenStillPassesThrough(t *testing.T) {
	sock := filepath.Join(shortSocketDir(t), "a.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ServeWithTokenSource(ctx, sock, &fakeCache{
			snap: &models.ClusterSnapshot{Status: models.SnapshotHealthy},
			meta: daemon.Metadata{Ready: true},
		}, auth.StaticToken(""), false, nil)
	}()
	defer func() {
		cancel()
		<-done
	}()

	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}}
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := client.Get("http://unix/snapshot")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET /snapshot = %d, want 200 (F9)", resp.StatusCode)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not come up: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
