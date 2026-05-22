package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/execution"
)

func TestFetchDaemonMeshReturnsPeers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/v2/mesh" {
			t.Fatalf("expected /v2/mesh, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"peers":[{"name":"alpha","hostname":"10.0.0.1","state":"verified","source":"gossip","last_seen":"2026-05-22T22:00:00Z"}],"count":1}`))
	}))
	defer server.Close()

	peers, err := fetchDaemonMesh(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("fetchDaemonMesh: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(peers))
	}
	if peers[0].Name != "alpha" {
		t.Fatalf("expected peer name alpha, got %q", peers[0].Name)
	}
}

func TestFetchDaemonMeshReturnsEmptyWhenNoPeers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"peers":[],"count":0}`))
	}))
	defer server.Close()

	peers, err := fetchDaemonMesh(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("fetchDaemonMesh: %v", err)
	}
	if len(peers) != 0 {
		t.Fatalf("expected 0 peers, got %d", len(peers))
	}
}

func TestDaemonMeshCommandRendersTable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/mesh" {
			t.Fatalf("expected /v2/mesh, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"peers":[{"name":"alpha","hostname":"10.0.0.1","state":"verified","source":"gossip","last_seen":"2026-05-22T22:00:00Z"}],"count":1}`))
	}))
	defer server.Close()

	cmd := daemonCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--cache-addr", server.URL, "mesh"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("daemon mesh: %v", err)
	}
	if !strings.Contains(out.String(), "alpha") {
		t.Fatalf("expected peer name alpha in output, got %q", out.String())
	}
	if !strings.Contains(out.String(), "MESH PEERS") {
		t.Fatalf("expected MESH PEERS header, got %q", out.String())
	}
}

func TestDaemonMeshCommandHandlesEmptyPeers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"peers":[],"count":0}`))
	}))
	defer server.Close()

	cmd := daemonCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--cache-addr", server.URL, "mesh"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("daemon mesh: %v", err)
	}
	if !strings.Contains(out.String(), "No active mesh peers") {
		t.Fatalf("expected no-peers message, got %q", out.String())
	}
}

func TestHumanizeTimeFormatsRecent(t *testing.T) {
	now := time.Now()
	cases := []struct {
		t    time.Time
		want string
	}{
		{time.Time{}, "—"},
		{now.Add(-5 * time.Second), "5s ago"},
		{now.Add(-2 * time.Minute), "2m ago"},
		{now.Add(-3 * time.Hour), "3h ago"},
		{now.Add(-48 * time.Hour), "2d ago"},
	}
	for _, tc := range cases {
		got := humanizeTime(tc.t)
		if got != tc.want {
			t.Errorf("humanizeTime(%v) = %q, want %q", tc.t, got, tc.want)
		}
	}
}

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
