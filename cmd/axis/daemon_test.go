package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/execution"
)

func TestInvalidateDaemonCachePostsToEndpoint(t *testing.T) {
	var sawPost bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/invalidate" {
			t.Fatalf("expected /invalidate, got %s", r.URL.Path)
		}
		sawPost = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	if err := invalidateDaemonCache(context.Background(), server.URL); err != nil {
		t.Fatalf("invalidateDaemonCache: %v", err)
	}
	if !sawPost {
		t.Fatal("expected invalidate endpoint to be called")
	}
}

func TestRefreshDaemonCachePostsToEndpoint(t *testing.T) {
	var sawPost bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/refresh" {
			t.Fatalf("expected /refresh, got %s", r.URL.Path)
		}
		sawPost = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	if err := refreshDaemonCache(context.Background(), server.URL); err != nil {
		t.Fatalf("refreshDaemonCache: %v", err)
	}
	if !sawPost {
		t.Fatal("expected refresh endpoint to be called")
	}
}

func TestRefreshDaemonCacheWithTriggerPostsQueryParam(t *testing.T) {
	var gotTrigger string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/refresh" {
			t.Fatalf("expected /refresh, got %s", r.URL.Path)
		}
		gotTrigger = r.URL.Query().Get("trigger")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	if err := refreshDaemonCacheWithTrigger(context.Background(), server.URL, execution.StateChangeExecutionFinished); err != nil {
		t.Fatalf("refreshDaemonCacheWithTrigger: %v", err)
	}
	if gotTrigger != execution.StateChangeExecutionFinished {
		t.Fatalf("expected execution trigger query, got %q", gotTrigger)
	}
}

func TestDaemonStatusWarnsWhenVersionMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/snapshot/meta" {
			t.Fatalf("expected /snapshot/meta, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"source":"daemon-cache","ready":true,"refresh_interval_sec":60}`))
	}))
	defer server.Close()

	cmd := daemonCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--cache-addr", server.URL, "status"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("daemon status: %v", err)
	}
	if !strings.Contains(out.String(), "missing version information") {
		t.Fatalf("expected missing-version warning, got %q", out.String())
	}
}

func TestDaemonListenAddrNormalizesHostPort(t *testing.T) {
	got, err := daemonListenAddr("127.0.0.1:42425")
	if err != nil {
		t.Fatalf("daemonListenAddr: %v", err)
	}
	if got != "127.0.0.1:42425" {
		t.Fatalf("expected normalized host:port, got %q", got)
	}
}

func TestDaemonListenAddrAcceptsHTTPURL(t *testing.T) {
	got, err := daemonListenAddr("http://127.0.0.1:42425")
	if err != nil {
		t.Fatalf("daemonListenAddr: %v", err)
	}
	if got != "127.0.0.1:42425" {
		t.Fatalf("expected URL host:port, got %q", got)
	}
}

func TestPidAlive(t *testing.T) {
	if !pidAlive(os.Getpid()) {
		t.Error("pidAlive(os.Getpid()) = false, want true")
	}
	if pidAlive(999999) {
		t.Error("pidAlive(999999) = true, want false")
	}
}
