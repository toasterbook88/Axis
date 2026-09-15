package transport

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestBuildRemoteWriteCommandSmallPayloadSingleCommand(t *testing.T) {
	cmd := BuildRemoteWriteCommand("/tmp/axis-knows.json", []byte(`{"best_node":"cachyos"}`))
	if strings.Count(cmd, "base64 -d") != 1 || strings.Contains(cmd, ">>") {
		t.Fatalf("small payload should stay a single write, got: %.120s", cmd)
	}
	if strings.Contains(cmd, "&& : >") {
		t.Fatalf("small payload should not truncate-target, got: %.120s", cmd)
	}
}

// Round-trip: chunked delivery must reconstruct the exact bytes after decode.
// This pins the base64 4-char group invariant — a misaligned split corrupts silently.
func TestBuildRemoteWriteCommandChunkedRoundTrip(t *testing.T) {
	cases := map[string][]byte{
		"exact-group-multiple": bytesN(120_000, 'A'),
		"one-past-budget":      bytesN(90_001, 'B'),
		"odd-tail":             bytesN(97_123, 'C'),
		"tiny":                 []byte("hello"),
	}
	for name, payload := range cases {
		cmd := BuildRemoteWriteCommand("/tmp/rt.json", payload)
		// Rebuild the concatenated base64 from the printf segments. shellescape
		// may emit the chunk quoted or bare (base64 chars are shell-safe).
		var b strings.Builder
		parts := strings.Split(cmd, "printf '%s' ")
		for _, seg := range parts[1:] {
			seg = strings.TrimLeft(seg, " \t")
			if strings.HasPrefix(seg, "'") {
				if end := strings.Index(seg[1:], "'"); end >= 0 {
					b.WriteString(seg[1 : 1+end])
				}
				continue
			}
			// bare token: runs until whitespace
			if sp := strings.IndexAny(seg, " \t"); sp >= 0 {
				b.WriteString(seg[:sp])
			} else {
				b.WriteString(strings.TrimSpace(seg))
			}
		}
		got, err := base64.StdEncoding.DecodeString(b.String())
		if err != nil {
			t.Fatalf("%s: decode failed: %v", name, err)
		}
		if string(got) != string(payload) {
			t.Fatalf("%s: round-trip mismatch: got %d bytes, want %d", name, len(got), len(payload))
		}
		// Every single command inside the chain must be under the arg limit.
		for _, part := range strings.Split(cmd, " && ") {
			if len(part) > 131_071 {
				t.Fatalf("%s: command segment exceeds MAX_ARG_STRLEN (%d chars)", name, len(part))
			}
		}
	}
}

// The live failure: a 113KB indented snapshot base64s to ~150KB and must be chunked.
func TestBuildRemoteWriteCommandRealSnapshotSize(t *testing.T) {
	payload := bytesN(113_022, 'x')
	cmd := BuildRemoteWriteCommand("/tmp/axis-knows-1789510046272675793.json", payload)
	for _, part := range strings.Split(cmd, " && ") {
		if len(part) > 131_071 {
			t.Fatalf("segment %d chars exceeds MAX_ARG_STRLEN", len(part))
		}
	}
	if !strings.Contains(cmd, ">>") {
		t.Fatal("oversized payload must be delivered as ordered appends")
	}
}

func bytesN(n int, b byte) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}
