package mcpclient

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestMCPHTTPClientRejectsCrossOriginRedirects(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetRequests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	resp, err := newMCPHTTPClient().Get(source.URL)
	if err != nil {
		t.Fatalf("GET source: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusTemporaryRedirect)
	}
	if got := targetRequests.Load(); got != 0 {
		t.Fatalf("cross-origin target requests = %d, want 0", got)
	}
}

// A redirect to the same host on another scheme (an https to http
// downgrade) is not followed either: the origin is scheme and host.
func TestMCPHTTPClientRejectsSchemeChangeRedirects(t *testing.T) {
	// Same host and port, http instead of https. Followed, the plain request
	// would reach this TLS server and get a 400; unfollowed, the caller sees
	// the redirect itself.
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+"/downgraded", http.StatusTemporaryRedirect)
	}))
	defer secure.Close()

	client := newMCPHTTPClient()
	client.Transport = secure.Client().Transport // trust the test certificate
	resp, err := client.Get(secure.URL)
	if err != nil {
		t.Fatalf("GET secure: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want %d: the https to http redirect was followed", resp.StatusCode, http.StatusTemporaryRedirect)
	}
}

// A same-origin redirect loop stops at Go's default limit of ten redirects
// instead of running until the context expires.
func TestMCPHTTPClientStopsSameOriginRedirectLoop(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/loop", http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	_, err := newMCPHTTPClient().Get(server.URL + "/loop")
	if err == nil {
		t.Fatal("redirect loop returned no error")
	}
	if got := hits.Load(); got != 10 {
		t.Fatalf("server saw %d requests, want 10 (the first and 9 redirects before the limit)", got)
	}
}

func TestMCPHTTPClientAllowsSameOriginRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/final", http.StatusTemporaryRedirect)
			return
		}
		_, _ = fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	resp, err := newMCPHTTPClient().Get(server.URL + "/start")
	if err != nil {
		t.Fatalf("GET source: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}
