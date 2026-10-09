package chat

import (
	"context"
	"net"
	"testing"
)

func stubStartLocalOllama(t *testing.T) *int {
	t.Helper()
	calls := 0
	prev := startLocalOllama
	t.Cleanup(func() { startLocalOllama = prev })
	startLocalOllama = func() error { calls++; return nil }
	return &calls
}

// closedPort returns a loopback port with nothing listening on it.
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// An unreachable endpoint that is not this machine's default Ollama (a
// remote node, or an SSH tunnel on an ephemeral port) must fail, not start a
// local daemon that would never serve it.
func TestEnsureRunningDoesNotAutoStartForNonDefaultEndpoint(t *testing.T) {
	calls := stubStartLocalOllama(t)
	c := NewClient("http://"+closedPort(t), "m")
	if err := c.EnsureRunning(context.Background(), nil); err == nil {
		t.Fatal("want error for unreachable endpoint")
	}
	if *calls != 0 {
		t.Fatalf("started local ollama %d time(s) for a non-default endpoint", *calls)
	}
}

func TestEnsureRunningDoesNotAutoStartWhenCanceled(t *testing.T) {
	calls := stubStartLocalOllama(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := NewClient("http://127.0.0.1:11434", "m")
	if err := c.EnsureRunning(ctx, nil); err == nil {
		t.Fatal("want error for canceled context")
	}
	if *calls != 0 {
		t.Fatalf("started local ollama %d time(s) after cancellation", *calls)
	}
}

func TestIsDefaultLocalOllama(t *testing.T) {
	for endpoint, want := range map[string]bool{
		"http://127.0.0.1:11434":    true,
		"http://localhost:11434/":   true,
		"http://[::1]:11434":        true,
		"http://127.0.0.1:41851":    false, // SSH tunnel
		"http://198.51.100.7:11434": false, // remote node
		"http://worker:11434":       false,
		"not a url":                 false,
	} {
		if got := isDefaultLocalOllama(endpoint); got != want {
			t.Errorf("isDefaultLocalOllama(%q) = %v, want %v", endpoint, got, want)
		}
	}
}
