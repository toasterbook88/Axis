package modellife

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func TestAwaitInstance_RetriesUntilReady(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&attempts, 1)
		if count < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"loading model"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())

	instance := models.ModelInstance{
		ID:     "mi-test-2",
		Node:   "node-2",
		Engine: "llama.cpp",
		Port:   port,
		Model:  "test-model",
	}

	receipt, err := AwaitInstance(context.Background(), instance, AwaitOptions{
		Timeout:  2 * time.Second,
		Interval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("AwaitInstance failed: %v", err)
	}
	if receipt.Status != models.ModelOperationCompleted {
		t.Fatalf("status = %q, want completed", receipt.Status)
	}
	if receipt.Disposition != "ready" {
		t.Fatalf("disposition = %q, want ready", receipt.Disposition)
	}
	if atomic.LoadInt32(&attempts) < 3 {
		t.Fatalf("attempts = %d, want >= 3", atomic.LoadInt32(&attempts))
	}
}

func TestAwaitInstance_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"loading model"}`))
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())

	instance := models.ModelInstance{
		ID:     "mi-timeout",
		Node:   "node-timeout",
		Engine: "llama.cpp",
		Port:   port,
		Model:  "test-model",
	}

	receipt, err := AwaitInstance(context.Background(), instance, AwaitOptions{
		Timeout:  150 * time.Millisecond,
		Interval: 30 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if receipt.Status != models.ModelOperationFailed {
		t.Fatalf("status = %q, want failed", receipt.Status)
	}
	if receipt.Disposition != "timeout" {
		t.Fatalf("disposition = %q, want timeout", receipt.Disposition)
	}
}

func TestAwaitInstance_InvalidPort(t *testing.T) {
	instance := models.ModelInstance{
		ID:   "mi-bad-port",
		Port: 0,
	}
	receipt, err := AwaitInstance(context.Background(), instance, AwaitOptions{})
	if err == nil {
		t.Fatal("expected invalid port error")
	}
	if receipt.Status != models.ModelOperationRejected {
		t.Fatalf("status = %q, want rejected", receipt.Status)
	}
	if receipt.Disposition != "invalid_target" {
		t.Fatalf("disposition = %q, want invalid_target", receipt.Disposition)
	}
}
