package main

import (
	"context"
	"net/http"
	"net/http/httptest"
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
