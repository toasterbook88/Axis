package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestWithRequestIDGeneratesWhenMissing(t *testing.T) {
	captureLogs(t)
	var seen string
	h := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Context().Value(requestIDKey{}).(string)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	got := rec.Header().Get(requestIDHeader)
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(got) {
		t.Fatalf("generated id = %q", got)
	}
	if seen != got {
		t.Fatalf("context id %q != header %q", seen, got)
	}
}

func TestWithRequestIDEchoesValidAndReplacesInvalid(t *testing.T) {
	captureLogs(t)
	h := withRequestID(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for in, keep := range map[string]bool{"cli-abc-123": true, "bad id!": false, strings.Repeat("a", 65): false} {
		req := httptest.NewRequest(http.MethodGet, "/status", nil)
		req.Header.Set(requestIDHeader, in)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if got := rec.Header().Get(requestIDHeader); (got == in) != keep || got == "" {
			t.Fatalf("in=%q got=%q keep=%v", in, got, keep)
		}
	}
}

func TestWithRequestIDLogsWithoutSecretsOrQuery(t *testing.T) {
	logs := captureLogs(t)
	h := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	req := httptest.NewRequest(http.MethodPost, "/run?token=sekret-q", nil)
	req.Header.Set("Authorization", "Bearer sekret-h")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get(requestIDHeader) == "" {
		t.Fatal("401 response missing request id")
	}
	out := logs.String()
	for _, want := range []string{`"msg":"api: request"`, `"method":"POST"`, `"path":"/run"`, `"status":401`, `"request_id":`, `"duration_ms":`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in %s", want, out)
		}
	}
	if strings.Contains(out, "sekret") || strings.Contains(out, "token=") {
		t.Fatalf("secret leaked into logs: %s", out)
	}
}

func TestWithRequestIDPreservesFlusher(t *testing.T) {
	captureLogs(t)
	h := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Fatal("wrapped writer lost http.Flusher (breaks /run streaming)")
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Fatalf("flush: %v", err)
		}
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/run", nil))
}
