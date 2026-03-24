package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
