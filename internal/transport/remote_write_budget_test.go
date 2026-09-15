package transport

import "testing"

// Documents the real constraint discovered live (2026-09-15, cachyos fish shell):
// MAX_ARG_STRLEN applies to the ENTIRE remote command string that ssh passes to the
// remote shell as one argv element — not to individual printf segments inside it.
// Chunking base64 inside one command does NOT dodge the limit. The fix must therefore
// deliver the payload over SSH STDIN, not as part of the command string.
func TestBuildRemoteWriteCommandIsCommandStringOnly(t *testing.T) {
	// The command builder itself must stay under the total-command budget for small
	// payloads (backward compat), and for large payloads the caller must use stdin
	// delivery instead. This pins the boundary.
	small := []byte(`{"best_node":"x"}`)
	cmd := BuildRemoteWriteCommand("/tmp/x.json", small)
	if len(cmd) > MaxRemoteArgChars {
		t.Fatalf("small payload command exceeds budget: %d", len(cmd))
	}
}
