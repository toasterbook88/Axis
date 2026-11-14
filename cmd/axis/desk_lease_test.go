package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/seatlease"
)

func TestDeskLeaseRefusalsDoNotSend(t *testing.T) {
	now := time.Date(2026, 11, 13, 21, 0, 0, 0, time.UTC)
	calls := 0
	prev := deskLeaseComplete
	deskLeaseComplete = func(url, token, prompt string) (string, error) {
		calls++
		return "sent", nil
	}
	t.Cleanup(func() { deskLeaseComplete = prev })

	cases := []struct {
		name    string
		lease   *seatlease.Lease
		omit    bool
		refusal string
	}{
		{name: "missing file", omit: true, refusal: seatlease.RefusalMissing},
		{name: "stale", lease: &seatlease.Lease{State: "awake", Foreground: true, ExpiresAt: now.Add(-time.Second), CompletionURL: "https://seat.example/v1/chat/completions"}, refusal: seatlease.RefusalStale},
		{name: "busy", lease: &seatlease.Lease{State: "awake", Busy: true, Foreground: true, ExpiresAt: now.Add(time.Minute), CompletionURL: "https://seat.example/v1/chat/completions"}, refusal: seatlease.RefusalBusy},
		{name: "ended", lease: &seatlease.Lease{State: "ended", Foreground: true, ExpiresAt: now.Add(time.Minute), CompletionURL: "https://seat.example/v1/chat/completions"}, refusal: seatlease.RefusalEnded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := calls
			path := filepath.Join(t.TempDir(), "lease.json")
			if !tc.omit {
				writeLease(t, path, tc.lease)
			}
			err := runDeskLease(io.Discard, path, "hello", now)
			if ExitCode(err) == 0 || err == nil || !strings.Contains(err.Error(), tc.refusal) {
				t.Fatalf("err = %v, want refusal %s", err, tc.refusal)
			}
			if calls != before {
				t.Fatalf("completion calls = %d, want %d", calls, before)
			}
		})
	}
}

func TestDeskLeaseUsableSendsOneCompletion(t *testing.T) {
	now := time.Date(2026, 11, 13, 21, 0, 0, 0, time.UTC)
	var gotAuth, gotBody string
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "desk-ok")
	}))
	t.Cleanup(server.Close)

	path := filepath.Join(t.TempDir(), "lease.json")
	writeLease(t, path, &seatlease.Lease{
		State: "awake", Foreground: true, ExpiresAt: now.Add(time.Minute),
		CompletionURL: server.URL, Token: "fixture-token",
	})
	var out strings.Builder
	if err := runDeskLease(&out, path, "hello", now); err != nil {
		t.Fatal(err)
	}
	if out.String() != "desk-ok" || hits != 1 {
		t.Fatalf("out %q hits %d", out.String(), hits)
	}
	if gotAuth != "Bearer fixture-token" || !strings.Contains(gotBody, "hello") {
		t.Fatalf("auth %q body %s", gotAuth, gotBody)
	}
}

func TestDeskLeaseUnavailableDoesNotRetry(t *testing.T) {
	now := time.Date(2026, 11, 13, 21, 0, 0, 0, time.UTC)
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "lease.json")
	writeLease(t, path, &seatlease.Lease{
		State: "awake", Foreground: true, ExpiresAt: now.Add(time.Minute),
		CompletionURL: server.URL,
	})
	err := runDeskLease(io.Discard, path, "hello", now)
	if ExitCode(err) == 0 || !strings.Contains(err.Error(), seatlease.RefusalUnavailable) || hits != 1 {
		t.Fatalf("err=%v hits=%d", err, hits)
	}
}

func TestDeskLeaseRedirectIsNotFollowed(t *testing.T) {
	now := time.Date(2026, 11, 13, 21, 0, 0, 0, time.UTC)
	nextHits := 0
	next := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextHits++
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "followed")
	}))
	t.Cleanup(next.Close)
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Redirect(w, r, next.URL, http.StatusFound)
	}))
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "lease.json")
	writeLease(t, path, &seatlease.Lease{
		State: "awake", Foreground: true, ExpiresAt: now.Add(time.Minute),
		CompletionURL: server.URL,
	})
	err := runDeskLease(io.Discard, path, "hello", now)
	if ExitCode(err) == 0 || !strings.Contains(err.Error(), seatlease.RefusalUnavailable) || hits != 1 || nextHits != 0 {
		t.Fatalf("err=%v hits=%d next=%d", err, hits, nextHits)
	}
}

func TestAgentDeskLeaseFlagFailsClosedBeforeCluster(t *testing.T) {
	now := time.Date(2026, 11, 13, 21, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "lease.json")
	writeLease(t, path, &seatlease.Lease{
		State: "awake", Foreground: true, ExpiresAt: now.Add(-time.Minute),
		CompletionURL: "https://seat.example/v1/chat/completions",
	})
	cmd := agentCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"-p", "--desk-lease", path, "hello"})
	err := cmd.Execute()
	if ExitCode(err) == 0 || err == nil || !strings.Contains(err.Error(), seatlease.RefusalStale) {
		t.Fatalf("err = %v", err)
	}
}

func writeLease(t *testing.T, path string, lease *seatlease.Lease) {
	t.Helper()
	raw, err := json.Marshal(lease)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
