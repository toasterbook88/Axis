package llmrouter

import (
	"testing"
	"time"
)

func TestNewEngineHTTPClientHasTimeout(t *testing.T) {
	engine := NewEngine()
	if engine.httpClient.Timeout <= 0 {
		t.Fatalf("expected non-zero HTTP client timeout, got %s", engine.httpClient.Timeout)
	}
	if engine.httpClient.Timeout != 30*time.Second {
		t.Fatalf("expected 30s HTTP client timeout, got %s", engine.httpClient.Timeout)
	}
}
