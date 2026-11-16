package chat

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
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

// failNThenOK refuses the first n requests and answers 200 after that.
// The test uses it so a default-local endpoint string is never dialed.
type failNThenOK struct {
	left int
}

func (f *failNThenOK) RoundTrip(req *http.Request) (*http.Response, error) {
	if f.left > 0 {
		f.left--
		return nil, errors.New("not reachable")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Body:       io.NopCloser(strings.NewReader(`{}`)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func TestEnsureRunningDisableAutoStartDoesNotLaunch(t *testing.T) {
	calls := stubStartLocalOllama(t)
	c := NewClient("http://127.0.0.1:11434", "m")
	c.http = &http.Client{Transport: &failNThenOK{left: 1}}
	c.DisableAutoStart()
	err := c.EnsureRunning(context.Background(), nil)
	if err == nil {
		t.Fatal("want an error when the daemon is down")
	}
	if *calls != 0 {
		t.Fatalf("start calls = %d, want 0", *calls)
	}
	if strings.Contains(strings.ToLower(err.Error()), "pull") || strings.Contains(err.Error(), "ollama serve") {
		t.Fatalf("down consult path names a start or a pull: %v", err)
	}
}

func TestEnsureRunningDefaultClientStillStartsLocalOllama(t *testing.T) {
	calls := stubStartLocalOllama(t)
	c := NewClient("http://127.0.0.1:11434", "m")
	if !c.AutoStartEnabled() {
		t.Fatal("a new client auto-starts")
	}
	c.http = &http.Client{Transport: &failNThenOK{left: 1}}
	if err := c.EnsureRunning(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Fatalf("start calls = %d, want 1", *calls)
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
