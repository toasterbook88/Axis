package llmrouter

import (
	"testing"
)

// The classification budget is enforced per-call via context.WithTimeout in
// Classify (WithTimeout is authoritative). The HTTP client must NOT carry a
// hard client-level Timeout, which would silently cap that per-call budget.
func TestNewEngineHTTPClientHasNoHardTimeout(t *testing.T) {
	engine := NewEngine()
	if engine.httpClient.Timeout != 0 {
		t.Fatalf("expected no client-level HTTP timeout (context-bounded), got %s", engine.httpClient.Timeout)
	}
}
